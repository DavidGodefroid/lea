package daemon

import (
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/store"
)

// manualTimers replaces a daemon's afterFunc (#278): the timers it arms never fire on their
// own, so a test fires them itself instead of racing the wall clock on a loaded runner. A timer
// the daemon stopped stays recorded: its callback re-checks it is still the current one (its
// map entry, its generation), so firing it is a no-op, like a stopped timer's.
type manualTimers struct {
	mu    sync.Mutex
	armed []manualTimer
}

type manualTimer struct {
	timer *time.Timer
	delay time.Duration
	f     func()
}

func newManualTimers(t *testing.T) *manualTimers {
	m := &manualTimers{}
	t.Cleanup(func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		for _, a := range m.armed {
			a.timer.Stop()
		}
	})
	return m
}

// use replaces the daemon's afterFunc with m's, under the daemon's lock like the other seams.
func (m *manualTimers) use(d *Daemon) {
	d.mu.Lock()
	d.afterFunc = m.after
	d.mu.Unlock()
}

// after arms a timer like time.AfterFunc, but one that never fires: fire runs it.
func (m *manualTimers) after(delay time.Duration, f func()) *time.Timer {
	timer := time.AfterFunc(24*time.Hour, f)
	m.mu.Lock()
	m.armed = append(m.armed, manualTimer{timer, delay, f})
	m.mu.Unlock()
	return timer
}

// delays is the delay of each timer armed so far, oldest first.
func (m *manualTimers) delays() []time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	ds := make([]time.Duration, len(m.armed))
	for i, a := range m.armed {
		ds[i] = a.delay
	}
	return ds
}

// fire runs the oldest armed timer's function, like its delay elapsing, and forgets it.
func (m *manualTimers) fire(t *testing.T) {
	t.Helper()
	m.mu.Lock()
	if len(m.armed) == 0 {
		m.mu.Unlock()
		t.Fatal("no armed timer to fire")
	}
	a := m.armed[0]
	m.armed = m.armed[1:]
	m.mu.Unlock()
	a.f()
}

func agentState(t *testing.T, e env, key string) string {
	t.Helper()
	a, err := e.d.st.AgentByKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return a.State
}

// waitCall waits until the fake agents recorded a call starting with prefix.
func waitCall(t *testing.T, fa *fakeAgents, prefix string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		fa.mu.Lock()
		i := slices.IndexFunc(fa.calls, func(c string) bool { return strings.HasPrefix(c, prefix) })
		var c string
		if i >= 0 {
			c = fa.calls[i]
		}
		fa.mu.Unlock()
		if i >= 0 {
			return c
		}
		if time.Now().After(deadline) {
			t.Fatalf("no call %q in %q", prefix, fa.calls)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func noCall(t *testing.T, fa *fakeAgents, prefix string) {
	t.Helper()
	fa.mu.Lock()
	defer fa.mu.Unlock()
	if i := slices.IndexFunc(fa.calls, func(c string) bool { return strings.HasPrefix(c, prefix) }); i >= 0 {
		t.Fatalf("unexpected call %q", fa.calls[i])
	}
}

// A suspended agent keeps its state through the hooks that still come after the suspending one
// (lea's own PostToolUse, idle_prompt, the soft stop's SessionEnd), and lea stops it, alone,
// once its keepalive is over.
func TestASuspendedAgentIsSoftStoppedAfterItsKeepalive(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{st: e.d.st}
	e.d.SetAgents(fa)
	kid := spawned(t, e, "kid", "dev", "")
	e.d.SetKeepalive(150 * time.Millisecond)
	timers := newManualTimers(t)
	timers.use(e.d)
	hookEvent(t, e, "kid", "UserPromptSubmit")
	if err := e.d.suspend(kid); err != nil {
		t.Fatal(err)
	}
	if ds := timers.delays(); len(ds) != 1 || ds[0] != 150*time.Millisecond {
		t.Fatalf("armed timers %v, want the 150ms keepalive", ds)
	}
	hookEvent(t, e, "kid", "PostToolUse")
	if err := call(t, e.bus, "kid", "hook_event", map[string]any{"event": "Notification", "session_id": "s1",
		"payload": map[string]any{"notification_type": "idle_prompt"}}, nil); err != nil {
		t.Fatal(err)
	}
	if s := agentState(t, e, kid); s != store.StateSuspendedWarm {
		t.Fatalf("state %s, want %s", s, store.StateSuspendedWarm)
	}
	timers.fire(t) // the keepalive ends: lea stops that agent alone
	waitCall(t, fa, "softstop "+kid)
	hookEvent(t, e, "kid", "SessionEnd")
	if s := agentState(t, e, kid); s != store.StateSuspendedCold {
		t.Fatalf("state %s after the soft stop, want %s", s, store.StateSuspendedCold)
	}
}

// A woken agent (a new turn: a decision or a message woke it) is not stopped.
func TestAWokenAgentKeepsRunning(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{st: e.d.st}
	e.d.SetAgents(fa)
	kid := spawned(t, e, "kid", "dev", "")
	e.d.SetKeepalive(100 * time.Millisecond)
	timers := newManualTimers(t)
	timers.use(e.d)
	if err := e.d.suspend(kid); err != nil {
		t.Fatal(err)
	}
	hookEvent(t, e, "kid", "UserPromptSubmit")
	if s := agentState(t, e, kid); s != "working" {
		t.Fatalf("state %s after a new turn, want working", s)
	}
	timers.fire(t) // the keepalive goes by: the new turn cancelled it, nobody is stopped
	noCall(t, fa, "softstop")
	e.d.mu.Lock()
	_, armed := e.d.keepTimers[kid]
	e.d.mu.Unlock()
	if armed {
		t.Fatal("the new turn left the keepalive armed")
	}
}

// A Stop while warm ends a turn the agent was woken for, even one with no tool call: it is idle,
// not stopped by its keepalive.
func TestAStopEndsTheSuspension(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{st: e.d.st}
	e.d.SetAgents(fa)
	kid := spawned(t, e, "kid", "dev", "")
	e.d.SetKeepalive(100 * time.Millisecond)
	timers := newManualTimers(t)
	timers.use(e.d)
	if err := e.d.suspend(kid); err != nil {
		t.Fatal(err)
	}
	if ds := timers.delays(); len(ds) != 1 || ds[0] != 100*time.Millisecond {
		t.Fatalf("armed timers %v, want the 100ms keepalive", ds)
	}
	hookEvent(t, e, "kid", "Stop")
	if s := agentState(t, e, kid); s != "idle" {
		t.Fatalf("state %s after a Stop, want idle", s)
	}
	timers.fire(t) // the keepalive goes by: the Stop cancelled it, nobody is stopped
	noCall(t, fa, "softstop")
	e.d.mu.Lock()
	_, armed := e.d.keepTimers[kid]
	e.d.mu.Unlock()
	if armed {
		t.Fatal("the Stop left the keepalive armed")
	}
}

// A warm agent's turn is over: the rewake watch of its Lock call wakes it on a message.
func TestAMessageReachesAWarmAgentsRewakeWatch(t *testing.T) {
	e := start(t)
	e.d.SetAgents(&fakeAgents{st: e.d.st})
	kid := spawned(t, e, "kid", "dev", "")
	hookEvent(t, e, "kid", "UserPromptSubmit")
	w := rewakeWatch(t, e, "kid")
	if err := e.d.suspend(kid); err != nil {
		t.Fatal(err)
	}
	quiet(t, w, "no message yet")
	if err := call(t, e.api, "", "msg_send", map[string]any{"to": "kid", "body": "ping", "kind": "note"}, nil); err != nil {
		t.Fatal(err)
	}
	if r := next(t, w); r.Count != 1 {
		t.Fatalf("watch %+v", r)
	}
}

// A message to a stopped suspended agent resumes it, with the unread notice as its next turn: a
// lead waiting for a decision still hears its children.
func TestAChildsMessageResumesASuspendedLead(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{st: e.d.st}
	e.d.SetAgents(fa)
	boss := spawned(t, e, "boss", "lead", "")
	spawned(t, e, "kid", "dev", boss)
	if err := e.d.st.SetState(boss, store.StateSuspendedCold, "11111111-2222-3333-4444-555555555555", 2); err != nil {
		t.Fatal(err)
	}
	if err := call(t, e.bus, "kid", "msg_send", map[string]any{"to": "boss", "body": "done", "kind": "result"}, nil); err != nil {
		t.Fatal(err)
	}
	c := waitCall(t, fa, "resume "+boss)
	if !strings.Contains(c, "1 unread message(s) from lea-a/kid") || !strings.Contains(c, "msg_inbox") {
		t.Fatalf("resume prompt %q", c)
	}
	noCall(t, fa, "resume lea-a/kid")
}

// A restarted daemon lost its keepalive timers: the sweep stops the agents whose keepalive went
// by, and resumes the stopped ones that got a message meanwhile.
func TestTheSweepTakesOverTheSuspendedAgents(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{st: e.d.st}
	e.d.SetAgents(fa)
	warm := spawned(t, e, "warm", "dev", "")
	cold := spawned(t, e, "cold", "dev", "")
	if err := e.d.st.SetState(warm, store.StateSuspendedWarm, "", e.clock.Load()-600); err != nil {
		t.Fatal(err)
	}
	if err := e.d.st.SetState(cold, store.StateSuspendedCold, "11111111-2222-3333-4444-555555555555", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.st.Send(store.Message{From: "user", To: cold, Kind: "note", Body: "hi", At: 3}); err != nil {
		t.Fatal(err)
	}
	if err := call(t, e.api, "", "agents_sweep_stacks", map[string]any{"container": "lea-a"}, nil); err != nil {
		t.Fatal(err)
	}
	waitCall(t, fa, "softstop "+warm)
	waitCall(t, fa, "resume "+cold)
}
