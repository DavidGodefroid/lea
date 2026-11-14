package daemon

import (
	"strings"
	"testing"

	"github.com/DavidGodefroid/lea/internal/limits"
)

// budgetEnv: a daemon whose agent manager records pauses in the store, with live limits.
func budgetEnv(t *testing.T) (env, *shedAgents) {
	t.Helper()
	e := start(t)
	sa := &shedAgents{fakeAgents: &fakeAgents{}, st: e.d.st}
	e.d.SetAgents(sa)
	if err := e.d.SetLimits(newMapLimits()); err != nil {
		t.Fatal(err)
	}
	return e, sa
}

func setLimit(t *testing.T, e env, key, v string) {
	t.Helper()
	if err := call(t, e.api, "", "limits_set", map[string]any{"key": key, "value": v}, nil); err != nil {
		t.Fatal(err)
	}
}

func costReport(t *testing.T, e env, agent string, cost float64) {
	t.Helper()
	if err := call(t, e.bus, agent, "status_report", map[string]any{"ctx_pct": 10, "cost_usd": cost}, nil); err != nil {
		t.Fatal(err)
	}
}

// notes are the notes lea sent to the agent key.
func notes(t *testing.T, e env, key string) []string {
	t.Helper()
	ms, err := e.d.st.Unread(key, 50)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range ms {
		if m.Kind == "note" && m.From == "user" {
			out = append(out, m.Body)
		}
	}
	return out
}

// settleBudget waits until no queued spawn is starting and no budget pause or resume runs.
func settleBudget(t *testing.T, e env) {
	t.Helper()
	e.d.budgetWG.Wait()
	settle(t, e)
}

func budgetLabel(e env, key string) string {
	e.d.mu.Lock()
	defer e.d.mu.Unlock()
	return e.d.budgetLabelLocked(key)
}

// An agent whose cost crosses budget_usd_agent is paused once, its lead gets one note and the
// board carries the reason; under the budget, or with the key at 0, nothing happens.
func TestBudgetAgentPausesOnce(t *testing.T) {
	e, sa := budgetEnv(t)
	boss := spawned(t, e, "boss", "lead", "")
	kid := spawned(t, e, "kid", "dev", boss)
	// Off (0): no spend pauses.
	costReport(t, e, "kid", 500)
	settleBudget(t, e)
	if sa.log() != "" {
		t.Fatalf("off: %s", sa.log())
	}
	setLimit(t, e, "budget_usd_agent", "600")
	costReport(t, e, "kid", 599.99)
	if sa.log() != "" || budgetLabel(e, kid) != "" {
		t.Fatalf("under: %s", sa.log())
	}
	if err := call(t, e.bus, "kid", "task_update", map[string]any{"status_text": "working"}, nil); err != nil {
		t.Fatal(err)
	}
	costReport(t, e, "kid", 612.4)
	waitFor(t, func() bool { return sa.log() == "pause "+kid })
	want := "budget reached: $612.40 ≥ budget_usd_agent $600"
	waitFor(t, func() bool { return len(notes(t, e, boss)) == 1 })
	if n := notes(t, e, boss); !strings.Contains(n[0], "lea paused kid, its session kept: "+want) {
		t.Fatalf("note=%q", n)
	}
	if got := budgetLabel(e, kid); got != want {
		t.Fatalf("label=%q", got)
	}
	var b boardView
	if err := call(t, e.api, "", "board_list", nil, &b); err != nil {
		t.Fatal(err)
	}
	if len(b.Items) != 1 || b.Items[0].Budget != want {
		t.Fatalf("board=%+v", b.Items)
	}
	// It reports again (its status line still runs): no second pause, no second note.
	costReport(t, e, "kid", 613)
	settleBudget(t, e)
	if sa.log() != "pause "+kid || len(notes(t, e, boss)) != 1 {
		t.Fatalf("again: %s, %d notes", sa.log(), len(notes(t, e, boss)))
	}
}

// budget_usd_total sums the agents' spend since the reset point and survives a cost counter that
// goes back to 0; crossing it pauses every running agent of known cost and holds new spawns
// (refused, or queued with queue: true). Raising it resumes them and drains the queue; reset
// starts a new period.
func TestBudgetTotalPausesAllAndHoldsSpawns(t *testing.T) {
	e, sa := budgetEnv(t)
	boss := spawned(t, e, "boss", "lead", "")
	k1 := spawned(t, e, "kid1", "dev", boss)
	k2 := spawned(t, e, "kid2", "dev", boss)
	setLimit(t, e, "budget_usd_total", "20")
	costReport(t, e, "kid1", 8)
	costReport(t, e, "kid1", 0.5) // a new session: its counter starts again
	costReport(t, e, "kid1", 3)
	e.d.mu.Lock()
	spent := e.d.totalSpentLocked()
	e.d.mu.Unlock()
	if spent != 11 || sa.log() != "" {
		t.Fatalf("spent=%v log=%s", spent, sa.log())
	}
	costReport(t, e, "kid2", 9)
	waitFor(t, func() bool {
		l := sa.log()
		return strings.Contains(l, "pause "+k1) && strings.Contains(l, "pause "+k2)
	})
	if strings.Contains(sa.log(), boss) {
		t.Fatalf("the lead, of unknown cost, was paused: %s", sa.log())
	}
	if l := budgetLabel(e, k1); l != "budget reached: total $20 ≥ budget_usd_total $20" {
		t.Fatalf("label=%q", l)
	}
	err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"brief": "x"}, nil)
	if err == nil || !strings.Contains(err.Error(), "budget reached: total spend $20 ≥ budget_usd_total $20") || newCalled(sa.fakeAgents) {
		t.Fatalf("spawn: %v", err)
	}
	var r struct {
		Queued int64
		Reason string
	}
	if err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"brief": "x", "queue": true}, &r); err != nil || r.Queued == 0 {
		t.Fatalf("queued: %+v %v", r, err)
	}
	var b boardView
	if err := call(t, e.api, "", "board_list", nil, &b); err != nil {
		t.Fatal(err)
	}
	if len(b.Queued) != 1 || b.Queued[0].Reason != "queued: budget_usd_total $20/$20" {
		t.Fatalf("board queued=%+v", b.Queued)
	}
	// Raised: lea resumes what it paused, and the queued spawn starts.
	setLimit(t, e, "budget_usd_total", "50")
	waitFor(t, func() bool {
		l := sa.log()
		return strings.Contains(l, "resume "+k1) && strings.Contains(l, "resume "+k2)
	})
	waitFor(t, func() bool { return newCalled(sa.fakeAgents) })
	settleBudget(t, e)
	if budgetLabel(e, k1) != "" {
		t.Fatal("label kept after the resume")
	}
	// Reached again, then a new period: what was spent no longer counts.
	costReport(t, e, "kid2", 40)
	waitFor(t, func() bool { return strings.Count(sa.log(), "pause ") == 4 })
	if err := call(t, e.api, "", "limits_set", map[string]any{"key": "budget_usd_total", "reset": true}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return strings.Count(sa.log(), "resume ") == 4 })
	e.d.mu.Lock()
	spent = e.d.totalSpentLocked()
	v := e.d.limitLocked(mustDef(t, "budget_usd_total"))
	e.d.mu.Unlock()
	if spent != 0 || v != "50" {
		t.Fatalf("after reset: spent=%v value=%s", spent, v)
	}
	costReport(t, e, "kid2", 41) // the new period counts from here
	e.d.mu.Lock()
	spent = e.d.totalSpentLocked()
	e.d.mu.Unlock()
	if spent != 1 {
		t.Fatalf("new period: spent=%v", spent)
	}
}

// An agent resumed by hand over its budget is not paused again before it spends budget_usd_step
// percent of the budget more.
func TestBudgetHandResumeWaitsAStep(t *testing.T) {
	e, sa := budgetEnv(t)
	kid := spawned(t, e, "kid", "dev", "")
	setLimit(t, e, "budget_usd_agent", "10")
	costReport(t, e, "kid", 10.5)
	waitFor(t, func() bool { return sa.log() == "pause "+kid })
	settleBudget(t, e)
	// The user resumes it (the deck's p).
	if err := e.d.st.SetPaused(kid, false); err != nil {
		t.Fatal(err)
	}
	costReport(t, e, "kid", 12.4) // under 10.5 + 20 % of 10
	settleBudget(t, e)
	if sa.log() != "pause "+kid || budgetLabel(e, kid) != "" {
		t.Fatalf("paused again too soon: %s", sa.log())
	}
	costReport(t, e, "kid", 12.5)
	waitFor(t, func() bool { return sa.log() == "pause "+kid+", pause "+kid })
	// A larger step, set live, applies to the next hand resume.
	waitFor(t, func() bool { return budgetLabel(e, kid) != "" })
	settleBudget(t, e)
	setLimit(t, e, "budget_usd_step", "50")
	if err := e.d.st.SetPaused(kid, false); err != nil {
		t.Fatal(err)
	}
	costReport(t, e, "kid", 17.4)
	settleBudget(t, e)
	if strings.Count(sa.log(), "pause ") != 2 {
		t.Fatalf("step 50: %s", sa.log())
	}
	costReport(t, e, "kid", 17.5)
	waitFor(t, func() bool { return strings.Count(sa.log(), "pause ") == 3 })
}

// An agent of unknown cost (-1) is never paused; limits_get lists it as not covered, with the
// budgets and the spend.
func TestBudgetUnknownCostNotCovered(t *testing.T) {
	e, sa := budgetEnv(t)
	spawned(t, e, "dark", "dev", "")
	known := spawned(t, e, "lit", "dev", "")
	setLimit(t, e, "budget_usd_agent", "1")
	setLimit(t, e, "budget_usd_total", "2")
	costReport(t, e, "dark", -1)
	costReport(t, e, "lit", 0.5)
	settleBudget(t, e)
	if sa.log() != "" {
		t.Fatalf("log=%s", sa.log())
	}
	var r struct {
		Limits []struct {
			Key, Value string
			Spent      *float64 `json:"spent_usd"`
		}
		Budget budgetView
	}
	if err := call(t, e.bus, "lit", "limits_get", nil, &r); err != nil {
		t.Fatal(err)
	}
	if strings.Join(r.Budget.NotCovered, ",") != "dark" || r.Budget.Total != 0.5 || r.Budget.Limit != 2 || r.Budget.Reached {
		t.Fatalf("budget=%+v", r.Budget)
	}
	got := map[string]string{}
	for _, l := range r.Limits {
		if l.Spent != nil {
			got[l.Key] = l.Value + "/" + usd(*l.Spent)
		}
	}
	if got["budget_usd_agent"] != "1/$0.50" || got["budget_usd_total"] != "2/$0.50" {
		t.Fatalf("limits=%v", got)
	}
	costReport(t, e, "lit", 1.5)
	waitFor(t, func() bool { return sa.log() == "pause "+known })
	costReport(t, e, "dark", -1)
	settleBudget(t, e)
	if sa.log() != "pause "+known {
		t.Fatalf("log=%s", sa.log())
	}
	// An agent cannot raise its own budget.
	if err := call(t, e.bus, "lit", "limits_set", map[string]any{"key": "budget_usd_agent", "value": "100"}, nil); err == nil || !strings.Contains(err.Error(), "host only") {
		t.Fatalf("agent set: %v", err)
	}
}

// The spend of the period and what lea paused outlive a daemon restart.
func TestBudgetStateOutlivesARestart(t *testing.T) {
	e, sa := budgetEnv(t)
	kid := spawned(t, e, "kid", "dev", "")
	setLimit(t, e, "budget_usd_total", "5")
	costReport(t, e, "kid", 6)
	waitFor(t, func() bool { return sa.log() == "pause "+kid })
	settleBudget(t, e)
	e.d.mu.Lock()
	e.d.budget = nil // a new daemon reads it back from its run directory
	spent := e.d.totalSpentLocked()
	label := e.d.budgetLabelLocked(kid)
	e.d.mu.Unlock()
	if spent != 6 || label == "" {
		t.Fatalf("spent=%v label=%q", spent, label)
	}
}

func mustDef(t *testing.T, name string) limits.Def {
	t.Helper()
	d, err := limits.Lookup(name)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
