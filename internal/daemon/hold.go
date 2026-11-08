package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/DavidGodefroid/lea/internal/bus"
	"github.com/DavidGodefroid/lea/internal/store"
)

// The user's pause of an agent's inbox (#95, the deck's H, lea hold): the agents' messages to it
// stay unread and wake it by no path (Stop hook, rewake, context notice, msg_inbox, resume of a
// suspended agent) until the user releases it, then arrive at once. Nothing is frozen: the agent
// still takes the user's turns, the user's messages reach it, and so do the replies in a thread
// it opened during the pause. Only the user pauses or releases an inbox (host only); lea
// releases one on its own after LEA_HOLD_MAX.

// DefaultHoldMax is how long a pause lasts before lea releases it (LEA_HOLD_MAX).
const DefaultHoldMax = 30 * time.Minute

// SetHoldMax is how long a pause lasts before lea releases it; 0 never releases.
func (d *Daemon) SetHoldMax(m time.Duration) {
	d.mu.Lock()
	d.holdMax = m
	d.mu.Unlock()
}

// inboxHold is what me's inbox holds back now: the agents' asks while the user has its open
// question, and everything the user's pause holds.
func (d *Daemon) inboxHold(me string) store.Hold {
	h := store.Hold{Asks: d.holdsAsks(me)}
	if a, err := d.st.AgentByKey(me); err == nil {
		h.Since = a.HoldSince
	}
	return h
}

// holdNote is the line msg_inbox adds while me's inbox is paused and holds messages: the agent
// can tell the user, and does not read its inbox in a loop.
func (d *Daemon) holdNote(me string) (deliveredView, bool) {
	a, err := d.st.AgentByKey(me)
	if err != nil || a.HoldSince == 0 {
		return deliveredView{}, false
	}
	n, err := d.st.HeldCount(me, a.HoldSince)
	if err != nil || n == 0 {
		return deliveredView{}, false
	}
	return deliveredView{From: "lea", Kind: "note", Text: fmt.Sprintf(
		"lea: the user paused your inbox: %d message(s) from agents are held; they arrive when the user releases it. "+
			"Do not read your inbox again for them meanwhile.", n)}, true
}

// holdView is agent_hold's answer.
type holdView struct {
	Agent string `json:"agent"`
	Held  bool   `json:"held"`
	Count int    `json:"count"`
}

// agentHold is agent_hold (host only: the deck's H, lea hold): the user pauses an agent's inbox
// or releases it. With no agent, it is the only live lead's.
func (d *Daemon) agentHold(raw json.RawMessage) (any, error) {
	var p struct {
		Agent string `json:"agent"`
		Held  bool   `json:"held"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	key, err := d.holdTarget(p.Agent)
	if err != nil {
		return nil, err
	}
	if p.Held {
		return d.hold(key)
	}
	return d.release(key, "the user released it")
}

// holdTarget is the agent agent_hold names, or the only live lead when it names none.
func (d *Daemon) holdTarget(name string) (string, error) {
	as, err := d.st.Agents()
	if err != nil {
		return "", err
	}
	if name != "" {
		ks := make([]string, 0, len(as))
		for _, a := range as {
			ks = append(ks, a.Key)
		}
		k, err := bus.Resolve(name, ks)
		if err != nil {
			return "", fmt.Errorf("hold: %w", err)
		}
		return k, nil
	}
	var leads []string
	for _, a := range as {
		if canSpawn(a.Role) && a.State != "done" && a.State != "stopped" {
			leads = append(leads, a.Key)
		}
	}
	if len(leads) != 1 {
		return "", fmt.Errorf("hold: %d live leads: name the agent", len(leads))
	}
	return leads[0], nil
}

// hold pauses key's inbox; one already paused keeps its start.
func (d *Daemon) hold(key string) (holdView, error) {
	a, err := d.st.AgentByKey(key)
	if errors.Is(err, store.ErrNotFound) {
		return holdView{}, fmt.Errorf("hold: no agent %s", key)
	}
	if err != nil {
		return holdView{}, err
	}
	if a.State == "done" || a.State == "stopped" {
		return holdView{}, fmt.Errorf("hold: %s is not running", key)
	}
	since := a.HoldSince
	if since == 0 {
		since = d.now()
		if err := d.st.SetHold(key, since); err != nil {
			return holdView{}, err
		}
		d.changed()
	}
	n, err := d.st.HeldCount(key, since)
	return holdView{Agent: key, Held: true, Count: n}, err
}

// release ends key's pause (why says by whom): the agent hears once of the messages it held, and
// a rewake watch held meanwhile answers.
func (d *Daemon) release(key, why string) (holdView, error) {
	a, err := d.st.AgentByKey(key)
	if errors.Is(err, store.ErrNotFound) {
		return holdView{}, fmt.Errorf("hold: no agent %s", key)
	}
	if err != nil {
		return holdView{}, err
	}
	if a.HoldSince == 0 {
		return holdView{Agent: key}, nil
	}
	n, err := d.st.HeldCount(key, a.HoldSince)
	if err != nil {
		return holdView{}, err
	}
	if err := d.st.SetHold(key, 0); err != nil {
		return holdView{}, err
	}
	if n > 0 {
		d.post("lea", key, "note", fmt.Sprintf("lea: your inbox was paused (%s): %d message(s) were held. "+
			"Read them all with msg_inbox, until it is empty, and give the user one briefing of them.", why, n), 0)
	}
	d.wake(key)
	d.changed()
	return holdView{Agent: key, Count: n}, nil
}

// RunHoldExpiry releases the pauses older than the hold max, until ctx ends.
func (d *Daemon) RunHoldExpiry(ctx context.Context) {
	t := time.NewTicker(questionTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.expireHolds()
		}
	}
}

func (d *Daemon) expireHolds() {
	d.mu.Lock()
	m := d.holdMax
	d.mu.Unlock()
	if m <= 0 {
		return
	}
	ks, err := d.st.ExpiredHolds(d.now() - int64(m/time.Second))
	if err != nil {
		log.Printf("lea daemon: expired holds: %v", err)
		return
	}
	for _, k := range ks {
		if _, err := d.release(k, fmt.Sprintf("lea released it after %s", m)); err != nil {
			log.Printf("lea daemon: release %s: %v", k, err)
		}
	}
}
