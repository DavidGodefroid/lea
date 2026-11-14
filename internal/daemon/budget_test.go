package daemon

import (
	"strings"
	"testing"
)

// usageReport sends agent's status report with the 5-hour (and, when week >= 0, the 7-day) usage.
func usageReport(t *testing.T, e env, agent string, pct int, resets int64, week int) {
	t.Helper()
	ls := []map[string]any{{"window": "5h", "pct": pct, "resets_at": resets}}
	if week >= 0 {
		ls = append(ls, map[string]any{"window": "7d", "pct": week, "resets_at": resets + 86400})
	}
	if err := call(t, e.bus, agent, "status_report", map[string]any{"ctx_pct": 1, "cost_usd": -1, "limits": ls}, nil); err != nil {
		t.Fatal(err)
	}
}

func usage5h(e env) (acctWindow, bool) {
	e.d.mu.Lock()
	defer e.d.mu.Unlock()
	return e.d.usageLocked("5h")
}

func setBudget(t *testing.T, e env, v string) {
	t.Helper()
	if err := call(t, e.api, "", "limits_set", map[string]any{"key": "budget_5h_pct", "value": v}, nil); err != nil {
		t.Fatal(err)
	}
}

func budgetNotes(t *testing.T, e env, lead string) int {
	t.Helper()
	n := 0
	for _, m := range inboxOf(t, e, lead) {
		if m.Kind == "note" && strings.Contains(m.Text, "budget_5h_pct") {
			n++
		}
	}
	return n
}

// The account's usage keeps the newest report: a later window, or more usage in the same one; an
// older status line (an earlier window, or less usage in the same window) does not overwrite it.
// A window whose reset passed is unknown.
func TestAccountUsageKeepsTheNewestReport(t *testing.T) {
	e := start(t)
	spawned(t, e, "a", "dev", "")
	spawned(t, e, "b", "dev", "")
	if _, ok := usage5h(e); ok {
		t.Fatal("usage known before any report")
	}
	usageReport(t, e, "a", 40, 5000, 10)
	usageReport(t, e, "b", 30, 5030, -1) // b's status line predates a's last request
	if w, _ := usage5h(e); w.pct != 40 || w.resetsAt != 5000 {
		t.Fatalf("an older report overwrote: %+v", w)
	}
	usageReport(t, e, "b", 55, 5010, -1)
	if w, _ := usage5h(e); w.pct != 55 {
		t.Fatalf("a newer report was dropped: %+v", w)
	}
	usageReport(t, e, "a", 90, 2000, -1) // an earlier window
	if w, _ := usage5h(e); w.pct != 55 {
		t.Fatalf("an earlier window overwrote: %+v", w)
	}
	usageReport(t, e, "a", 5, 23000, -1) // the window reset: a later one
	if w, _ := usage5h(e); w.pct != 5 || w.resetsAt != 23000 {
		t.Fatalf("the next window was dropped: %+v", w)
	}
	e.d.mu.Lock()
	week, ok := e.d.usageLocked("7d")
	e.d.mu.Unlock()
	if !ok || week.pct != 10 {
		t.Fatalf("7d=%+v %v", week, ok)
	}
	e.clock.Store(23000)
	if w, ok := usage5h(e); ok {
		t.Fatalf("a window that reset is still known: %+v", w)
	}
	if v := e.d.usageView(); v == nil || v.FiveHour != nil || v.SevenDay == nil {
		t.Fatalf("view=%+v", v)
	}
}

// Above budget_5h_pct a spawn is refused without queue and queued with it; at 0, below it or with
// unknown usage it proceeds. limits_get answers the usage, read-only.
func TestSpawnAboveTheBudget(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	e.d.SetLimits(newMapLimits())
	boss := spawned(t, e, "boss", "lead", "")
	spawned(t, e, "kid", "dev", boss)
	// Unknown usage never holds.
	if err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"brief": "x"}, nil); err != nil {
		t.Fatalf("unknown usage: %v", err)
	}
	e.d.rates = rates{}
	usageReport(t, e, "kid", 64, 9000, 18)
	err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"brief": "x"}, nil)
	if err == nil || !strings.Contains(err.Error(), "5h usage 64 % ≥ budget_5h_pct 60: retry with queue: true") {
		t.Fatalf("above: err=%v", err)
	}
	var r struct {
		Queued int64
		Reason string
	}
	if err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"brief": "x", "queue": true}, &r); err != nil {
		t.Fatal(err)
	}
	if r.Queued == 0 || !strings.Contains(r.Reason, "5h usage 64 % ≥ budget_5h_pct 60") {
		t.Fatalf("queued=%+v", r)
	}
	var b boardView
	if err := call(t, e.api, "", "board_list", nil, &b); err != nil {
		t.Fatal(err)
	}
	if len(b.Queued) != 1 || b.Queued[0].Reason != "queued: 5h usage 64 % ≥ budget_5h_pct 60" {
		t.Fatalf("board queued=%+v", b.Queued)
	}
	var g struct {
		Limits []limitOut
		Usage  struct {
			FiveHour struct{ Pct int } `json:"five_hour"`
			SevenDay struct{ Pct int } `json:"seven_day"`
			Budget   int
			Over     bool
		}
	}
	if err := call(t, e.bus, "boss", "limits_get", nil, &g); err != nil {
		t.Fatal(err)
	}
	if g.Usage.FiveHour.Pct != 64 || g.Usage.SevenDay.Pct != 18 || g.Usage.Budget != 60 || !g.Usage.Over {
		t.Fatalf("limits_get usage=%+v", g.Usage)
	}
	for _, l := range g.Limits {
		if l.Key == "budget_5h_pct" && (l.Value != "60" || l.InUse == nil || *l.InUse != 64) {
			t.Fatalf("budget_5h_pct=%+v", l)
		}
	}
	// 0: off.
	e.d.mu.Lock()
	e.d.spawnQueue = nil // the queued one would start now
	e.d.mu.Unlock()
	setBudget(t, e, "0")
	e.d.rates = rates{}
	if err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"brief": "x"}, nil); err != nil {
		t.Fatalf("off: %v", err)
	}
	// Below it.
	setBudget(t, e, "65")
	e.d.rates = rates{}
	if err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"brief": "x"}, nil); err != nil {
		t.Fatalf("below: %v", err)
	}
}

// Raising the budget above the usage starts the queued spawn at once; so does a usage drop (the
// window reset). The leads hear once per crossing, not per spawn attempt, and no running agent
// is stopped.
func TestBudgetDrainsTheQueueAndNotesOncePerCrossing(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	e.d.SetLimits(newMapLimits())
	boss := spawned(t, e, "boss", "lead", "")
	spawned(t, e, "boss2", "lead", "")
	spawned(t, e, "kid", "dev", boss)
	withPressure(t, e)
	usageReport(t, e, "kid", 70, 9000, -1)
	usageReport(t, e, "kid", 75, 9000, -1) // still over: no second note
	for i := 0; i < 2; i++ {
		e.d.rates = rates{}
		if err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"brief": "x", "queue": true}, nil); err != nil {
			t.Fatal(err)
		}
	}
	e.d.samplePressure(e.ctx)
	settle(t, e)
	if newCalled(fa) {
		t.Fatal("started above the budget")
	}
	if n, n2 := budgetNotes(t, e, "boss"), budgetNotes(t, e, "boss2"); n != 1 || n2 != 1 {
		t.Fatalf("notes boss=%d boss2=%d, want 1 each", n, n2)
	}
	fa.mu.Lock()
	for _, c := range fa.calls {
		if strings.HasPrefix(c, "stop") || strings.HasPrefix(c, "softstop") || strings.HasPrefix(c, "park") || strings.HasPrefix(c, "paused") || strings.HasPrefix(c, "end") {
			fa.mu.Unlock()
			t.Fatalf("the budget acted on a running agent: %q", fa.calls)
		}
	}
	fa.mu.Unlock()
	if a, _ := e.d.st.AgentByKey("lea-a/kid"); a.State == "done" || a.State == "stopped" {
		t.Fatalf("kid is %s", a.State)
	}
	// The user raises the budget above the usage: the first queued spawn starts at once.
	setBudget(t, e, "80")
	waitFor(t, func() bool { return newCount(fa) == 1 })
	settle(t, e)
	// Lowered again under the usage: a new crossing, one more note each.
	setBudget(t, e, "60")
	if n := budgetNotes(t, e, "boss"); n != 1 {
		t.Fatalf("second crossing: %d new notes, want 1", n)
	}
	e.d.samplePressure(e.ctx)
	settle(t, e)
	if n := newCount(fa); n != 1 {
		t.Fatalf("started while over again: %d", n)
	}
	// The window resets: the next report shows little usage, and the queued spawn starts.
	usageReport(t, e, "kid", 3, 27000, -1)
	waitFor(t, func() bool { return newCount(fa) == 2 })
	settle(t, e)
}

// A window whose reset passed without any new report lets the held spawns start at the next
// pressure sample.
func TestBudgetWindowResetDrainsWithoutAReport(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	e.d.SetLimits(newMapLimits())
	boss := spawned(t, e, "boss", "lead", "")
	spawned(t, e, "kid", "dev", boss)
	withPressure(t, e)
	usageReport(t, e, "kid", 90, 5000, -1)
	if err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"brief": "x", "queue": true}, nil); err != nil {
		t.Fatal(err)
	}
	e.d.samplePressure(e.ctx)
	settle(t, e)
	if newCalled(fa) {
		t.Fatal("started above the budget")
	}
	e.clock.Store(5000)
	e.d.samplePressure(e.ctx)
	waitFor(t, func() bool { return newCount(fa) == 1 })
	settle(t, e)
}

func TestNewerWindow(t *testing.T) {
	for _, c := range []struct {
		k, r acctWindow
		want bool
	}{
		{acctWindow{50, 1000}, acctWindow{40, 1000}, false},
		{acctWindow{50, 1000}, acctWindow{50, 1060}, true},
		{acctWindow{50, 1000}, acctWindow{60, 1100}, true},
		{acctWindow{50, 1000}, acctWindow{1, 2000}, true},
		{acctWindow{50, 2000}, acctWindow{99, 1000}, false},
		{acctWindow{50, 0}, acctWindow{10, 0}, true},
		{acctWindow{50, 1000}, acctWindow{10, 0}, true},
	} {
		if got := newerWindow(c.k, c.r); got != c.want {
			t.Errorf("newerWindow(%+v, %+v)=%v, want %v", c.k, c.r, got, c.want)
		}
	}
}
