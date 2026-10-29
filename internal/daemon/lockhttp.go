package daemon

import (
	"context"
	"regexp"
	"sort"

	"github.com/DavidGodefroid/lea/internal/agents"
)

// Lock approvals (#153): each Lock (locksql, lockmq, lockfile, lockhttp) whose approvals lea
// learns of has a status: its console's state and its requests waiting for the user, per lea
// container. LockHTTP's comes from the gate's status file; the hub's facade feeds the others. The
// deck counts them all.

// LockHTTPStatus is the LockHTTP gate's status file (lockhttp's docs/lea.md, §5).
type LockHTTPStatus struct {
	Version            int            `json:"version"`
	Console            string         `json:"console"`
	Pending            int            `json:"pending"`
	PendingByContainer map[string]int `json:"pending_by_container"`
}

// LockStatus is one Lock's approvals as lea knows them: its console ("up", "down", "" unknown)
// and its requests waiting for the user, per lea container.
type LockStatus struct {
	Console            string
	PendingByContainer map[string]int
}

var lockName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// SetLockHTTPStatus records the gate's status (LEA_LOCKHTTP=1).
func (d *Daemon) SetLockHTTPStatus(ctx context.Context, s LockHTTPStatus) {
	d.SetLockStatus(ctx, "lockhttp", LockStatus{Console: s.Console, PendingByContainer: s.PendingByContainer})
}

// SetLockStatus records lock's status and tells the decks when it changed. A container whose
// requests of that Lock start waiting (none waited before) rings the attention sound, while a deck
// shows it (D6). A name that is no Lock's is ignored.
func (d *Daemon) SetLockStatus(ctx context.Context, lock string, s LockStatus) {
	if !lockName.MatchString(lock) {
		return
	}
	d.mu.Lock()
	if d.locks == nil {
		d.locks = map[string]*LockStatus{}
	}
	old := d.locks[lock]
	d.locks[lock] = &s
	d.mu.Unlock()
	if old != nil && old.Console == s.Console && sameCounts(old.PendingByContainer, s.PendingByContainer) {
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

// lockViews is every known Lock's state for container's deck, by name; none when lea knows of
// no Lock's approvals.
func (d *Daemon) lockViews(container string) []agents.LockView {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []agents.LockView
	for name, s := range d.locks {
		out = append(out, agents.LockView{Lock: name, Console: s.Console, Pending: max(0, s.PendingByContainer[container])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Lock < out[j].Lock })
	return out
}

// lockHTTPView is the gate's state for container's deck, nil when LockHTTP is off.
func (d *Daemon) lockHTTPView(container string) *agents.LockView {
	for _, v := range d.lockViews(container) {
		if v.Lock == "lockhttp" {
			return &v
		}
	}
	return nil
}
