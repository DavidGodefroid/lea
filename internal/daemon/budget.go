package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/DavidGodefroid/lea/internal/limits"
	"github.com/DavidGodefroid/lea/internal/store"
)

// Spend budgets (#424, #404): on API billing nothing else stops a runaway agent. Two live limits
// (limits.go): budget_usd_agent, the most one agent may spend (its status line's cost_usd), and
// budget_usd_total, the most all agents may spend together since a reset point. Crossing one
// pauses the agent, or every running agent, with the mechanism shedding uses (SetPaused): its
// session and its work are kept, nothing is killed. While the total is reached, new spawns wait
// (queue: true) or are refused. An agent whose cost is unknown (-1) is never paused.
//
// The user resumes by raising the limit (lea resumes the agents it paused), lea limits reset
// budget_usd_total (a new period), or by resuming an agent by hand (the deck's p): that agent is
// not paused again before it spends budget_usd_step percent more of the budget it crossed.

// budgetFile keeps the spend of the period and the agents lea paused across a daemon restart.
const budgetFile = "budget.json"

// budgetRetry is how long lea waits before it tries again to pause an agent whose pause failed.
const budgetRetry = time.Minute

// budgetSpend is one agent's spend in the period: Last is the cost its status line last
// reported, Acc what it spent since the period began. A cost that goes down (a new session's
// counter) adds its whole value: the spend before it is kept.
type budgetSpend struct {
	Last float64 `json:"last"`
	Acc  float64 `json:"acc"`
}

// budgetPause is an agent lea paused on a budget: which one ("agent" or "total") and why.
type budgetPause struct {
	Budget string `json:"budget"`
	Reason string `json:"reason"`
	At     int64  `json:"at"`
	// Cost and Total are the agent's cost and the total when lea paused it: a frozen agent
	// spends nothing, so a hand resume starts its grace from there.
	Cost  float64 `json:"cost"`
	Total float64 `json:"total"`
	// pending: the pause is in flight; the agent's row may not say paused yet.
	pending bool
}

// budgetGrace is where a hand resume leaves an agent over a budget: its cost (Agent) and the
// total (Total) then, 0 when it was not over that budget. lea pauses it again only once it
// spends budget_usd_step percent of the budget more.
type budgetGrace struct {
	Agent float64 `json:"agent,omitempty"`
	Total float64 `json:"total,omitempty"`
}

type budgetState struct {
	// Since is when the period began (unix seconds).
	Since  int64                   `json:"since"`
	Spend  map[string]*budgetSpend `json:"spend"`
	Paused map[string]*budgetPause `json:"paused"`
	Grace  map[string]budgetGrace  `json:"grace"`
	// failed: when a pause last failed, by agent (memory only).
	failed map[string]time.Time
}

// budgetLocked is the budget state, loaded from the run directory the first time. Callers hold
// d.mu.
func (d *Daemon) budgetLocked() *budgetState {
	if d.budget != nil {
		return d.budget
	}
	b := &budgetState{}
	if raw, err := os.ReadFile(filepath.Join(d.runDir, budgetFile)); err == nil {
		if json.Unmarshal(raw, b) != nil {
			b = &budgetState{}
		}
	}
	if b.Since == 0 {
		b.Since = d.now()
	}
	if b.Spend == nil {
		b.Spend = map[string]*budgetSpend{}
	}
	if b.Paused == nil {
		b.Paused = map[string]*budgetPause{}
	}
	if b.Grace == nil {
		b.Grace = map[string]budgetGrace{}
	}
	for k, s := range b.Spend {
		if s == nil || s.Acc < 0 || s.Last < 0 {
			delete(b.Spend, k)
		}
	}
	for k, p := range b.Paused {
		if p == nil {
			delete(b.Paused, k)
		}
	}
	b.failed = map[string]time.Time{}
	d.budget = b
	return b
}

// saveBudgetLocked writes the budget state. Callers hold d.mu.
func (d *Daemon) saveBudgetLocked() {
	b := d.budgetLocked()
	raw, err := json.Marshal(b)
	if err != nil {
		return
	}
	if err := os.MkdirAll(d.runDir, 0o700); err != nil {
		return
	}
	p := filepath.Join(d.runDir, budgetFile)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err == nil {
		_ = os.Rename(tmp, p)
	}
}

// budgetLimitsLocked is budget_usd_agent, budget_usd_total (0: off) and budget_usd_step (a
// percent). Callers hold d.mu.
func (d *Daemon) budgetLimitsLocked() (agent, total float64, step int) {
	da, _ := limits.Lookup("budget_usd_agent")
	dt, _ := limits.Lookup("budget_usd_total")
	ds, _ := limits.Lookup("budget_usd_step")
	return da.USD(d.limitLocked(da)), dt.USD(d.limitLocked(dt)), ds.Int(d.limitLocked(ds))
}

// usd prints an amount: "$10", "$12.40".
func usd(v float64) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("$%d", int64(v))
	}
	return fmt.Sprintf("$%.2f", v)
}

// observeCostLocked records the cost an agent's status line reports (-1: unknown) in the
// period's spend. Callers hold d.mu.
func (d *Daemon) observeCostLocked(key string, cost float64) {
	if cost < 0 {
		return
	}
	b := d.budgetLocked()
	s := b.Spend[key]
	if s == nil {
		s = &budgetSpend{}
		b.Spend[key] = s
	}
	if s.Last == cost {
		return
	}
	if cost > s.Last {
		s.Acc += cost - s.Last
	} else {
		s.Acc += cost // a new session: its counter started again at 0
	}
	s.Last = cost
	d.saveBudgetLocked()
}

// totalSpentLocked is what all agents spent in the period. Callers hold d.mu.
func (d *Daemon) totalSpentLocked() float64 {
	t := 0.0
	for _, s := range d.budgetLocked().Spend {
		t += s.Acc
	}
	return float64(int64(t*100+0.5)) / 100
}

// budgetGateLocked is nil when a new spawn may start under budget_usd_total, else the refusal.
// Callers hold d.mu.
func (d *Daemon) budgetGateLocked() error {
	_, total, _ := d.budgetLimitsLocked()
	if total <= 0 {
		return nil
	}
	if t := d.totalSpentLocked(); t >= total {
		return fmt.Errorf("budget reached: total spend %s ≥ budget_usd_total %s: the user raises it or starts a new period (lea limits reset budget_usd_total); retry with queue: true",
			usd(t), usd(total))
	}
	return nil
}

// budgetReasonLocked is why a queued spawn waits on the total budget, "" when it does not.
// Callers hold d.mu.
func (d *Daemon) budgetReasonLocked() string {
	_, total, _ := d.budgetLimitsLocked()
	if t := d.totalSpentLocked(); total > 0 && t >= total {
		return fmt.Sprintf("queued: budget_usd_total %s/%s", usd(t), usd(total))
	}
	return ""
}

// budgetAction is one pause or resume a budget check decided.
type budgetAction struct {
	pause      bool
	key, why   string
	parent     string
	name, cost string
}

// budgetCheck pauses the agents over a budget and resumes the ones lea paused that no budget
// holds any more, beside the caller.
func (d *Daemon) budgetCheck() {
	d.mu.Lock()
	a := d.agents
	acts := d.budgetStepLocked(time.Now())
	d.mu.Unlock()
	if a == nil || len(acts) == 0 {
		return
	}
	d.budgetWG.Add(1)
	go func() {
		defer d.budgetWG.Done()
		d.budgetRun(a, acts)
	}()
}

// budgetCosts is the cost each agent's status line last reported (-1: unknown), by key.
func (d *Daemon) budgetCosts(as []store.Agent) map[string]float64 {
	out := map[string]float64{}
	seen := map[string]bool{}
	for _, a := range as {
		if seen[a.Container] {
			continue
		}
		seen[a.Container] = true
		us, err := d.st.Usages(a.Container)
		if err != nil {
			log.Printf("lea daemon: budget: %v", err)
			continue
		}
		for k, u := range us {
			out[k] = u.CostUSD
		}
	}
	return out
}

// budgetLive says a is an agent a budget covers: one lea spawned that still runs.
func budgetLive(a store.Agent) bool {
	return a.Profile != "" && a.State != "done" && a.State != "stopped"
}

// budgetStepLocked decides the pauses and resumes. Callers hold d.mu.
func (d *Daemon) budgetStepLocked(now time.Time) []budgetAction {
	b := d.budgetLocked()
	agentMax, totalMax, step := d.budgetLimitsLocked()
	if agentMax <= 0 && totalMax <= 0 && len(b.Paused) == 0 && len(b.Grace) == 0 {
		return nil
	}
	as, err := d.st.Agents()
	if err != nil {
		log.Printf("lea daemon: budget: %v", err)
		return nil
	}
	costs := d.budgetCosts(as)
	total := d.totalSpentLocked()
	byKey := map[string]store.Agent{}
	for _, a := range as {
		byKey[a.Key] = a
	}
	dirty := false
	// What the user did meanwhile: an agent lea paused that ended or that they resumed by hand.
	for k, p := range b.Paused {
		if p.pending {
			continue
		}
		a, ok := byKey[k]
		switch {
		case !ok || !budgetLive(a):
			delete(b.Paused, k)
			dirty = true
		case !a.Paused:
			g := budgetGrace{}
			if agentMax > 0 && p.Cost >= agentMax {
				g.Agent = p.Cost
			}
			if totalMax > 0 && p.Total >= totalMax {
				g.Total = p.Total
			}
			b.Grace[k] = g
			delete(b.Paused, k)
			dirty = true
			log.Printf("lea daemon: %s resumed by hand over its budget: paused again after %d%% more", k, step)
		}
	}
	overAgent := func(c float64) bool { return agentMax > 0 && c >= agentMax }
	overTotal := totalMax > 0 && total >= totalMax
	// A budget that no longer holds: its grace ends, and what lea paused on it resumes.
	for k, g0 := range b.Grace {
		g := g0
		if g.Agent > 0 && !overAgent(costs[k]) {
			g.Agent = 0
		}
		if g.Total > 0 && !overTotal {
			g.Total = 0
		}
		switch _, ok := byKey[k]; {
		case !ok || g == (budgetGrace{}):
			delete(b.Grace, k)
			dirty = true
		case g != g0:
			b.Grace[k] = g
			dirty = true
		}
	}
	var out []budgetAction
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		a := byKey[k]
		c, known := costs[k]
		if !known {
			c = -1
		}
		if p := b.Paused[k]; p != nil {
			// The cost at the pause, not a later report: a frozen agent spends nothing, and a
			// lower report (from its container) must not lift the pause; only the host does.
			if !p.pending && !overAgent(max(c, p.Cost)) && !overTotal {
				delete(b.Paused, k)
				dirty = true
				out = append(out, budgetAction{key: k, parent: a.Parent, name: a.Name})
			}
			continue
		}
		if !budgetLive(a) || a.Paused || c < 0 {
			continue
		}
		if t, ok := b.failed[k]; ok && now.Sub(t) < budgetRetry {
			continue
		}
		g := b.Grace[k]
		why, which := "", ""
		switch {
		case overAgent(c) && (g.Agent == 0 || c+1e-9 >= g.Agent+agentMax*float64(step)/100):
			why, which = fmt.Sprintf("budget reached: %s ≥ budget_usd_agent %s", usd(c), usd(agentMax)), "agent"
		case overTotal && (g.Total == 0 || total+1e-9 >= g.Total+totalMax*float64(step)/100):
			why, which = fmt.Sprintf("budget reached: total %s ≥ budget_usd_total %s", usd(total), usd(totalMax)), "total"
		default:
			continue
		}
		delete(b.Grace, k)
		b.Paused[k] = &budgetPause{Budget: which, Reason: why, At: d.now(), Cost: c, Total: total, pending: true}
		dirty = true
		out = append(out, budgetAction{pause: true, key: k, why: why, parent: a.Parent, name: a.Name, cost: usd(c)})
	}
	if dirty {
		d.saveBudgetLocked()
	}
	return out
}

// budgetRun runs the pauses and resumes a budget check decided, and tells each agent's lead.
func (d *Daemon) budgetRun(a Agents, acts []budgetAction) {
	again := false
	for _, act := range acts {
		ctx, cancel := context.WithTimeout(context.Background(), shedTimeout)
		err := a.SetPaused(ctx, act.key, act.pause)
		cancel()
		c, _, _ := strings.Cut(act.key, "/")
		if !act.pause {
			if err != nil {
				log.Printf("lea daemon: resume %s under its budget: %v", act.key, err)
				continue
			}
			d.wake(act.key)
			log.Printf("lea daemon: resumed %s: under its budget again", act.key)
			if act.parent != "" {
				d.post("user", act.parent, "note", fmt.Sprintf("lea resumed %s: no spend budget holds it any more.", act.name), 0)
			}
			continue
		}
		d.mu.Lock()
		b := d.budgetLocked()
		if err != nil {
			delete(b.Paused, act.key)
			b.failed[act.key] = time.Now()
		} else if p := b.Paused[act.key]; p != nil {
			p.pending = false
			delete(b.failed, act.key)
		}
		d.saveBudgetLocked()
		d.mu.Unlock()
		if err != nil {
			log.Printf("lea daemon: pause %s on its budget: %v", act.key, err)
			continue
		}
		again = true
		log.Printf("lea daemon: paused %s (%s)", act.key, act.why)
		d.sound(context.Background(), c, SoundAttention)
		if act.parent != "" {
			d.post("user", act.parent, "note", fmt.Sprintf("lea paused %s, its session kept: %s. The user resumes it: lea limits set, lea limits reset budget_usd_total, or p in the deck.",
				act.name, act.why), 0)
		}
	}
	d.changed()
	if again {
		// A limit raised while a pause was in flight resumes it now.
		d.budgetCheck()
	}
}

// budgetResetPeriod starts a new period of budget_usd_total: what the agents spent so far no
// longer counts.
func (d *Daemon) budgetResetPeriod() {
	d.mu.Lock()
	defer d.mu.Unlock()
	b := d.budgetLocked()
	for _, s := range b.Spend {
		s.Acc = 0
	}
	b.Since = d.now()
	d.saveBudgetLocked()
}

// budgetLabelLocked is why lea paused key on a budget, for the deck and the board, "" when it
// did not. Callers hold d.mu.
func (d *Daemon) budgetLabelLocked(key string) string {
	if p := d.budgetLocked().Paused[key]; p != nil {
		return p.Reason
	}
	return ""
}

// budgetView is what limits_get and the deck show of the spend.
type budgetView struct {
	// Total is what all agents spent since Since (unix seconds), Limit budget_usd_total (0: off).
	Total float64 `json:"total_usd"`
	Limit float64 `json:"limit_usd"`
	Since int64   `json:"since"`
	// Reached: the total budget is reached.
	Reached bool `json:"reached,omitempty"`
	// Paused are the agents lea paused on a budget, with why; NotCovered the running agents
	// whose cost is unknown, which no budget pauses.
	Paused     []budgetPausedView `json:"paused,omitempty"`
	NotCovered []string           `json:"not_covered,omitempty"`
}

type budgetPausedView struct {
	Agent  string `json:"agent"`
	Reason string `json:"reason"`
}

// budgetViewLocked is the spend; container "" every container's agents. Callers hold d.mu.
func (d *Daemon) budgetViewLocked(container string) budgetView {
	b := d.budgetLocked()
	_, total, _ := d.budgetLimitsLocked()
	v := budgetView{Total: d.totalSpentLocked(), Limit: total, Since: b.Since}
	v.Reached = total > 0 && v.Total >= total
	as, err := d.st.Agents()
	if err != nil {
		return v
	}
	costs := d.budgetCosts(as)
	for _, a := range as {
		if container != "" && a.Container != container {
			continue
		}
		if p := b.Paused[a.Key]; p != nil {
			v.Paused = append(v.Paused, budgetPausedView{Agent: shortName(a.Key), Reason: p.Reason})
		}
		if c, ok := costs[a.Key]; budgetLive(a) && (!ok || c < 0) {
			v.NotCovered = append(v.NotCovered, shortName(a.Key))
		}
	}
	return v
}
