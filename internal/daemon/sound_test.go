package daemon

import (
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

// A permission request rings once, when the agent starts waiting; a turn that ends rings done.
func TestSoundsWhenAnAgentWaitsAndWhenATurnEnds(t *testing.T) {
	e, rang := soundEnv(t)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "UserPromptSubmit"}, nil)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "PermissionRequest"}, nil)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "PermissionRequest"}, nil)
	if got := rang(); len(got) != 1 || got[0] != SoundAttention {
		t.Fatalf("rang %q", got)
	}
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "Stop"}, nil)
	call(t, e.bus, "dev", "stop_check", nil, nil)
	if got := rang(); len(got) != 2 || got[1] != SoundDone {
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

// Agents ending together ring once.
func TestSoundsOfOneKindAreSpaced(t *testing.T) {
	e, rang := soundEnv(t)
	e.d.sound(SoundDone)
	e.d.sound(SoundDone)
	e.d.sound(SoundAttention)
	if got := rang(); len(got) != 2 {
		t.Fatalf("rang %q", got)
	}
	e.d.mu.Lock()
	e.d.rang[SoundDone] = time.Now().Add(-soundGap)
	e.d.mu.Unlock()
	e.d.sound(SoundDone)
	if got := rang(); len(got) != 3 {
		t.Fatalf("rang %q", got)
	}
}
