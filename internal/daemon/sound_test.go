package daemon

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func soundEnv(t *testing.T) (env, func() []string) {
	t.Helper()
	e := start(t)
	var mu sync.Mutex
	var rang []string
	e.d.SetSounder(func(kind string) {
		mu.Lock()
		rang = append(rang, kind)
		mu.Unlock()
	})
	return e, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), rang...)
	}
}

// waitRang polls until n sounds rang or the deadline passes.
func waitRang(rang func() []string, n int) []string {
	deadline := time.Now().Add(5 * time.Second)
	for len(rang()) < n && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	return rang()
}

// hookEv sends a hook event of the agent dev with a payload.
func hookEv(t *testing.T, e env, event string, payload map[string]any) {
	t.Helper()
	if err := call(t, e.bus, "dev", "hook_event", map[string]any{"event": event, "payload": payload}, nil); err != nil {
		t.Fatal(err)
	}
}

// hookTool is a tool event of an agent-type Stop hook's subagent; mainTool one of the agent
// itself, as the lea hook forwards them (an empty agent_id).
func hookTool(t *testing.T, e env, event, id, tool string) {
	t.Helper()
	hookEv(t, e, event, map[string]any{"agent_id": "hook-agent-" + id, "tool_name": tool})
}

func mainTool(t *testing.T, e env, event, tool string) {
	t.Helper()
	hookEv(t, e, event, map[string]any{"agent_id": "", "tool_name": tool})
}

func endTurn(t *testing.T, e env) {
	t.Helper()
	hookEv(t, e, "Stop", nil)
	if err := call(t, e.bus, "dev", "stop_check", nil, nil); err != nil {
		t.Fatal(err)
	}
}

func devState(t *testing.T, e env) string {
	t.Helper()
	var as []agentView
	call(t, e.api, "", "agents_list", nil, &as)
	return as[0].State
}

// A permission request rings once, when the agent starts waiting; a turn that ends rings nothing
// (#294).
func TestSoundsWhenAnAgentWaitsNeverWhenATurnEnds(t *testing.T) {
	e, rang := soundEnv(t)
	hookEv(t, e, "UserPromptSubmit", nil)
	hookEv(t, e, "PermissionRequest", nil)
	hookEv(t, e, "PermissionRequest", nil)
	if got := rang(); len(got) != 1 || got[0] != SoundAttention {
		t.Fatalf("rang %q", got)
	}
	hookEv(t, e, "PostToolUse", nil)
	endTurn(t, e)
	hookEv(t, e, "UserPromptSubmit", nil)
	endTurn(t, e)
	time.Sleep(50 * time.Millisecond)
	if got := rang(); len(got) != 1 {
		t.Fatalf("rang %q: a turn end rang", got)
	}
}

// Agent-type Stop hooks at work after the Stop leave the agent idle, and ring nothing.
func TestStopHooksAtWorkLeaveTheAgentIdle(t *testing.T) {
	e, rang := soundEnv(t)
	endTurn(t, e)
	hookTool(t, e, "PreToolUse", "a", "Glob")
	hookTool(t, e, "PostToolUse", "a", "Glob")
	hookTool(t, e, "PreToolUse", "a", "StructuredOutput")
	hookTool(t, e, "PostToolUse", "a", "StructuredOutput")
	if s := devState(t, e); s != "idle" {
		t.Fatalf("state=%s while the Stop hooks work, want idle", s)
	}
	if got := rang(); len(got) != 0 {
		t.Fatalf("rang %q", got)
	}
}

// A Stop hook blocks the stop and the agent works again (its own tools): the deck shows it
// working, and its real end rings nothing either.
func TestAStopBlockedByAnotherHookWorksAgainSilently(t *testing.T) {
	e, rang := soundEnv(t)
	endTurn(t, e)
	hookTool(t, e, "PreToolUse", "a", "StructuredOutput")
	hookTool(t, e, "PostToolUse", "a", "StructuredOutput")
	mainTool(t, e, "PreToolUse", "Bash")
	if s := devState(t, e); s != "working" {
		t.Fatalf("state=%s, want working", s)
	}
	mainTool(t, e, "PostToolUse", "Bash")
	endTurn(t, e)
	if got := rang(); len(got) != 0 {
		t.Fatalf("rang %q", got)
	}
}

// A background subagent's tools after the Stop do not make the agent work again; a tool event
// without an agent_id (an older lea hook) neither.
func TestOtherToolsAfterTheStopLeaveTheAgentIdle(t *testing.T) {
	e, rang := soundEnv(t)
	endTurn(t, e)
	hookEv(t, e, "PreToolUse", map[string]any{"agent_id": "a1b2c3", "tool_name": "Bash"})
	hookEv(t, e, "PreToolUse", map[string]any{"tool_name": "Glob"})
	if s := devState(t, e); s != "idle" {
		t.Fatalf("state=%s, want idle", s)
	}
	if got := rang(); len(got) != 0 {
		t.Fatalf("rang %q", got)
	}
}

// A stop blocked by waiting messages keeps the agent working: no sound.
func TestABlockedStopDoesNotRing(t *testing.T) {
	e, rang := soundEnv(t)
	call(t, e.bus, "dev", "agents_list", nil, nil)
	call(t, e.api, "", "msg_send", map[string]any{"to": "dev", "body": "ping", "kind": "note"}, nil)
	hookEv(t, e, "Stop", nil)
	var r struct{ Block bool }
	call(t, e.bus, "dev", "stop_check", nil, &r)
	if !r.Block || len(rang()) != 0 {
		t.Fatalf("block %v, rang %q", r.Block, rang())
	}
}

// resetGap lets the next sound ring at once, as if soundGap had passed.
func resetGap(e env) {
	e.d.mu.Lock()
	e.d.rang = map[string]time.Time{}
	e.d.mu.Unlock()
}

// A request an agent sends the user rings; a note, a result, or a request to another agent does
// not (#294).
func TestARequestToTheUserRings(t *testing.T) {
	e, rang := soundEnv(t)
	call(t, e.bus, "dev", "agents_list", nil, nil)
	call(t, e.bus, "rev", "agents_list", nil, nil)
	for _, m := range []map[string]any{
		{"to": "user", "body": "fyi", "kind": "note"},
		{"to": "user", "body": "done", "kind": "result"},
		{"to": "rev", "body": "review L88", "kind": "request"},
	} {
		if err := call(t, e.bus, "dev", "msg_send", m, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := rang(); len(got) != 0 {
		t.Fatalf("rang %q for messages that need nobody", got)
	}
	if err := call(t, e.bus, "dev", "msg_send", map[string]any{"to": "user", "body": "merge or wait?", "kind": "request"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := rang(); len(got) != 1 || got[0] != SoundAttention {
		t.Fatalf("rang %q, want attention", got)
	}
	// The user's own request to an agent rings nothing.
	resetGap(e)
	if err := call(t, e.api, "", "msg_send", map[string]any{"to": "dev", "body": "rebase", "kind": "request"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := rang(); len(got) != 1 {
		t.Fatalf("rang %q for the user's own message", got)
	}
}

// An answer to the user with status needs_user rings; ok and fail do not.
func TestANeedsUserReplyToTheUserRings(t *testing.T) {
	e, rang := soundEnv(t)
	call(t, e.bus, "dev", "agents_list", nil, nil)
	ask := func() int64 {
		var sent struct{ ID int64 }
		if err := call(t, e.api, "", "msg_send", map[string]any{"to": "dev", "body": "deploy?", "kind": "request"}, &sent); err != nil {
			t.Fatal(err)
		}
		return sent.ID
	}
	for _, st := range []string{"ok", "fail"} {
		if err := call(t, e.bus, "dev", "msg_reply", map[string]any{"id": ask(), "status": st, "body": "x"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := rang(); len(got) != 0 {
		t.Fatalf("rang %q", got)
	}
	if err := call(t, e.bus, "dev", "msg_reply", map[string]any{"id": ask(), "status": "needs_user", "body": "which env?"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := rang(); len(got) != 1 || got[0] != SoundAttention {
		t.Fatalf("rang %q, want attention", got)
	}
}

// An agent that reports it is blocked on a human rings once, when blocked_on becomes non-empty:
// the same report again, or another blocker while still blocked, does not ring again; cleared then
// blocked again, it rings again.
func TestAnAgentBlockedOnAHumanRings(t *testing.T) {
	e, rang := soundEnv(t)
	boss := spawned(t, e, "boss", "lead", "")
	spawned(t, e, "kid", "dev", boss)
	upd := func(blocked string) {
		t.Helper()
		if err := call(t, e.bus, "kid", "task_update", map[string]any{"status_text": "migrating", "blocked_on": blocked}, nil); err != nil {
			t.Fatal(err)
		}
		resetGap(e)
	}
	upd("")
	upd("   ")
	if got := rang(); len(got) != 0 {
		t.Fatalf("rang %q without a blocker", got)
	}
	upd("David must approve the prod migration")
	if got := rang(); len(got) != 1 || got[0] != SoundAttention {
		t.Fatalf("rang %q, want attention", got)
	}
	upd("David must approve the prod migration")
	upd("David must give the prod password")
	if got := rang(); len(got) != 1 {
		t.Fatalf("rang %q while still blocked", got)
	}
	upd("")
	upd("David must approve again")
	if got := rang(); len(got) != 2 {
		t.Fatalf("rang %q, want a second sound", got)
	}
}

// Agents waiting together ring once.
func TestSoundsOfOneKindAreSpaced(t *testing.T) {
	e, rang := soundEnv(t)
	e.d.sound(context.Background(), "lea-a", SoundAttention)
	e.d.sound(context.Background(), "lea-a", SoundAttention)
	if got := rang(); len(got) != 1 {
		t.Fatalf("rang %q", got)
	}
	e.d.mu.Lock()
	e.d.rang[SoundAttention] = time.Now().Add(-soundGap)
	e.d.mu.Unlock()
	e.d.sound(context.Background(), "lea-a", SoundAttention)
	if got := rang(); len(got) != 2 {
		t.Fatalf("rang %q", got)
	}
}

// No deck shows the agent's container: neither a wait nor a request to the user rings.
func TestNoSoundWithoutAnAttachedDeck(t *testing.T) {
	e, rang := soundEnv(t)
	fa := &fakeAgents{unattached: true}
	e.d.SetAgents(fa)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "UserPromptSubmit"}, nil)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "PermissionRequest"}, nil)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "PostToolUse"}, nil)
	call(t, e.bus, "dev", "msg_send", map[string]any{"to": "user", "body": "merge?", "kind": "request"}, nil)
	endTurn(t, e)
	if got := rang(); len(got) != 0 {
		t.Fatalf("rang %q without a deck", got)
	}
	fa.mu.Lock()
	asked := strings.Join(fa.calls, ",")
	fa.mu.Unlock()
	if !strings.Contains(asked, "attached lea-a") {
		t.Fatalf("calls %q: the deck of the agent's container was not asked", asked)
	}
}

// With a deck attached, sounds ring as before.
func TestSoundsRingWithAnAttachedDeck(t *testing.T) {
	e, rang := soundEnv(t)
	e.d.SetAgents(&fakeAgents{})
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "PermissionRequest"}, nil)
	endTurn(t, e)
	if got := rang(); len(got) != 1 || got[0] != SoundAttention {
		t.Fatalf("rang %q", got)
	}
}

// The user turns sounds off at runtime: nothing rings until they turn them on again.
func TestSoundsCanBeTurnedOffAndOnAtRuntime(t *testing.T) {
	e, rang := soundEnv(t)
	var r struct{ Sound string }
	if err := call(t, e.api, "", "sound", map[string]any{"on": false}, &r); err != nil || r.Sound != "off" {
		t.Fatalf("off: %+v %v", r, err)
	}
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "PermissionRequest"}, nil)
	e.d.sound(context.Background(), "lea-a", SoundAttention)
	if got := rang(); len(got) != 0 {
		t.Fatalf("rang %q while off", got)
	}
	if err := call(t, e.api, "", "sound", nil, &r); err != nil || r.Sound != "off" {
		t.Fatalf("read: %+v %v", r, err)
	}
	if err := call(t, e.api, "", "sound", map[string]any{"on": true}, &r); err != nil || r.Sound != "on" {
		t.Fatalf("on: %+v %v", r, err)
	}
	resetGap(e)
	e.d.sound(context.Background(), "lea-a", SoundAttention)
	if got := rang(); len(got) != 1 || got[0] != SoundAttention {
		t.Fatalf("rang %q once on", got)
	}
}

// Sounds start off when the daemon is told so (LEA_SOUND=0), and the user can still turn them on.
func TestSoundsCanStartOff(t *testing.T) {
	e, rang := soundEnv(t)
	e.d.SetSoundOn(false)
	e.d.sound(context.Background(), "lea-a", SoundAttention)
	var r struct{ Sound string }
	if call(t, e.api, "", "sound", nil, &r); r.Sound != "off" || len(rang()) != 0 {
		t.Fatalf("sound %q rang %q", r.Sound, rang())
	}
}

// Without a player there is nothing to turn on: the state reads "".
func TestSoundWithoutAPlayer(t *testing.T) {
	e := start(t)
	var r struct{ Sound string }
	if err := call(t, e.api, "", "sound", map[string]any{"on": true}, &r); err != nil || r.Sound != "" {
		t.Fatalf("%+v %v", r, err)
	}
}

// Only the user switches sounds: an agent's bus refuses the method.
func TestAnAgentCannotSwitchSounds(t *testing.T) {
	e, _ := soundEnv(t)
	if err := call(t, e.bus, "dev", "sound", map[string]any{"on": false}, nil); err == nil || !strings.Contains(err.Error(), "host only") {
		t.Fatalf("err=%v", err)
	}
}

// A deck watching the agents sees the sound state, and a switch wakes it.
func TestTheDeckSeesTheSoundState(t *testing.T) {
	e, _ := soundEnv(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	var s struct{ Sound string }
	if err := call(t, e.api, "", "agents_watch", map[string]any{"container": "lea-a", "since": 0, "timeout_s": 1}, &s); err != nil || s.Sound != "on" {
		t.Fatalf("watch %+v %v", s, err)
	}
	fa.mu.Lock()
	before := fa.changed
	fa.mu.Unlock()
	call(t, e.api, "", "sound", map[string]any{"on": false}, nil)
	fa.mu.Lock()
	after := fa.changed
	fa.mu.Unlock()
	if after <= before {
		t.Fatal("the switch did not tell the decks")
	}
	if call(t, e.api, "", "agents_watch", map[string]any{"container": "lea-a", "since": 0, "timeout_s": 1}, &s); s.Sound != "off" {
		t.Fatalf("watch %+v", s)
	}
}

// In lea-ctl (#165) a sound is the terminal bell of each client the list names.
func TestBellSounderRingsEachTerminal(t *testing.T) {
	dir := t.TempDir()
	a, b := dir+"/a", dir+"/b"
	for _, f := range []string{a, b} {
		if err := os.WriteFile(f, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	BellSounder(func() []string { return []string{a, b, dir + "/missing"} })(SoundAttention)
	for _, f := range []string{a, b} {
		if got, _ := os.ReadFile(f); string(got) != "\a" {
			t.Fatalf("%s: %q", f, got)
		}
	}
}
