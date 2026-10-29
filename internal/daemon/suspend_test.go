package daemon

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/store"
)

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
	hookEvent(t, e, "kid", "UserPromptSubmit")
	if err := e.d.suspend(kid); err != nil {
		t.Fatal(err)
	}
	hookEvent(t, e, "kid", "PostToolUse")
	if err := call(t, e.bus, "kid", "hook_event", map[string]any{"event": "Notification", "session_id": "s1",
		"payload": map[string]any{"notification_type": "idle_prompt"}}, nil); err != nil {
		t.Fatal(err)
	}
	hookEvent(t, e, "kid", "Stop")
	if s := agentState(t, e, kid); s != store.StateSuspendedWarm {
		t.Fatalf("state %s, want %s", s, store.StateSuspendedWarm)
	}
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
	if err := e.d.suspend(kid); err != nil {
		t.Fatal(err)
	}
	hookEvent(t, e, "kid", "UserPromptSubmit")
	if s := agentState(t, e, kid); s != "working" {
		t.Fatalf("state %s after a new turn, want working", s)
	}
	time.Sleep(300 * time.Millisecond)
	noCall(t, fa, "softstop")
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
	if err := e.d.st.SetState(warm, store.StateSuspendedWarm, "", *e.clock-600); err != nil {
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
