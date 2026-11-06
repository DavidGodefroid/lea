package daemon

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/agents"
)

// The deck's snapshot says whether lox run serves (#300): "off" while the workspace has locks and
// none does, so the deck says to run it in another terminal; each change wakes the decks, and a
// read that changes nothing does not.
func TestLoxRunStateReachesTheDeck(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	var mu sync.Mutex
	st := "off"
	e.d.SetLoxRunWatch(func() string { mu.Lock(); defer mu.Unlock(); return st })
	changed := func() int { fa.mu.Lock(); defer fa.mu.Unlock(); return fa.changed }
	watch := func() string {
		t.Helper()
		var snap agents.Snapshot
		if err := call(t, e.api, "", "agents_watch", map[string]any{"container": "lea-a", "since": 0, "timeout_s": 1}, &snap); err != nil {
			t.Fatal(err)
		}
		return snap.LoxRun
	}
	if got := watch(); got != "" {
		t.Fatalf("before the first read: %q", got)
	}
	n := changed()
	e.d.readLoxRun()
	if got := watch(); got != "off" || changed() != n+1 {
		t.Fatalf("lox_run %q, changed %d (was %d)", got, changed(), n)
	}
	e.d.readLoxRun()
	if changed() != n+1 {
		t.Fatalf("a read that changed nothing woke the decks")
	}
	mu.Lock()
	st = "serving"
	mu.Unlock()
	e.d.readLoxRun()
	if got := watch(); got != "serving" || changed() != n+2 {
		t.Fatalf("lox_run %q, changed %d", got, changed())
	}
}

// RunLoxRunWatch reads at once, then every loxRunEvery, until its context ends; with no watch set
// the deck never says anything of lox run.
func TestRunLoxRunWatchLooksAgain(t *testing.T) {
	old := loxRunEvery
	loxRunEvery = 10 * time.Millisecond
	t.Cleanup(func() { loxRunEvery = old })
	e := start(t)
	e.d.RunLoxRunWatch(canceled()) // no watch: returns at once, nothing read
	if got := e.d.loxRunState(); got != "" {
		t.Fatalf("no watch: %q", got)
	}
	var mu sync.Mutex
	reads := 0
	e.d.SetLoxRunWatch(func() string { mu.Lock(); defer mu.Unlock(); reads++; return "off" })
	ctx, cancel := context.WithTimeout(e.ctx, 200*time.Millisecond)
	defer cancel()
	e.d.RunLoxRunWatch(ctx)
	mu.Lock()
	defer mu.Unlock()
	if reads < 2 || e.d.loxRunState() != "off" {
		t.Fatalf("%d reads, state %q", reads, e.d.loxRunState())
	}
}

func canceled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}
