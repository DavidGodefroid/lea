package agents

import (
	"context"
	"errors"
	"fmt"

	"github.com/DavidGodefroid/lea/internal/store"
)

// Suspended agents (the Lock Family approval hub, #161): an agent whose Lock call waits for a
// human's decision ends its turn, suspended_warm, its process still running. When its keepalive
// ends, lea stops that agent alone, keeping its session: suspended_cold. A decision or a message
// resumes it. lea never forgets a suspended agent: its session is what the resume continues.

// ErrNotSuspended is SoftStop's or ResumeSuspended's answer for an agent that is no longer in
// the state it expects (it woke up meanwhile, or it was already stopped).
var ErrNotSuspended = errors.New("agent not suspended")

// SoftStop stops an agent whose keepalive ended (suspended_warm): its process and its pane go,
// its row and its session stay, as suspended_cold. What it started in the nested Podman stays
// too: it is the agent's again when it resumes.
func (m *Manager) SoftStop(ctx context.Context, key string) error {
	a, err := m.spawned(key)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// Read again under the lock: a new turn may have woken it since.
	if a, err = m.spawned(key); err != nil {
		return err
	}
	if a.State != store.StateSuspendedWarm {
		return fmt.Errorf("%s is %s: %w", a.Name, a.State, ErrNotSuspended)
	}
	// The state first: the SessionEnd of the stopped process finds the agent cold, and reconcile
	// does not take it for ended.
	if err := m.cfg.Store.SetState(key, store.StateSuspendedCold, "", m.cfg.Now()); err != nil {
		return err
	}
	if err := m.cfg.Containers.Stop(ctx, a.Container, a.Name); err != nil {
		if serr := m.cfg.Store.SetState(key, store.StateSuspendedWarm, "", m.cfg.Now()); serr != nil {
			err = errors.Join(err, serr)
		}
		return err
	}
	if s, err := m.session(ctx, a.Container); err == nil {
		if ps, err := listPanes(ctx, s, a.Container); err == nil {
			if p, ok := byAgent(ps, a.Name); ok {
				if err := m.removePane(ctx, s, ps, p); err != nil {
					return err
				}
			}
		}
	}
	m.smu.Lock()
	if m.shown[a.Container] == key {
		m.shown[a.Container] = ""
	}
	m.smu.Unlock()
	m.feed.bump()
	return nil
}

// ResumeSuspended starts a suspended_cold agent again in its container, parked, with `--resume
// <session>` and prompt as its next turn: what woke it (a decision, unread messages). Without a
// session Claude reported, there is nothing to resume: the agent ends (done).
func (m *Manager) ResumeSuspended(ctx context.Context, key, prompt string) error {
	if prompt == "" {
		return errors.New("resume: empty prompt")
	}
	a, err := m.spawned(key)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, err = m.spawned(key); err != nil {
		return err
	}
	if a.State != store.StateSuspendedCold {
		return fmt.Errorf("%s is %s: %w", a.Name, a.State, ErrNotSuspended)
	}
	if !sessionRe.MatchString(a.SessionID) {
		if _, err := m.endSuspendedLocked(a); err != nil {
			return err
		}
		return fmt.Errorf("%s has no session to resume", a.Name)
	}
	s, err := m.session(ctx, a.Container)
	if err != nil {
		return err
	}
	defer m.feed.bump()
	return m.restartWith(ctx, s, a.Container, a, a.SessionID, prompt)
}

// EndSuspended ends an agent lea gave up resuming: done, only while it is still suspended_cold
// (one that woke or ended meanwhile is left alone, false), and what it started in the nested
// Podman is torn down, as for any agent that ends: SoftStop kept it for a resume that will not
// come.
func (m *Manager) EndSuspended(ctx context.Context, key string) (bool, error) {
	a, err := m.spawned(key)
	if err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.endSuspendedLocked(a)
}

// endSuspendedLocked is EndSuspended under m.mu.
func (m *Manager) endSuspendedLocked(a store.Agent) (bool, error) {
	ok, err := m.cfg.Store.EndSuspended(a.Key, m.cfg.Now())
	if err != nil || !ok {
		return false, err
	}
	m.teardownLater(a.Container, a.Name)
	m.feed.bump()
	return true, nil
}
