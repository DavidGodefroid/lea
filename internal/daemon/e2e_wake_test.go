package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/store"
)

// firstTurn waits for the agent's first turn to end (its Stop) and returns it.
func (e *claudeE2E) firstTurn() store.Agent {
	e.t.Helper()
	return e.until("the first turn", 45, func(a store.Agent) bool {
		ps, _ := e.st.EventPayloads(a.Key, "Stop")
		return a.State == "idle" && len(a.SessionID) == 36 && len(ps) > 0
	})
}

// requests is how many Messages API requests the fake API had.
func (e *claudeE2E) requests() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.bodies)
}

// wokenFor checks a message from the user starts a turn of the idle agent with lea's unread
// notice, the body not in it, with nobody typing, and that lea recorded the wake (#297).
func (e *claudeE2E) wokenFor(api string) {
	e.t.Helper()
	before := e.requests()
	if err := call(e.t, api, "", "msg_send", map[string]any{"to": "dev", "body": "your lead answered: QUOKKA297", "kind": "result"}, nil); err != nil {
		e.t.Fatal(err)
	}
	e.until("the woken turn", 75, func(b store.Agent) bool {
		ps, _ := e.st.EventPayloads(b.Key, "rewake")
		return b.State == "idle" && len(ps) > 0 && e.requests() > before
	})
	e.mu.Lock()
	defer e.mu.Unlock()
	// The woken request carries the notice only; the bodies come later, from the inbox (here the
	// turn's Stop, as the fake model calls no tool), in lea's untrusted wrapping.
	for _, b := range e.bodies[before:] {
		if strings.Contains(b, "unread message(s) from user") {
			if strings.Contains(b, "QUOKKA297") {
				e.t.Fatalf("the wake holds the message's body:\n%s", b[max(0, len(b)-3000):])
			}
			return
		}
	}
	woken := strings.Join(e.bodies[before:], "\n")
	e.t.Fatalf("no woken request carries the unread notice:\n%s", woken[max(0, len(woken)-3000):])
}

// The real Claude Code, idle after its turn, is woken for a message after lea's daemon restarted
// (#297): its Rewake job tries again until the new daemon's bus is up, and the new daemon knows
// the agent idle from the store.
func TestClaudeIdleAgentIsWokenAfterADaemonRestart(t *testing.T) {
	e := newClaudeE2E(t)
	e.setLimited(false)
	e.run("say hi")
	e.firstTurn()
	time.Sleep(3 * time.Second) // the Rewake job of the Stop watches
	e.restartDaemon(func() { time.Sleep(2 * time.Second) })
	e.wokenFor(e.api)
}

// A session resumed after a container restart (claude --resume, no prompt) sits idle with no
// Stop: its SessionStart Rewake job wakes it for a message (#297).
func TestClaudeResumedSessionIsWokenByAMessage(t *testing.T) {
	e := newClaudeE2E(t)
	e.setLimited(false)
	kill := e.run("say hi")
	a := e.firstTurn()
	starts, _ := e.st.EventPayloads(a.Key, "SessionStart")
	kill()
	time.Sleep(2 * time.Second) // the ended session's last hooks
	e.run("--resume", a.SessionID)
	e.until("the resumed session", 45, func(b store.Agent) bool {
		ps, _ := e.st.EventPayloads(b.Key, "SessionStart")
		return len(ps) > len(starts) && b.State == "idle"
	})
	e.wokenFor(e.api)
}
