package daemon

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/DavidGodefroid/lea/internal/agents"
	"github.com/DavidGodefroid/lea/internal/store"
)

// Suspended agents (the Lock Family approval hub, #161). An agent whose Lock call is pending a
// human's decision ends its turn suspended_warm: its process runs on for the keepalive, and a
// decision or a message that comes meanwhile wakes it like any message (the Rewake job of its
// Lock call, lea hook claude Rewake). When the keepalive ends, lea stops that agent alone and
// keeps its session (suspended_cold); a decision or a message then resumes it.

// DefaultKeepalive is how long a suspended agent's process runs before lea stops it.
const DefaultKeepalive = 5 * time.Minute

// suspendTimeout bounds one soft stop or one resume.
const suspendTimeout = 2 * time.Minute

// wakeEvents are the hooks that end a suspension: the agent works again (a decision or a message
// woke it, or the user typed). Every other hook leaves a suspended agent as it is: Claude fires no
// Stop after the suspending hook (spike #154) but lea's own PostToolUse, Notification idle_prompt
// and the SessionEnd of a soft stop still come.
var wakeEvents = map[string]bool{"UserPromptSubmit": true, "PreToolUse": true, "PermissionRequest": true}

// SetKeepalive is how long a suspended agent's process runs before lea stops it; 0 stops it at once.
func (d *Daemon) SetKeepalive(k time.Duration) {
	d.mu.Lock()
	d.keepalive = max(k, 0)
	d.mu.Unlock()
}

// suspend records that the agent's turn ended on a pending Lock decision: suspended_warm, its turn
// over (a rewake watch answers), its keepalive armed.
func (d *Daemon) suspend(key string) error {
	if err := d.st.SetState(key, store.StateSuspendedWarm, "", d.now()); err != nil {
		return err
	}
	d.mu.Lock()
	d.stopped[key] = true
	d.mu.Unlock()
	d.armKeepalive(key, 0)
	d.stackActivity(key, store.StateSuspendedWarm)
	d.changed()
	d.wake(key) // a rewake watch looks at the messages that came during the turn
	return nil
}

// armKeepalive starts the agent's keepalive; spent is how much of it already went (a restarted
// daemon).
func (d *Daemon) armKeepalive(key string, spent time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if t := d.keepTimers[key]; t != nil {
		t.Stop()
	}
	var t *time.Timer
	t = time.AfterFunc(max(d.keepalive-spent, 0), func() {
		d.mu.Lock()
		mine := d.keepTimers[key] == t
		if mine {
			delete(d.keepTimers, key)
		}
		d.mu.Unlock()
		if mine {
			d.softStop(key)
		}
	})
	d.keepTimers[key] = t
}

// keepActivity follows the agent's state: any state but suspended_warm cancels its keepalive.
func (d *Daemon) keepActivity(key, state string) {
	if state == store.StateSuspendedWarm {
		return
	}
	d.mu.Lock()
	d.forgetKeepaliveLocked(key)
	d.mu.Unlock()
}

// forgetKeepaliveLocked drops the agent's keepalive. Callers hold d.mu.
func (d *Daemon) forgetKeepaliveLocked(key string) {
	if t := d.keepTimers[key]; t != nil {
		t.Stop()
	}
	delete(d.keepTimers, key)
}

// softStop stops a suspended agent whose keepalive ended; one that woke meanwhile is left alone.
// A message that came while it stopped resumes it.
func (d *Daemon) softStop(key string) {
	d.mu.Lock()
	a := d.agents
	d.mu.Unlock()
	if a == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), suspendTimeout)
	defer cancel()
	if err := a.SoftStop(ctx, key); err != nil {
		if !errors.Is(err, agents.ErrNotSuspended) {
			log.Printf("lea daemon: stop suspended %s: %v", key, err)
		}
		return
	}
	d.mu.Lock()
	delete(d.stopped, key)
	d.mu.Unlock()
	d.changed()
	d.wakeSuspended(key)
}

// wakeSuspended resumes a suspended_cold agent that has unread messages, in the background: a
// message (from its lead, a child, the user, or the decision lea delivers) wakes it, so a lead
// waiting for a decision never blocks the children that write to it. A warm agent hears them
// through its Rewake job.
func (d *Daemon) wakeSuspended(key string) {
	ag, err := d.st.AgentByKey(key)
	if err != nil || ag.State != store.StateSuspendedCold {
		return
	}
	n, err := d.unreadNotice(key)
	if err != nil || n["count"] == 0 {
		return
	}
	text, _ := n["text"].(string)
	d.mu.Lock()
	a := d.agents
	d.mu.Unlock()
	if a == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), suspendTimeout)
		defer cancel()
		if err := a.ResumeSuspended(ctx, key, text); err != nil && !errors.Is(err, agents.ErrNotSuspended) {
			log.Printf("lea daemon: resume suspended %s: %v", key, err)
		}
		d.changed()
	}()
}

// suspendedSince, in a container the launcher has just started or a daemon that started again,
// stops the suspended agents whose keepalive went by (their timers are lost), arms the others,
// and resumes the stopped ones that have unread messages.
func (d *Daemon) suspendedSince(container string) {
	as, err := d.st.SpawnedAgents(container)
	if err != nil {
		return
	}
	for _, ag := range as {
		switch ag.State {
		case store.StateSuspendedWarm:
			d.mu.Lock()
			_, armed := d.keepTimers[ag.Key]
			d.stopped[ag.Key] = true
			d.mu.Unlock()
			if !armed {
				d.armKeepalive(ag.Key, time.Duration(d.now()-ag.Updated)*time.Second)
			}
		case store.StateSuspendedCold:
			d.wakeSuspended(ag.Key)
		}
	}
}
