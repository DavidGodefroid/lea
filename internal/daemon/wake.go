package daemon

import (
	"encoding/json"
	"log"
	"time"
)

// Waking an idle agent for its inbox (#297). The waker is the agent's Rewake job (lea hook
// claude Rewake, an asyncRewake hook of Claude Code): a message to an agent whose turn is over
// ends the job's rewake watch, and Claude Code starts a turn with lea's unread notice; the turn
// reads the messages with msg_inbox, under the untrusted wrapping. lea types nothing into a
// terminal.
//
// The job starts at every turn end (Stop), every session start (SessionStart: a session resumed
// after a container restart is idle without a Stop) and after each Lock call; it renews its
// watch for days and tries again while the daemon restarts. A daemon that starts again knows
// the agents idle in the store as idle (idleSince), so the job's watch on the new daemon answers.
//
// The daemon holds a wake (the watch keeps waiting, the messages stay unread) while the agent is
// paused in the deck, at work (a stale turn-over mark must not wake it mid-turn), stopped by the
// user or ended, stopped on its usage limit (its own resume comes first), or woken rewakeBurst
// times within rewakeWindow: two agents that keep writing to each other cannot keep each other
// awake. A wake shows in the deck ("woken by lea") until its turn ends or the user types, and is
// recorded as an event.

var (
	// rewakeBurst wakes of one agent within rewakeWindow seconds at most; the next waits.
	rewakeBurst        = 10
	rewakeWindow int64 = 600
)

// rewakeHold is how long a rewake watch that would answer waits before it looks again: 0 when it
// may answer now.
func (d *Daemon) rewakeHold(me string) time.Duration {
	if a, err := d.st.AgentByKey(me); err == nil {
		switch {
		case a.Paused, a.LimitUntil != 0:
			return pausedRecheck
		case a.State == "stopped", a.State == "done", a.State == "blocked", a.State == "working":
			// Working too: the store may show the agent mid-turn while its turn-over mark is
			// stale (the user typed while the daemon was down); never wake an agent mid-turn.
			return pausedRecheck
		}
	}
	now := d.now()
	d.mu.Lock()
	defer d.mu.Unlock()
	ts := d.rewoken[me]
	for len(ts) > 0 && ts[0] <= now-rewakeWindow {
		ts = ts[1:]
	}
	d.rewoken[me] = ts
	if len(ts) < rewakeBurst {
		return 0
	}
	return time.Duration(max(ts[0]+rewakeWindow-now, 1)) * time.Second
}

// rewoke records that a rewake watch answered for n unread messages: the rate window, the
// event, and the deck's mark until the woken turn ends.
func (d *Daemon) rewoke(me string, n any) {
	now := d.now()
	d.mu.Lock()
	d.rewoken[me] = append(d.rewoken[me], now)
	d.woken[me] = true
	d.mu.Unlock()
	b, _ := json.Marshal(map[string]any{"unread": n})
	if err := d.st.AddEvent(me, "lea", "rewake", string(b), now); err != nil {
		log.Printf("lea daemon: rewake event of %s: %v", me, err)
	}
	if err := d.st.SetWoken(me, true); err != nil {
		log.Printf("lea daemon: mark %s woken: %v", me, err)
	}
	log.Printf("lea daemon: woke %s for %v unread message(s)", me, n)
	d.changed()
}

// endWoken clears the deck's mark of a woken agent: its turn ended, or the user typed.
func (d *Daemon) endWoken(me string) {
	d.mu.Lock()
	was := d.woken[me]
	delete(d.woken, me)
	d.mu.Unlock()
	if !was {
		return
	}
	if err := d.st.SetWoken(me, false); err != nil {
		log.Printf("lea daemon: clear woken %s: %v", me, err)
	}
	d.changed()
}

// idleSinceLocked, in a daemon that attaches a container's bus, takes the agents the store shows
// idle as idle: their turn is over (a restarted daemon lost what their hooks told the previous
// one), so their Rewake job, back on this bus, wakes them for a message. It runs before the bus
// serves, so no hook of theirs races it. A woken mark left by the previous daemon is cleared.
// Callers hold d.mu.
func (d *Daemon) idleSinceLocked(container string) {
	as, err := d.st.Agents()
	if err != nil {
		log.Printf("lea daemon: idle agents of %s: %v", container, err)
		return
	}
	for _, a := range as {
		if a.Container != container {
			continue
		}
		if a.Woken && !d.woken[a.Key] {
			if err := d.st.SetWoken(a.Key, false); err != nil {
				log.Printf("lea daemon: clear woken %s: %v", a.Key, err)
			}
		}
		if a.State == "idle" {
			d.stopped[a.Key] = true
		}
	}
}

// idleAtStart says a SessionStart leaves the agent at its prompt, its turn over: a resumed
// session (a container restart, lea's resume) or a cleared one. A new session gets its first
// prompt next; a compaction may run inside a turn.
func idleAtStart(payload json.RawMessage) bool {
	var p struct {
		Source string `json:"source"`
	}
	return json.Unmarshal(payload, &p) == nil && (p.Source == "resume" || p.Source == "clear")
}

// forgetWokenLocked drops an agent's wake bookkeeping. Callers hold d.mu.
func (d *Daemon) forgetWokenLocked(key string) {
	delete(d.rewoken, key)
	delete(d.woken, key)
}
