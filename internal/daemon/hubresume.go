package daemon

import (
	"errors"
	"log"
	"strings"

	"github.com/DavidGodefroid/lea/internal/store"
)

// Resume on decision (#163). A suspended_cold agent whose ticket the hub decided resumes with the
// decision as its next turn: the resume prompt of its session (claude --resume <session>
// <prompt>), which reaches its first turn headless and interactive alike (spike #155). An async
// SessionStart hook would not: its context is lost in an interactive resume.

// hasUndelivered reports whether agent has a decided wait that has not reached it yet.
func (d *Daemon) hasUndelivered(agent string) bool {
	ws, err := d.st.UndeliveredHubWaits(agent)
	return err == nil && len(ws) > 0
}

// decisionsOf are agent's decided waits that have not reached it yet.
func (d *Daemon) decisionsOf(agent string) []store.HubWait {
	ws, err := d.st.UndeliveredHubWaits(agent)
	if err != nil {
		log.Printf("lea daemon: decisions of %s: %v", agent, err)
		return nil
	}
	return ws
}

// markDelivered records that the decisions reached agent, once its resume carried them: until
// then they stay undelivered, so a failed resume or a daemon that stops in between loses none.
// One delivered meanwhile by another path is left as it is.
func (d *Daemon) markDelivered(agent string, ws []store.HubWait) {
	for _, w := range ws {
		if err := d.st.MarkHubWaitDelivered(agent, w.Ticket, d.now()); err != nil && !errors.Is(err, store.ErrNotFound) {
			log.Printf("lea daemon: deliver %s to %s: %v", w.Ticket, agent, err)
		}
	}
}

// resumePrompt is a suspended agent's next turn: the decisions taken on its tickets, then what
// waits in its inbox.
func resumePrompt(ws []store.HubWait, notice string) string {
	var lines []string
	for _, w := range ws {
		lines = append(lines, hubDecisionText(w))
	}
	if notice != "" {
		lines = append(lines, notice)
	}
	return strings.Join(lines, "\n")
}
