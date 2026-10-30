package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

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

// With the hub on, a wait is only worth registering when lea can read its decision: no facade
// configured (no LEA_HUB_SOCKET nor XDG_RUNTIME_DIR), or a facade socket nothing answers on, and
// the agent would wait on a decision nothing ever syncs. lea then refuses the wait, tells the
// agent, and the deck and lea doctor say the hub is unreachable.

// errNoHubFacade is the suspend hook's call refused for want of a reachable hub facade.
var errNoHubFacade = errors.New("hub_wait_register: no Lock Family hub facade is reachable (LEA_HUB=1, but no lockhub runs on this host, or LEA_HUB_SOCKET names another socket)")

// hubPingTimeout bounds the check that the facade answers.
const hubPingTimeout = 2 * time.Second

// hubPinger is a facade that can tell whether its hub answers (SocketFacade); a facade that
// cannot (a test's) counts as reachable.
type hubPinger interface {
	Ping(ctx context.Context) error
}

// hubReachable is nil when lea can read the hub's decisions now, else errNoHubFacade; it records
// the answer for the deck.
func (d *Daemon) hubReachable() error {
	d.mu.Lock()
	f := d.hubFacade
	d.mu.Unlock()
	err := error(nil)
	if f == nil {
		err = errNoHubFacade
	} else if p, ok := f.(hubPinger); ok {
		ctx, cancel := context.WithTimeout(context.Background(), hubPingTimeout)
		if p.Ping(ctx) != nil {
			err = errNoHubFacade
		}
		cancel()
	}
	d.setHubDown(err != nil)
	return err
}

// setHubDown records whether the hub's facade answered last, and redraws the decks on a change.
func (d *Daemon) setHubDown(down bool) {
	d.mu.Lock()
	was := d.hubDown
	d.hubDown = down
	d.mu.Unlock()
	if was != down {
		d.changed()
	}
}

// hubState is what the deck shows of the hub: "down" when it is on but lea cannot reach its
// facade (no facade configured, or none answered last), else "".
func (d *Daemon) hubState() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.hubOff {
		return ""
	}
	if d.hubFacade == nil || d.hubDown {
		return "down"
	}
	return ""
}

// noHubText is what an agent whose wait lea refused is told: the Lock's ticket stands, but its
// decision will not reach it through lea.
func noHubText(ticket, lock, instance string) string {
	return fmt.Sprintf("[status: fail] lea did not suspend you on ticket %s on %s (%s): it cannot reach the Lock Family hub (no lockhub facade answers), so the decision would never reach you. Do not wait for it: tell the user that the hub is not running, and carry on with what does not need that call.", ticket, lock, instance)
}
