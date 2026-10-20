package daemon

import (
	"strings"
	"testing"
	"time"
)

type watchNotice struct {
	Count      int
	Text       string
	Superseded bool
}

func hookEvent(t *testing.T, e env, agent, event string) {
	t.Helper()
	if err := call(t, e.bus, agent, "hook_event", map[string]any{"event": event, "session_id": "s1", "payload": map[string]any{}}, nil); err != nil {
		t.Fatal(err)
	}
}

func rewakeWatch(t *testing.T, e env, agent string) <-chan watchNotice {
	t.Helper()
	out := make(chan watchNotice, 1)
	go func() {
		var r watchNotice
		// An error here is the test ending while the watch waits: the result goes unread.
		_ = call(t, e.bus, agent, "msg_watch", map[string]any{"timeout_s": 10, "rewake": true}, &r)
		out <- r
	}()
	time.Sleep(100 * time.Millisecond) // the watch is registered
	return out
}

func quiet(t *testing.T, ch <-chan watchNotice, what string) {
	t.Helper()
	select {
	case r := <-ch:
		t.Fatalf("%s: the watch returned %+v", what, r)
	case <-time.After(300 * time.Millisecond):
	}
}

func next(t *testing.T, ch <-chan watchNotice) watchNotice {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("the watch never returned")
		return watchNotice{}
	}
}

// The Stop hook's rewake watch ends when a message reaches an agent whose turn is over, so
// Claude Code wakes it: a lead hears its children without the user typing.
func TestRewakeWatchWakesAnIdleAgent(t *testing.T) {
	e := start(t)
	hookEvent(t, e, "dev", "SessionStart")
	hookEvent(t, e, "dev", "Stop")
	w := rewakeWatch(t, e, "dev")
	quiet(t, w, "no message yet")
	userSends(t, e, "dev", "done, see the branch")
	r := next(t, w)
	if r.Count != 1 || r.Superseded || !strings.Contains(r.Text, "msg_inbox") || strings.Contains(r.Text, "see the branch") {
		t.Fatalf("notice=%+v", r)
	}
}

// One rewake watch per agent: each Stop starts one, and the newest ends the older ones without
// a wake, so a message wakes the agent once however many turns it ended.
func TestANewRewakeWatchSupersedesTheOld(t *testing.T) {
	e := start(t)
	hookEvent(t, e, "dev", "SessionStart")
	hookEvent(t, e, "dev", "Stop")
	old := rewakeWatch(t, e, "dev")
	hookEvent(t, e, "dev", "UserPromptSubmit")
	hookEvent(t, e, "dev", "Stop")
	cur := rewakeWatch(t, e, "dev")
	if r := next(t, old); r.Count != 0 || !r.Superseded {
		t.Fatalf("old=%+v", r)
	}
	userSends(t, e, "dev", "hello")
	if r := next(t, cur); r.Count != 1 || r.Superseded {
		t.Fatalf("cur=%+v", r)
	}
}

// A message that arrives while the agent works does not wake it: its hooks deliver it in the
// turn, and the watch keeps waiting.
func TestRewakeWatchIgnoresAMessageMidTurn(t *testing.T) {
	e := start(t)
	hookEvent(t, e, "dev", "SessionStart")
	hookEvent(t, e, "dev", "Stop")
	w := rewakeWatch(t, e, "dev")
	hookEvent(t, e, "dev", "UserPromptSubmit")
	userSends(t, e, "dev", "while you work")
	quiet(t, w, "mid-turn")
}

// An agent waiting on a permission dialog is in a turn: no wake.
func TestRewakeWatchIgnoresABlockedAgent(t *testing.T) {
	e := start(t)
	hookEvent(t, e, "dev", "SessionStart")
	hookEvent(t, e, "dev", "Stop")
	w := rewakeWatch(t, e, "dev")
	hookEvent(t, e, "dev", "UserPromptSubmit")
	hookEvent(t, e, "dev", "PermissionRequest")
	userSends(t, e, "dev", "are you there")
	quiet(t, w, "blocked")
}

// A plain msg_watch (lea inbox --agent --wait) keeps its behaviour: no turn check, no supersede.
func TestPlainWatchIsNotSupersededByARewakeWatch(t *testing.T) {
	e := start(t)
	hookEvent(t, e, "dev", "SessionStart")
	plain := make(chan watchNotice, 1)
	go func() {
		var r watchNotice
		call(t, e.bus, "dev", "msg_watch", map[string]any{"timeout_s": 10}, &r)
		plain <- r
	}()
	time.Sleep(100 * time.Millisecond)
	hookEvent(t, e, "dev", "Stop")
	_ = rewakeWatch(t, e, "dev")
	quiet(t, plain, "a rewake watch started")
	userSends(t, e, "dev", "hi")
	if r := next(t, plain); r.Count != 1 {
		t.Fatalf("plain=%+v", r)
	}
}

// A message that came during a turn wakes the agent once the turn is over, also when no Stop
// fires (the user interrupted it: idle_prompt ends the turn).
func TestRewakeWatchWakesWhenAnInterruptedTurnEnds(t *testing.T) {
	e := start(t)
	hookEvent(t, e, "dev", "SessionStart")
	hookEvent(t, e, "dev", "Stop")
	w := rewakeWatch(t, e, "dev")
	hookEvent(t, e, "dev", "UserPromptSubmit")
	userSends(t, e, "dev", "while you work")
	quiet(t, w, "mid-turn")
	if err := call(t, e.bus, "dev", "hook_event", map[string]any{"event": "Notification", "session_id": "s1",
		"payload": map[string]any{"notification_type": "idle_prompt"}}, nil); err != nil {
		t.Fatal(err)
	}
	if r := next(t, w); r.Count != 1 || r.Superseded {
		t.Fatalf("notice=%+v", r)
	}
}
