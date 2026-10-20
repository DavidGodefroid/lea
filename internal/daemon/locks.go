package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/DavidGodefroid/lea/internal/agents"
	"github.com/DavidGodefroid/lea/internal/store"
)

// Lock requests (issue #18, phase 2): an agent asks the user to open a lock tool's console; it
// never opens one. The request names a tool and a profile, both checked (a lock tool lea opens, a
// profile on the user's allow-list for agents), and nothing else: no flag, no argv, no text of the
// agent's that the deck would show. The deck shows it as a card on the agent's row; the user
// opens the console (lock_request_decide, host only: the console opens through LockOpen, the
// user's own path) or declines. The agent learns opened, declined, failed or expired, never what
// the console shows or whether one runs.

// lockRequestGap is the time between two requests for a tool from one container. The container,
// not the agent, is the unit: any process in it can claim any agent name.
const lockRequestGap = 60

// lockRequest is an agent's request (bus, agents only). It answers requested, or an error that
// says why it was refused.
func (d *Daemon) lockRequest(ctx context.Context, o Origin, me string, raw json.RawMessage) (any, error) {
	caller, err := d.spawnedCaller(o, me, "lock_request")
	if err != nil {
		return nil, err
	}
	var p struct {
		Tool    string `json:"tool"`
		Profile string `json:"profile"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if err := d.rates.allow("lock_request", me, d.now()); err != nil {
		return nil, fmt.Errorf("lock_request refused: %w", err)
	}
	a := d.manager()
	if a == nil {
		return nil, errors.New("lock_request refused: lock consoles are not available in this daemon")
	}
	if err := a.LockCheckRequest(ctx, o.Container, p.Tool, p.Profile); err != nil {
		return nil, fmt.Errorf("lock_request refused: %w", err)
	}
	r, err := d.st.AddLockRequest(o.Container, caller.Key, p.Tool, p.Profile, agents.LockRequestTTL, lockRequestGap, d.now())
	if err != nil {
		return nil, fmt.Errorf("lock_request refused: %w", err)
	}
	log.Printf("lea daemon: lock request #%d: %s asks for the %s/%s console", r.ID, caller.Key, r.Tool, r.Profile)
	// A sweep closes it when its time is up, and tells the agent.
	time.AfterFunc(time.Duration(agents.LockRequestTTL+1)*time.Second, d.sweepLockRequests)
	d.changed()
	d.sound(ctx, o.Container, SoundAttention)
	return map[string]any{"status": "requested", "request": r.ID}, nil
}

// lockRequestDecide is the user's answer to a lock request in the deck (host only): open opens
// the console the request named, through LockOpen as the user's L would; else the request is
// declined. Only a pending, unexpired request of that container is answered, once.
func (d *Daemon) lockRequestDecide(ctx context.Context, a Agents, raw json.RawMessage) (any, error) {
	var p struct {
		Container string `json:"container"`
		ID        int64  `json:"id"`
		Open      bool   `json:"open"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	state := "declined"
	if p.Open {
		state = "opened"
	}
	r, err := d.st.ClaimLockRequest(p.ID, p.Container, state, agents.LockRequestTTL, d.now())
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, fmt.Errorf("no lock request #%d in this deck", p.ID)
	case errors.Is(err, store.ErrDecided):
		return nil, fmt.Errorf("lock request #%d is already answered", p.ID)
	case errors.Is(err, store.ErrExpired):
		d.lockNote(r, "expired: nobody answered in time")
		d.changed()
		return nil, fmt.Errorf("lock request #%d expired", p.ID)
	case err != nil:
		return nil, err
	}
	if !p.Open {
		log.Printf("lea daemon: lock request #%d declined by the user", r.ID)
		d.lockNote(r, "declined by the user")
		d.changed()
		return map[string]string{"status": "declined"}, nil
	}
	st, err := a.LockOpen(ctx, r.Container, r.Tool, r.Profile)
	if err != nil {
		if ferr := d.st.FailLockRequest(r.ID, err.Error()); ferr != nil {
			log.Printf("lea daemon: record lock request #%d: %v", r.ID, ferr)
		}
		log.Printf("lea daemon: lock request #%d: the console did not open: %v", r.ID, err)
		r.State = "failed"
		// The reason is the user's to read, in the deck: the agent learns only that it failed.
		d.lockNote(r, "the user said yes, but the console did not open")
		d.changed()
		return nil, err
	}
	log.Printf("lea daemon: lock request #%d opened by the user (%s)", r.ID, st)
	d.lockNote(r, "opened by the user")
	d.changed()
	return map[string]string{"status": st}, nil
}

// lockNote tells the agent that asked what came of its request: lea's words, the checked tool and
// profile, and nothing of the console.
func (d *Daemon) lockNote(r store.LockRequest, what string) {
	status := "ok"
	if r.State != "opened" {
		status = "fail"
	}
	d.post("user", r.Agent, "result", fmt.Sprintf("[status: %s] lock request #%d (%s/%s): %s.", status, r.ID, r.Tool, r.Profile, what), 0)
}

// sweepLockRequests closes the requests nobody answered in time and tells their agents.
func (d *Daemon) sweepLockRequests() {
	rs, err := d.st.SweepLockRequests(agents.LockRequestTTL, d.now())
	if err != nil {
		log.Printf("lea daemon: sweep lock requests: %v", err)
		return
	}
	for _, r := range rs {
		d.lockNote(r, "expired: nobody answered in time")
	}
	if len(rs) > 0 {
		d.changed()
	}
}

// SweepLockRequestsLater closes, now and once their time is up, the requests a previous daemon
// left pending, and tells their agents (lea's daemon at its start).
func (d *Daemon) SweepLockRequestsLater() {
	d.sweepLockRequests()
	time.AfterFunc(time.Duration(agents.LockRequestTTL+1)*time.Second, d.sweepLockRequests)
}
