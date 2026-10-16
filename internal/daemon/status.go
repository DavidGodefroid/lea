package daemon

import (
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
// Only agents report: the user has no status line.
func (d *Daemon) statusReport(o Origin, me string, raw json.RawMessage) (any, error) {
	if o.Container == "" {
		return nil, errors.New("status_report: agents only")
	}
	var p struct {
		CtxPct  int      `json:"ctx_pct"`
		CostUSD float64  `json:"cost_usd"`
		Paths   []string `json:"paths"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
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
	changed, err := d.st.SetUsage(me, store.Usage{CtxPct: p.CtxPct, CostUSD: math.Round(p.CostUSD*100) / 100})
	if err != nil {
		return nil, err
	}
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
				return struct{}{}, nil
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
	return struct{}{}, nil
}
