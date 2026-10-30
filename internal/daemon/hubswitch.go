package daemon

import (
	"errors"
	"log"

	"github.com/DavidGodefroid/lea/internal/store"
)

// The hub switch (LEA_HUB, the plan's rollback): with the hub off, lea reads no hub and registers
// no wait, so its suspend hook leaves the turn alone, and an agent it suspended before is resumed
// with a "hub disabled" message: lea no longer waits on its tickets.

// hubDisabledReason is what an agent whose waits lea abandons is told.
const hubDisabledReason = "the approval hub is disabled in lea (LEA_HUB=0)"

var errHubDisabled = errors.New("hub_wait_register: " + hubDisabledReason)

func (d *Daemon) hubDisabled() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.hubOff
}

// DisableHub switches the hub off: no hub is read, no wait is registered any more, every pending
// wait is abandoned, and each suspended agent is told: a warm one in its inbox (its Rewake job
// wakes it), a cold one in the prompt of its resume, once its container is attached (the
// resume lane, suspendedSince).
func (d *Daemon) DisableHub() {
	d.mu.Lock()
	d.hubOff = true
	d.hubFacade = nil
	d.mu.Unlock()
	if ws, err := d.st.AbandonAllHubWaits(hubDisabledReason, d.now()); err != nil {
		log.Printf("lea daemon: hub disabled: %v", err)
	} else if len(ws) > 0 {
		agents := map[string]bool{}
		for _, w := range ws {
			agents[w.Agent] = true
		}
		log.Printf("lea daemon: hub disabled (LEA_HUB=0): abandoned %d pending wait(s) of %d agent(s); their tickets stay on the hub", len(ws), len(agents))
	}
	// A suspended agent with nothing to read yet (no undelivered decision, no unread message)
	// hears it too, once: an earlier start's copy is still unread.
	as, err := d.st.Agents()
	if err != nil {
		log.Printf("lea daemon: hub disabled: %v", err)
	}
	for _, a := range as {
		if !store.Suspended(a.State) || d.hasUndelivered(a.Key) {
			continue
		}
		if n, err := d.st.UnreadCount(a.Key); err == nil && n == 0 {
			d.post("user", a.Key, "result", "[status: fail] "+hubDisabledReason+": lea no longer suspends you on a Lock call; carry on with your task.", 0)
		}
	}
	if err := d.deliverHubDecisions(); err != nil {
		log.Printf("lea daemon: hub disabled: %v", err)
	}
}
