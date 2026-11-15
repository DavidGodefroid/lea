package daemon

import "testing"

// blockerOf is the blocker of agent key's open task, as the board and the deck read it.
func blockerOf(t *testing.T, e env, name string) string {
	t.Helper()
	tk, err := e.d.st.TaskOf("lea-a/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return tk.BlockedOn
}

func block(t *testing.T, e env, name string) {
	t.Helper()
	if err := call(t, e.bus, name, "task_update", map[string]any{"status_text": "migrating", "blocked_on": "David must approve"}, nil); err != nil {
		t.Fatal(err)
	}
	if b := blockerOf(t, e, name); b != "David must approve" {
		t.Fatalf("blocker=%q", b)
	}
}

// An agent blocked on a human is unblocked as soon as the human may have answered (#484): the
// user types to it (a prompt starts its turn), or a message from the user, its lead or lea
// reaches it. Its status text stays; the board and the deck no longer show it blocked.
func TestAPromptOrAnAnswerClearsTheBlocker(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	spawned(t, e, "kid", "dev", spawned(t, e, "boss", "lead", ""))
	boss, kid := "boss", "kid"

	block(t, e, kid)
	fa.mu.Lock()
	before := fa.changed
	fa.mu.Unlock()
	if err := call(t, e.bus, kid, "hook_event", map[string]any{"event": "UserPromptSubmit"}, nil); err != nil {
		t.Fatal(err)
	}
	if b := blockerOf(t, e, kid); b != "" {
		t.Fatalf("after a prompt: blocker=%q", b)
	}
	fa.mu.Lock()
	after := fa.changed
	fa.mu.Unlock()
	if after == before {
		t.Fatal("the cleared blocker did not refresh the deck")
	}
	if tk, _ := e.d.st.TaskOf("lea-a/" + kid); tk.StatusText != "migrating" {
		t.Fatalf("status=%q, want it kept", tk.StatusText)
	}

	for _, from := range []struct{ sock, agent string }{{e.api, ""}, {e.bus, boss}} {
		block(t, e, kid)
		if err := call(t, from.sock, from.agent, "msg_send", map[string]any{"to": kid, "body": "approved", "kind": "note"}, nil); err != nil {
			t.Fatal(err)
		}
		if b := blockerOf(t, e, kid); b == "" {
			t.Fatal("a message cleared the blocker before the agent read it")
		}
		if err := call(t, e.bus, kid, "msg_inbox", nil, nil); err != nil {
			t.Fatal(err)
		}
		if b := blockerOf(t, e, kid); b != "" {
			t.Fatalf("after a message from %q: blocker=%q", from.agent, b)
		}
	}

	// lea's own message (a decided request) answers too.
	block(t, e, kid)
	e.d.post("lea", "lea-a/"+kid, "note", "grant g1 approved", 0)
	if err := call(t, e.bus, kid, "msg_wait", map[string]any{"timeout_s": 1}, nil); err != nil {
		t.Fatal(err)
	}
	if b := blockerOf(t, e, kid); b != "" {
		t.Fatalf("after lea's message: blocker=%q", b)
	}
}

// A lead waiting on the user keeps its blocker while its team reports: only the user, its own
// lead or lea can answer it.
func TestAMessageFromAnotherAgentKeepsTheBlocker(t *testing.T) {
	e := start(t)
	lead := spawned(t, e, "boss", "lead", "")
	spawned(t, e, "kid", "dev", lead)
	spawned(t, e, "peer", "dev", lead)
	boss, kid, peer := "boss", "kid", "peer"
	block(t, e, boss)
	for _, from := range []string{kid, peer} {
		if err := call(t, e.bus, from, "msg_send", map[string]any{"to": boss, "body": "done", "kind": "result"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	block(t, e, peer)
	if err := call(t, e.bus, kid, "msg_send", map[string]any{"to": peer, "body": "hi", "kind": "note"}, nil); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{boss, peer} {
		if err := call(t, e.bus, k, "msg_inbox", nil, nil); err != nil {
			t.Fatal(err)
		}
		if b := blockerOf(t, e, k); b != "David must approve" {
			t.Fatalf("%s: blocker=%q after another agent's message", k, b)
		}
	}
}

// A tool that fails after its permission was granted (PostToolUseFailure, #484) takes the agent
// out of blocked: it works on. After Stop, a Stop hook's failed tool leaves the agent idle.
func TestAFailedToolEndsTheBlockedState(t *testing.T) {
	e := start(t)
	state := func() string {
		t.Helper()
		var as []agentView
		if err := call(t, e.api, "", "agents_list", nil, &as); err != nil {
			t.Fatal(err)
		}
		return as[0].State
	}
	ev := func(name string) {
		t.Helper()
		if err := call(t, e.bus, "dev", "hook_event", map[string]any{"event": name, "session_id": "s1"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	ev("UserPromptSubmit")
	ev("PreToolUse")
	ev("PermissionRequest")
	if s := state(); s != "blocked" {
		t.Fatalf("state=%s, want blocked", s)
	}
	ev("PostToolUseFailure")
	if s := state(); s != "working" {
		t.Fatalf("after the failed tool: state=%s, want working", s)
	}
	ev("Stop")
	ev("PermissionRequest") // a Stop hook's own agent asks
	ev("PostToolUseFailure")
	if s := state(); s != "idle" {
		t.Fatalf("after the Stop hook's failed tool: state=%s, want idle", s)
	}
}
