package daemon

import (
	"context"
	"time"
)

// Approvals live outside lea (#300): lox run, in a terminal of its own, decides the agents' Lock
// requests. The deck says to run it when the workspace has locks and none serves; the daemon
// looks every loxRunEvery and wakes the decks when that changes.

// loxRunEvery is how often the daemon looks whether lox run serves.
var loxRunEvery = 5 * time.Second

// LoxRunWatch tells whether lox run serves the workspace's decisions: "serving", "off" (the
// workspace has locks and none serves: the deck says to run it) or "" (no lock, or lea cannot
// tell). Nil: the deck never says.
type LoxRunWatch func() string

// SetLoxRunWatch sets how the daemon tells whether lox run serves (RunLoxRunWatch).
func (d *Daemon) SetLoxRunWatch(w LoxRunWatch) {
	d.mu.Lock()
	d.loxRunWatch = w
	d.mu.Unlock()
}

// RunLoxRunWatch reads whether lox run serves at once, then every loxRunEvery until ctx ends;
// each change wakes the decks.
func (d *Daemon) RunLoxRunWatch(ctx context.Context) {
	for {
		d.readLoxRun()
		select {
		case <-ctx.Done():
			return
		case <-time.After(loxRunEvery):
		}
	}
}

// readLoxRun reads the state once, and wakes the decks when it changed.
func (d *Daemon) readLoxRun() {
	d.mu.Lock()
	w := d.loxRunWatch
	d.mu.Unlock()
	if w == nil {
		return
	}
	st := w()
	d.mu.Lock()
	changed := st != d.loxRun
	d.loxRun = st
	d.mu.Unlock()
	if changed {
		d.changed()
	}
}

// loxRunState is what the deck shows of lox run (Snapshot.LoxRun).
func (d *Daemon) loxRunState() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.loxRun
}
