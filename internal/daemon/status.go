package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/DavidGodefroid/lea/internal/store"
)

var statusPath = regexp.MustCompile(`^[A-Za-z0-9_.\[\]]{1,128}$`)

const (
	maxStatusPaths = 64
	// maxPathEvents caps the path events kept per agent: the agent controls the lists, and a
	// flapping one must not grow lea.db without bound.
	maxPathEvents = 20
)

// statusReport stores an agent's context % and cost from its status line, and records the key
// paths of Claude's status-line JSON whenever they change, so the field names can be checked.
// Only agents report: the user has no status line. The answer names the locks lox run runs that
// the agent's container does not mount (locks_added): its status line says to restart lea.
func (d *Daemon) statusReport(ctx context.Context, o Origin, me string, raw json.RawMessage) (any, error) {
	if o.Container == "" {
		return nil, errors.New("status_report: agents only")
	}
	var p struct {
		CtxPct  int      `json:"ctx_pct"`
		CostUSD float64  `json:"cost_usd"`
		Paths   []string `json:"paths"`
		Limits  []struct {
			Window   string `json:"window"`
			Pct      int    `json:"pct"`
			ResetsAt int64  `json:"resets_at"`
		} `json:"limits"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if len(p.Limits) > 2 {
		return nil, errors.New("at most 2 limits")
	}
	var ws []limitWindow
	for _, l := range p.Limits {
		if l.Window != "5h" && l.Window != "7d" {
			return nil, errors.New(`limit window must be "5h" or "7d"`)
		}
		if l.Pct < 0 || l.Pct > 100 {
			return nil, errors.New("limit pct must be 0 to 100")
		}
		if l.ResetsAt < 0 || l.ResetsAt > 1e11 {
			return nil, errors.New("limit resets_at must be unix seconds")
		}
		ws = append(ws, limitWindow{pct: l.Pct, resetsAt: l.ResetsAt})
	}
	if p.CtxPct < -1 || p.CtxPct > 100 {
		return nil, errors.New("ctx_pct must be -1 (unknown) or 0 to 100")
	}
	if p.CostUSD != -1 && (p.CostUSD < 0 || p.CostUSD >= 1e6) {
		return nil, errors.New("cost_usd must be -1 (unknown) or 0 to 1e6")
	}
	if len(p.Paths) > maxStatusPaths {
		return nil, fmt.Errorf("at most %d paths", maxStatusPaths)
	}
	for _, s := range p.Paths {
		if !statusPath.MatchString(s) {
			return nil, fmt.Errorf("invalid path %q", s)
		}
	}
	d.recordLimits(me, ws)
	cost := math.Round(p.CostUSD*100) / 100
	changed, err := d.st.SetUsage(me, store.Usage{CtxPct: p.CtxPct, CostUSD: cost})
	if err != nil {
		return nil, err
	}
	// The spend budgets (#424): the period's spend, then a pause if one is crossed.
	d.mu.Lock()
	d.observeCostLocked(me, cost)
	d.mu.Unlock()
	d.budgetCheck()
	if changed {
		d.changed()
	}
	if len(p.Paths) > 0 {
		joined := strings.Join(p.Paths, "\n")
		d.mu.Lock()
		same := d.statusPaths[me] == joined
		d.statusPaths[me] = joined
		d.mu.Unlock()
		if !same {
			if prev, err := d.st.EventPayloads(me, "paths"); err == nil && len(prev) >= maxPathEvents {
				return d.statusAnswer(ctx, o.Container, me), nil
			}
			b, err := json.Marshal(map[string]any{"paths": p.Paths})
			if err != nil {
				return nil, err
			}
			if err := d.st.AddEvent(me, "statusline", "paths", string(b), d.now()); err != nil {
				return nil, err
			}
		}
	}
	return d.statusAnswer(ctx, o.Container, me), nil
}

// statusAnswer is status_report's answer for agent me of container: the locks it does not mount
// (locks_added), lox_run "off" when the workspace has locks and no lox run serves them (#343),
// its questions to the user and the pause of its inbox (#95).
func (d *Daemon) statusAnswer(ctx context.Context, container, me string) any {
	var ans struct {
		LocksAdded      []string `json:"locks_added,omitempty"`
		LoxRun          string   `json:"lox_run,omitempty"`
		QuestionsOpen   int      `json:"questions_open,omitempty"`
		QuestionsQueued int      `json:"questions_queued,omitempty"`
		Held            bool     `json:"held,omitempty"`
		HeldCount       int      `json:"held_count,omitempty"`
	}
	ans.LocksAdded = d.locksAdded(ctx, container)
	if d.loxRunState() == "off" {
		ans.LoxRun = "off"
	}
	if open, queued, err := d.st.QuestionCounts(); err == nil {
		ans.QuestionsOpen, ans.QuestionsQueued = open[me], queued[me]
	}
	if a, err := d.st.AgentByKey(me); err == nil && a.HoldSince > 0 {
		ans.Held = true
		ans.HeldCount, _ = d.st.HeldCount(me, a.HoldSince)
	}
	return ans
}
