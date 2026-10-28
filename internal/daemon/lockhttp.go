package daemon

import (
	"context"

	"github.com/DavidGodefroid/lea/internal/agents"
)

// LockHTTPStatus is the LockHTTP gate's status file (lockhttp's docs/lea.md, §5).
type LockHTTPStatus struct {
	Version            int            `json:"version"`
	Console            string         `json:"console"`
	Pending            int            `json:"pending"`
	PendingByContainer map[string]int `json:"pending_by_container"`
}

// SetLockHTTPStatus records the gate's status (LEA_LOCKHTTP=1) and tells the decks when it
// changed. A container whose requests start waiting in the console (none waited before) rings
// the attention sound, while a deck shows it (D6).
func (d *Daemon) SetLockHTTPStatus(ctx context.Context, s LockHTTPStatus) {
	d.mu.Lock()
	old := d.lockhttp
	d.lockhttp = &s
	d.mu.Unlock()
	if old != nil && old.Console == s.Console && old.Pending == s.Pending && sameCounts(old.PendingByContainer, s.PendingByContainer) {
		return
	}
	for c, n := range s.PendingByContainer {
		if n > 0 && (old == nil || old.PendingByContainer[c] == 0) {
			d.sound(ctx, c, SoundAttention)
		}
	}
	d.changed()
}

func sameCounts(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// lockHTTPView is the gate's state for container's deck, nil when LockHTTP is off.
func (d *Daemon) lockHTTPView(container string) *agents.LockHTTPView {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.lockhttp == nil {
		return nil
	}
	return &agents.LockHTTPView{Console: d.lockhttp.Console, Pending: d.lockhttp.PendingByContainer[container]}
}
