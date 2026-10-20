package daemon

import (
	"context"
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

// testDoneDelay is the done-sound delay of the tests: short, yet longer than a few hook calls.
const testDoneDelay = 100 * time.Millisecond

func quickDone(e env) {
	e.d.mu.Lock()
	e.d.doneDelay = testDoneDelay
	e.d.mu.Unlock()
}

// waitRang polls until n sounds rang or the deadline passes.
func waitRang(rang func() []string, n int) []string {
	deadline := time.Now().Add(5 * time.Second)
	for len(rang()) < n && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	return rang()
}

// A permission request rings once, when the agent starts waiting; a turn that ends rings done.
func TestSoundsWhenAnAgentWaitsAndWhenATurnEnds(t *testing.T) {
	e, rang := soundEnv(t)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "UserPromptSubmit"}, nil)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "PermissionRequest"}, nil)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "PermissionRequest"}, nil)
	if got := rang(); len(got) != 1 || got[0] != SoundAttention {
		t.Fatalf("rang %q", got)
	}
	quickDone(e)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "Stop"}, nil)
	call(t, e.bus, "dev", "stop_check", nil, nil)
	if got := rang(); len(got) != 1 {
		t.Fatalf("done rang before the delay: %q", got)
	}
	if got := waitRang(rang, 2); len(got) != 2 || got[1] != SoundDone {
		t.Fatalf("rang %q", got)
	}
}

// A quiet stop rings done once, after the delay.
func TestAQuietStopRingsOnceAfterTheDelay(t *testing.T) {
	e, rang := soundEnv(t)
	quickDone(e)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "Stop"}, nil)
	call(t, e.bus, "dev", "stop_check", nil, nil)
	if got := rang(); len(got) != 0 {
		t.Fatalf("done rang before the delay: %q", got)
	}
	if got := waitRang(rang, 1); len(got) != 1 || got[0] != SoundDone {
		t.Fatalf("rang %q", got)
	}
	time.Sleep(3 * testDoneDelay)
	if got := rang(); len(got) != 1 {
		t.Fatalf("rang %q", got)
	}
}

// Another Stop hook blocked the stop: the agent goes on with tools, each sooner than the delay
// after the last, so the turn did not end.
func TestAStopBlockedByAnotherHookDoesNotRing(t *testing.T) {
	e, rang := soundEnv(t)
	quickDone(e)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "Stop"}, nil)
	call(t, e.bus, "dev", "stop_check", nil, nil)
	for i := 0; i < 6; i++ {
		call(t, e.bus, "dev", "hook_event", map[string]any{"event": "PreToolUse"}, nil)
		call(t, e.bus, "dev", "hook_event", map[string]any{"event": "PostToolUse"}, nil)
		time.Sleep(testDoneDelay / 2)
	}
	if got := rang(); len(got) != 0 {
		t.Fatalf("rang %q", got)
	}
}

// An agent-type Stop hook runs its tools, then lets the stop through: no stop_check follows, and
// the turn still rings done once, after the delay from the hook's last tool.
func TestAStopHookThatLetsTheStopThroughRings(t *testing.T) {
	e, rang := soundEnv(t)
	quickDone(e)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "Stop"}, nil)
	call(t, e.bus, "dev", "stop_check", nil, nil)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "PreToolUse"}, nil)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "PostToolUse"}, nil)
	if got := rang(); len(got) != 0 {
		t.Fatalf("done rang before the delay: %q", got)
	}
	if got := waitRang(rang, 1); len(got) != 1 || got[0] != SoundDone {
		t.Fatalf("rang %q", got)
	}
}

// A stop blocked once by another hook, then a real end: one done sound, at the end.
func TestTwoStopsOfOneTurnRingOnce(t *testing.T) {
	e, rang := soundEnv(t)
	quickDone(e)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "Stop"}, nil)
	call(t, e.bus, "dev", "stop_check", nil, nil)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "PreToolUse"}, nil)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "PostToolUse"}, nil)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "Stop"}, nil)
	call(t, e.bus, "dev", "stop_check", nil, nil)
	if got := rang(); len(got) != 0 {
		t.Fatalf("done rang before the delay: %q", got)
	}
	// The gap between two sounds of a kind is longer than the test: a second ring would show.
	if got := waitRang(rang, 1); len(got) != 1 || got[0] != SoundDone {
		t.Fatalf("rang %q", got)
	}
	time.Sleep(3 * testDoneDelay)
	if got := rang(); len(got) != 1 {
		t.Fatalf("rang %q", got)
	}
}

// A stop blocked by waiting messages keeps the agent working: no done sound.
func TestABlockedStopDoesNotRing(t *testing.T) {
	e, rang := soundEnv(t)
	call(t, e.bus, "dev", "agents_list", nil, nil)
	call(t, e.api, "", "msg_send", map[string]any{"to": "dev", "body": "ping", "kind": "note"}, nil)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "Stop"}, nil)
	var r struct{ Block bool }
	call(t, e.bus, "dev", "stop_check", nil, &r)
	if !r.Block || len(rang()) != 0 {
		t.Fatalf("block %v, rang %q", r.Block, rang())
	}
}

// A removed agent does not ring for its last stop.
func TestARemovedAgentDoesNotRing(t *testing.T) {
	e, rang := soundEnv(t)
	quickDone(e)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "Stop"}, nil)
	call(t, e.bus, "dev", "stop_check", nil, nil)
	as, err := e.d.st.Agents()
	if err != nil || len(as) != 1 {
		t.Fatalf("agents %v %v", as, err)
	}
	if err := e.d.st.ForgetAgent(as[0].Key); err != nil {
		t.Fatal(err)
	}
	e.d.pruneForgotten()
	time.Sleep(3 * testDoneDelay)
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
	quickDone(e)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "UserPromptSubmit"}, nil)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "PermissionRequest"}, nil)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "PostToolUse"}, nil)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "Stop"}, nil)
	call(t, e.bus, "dev", "stop_check", nil, nil)
	time.Sleep(3 * testDoneDelay)
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
	quickDone(e)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "Stop"}, nil)
	call(t, e.bus, "dev", "stop_check", nil, nil)
	fa.mu.Lock()
	fa.unattached = true
	fa.mu.Unlock()
	time.Sleep(3 * testDoneDelay)
	if got := rang(); len(got) != 0 {
		t.Fatalf("rang %q after the deck closed", got)
	}
}

// With a deck attached, sounds ring as before.
func TestSoundsRingWithAnAttachedDeck(t *testing.T) {
	e, rang := soundEnv(t)
	e.d.SetAgents(&fakeAgents{})
	quickDone(e)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "PermissionRequest"}, nil)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "Stop"}, nil)
	call(t, e.bus, "dev", "stop_check", nil, nil)
	if got := waitRang(rang, 2); len(got) != 2 || got[0] != SoundAttention || got[1] != SoundDone {
		t.Fatalf("rang %q", got)
	}
}
