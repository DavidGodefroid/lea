package daemon

import (
	"strings"
	"sync"
	"testing"
)

// mapLimits is a LimitStore in memory: the user file (file) over the environment (env).
type mapLimits struct {
	mu        sync.Mutex
	file, env map[string]string
	saves     int
}

func newMapLimits() *mapLimits {
	return &mapLimits{file: map[string]string{}, env: map[string]string{}}
}

func (m *mapLimits) Load() (map[string]LimitValue, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]LimitValue{}
	for k, v := range m.env {
		out[k] = LimitValue{v, "environment"}
	}
	for k, v := range m.file {
		out[k] = LimitValue{v, "file"}
	}
	return out, nil
}

func (m *mapLimits) Save(k, v string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.file[k] = v
	m.saves++
	return nil
}

func (m *mapLimits) Reset(k string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.file, k)
	return nil
}

type limitOut struct {
	Key, Env, Value, Default, Source string
	InUse                            *int `json:"in_use"`
}

func getLimit(t *testing.T, e env, sock, agent string) limitOut {
	t.Helper()
	var r struct{ Limits []limitOut }
	if err := call(t, sock, agent, "limits_get", nil, &r); err != nil {
		t.Fatal(err)
	}
	for _, l := range r.Limits {
		if l.Key == "max_running" {
			return l
		}
	}
	t.Fatalf("no max_running in %+v", r.Limits)
	return limitOut{}
}

func setMax(t *testing.T, e env, v string) {
	t.Helper()
	if err := call(t, e.api, "", "limits_set", map[string]any{"key": "max_running", "value": v}, nil); err != nil {
		t.Fatal(err)
	}
}

// limits_set refuses an unknown key, a wrong type and an out-of-range value, and writes nothing
// then; limits_get answers the value, the default and the source.
func TestLimitsSetChecksAndGetAnswers(t *testing.T) {
	e := start(t)
	ls := newMapLimits()
	ls.env["LEA_MAX_RUNNING"] = "5"
	if err := e.d.SetLimits(ls); err != nil {
		t.Fatal(err)
	}
	if l := getLimit(t, e, e.api, ""); l.Value != "5" || l.Default != "3" || l.Source != "environment" || l.Env != "LEA_MAX_RUNNING" || l.InUse == nil || *l.InUse != 0 {
		t.Fatalf("limit=%+v", l)
	}
	for _, c := range []struct {
		params map[string]any
		want   string
	}{
		{map[string]any{"key": "max_spawns", "value": "2"}, `"max_spawns" is not a live limit (one of: max_running, budget_5h_pct)`},
		{map[string]any{"key": "max_running", "value": "two"}, `max_running is a whole number from 0 to 100, not "two"`},
		{map[string]any{"key": "max_running", "value": "101"}, "from 0 to 100"},
		{map[string]any{"key": "max_running", "value": "-1"}, "from 0 to 100"},
		{map[string]any{"key": "max_running", "value": "03"}, "from 0 to 100"},
		{map[string]any{"key": "max_running", "value": ""}, "from 0 to 100"},
		{map[string]any{"key": "max_running", "value": 2}, "invalid parameters"},
	} {
		if err := call(t, e.api, "", "limits_set", c.params, nil); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%v: err=%v, want %q", c.params, err, c.want)
		}
	}
	if ls.saves != 0 {
		t.Fatalf("a refused value was written (%d saves)", ls.saves)
	}
	// The configuration key names it too; the value set is the file's and applies at once.
	if err := call(t, e.api, "", "limits_set", map[string]any{"key": "LEA_MAX_RUNNING", "value": "2"}, nil); err != nil {
		t.Fatal(err)
	}
	if l := getLimit(t, e, e.api, ""); l.Value != "2" || l.Source != "file" || ls.file["LEA_MAX_RUNNING"] != "2" {
		t.Fatalf("limit=%+v file=%v", l, ls.file)
	}
	// reset drops it from the file: the environment applies again.
	if err := call(t, e.api, "", "limits_set", map[string]any{"key": "max_running", "reset": true}, nil); err != nil {
		t.Fatal(err)
	}
	if l := getLimit(t, e, e.api, ""); l.Value != "5" || l.Source != "environment" || len(ls.file) != 0 {
		t.Fatalf("after reset: limit=%+v file=%v", l, ls.file)
	}
	// A value the environment sets that the check refuses gives the default.
	ls.env["LEA_MAX_RUNNING"] = "lots"
	if err := e.d.SetLimits(ls); err != nil {
		t.Fatal(err)
	}
	if l := getLimit(t, e, e.api, ""); l.Value != "3" || l.Source != "default" {
		t.Fatalf("invalid environment: limit=%+v", l)
	}
}

// limits_set is the host's: an agent is refused, even a lead, and reads the limits with
// limits_get only. Without a store, the defaults apply and limits_set says why.
func TestLimitsSetIsHostOnly(t *testing.T) {
	e := start(t)
	if l := getLimit(t, e, e.api, ""); l.Value != "3" || l.Source != "default" {
		t.Fatalf("defaults: %+v", l)
	}
	if err := call(t, e.api, "", "limits_set", map[string]any{"key": "max_running", "value": "2"}, nil); err == nil || !strings.Contains(err.Error(), "no user configuration") {
		t.Fatalf("no store: err=%v", err)
	}
	ls := newMapLimits()
	e.d.SetLimits(ls)
	spawned(t, e, "boss", "lead", "")
	if err := call(t, e.bus, "boss", "limits_set", map[string]any{"key": "max_running", "value": "50"}, nil); err == nil || !strings.Contains(err.Error(), "limits_set: host only") {
		t.Fatalf("agent: err=%v", err)
	}
	if ls.saves != 0 {
		t.Fatal("an agent wrote a limit")
	}
	if l := getLimit(t, e, e.bus, "boss"); l.Value != "3" {
		t.Fatalf("agent read %+v", l)
	}
}

// At the cap, a spawn is refused without queue and queued with it; the user's own sessions, the
// leads and the ended or stopped children do not count. The board and agents_list say why it
// waits.
func TestSpawnAtMaxRunning(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	e.d.SetLimits(newMapLimits())
	boss := spawned(t, e, "boss", "lead", "")
	spawned(t, e, "boss2", "lead", "")
	spawned(t, e, "mine", "dev", "") // the user's own session
	spawned(t, e, "kid1", "dev", boss)
	gone := spawned(t, e, "kid2", "dev", boss)
	stopped := spawned(t, e, "kid3", "reviewer", boss)
	if err := e.d.st.SetState(gone, "done", "", 2); err != nil {
		t.Fatal(err)
	}
	if err := e.d.st.SetState(stopped, "stopped", "", 2); err != nil {
		t.Fatal(err)
	}
	setMax(t, e, "1")
	if l := getLimit(t, e, e.api, ""); *l.InUse != 1 {
		t.Fatalf("in use: %+v", l)
	}
	err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"brief": "x"}, nil)
	if err == nil || !strings.Contains(err.Error(), "1 children already running (limit max_running 1): retry with queue: true or wait") || newCalled(fa) {
		t.Fatalf("err=%v calls=%q", err, fa.calls)
	}
	var r struct {
		Queued int64
		Reason string
	}
	if err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"brief": "x", "label": "lea#1 cart", "queue": true}, &r); err != nil {
		t.Fatal(err)
	}
	if r.Queued != 1 || !strings.Contains(r.Reason, "limit max_running 1") || newCalled(fa) {
		t.Fatalf("result=%+v calls=%q", r, fa.calls)
	}
	var b boardView
	if err := call(t, e.api, "", "board_list", nil, &b); err != nil {
		t.Fatal(err)
	}
	if len(b.Queued) != 1 || b.Queued[0].Reason != "queued: max_running 1/1" || b.Queued[0].Lead != "boss" || b.Queued[0].Label != "lea#1 cart" || b.Queued[0].Role != "dev" {
		t.Fatalf("board queued=%+v", b.Queued)
	}
	var as []agentView
	if err := call(t, e.bus, "boss", "agents_list", nil, &as); err != nil {
		t.Fatal(err)
	}
	last := as[len(as)-1]
	if last.Name != "queued #1" || last.State != "queued" || last.Task != "queued: max_running 1/1" || last.Parent != "boss" || last.Key != "" {
		t.Fatalf("agents_list last=%+v", last)
	}
	// max_running 0: no cap.
	setMax(t, e, "0")
	e.d.rates = rates{}
	settle(t, e)
	if err := call(t, e.bus, "boss2", "agents_spawn", map[string]any{"brief": "y"}, nil); err != nil {
		t.Fatalf("no cap: %v", err)
	}
}

// Lowering max_running below the children that run stops none of them; raising it starts the
// queued spawns up to the new cap: the first at once, then one per pressure sample.
func TestMaxRunningChangeStopsNoneAndDrainsTheQueue(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	e.d.SetLimits(newMapLimits())
	boss := spawned(t, e, "boss", "lead", "")
	for _, n := range []string{"kid1", "kid2", "kid3"} {
		spawned(t, e, n, "dev", boss)
	}
	fp := withPressure(t, e)
	setMax(t, e, "1")
	if got := fa.calls; len(got) != 0 {
		t.Fatalf("lowering the cap called %q", got)
	}
	for _, n := range []string{"kid1", "kid2", "kid3"} {
		if a, _ := e.d.st.AgentByKey("lea-a/" + n); a.State == "done" || a.State == "stopped" {
			t.Fatalf("%s is %s", n, a.State)
		}
	}
	for i := 0; i < 3; i++ {
		e.d.rates = rates{}
		if err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"brief": "x", "queue": true}, nil); err != nil {
			t.Fatal(i, err)
		}
	}
	e.d.samplePressure(e.ctx)
	settle(t, e)
	if newCalled(fa) {
		t.Fatal("started at the cap")
	}
	// The fake manager registers nobody: each start counts while it runs, then the three kids
	// remain. A cap of 5 lets two queued spawns in, never the third.
	fa.newHold = make(chan struct{})
	setMax(t, e, "5")
	waitFor(t, func() bool { return newCount(fa) == 1 })
	e.d.mu.Lock()
	inFlight := e.d.childrenLocked()
	e.d.mu.Unlock()
	if inFlight != 4 {
		t.Fatalf("while the first start runs: %d counted, want 4", inFlight)
	}
	close(fa.newHold)
	settle(t, e)
	fa.mu.Lock()
	fa.newHold = nil
	fa.mu.Unlock()
	// The started agent now runs (the fake registers nobody: register it as the manager would).
	spawned(t, e, "kid4", "dev", boss)
	e.d.samplePressure(e.ctx)
	settle(t, e)
	if n := newCount(fa); n != 2 {
		t.Fatalf("after one sample: %d started, want 2", n)
	}
	spawned(t, e, "kid5", "dev", boss)
	e.d.samplePressure(e.ctx)
	settle(t, e)
	if n := newCount(fa); n != 2 {
		t.Fatalf("at the new cap: %d started, want 2", n)
	}
	var b boardView
	if err := call(t, e.api, "", "board_list", nil, &b); err != nil {
		t.Fatal(err)
	}
	if len(b.Queued) != 1 || b.Queued[0].Reason != "queued: max_running 5/5" {
		t.Fatalf("board queued=%+v", b.Queued)
	}
	_ = fp
}

// A spawn under the cap reserves its start: two spawns that race at max_running - 1 never both
// pass.
func TestMaxRunningReservesTheStart(t *testing.T) {
	e := start(t)
	e.d.SetLimits(newMapLimits())
	boss := spawned(t, e, "boss", "lead", "")
	spawned(t, e, "kid1", "dev", boss)
	setMax(t, e, "2")
	e.d.mu.Lock()
	first := e.d.admitChildLocked("lea-a")
	second := e.d.admitChildLocked("lea-a")
	e.d.mu.Unlock()
	if first != nil || second == nil || !strings.Contains(second.Error(), "2 children already running") {
		t.Fatalf("first=%v second=%v", first, second)
	}
	e.d.endChildStart()
	e.d.mu.Lock()
	again := e.d.admitChildLocked("lea-a")
	e.d.mu.Unlock()
	if again != nil {
		t.Fatalf("after the start ended: %v", again)
	}
	e.d.endChildStart()
}
