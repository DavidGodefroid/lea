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

// fakeTimers stands for the done-sound timers: the test fires them, nothing waits for the clock.
type fakeTimers struct {
	mu     sync.Mutex
	timers []*fakeTimer
}

type fakeTimer struct {
	c       *fakeTimers
	after   time.Duration
	f       func()
	stopped bool
}

func (t *fakeTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	was := !t.stopped
	t.stopped = true
	return was
}

func (c *fakeTimers) afterFunc(d time.Duration, f func()) doneTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{c: c, after: d, f: f}
	c.timers = append(c.timers, t)
	return t
}

// pending is the wait of each timer still armed.
func (c *fakeTimers) pending() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	var ds []time.Duration
	for _, t := range c.timers {
		if !t.stopped {
			ds = append(ds, t.after)
		}
	}
	return ds
}

// fire runs the timers still armed, as if their time had come.
func (c *fakeTimers) fire() {
	c.mu.Lock()
	var due []*fakeTimer
	for _, t := range c.timers {
		if !t.stopped {
			t.stopped = true
			due = append(due, t)
		}
	}
	c.mu.Unlock()
	for _, t := range due {
		t.f()
	}
}

// fakeDone gives the daemon fake done-sound timers.
func fakeDone(e env) *fakeTimers {
	c := &fakeTimers{}
	e.d.mu.Lock()
	e.d.doneAfter = c.afterFunc
	e.d.mu.Unlock()
	return c
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

// A permission request rings once, when the agent starts waiting; a turn that ends rings done.
func TestSoundsWhenAnAgentWaitsAndWhenATurnEnds(t *testing.T) {
	e, rang := soundEnv(t)
	c := fakeDone(e)
	hookEv(t, e, "UserPromptSubmit", nil)
	hookEv(t, e, "PermissionRequest", nil)
	hookEv(t, e, "PermissionRequest", nil)
	if got := rang(); len(got) != 1 || got[0] != SoundAttention {
		t.Fatalf("rang %q", got)
	}
	endTurn(t, e)
	if got := rang(); len(got) != 1 {
		t.Fatalf("done rang before the delay: %q", got)
	}
	c.fire()
	if got := rang(); len(got) != 2 || got[1] != SoundDone {
		t.Fatalf("rang %q", got)
	}
}

// A quiet stop rings done once, after the short quiet wait.
func TestAQuietStopRingsOnceAfterTheDelay(t *testing.T) {
	e, rang := soundEnv(t)
	c := fakeDone(e)
	endTurn(t, e)
	if got := c.pending(); len(got) != 1 || got[0] != doneDelay {
		t.Fatalf("pending %v, want one wait of %v", got, doneDelay)
	}
	if doneDelay > 3*time.Second {
		t.Fatalf("doneDelay %v: the sound lags the deck", doneDelay)
	}
	c.fire()
	c.fire()
	if got := rang(); len(got) != 1 || got[0] != SoundDone {
		t.Fatalf("rang %q", got)
	}
}

// Agent-type Stop hooks at work hold the done sound until each has answered (its StructuredOutput
// call); then the turn rings once, after the quiet wait.
func TestStopHooksHoldTheDoneSoundUntilTheyAnswer(t *testing.T) {
	e, rang := soundEnv(t)
	c := fakeDone(e)
	endTurn(t, e)
	hookTool(t, e, "PreToolUse", "a", "Glob")
	hookTool(t, e, "PostToolUse", "a", "Glob")
	hookTool(t, e, "PreToolUse", "b", "Read")
	if got := c.pending(); len(got) != 1 || got[0] != doneHookWait {
		t.Fatalf("pending %v, want %v while hooks work", got, doneHookWait)
	}
	hookTool(t, e, "PreToolUse", "a", "StructuredOutput")
	hookTool(t, e, "PostToolUse", "a", "StructuredOutput")
	if got := c.pending(); len(got) != 1 || got[0] != doneHookWait {
		t.Fatalf("pending %v, want %v while hook b works", got, doneHookWait)
	}
	hookTool(t, e, "PostToolUse", "b", "Read")
	hookTool(t, e, "PreToolUse", "b", "StructuredOutput")
	hookTool(t, e, "PostToolUse", "b", "StructuredOutput")
	if got := c.pending(); len(got) != 1 || got[0] != doneDelay {
		t.Fatalf("pending %v, want %v once every hook answered", got, doneDelay)
	}
	if s := devState(t, e); s != "idle" {
		t.Fatalf("state=%s while the Stop hooks work, want idle", s)
	}
	c.fire()
	if got := rang(); len(got) != 1 || got[0] != SoundDone {
		t.Fatalf("rang %q", got)
	}
}

// A Stop hook blocks the stop and the agent works again (its own tools): no done sound however
// long it works, the deck shows it working, and its next end rings once.
func TestAStopBlockedByAnotherHookRingsOnlyAtTheRealEnd(t *testing.T) {
	e, rang := soundEnv(t)
	c := fakeDone(e)
	endTurn(t, e)
	hookTool(t, e, "PreToolUse", "a", "Glob")
	hookTool(t, e, "PostToolUse", "a", "Glob")
	hookTool(t, e, "PreToolUse", "a", "StructuredOutput")
	hookTool(t, e, "PostToolUse", "a", "StructuredOutput")
	mainTool(t, e, "PreToolUse", "Bash") // a long test suite, say
	if got := c.pending(); len(got) != 0 {
		t.Fatalf("pending %v while the agent works again", got)
	}
	if s := devState(t, e); s != "working" {
		t.Fatalf("state=%s, want working", s)
	}
	c.fire()
	mainTool(t, e, "PostToolUse", "Bash")
	c.fire()
	if got := rang(); len(got) != 0 {
		t.Fatalf("rang %q while the agent works", got)
	}
	endTurn(t, e)
	hookTool(t, e, "PreToolUse", "c", "StructuredOutput")
	hookTool(t, e, "PostToolUse", "c", "StructuredOutput")
	c.fire()
	if got := rang(); len(got) != 1 || got[0] != SoundDone {
		t.Fatalf("rang %q", got)
	}
}

// A blocked stop the agent answers without a tool comes back as a second Stop: one done sound.
func TestTwoStopsOfOneTurnRingOnce(t *testing.T) {
	e, rang := soundEnv(t)
	c := fakeDone(e)
	endTurn(t, e)
	hookTool(t, e, "PreToolUse", "a", "StructuredOutput")
	hookTool(t, e, "PostToolUse", "a", "StructuredOutput")
	endTurn(t, e)
	if got := c.pending(); len(got) != 1 {
		t.Fatalf("pending %v, want one done sound", got)
	}
	c.fire()
	if got := rang(); len(got) != 1 || got[0] != SoundDone {
		t.Fatalf("rang %q", got)
	}
}

// A background subagent's tools after the Stop neither end nor extend the turn.
func TestABackgroundSubagentDoesNotTouchTheDoneSound(t *testing.T) {
	e, rang := soundEnv(t)
	c := fakeDone(e)
	endTurn(t, e)
	hookEv(t, e, "PreToolUse", map[string]any{"agent_id": "a1b2c3", "tool_name": "Bash"})
	if got := c.pending(); len(got) != 1 || got[0] != doneDelay {
		t.Fatalf("pending %v", got)
	}
	if s := devState(t, e); s != "idle" {
		t.Fatalf("state=%s, want idle", s)
	}
	c.fire()
	if got := rang(); len(got) != 1 {
		t.Fatalf("rang %q", got)
	}
}

// A lea hook that forwards no agent_id (an older binary in a running container): a tool after the
// Stop restarts the wait, as before.
func TestToolsWithoutAnAgentIDRestartTheWait(t *testing.T) {
	e, rang := soundEnv(t)
	c := fakeDone(e)
	endTurn(t, e)
	hookEv(t, e, "PreToolUse", map[string]any{"tool_name": "Glob"})
	hookEv(t, e, "PostToolUse", map[string]any{"tool_name": "Glob"})
	if got := c.pending(); len(got) != 1 || got[0] != doneDelay {
		t.Fatalf("pending %v", got)
	}
	c.fire()
	if got := rang(); len(got) != 1 {
		t.Fatalf("rang %q", got)
	}
}

// A new prompt before the sound: the agent works again, nothing rings.
func TestANewPromptCancelsTheDoneSound(t *testing.T) {
	e, rang := soundEnv(t)
	c := fakeDone(e)
	endTurn(t, e)
	hookEv(t, e, "UserPromptSubmit", nil)
	c.fire()
	if got := rang(); len(got) != 0 {
		t.Fatalf("rang %q", got)
	}
}

// A stop blocked by waiting messages keeps the agent working: no done sound.
func TestABlockedStopDoesNotRing(t *testing.T) {
	e, rang := soundEnv(t)
	c := fakeDone(e)
	call(t, e.bus, "dev", "agents_list", nil, nil)
	call(t, e.api, "", "msg_send", map[string]any{"to": "dev", "body": "ping", "kind": "note"}, nil)
	hookEv(t, e, "Stop", nil)
	var r struct{ Block bool }
	call(t, e.bus, "dev", "stop_check", nil, &r)
	c.fire()
	if !r.Block || len(rang()) != 0 {
		t.Fatalf("block %v, rang %q", r.Block, rang())
	}
}

// A removed agent does not ring for its last stop.
func TestARemovedAgentDoesNotRing(t *testing.T) {
	e, rang := soundEnv(t)
	c := fakeDone(e)
	endTurn(t, e)
	as, err := e.d.st.Agents()
	if err != nil || len(as) != 1 {
		t.Fatalf("agents %v %v", as, err)
	}
	if err := e.d.st.ForgetAgent(as[0].Key); err != nil {
		t.Fatal(err)
	}
	e.d.pruneForgotten()
	c.fire()
	if got := rang(); len(got) != 0 {
		t.Fatalf("rang %q", got)
	}
}

// Agents ending together ring once.
func TestSoundsOfOneKindAreSpaced(t *testing.T) {
	e, rang := soundEnv(t)
	e.d.sound(context.Background(), "lea-a", SoundDone)
	e.d.sound(context.Background(), "lea-a", SoundDone)
	e.d.sound(context.Background(), "lea-a", SoundAttention)
	if got := rang(); len(got) != 2 {
		t.Fatalf("rang %q", got)
	}
	e.d.mu.Lock()
	e.d.rang[SoundDone] = time.Now().Add(-soundGap)
	e.d.mu.Unlock()
	e.d.sound(context.Background(), "lea-a", SoundDone)
	if got := rang(); len(got) != 3 {
		t.Fatalf("rang %q", got)
	}
}

// No deck shows the agent's container: neither a wait nor a turn end rings.
func TestNoSoundWithoutAnAttachedDeck(t *testing.T) {
	e, rang := soundEnv(t)
	fa := &fakeAgents{unattached: true}
	e.d.SetAgents(fa)
	c := fakeDone(e)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "UserPromptSubmit"}, nil)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "PermissionRequest"}, nil)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "PostToolUse"}, nil)
	endTurn(t, e)
	c.fire()
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

// The deck closes while a turn's done sound waits: it does not ring.
func TestADeckClosedDuringTheDelaySilencesDone(t *testing.T) {
	e, rang := soundEnv(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	c := fakeDone(e)
	endTurn(t, e)
	fa.mu.Lock()
	fa.unattached = true
	fa.mu.Unlock()
	c.fire()
	if got := rang(); len(got) != 0 {
		t.Fatalf("rang %q after the deck closed", got)
	}
}

// With a deck attached, sounds ring as before.
func TestSoundsRingWithAnAttachedDeck(t *testing.T) {
	e, rang := soundEnv(t)
	e.d.SetAgents(&fakeAgents{})
	c := fakeDone(e)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "PermissionRequest"}, nil)
	endTurn(t, e)
	c.fire()
	if got := rang(); len(got) != 2 || got[0] != SoundAttention || got[1] != SoundDone {
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
	e.d.sound(context.Background(), "lea-a", SoundDone)
	if got := rang(); len(got) != 0 {
		t.Fatalf("rang %q while off", got)
	}
	if err := call(t, e.api, "", "sound", nil, &r); err != nil || r.Sound != "off" {
		t.Fatalf("read: %+v %v", r, err)
	}
	if err := call(t, e.api, "", "sound", map[string]any{"on": true}, &r); err != nil || r.Sound != "on" {
		t.Fatalf("on: %+v %v", r, err)
	}
	e.d.sound(context.Background(), "lea-a", SoundDone)
	if got := rang(); len(got) != 1 || got[0] != SoundDone {
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
