package daemon

import (
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/DavidGodefroid/lea/internal/store"
	"github.com/DavidGodefroid/lea/internal/textsafe"
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

// takeDecisions marks agent's undelivered decisions delivered and returns them, the ones another
// path delivered meanwhile left out.
func (d *Daemon) takeDecisions(agent string) []store.HubWait {
	ws, err := d.st.UndeliveredHubWaits(agent)
	if err != nil {
		log.Printf("lea daemon: decisions of %s: %v", agent, err)
		return nil
	}
	var out []store.HubWait
	for _, w := range ws {
		if err := d.st.MarkHubWaitDelivered(agent, w.Ticket, d.now()); err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				log.Printf("lea daemon: deliver %s to %s: %v", w.Ticket, agent, err)
			}
			continue
		}
		out = append(out, w)
	}
	return out
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

// hubDecisionText is what an agent is told of the decision on its wait. The ticket, Lock and
// instance were checked when the wait was recorded; the reason is the hub's, at most 500 bytes.
func hubDecisionText(w store.HubWait) string {
	switch w.Decision {
	case store.DecisionApproved:
		return fmt.Sprintf("[status: ok] ticket %s on %s (%s) approved: call its run again with ticket=%s.", w.Ticket, w.Lock, w.Instance, w.Ticket)
	case store.DecisionDenied:
		return fmt.Sprintf("[status: fail] ticket %s on %s (%s) denied: %s. Do not retry it as is.", w.Ticket, w.Lock, w.Instance, orNone(w.Reason))
	case store.DecisionExpired:
		return fmt.Sprintf("[status: fail] ticket %s on %s (%s) expired before a decision: plan it again if it is still needed.", w.Ticket, w.Lock, w.Instance)
	default:
		return fmt.Sprintf("[status: fail] ticket %s on %s (%s) withdrawn by the Lock: %s.", w.Ticket, w.Lock, w.Instance, orNone(w.Reason))
	}
}

// orNone is the hub's reason on one line, without control, bidi or zero-width characters.
func orNone(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || textsafe.Unsafe(r) {
			return ' '
		}
		return r
	}, s)
	if strings.TrimSpace(s) == "" {
		return "no reason given"
	}
	return s
}
