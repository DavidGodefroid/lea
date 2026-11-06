package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/store"
)

func hookEventWith(t *testing.T, e env, agent, event string, payload map[string]any) {
	t.Helper()
	if err := call(t, e.bus, agent, "hook_event", map[string]any{"event": event, "session_id": "s1", "payload": payload}, nil); err != nil {
		t.Fatal(err)
	}
}

// startOn starts another daemon on st, as a lea daemon that stops and starts again on its store:
// it knows nothing of the previous daemon's hooks but what the store keeps.
func startOn(t *testing.T, st *store.Store) env {
	t.Helper()
	dir := shortDir(t)
	clock := new(atomic.Int64)
	clock.Store(1000)
	d := New(st, filepath.Join(dir, "run"), clock.Load)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	api := filepath.Join(dir, "d.sock")
	l, err := Listen(api)
	if err != nil {
		t.Fatal(err)
	}
	go d.Serve(ctx, l, Origin{})
	bus, err := d.AttachContainer(ctx, "lea-a")
	if err != nil {
		t.Fatal(err)
	}
	return env{d: d, api: api, bus: bus, ctx: ctx, clock: clock}
}

// A session resumed after a container restart sits at its prompt with no Stop: its turn is over,
// so the rewake watch of its SessionStart job wakes it for a message (#297). A new session waits
// for its first prompt instead, and a compaction may run inside a turn: neither is woken.
func TestARewakeWatchWakesAResumedSession(t *testing.T) {
	for _, source := range []string{"resume", "clear"} {
		e := start(t)
		hookEventWith(t, e, "dev", "SessionStart", map[string]any{"source": source})
		w := rewakeWatch(t, e, "dev")
		userSends(t, e, "dev", "your lead answered")
		if r := next(t, w); r.Count != 1 || r.Superseded || !strings.Contains(r.Text, "msg_inbox") {
			t.Fatalf("%s: notice=%+v", source, r)
		}
	}
	for _, source := range []string{"startup", "compact", ""} {
		e := start(t)
		hookEventWith(t, e, "dev", "SessionStart", map[string]any{"source": source})
		w := rewakeWatch(t, e, "dev")
		userSends(t, e, "dev", "hello")
		quiet(t, w, source)
	}
}

// A resumed session given a prompt (lea's resume of a suspended agent) works on it: no wake.
func TestAResumedSessionWithAPromptIsNotWoken(t *testing.T) {
	e := start(t)
	hookEventWith(t, e, "dev", "SessionStart", map[string]any{"source": "resume"})
	hookEvent(t, e, "dev", "UserPromptSubmit")
	w := rewakeWatch(t, e, "dev")
	userSends(t, e, "dev", "hello")
	quiet(t, w, "a resumed turn")
}

// A daemon that starts again lost what the hooks told the previous one: it takes the agents the
// store shows idle as idle, so the rewake job that came back on its bus wakes them (#297). A
// working agent stays in its turn.
func TestARestartedDaemonWakesAnAgentIdleBeforeIt(t *testing.T) {
	e := start(t)
	hookEvent(t, e, "dev", "SessionStart")
	hookEvent(t, e, "dev", "Stop")
	hookEvent(t, e, "rev", "SessionStart")
	hookEvent(t, e, "rev", "UserPromptSubmit")
	e2 := startOn(t, e.d.st)
	idle, working := rewakeWatch(t, e2, "dev"), rewakeWatch(t, e2, "rev")
	userSends(t, e2, "dev", "your lead answered")
	userSends(t, e2, "rev", "while you work")
	if r := next(t, idle); r.Count != 1 || r.Superseded {
		t.Fatalf("notice=%+v", r)
	}
	quiet(t, working, "a working agent after the restart")
}

// A wake is recorded and shows in the deck (store.Agent.Woken) until the woken turn ends or the
// user types.
func TestAWakeShowsUntilItsTurnEnds(t *testing.T) {
	e := start(t)
	hookEvent(t, e, "dev", "SessionStart")
	hookEvent(t, e, "dev", "Stop")
	w := rewakeWatch(t, e, "dev")
	userSends(t, e, "dev", "hello")
	next(t, w)
	if a, err := e.d.st.AgentByKey("lea-a/dev"); err != nil || !a.Woken {
		t.Fatalf("agent=%+v err=%v", a, err)
	}
	if ps, _ := e.d.st.EventPayloads("lea-a/dev", "rewake"); len(ps) != 1 || ps[0] != `{"unread":1}` {
		t.Fatalf("rewake events=%q", ps)
	}
	hookEvent(t, e, "dev", "PreToolUse") // the woken turn reads its inbox
	if a, _ := e.d.st.AgentByKey("lea-a/dev"); !a.Woken {
		t.Fatal("the mark went during the woken turn")
	}
	hookEvent(t, e, "dev", "Stop")
	if a, _ := e.d.st.AgentByKey("lea-a/dev"); a.Woken {
		t.Fatal("the mark stayed after the woken turn")
	}
	w = rewakeWatch(t, e, "dev")
	userSends(t, e, "dev", "again")
	next(t, w)
	hookEvent(t, e, "dev", "UserPromptSubmit") // the user typed
	if a, _ := e.d.st.AgentByKey("lea-a/dev"); a.Woken {
		t.Fatal("the mark stayed after the user typed")
	}
}

// An agent stopped on its usage limit is not woken by a message (its own resume comes first);
// the watch holds, looks again later and wakes it once the limit is over.
func TestARewakeWaitsForALimitedAgent(t *testing.T) {
	e := start(t)
	tm := newManualTimers(t)
	tm.use(e.d)
	hookEvent(t, e, "dev", "SessionStart")
	hookEvent(t, e, "dev", "Stop")
	if err := e.d.st.SetLimit("lea-a/dev", 5000, 0); err != nil {
		t.Fatal(err)
	}
	w := rewakeWatch(t, e, "dev")
	userSends(t, e, "dev", "hello")
	quiet(t, w, "limited")
	if ds := tm.delays(); len(ds) == 0 || ds[len(ds)-1] != pausedRecheck {
		t.Fatalf("recheck delays=%v", ds)
	}
	if err := e.d.st.SetLimit("lea-a/dev", 0, 0); err != nil {
		t.Fatal(err)
	}
	for len(tm.delays()) > 0 {
		tm.fire(t)
	}
	if r := next(t, w); r.Count != 1 {
		t.Fatalf("notice=%+v", r)
	}
}

// A paused agent is not woken; letting it continue (the deck's p) wakes the held watch at once.
func TestARewakeWaitsForAPausedAgent(t *testing.T) {
	e := start(t)
	tm := newManualTimers(t)
	tm.use(e.d)
	e.d.SetAgents(&fakeAgents{})
	if _, err := e.d.st.RegisterAgent("lea-a", "dev", "default", "/work", 1000); err != nil {
		t.Fatal(err)
	}
	hookEvent(t, e, "dev", "SessionStart")
	hookEvent(t, e, "dev", "Stop")
	if err := e.d.st.SetPaused("lea-a/dev", true); err != nil {
		t.Fatal(err)
	}
	w := rewakeWatch(t, e, "dev")
	userSends(t, e, "dev", "hello")
	quiet(t, w, "paused")
	if err := e.d.st.SetPaused("lea-a/dev", false); err != nil {
		t.Fatal(err)
	}
	if err := call(t, e.api, "", "agent_pause", map[string]any{"Key": "lea-a/dev", "Paused": false}, nil); err != nil {
		t.Fatal(err)
	}
	if r := next(t, w); r.Count != 1 {
		t.Fatalf("notice=%+v", r)
	}
}

// Two agents that keep writing to each other cannot keep each other awake: past rewakeBurst
// wakes within rewakeWindow, the next wake waits until the oldest leaves the window.
func TestRewakesOfOneAgentAreRateLimited(t *testing.T) {
	defer func(b int, w int64) { rewakeBurst, rewakeWindow = b, w }(rewakeBurst, rewakeWindow)
	rewakeBurst, rewakeWindow = 2, 600
	e := start(t)
	tm := newManualTimers(t)
	tm.use(e.d)
	hookEvent(t, e, "dev", "SessionStart")
	wakeOnce := func(body string) <-chan watchNotice {
		t.Helper()
		hookEvent(t, e, "dev", "Stop")
		w := rewakeWatch(t, e, "dev")
		userSends(t, e, "dev", body)
		return w
	}
	for i, body := range []string{"one", "two"} {
		if r := next(t, wakeOnce(body)); r.Count != 1 {
			t.Fatalf("wake %d: %+v", i, r)
		}
		var ms []delivered
		call(t, e.bus, "dev", "msg_inbox", nil, &ms)
		hookEvent(t, e, "dev", "UserPromptSubmit")
		e.clock.Add(10)
	}
	w := wakeOnce("three")
	quiet(t, w, "the third wake within the window")
	ds := tm.delays()
	if len(ds) == 0 || ds[len(ds)-1] != 580*time.Second {
		t.Fatalf("recheck delays=%v", ds)
	}
	e.clock.Add(580)
	for len(tm.delays()) > 0 {
		tm.fire(t)
	}
	if r := next(t, w); r.Count != 1 {
		t.Fatalf("notice=%+v", r)
	}
}
