package daemon

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/DavidGodefroid/lea/internal/agents"
)

// Idle stacks (#132): an agent's nested pods (a database, a search engine, a broker…) keep their
// memory and CPU while the agent waits. Once it has been idle stackIdle, lea stops them, without
// removing them: the agent starts them again when it needs them.

// DefaultStackIdle is how long an agent stays idle before its stack is stopped (LEA_STACK_IDLE).
const DefaultStackIdle = 10 * time.Minute

// parkTimeout bounds one stop of an agent's stack.
const parkTimeout = 2 * time.Minute

// SetStackIdle is how long an agent stays idle before its stack is stopped; 0 never stops it.
func (d *Daemon) SetStackIdle(idle time.Duration) {
	d.mu.Lock()
	d.stackIdle = max(idle, 0)
	d.mu.Unlock()
}

// stackActivity follows the agent's state: idle arms the stop of its stack, any other state
// cancels it, and a new turn forgets what the last stop freed (the agent may start it again).
func (d *Daemon) stackActivity(key, state string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if state == "idle" && d.parkTimers[key] != nil {
		// Still idle (idle_prompt after a Stop): the wait goes on from its start.
		return
	}
	if t := d.parkTimers[key]; t != nil {
		t.Stop()
		delete(d.parkTimers, key)
	}
	if state == "working" {
		delete(d.parked, key)
	}
	if state != "idle" || d.stackIdle <= 0 {
		return
	}
	var t *time.Timer
	t = time.AfterFunc(d.stackIdle, func() {
		d.mu.Lock()
		mine := d.parkTimers[key] == t
		if mine {
			delete(d.parkTimers, key)
		}
		d.mu.Unlock()
		if mine {
			d.parkIdle(key)
		}
	})
	d.parkTimers[key] = t
}

// forgetStackLocked drops the stack bookkeeping of an agent that is gone. Callers hold d.mu.
func (d *Daemon) forgetStackLocked(key string) {
	if t := d.parkTimers[key]; t != nil {
		t.Stop()
	}
	delete(d.parkTimers, key)
	delete(d.parked, key)
}

// parkIdle stops the stack of an agent lea started that is still idle, and records what it
// freed. It returns that, "" when nothing was stopped. An agent whose subagents still run, or
// whose stack still works (a background job), is looked at again one delay later; one that
// started a new turn while its stack stopped gets it started again.
func (d *Daemon) parkIdle(key string) string {
	d.mu.Lock()
	a := d.agents
	subs := 0
	if s := d.subs[key]; s != nil {
		subs = len(s.ids)
	}
	d.mu.Unlock()
	if a == nil {
		return ""
	}
	ag, err := d.st.AgentByKey(key)
	if err != nil || ag.Profile == "" || ag.State != "idle" {
		return ""
	}
	if subs > 0 {
		d.stackActivity(key, "idle")
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), parkTimeout)
	defer cancel()
	note, err := a.ParkStack(ctx, key)
	if errors.Is(err, agents.ErrStackBusy) {
		d.stackActivity(key, "idle")
		return ""
	}
	if err != nil {
		log.Printf("lea daemon: stop the stack of idle %s: %v", key, err)
		return ""
	}
	if note == "" {
		return ""
	}
	// A new turn came while the stack stopped: it is the agent's again.
	if ag, err := d.st.AgentByKey(key); err == nil && ag.State != "idle" {
		if _, err := a.UnparkStack(ctx, key); err != nil {
			log.Printf("lea daemon: start the stack of %s again: %v", key, err)
		}
		return ""
	}
	log.Printf("lea daemon: idle %s", note)
	d.mu.Lock()
	d.parked[key] = note + " at " + time.Unix(d.now(), 0).Format("15:04")
	d.mu.Unlock()
	d.changed()
	return note
}

// parkIdleSince stops the stacks of the container's agents idle for stackIdle already, and
// returns what each stop freed.
func (d *Daemon) parkIdleSince(container string) []string {
	d.mu.Lock()
	idle := d.stackIdle
	d.mu.Unlock()
	if idle <= 0 {
		return nil
	}
	as, err := d.st.SpawnedAgents(container)
	if err != nil {
		return nil
	}
	var notes []string
	for _, ag := range as {
		if ag.State != "idle" || time.Duration(d.now()-ag.Updated)*time.Second < idle {
			continue
		}
		if n := d.parkIdle(ag.Key); n != "" {
			notes = append(notes, n)
		}
	}
	return notes
}
