package agents

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/DavidGodefroid/lea/internal/textsafe"
)

// The ticket pane (#284): a hub ticket selected in the deck's hub panel shows whole in the slot,
// where the agent's session normally shows. It is a pane of lea's own, made on first use and
// parked in a window of its own like the placeholder, running `lea deck --ticket CONTAINER`, which
// watches the daemon and renders the ticket the snapshot names (Snapshot.Ticket) from the hub
// queue. The deck says which ticket to show (ShowTicket); showing anything else in the slot ends it.

const (
	roleTicket = "ticket"
	// maxTicketRef bounds the ticket reference the deck sends (instance/id); maxTicketNote what
	// it says under the ticket (a refusal and its fix, or the approve-all report).
	maxTicketRef  = 256
	maxTicketNote = 4096
)

// ticketView is the ticket the slot shows for one container: ref (instance/id, "" for a note
// alone) and the deck's note; in reports whether the ticket pane is in the slot.
type ticketView struct {
	ref, note string
	in        bool
}

func (m *Manager) ticketCmd(container string) []string {
	return []string{m.cfg.LeaBin, "deck", "--ticket", container}
}

// ShowTicket shows the hub ticket ref (instance/id) in the container's slot, the note under it,
// without moving the keyboard. A ref "" with a note shows the note alone; both "" hide the
// ticket: when the ticket pane holds the slot, the placeholder takes it back (the deck shows its
// agent next).
//
// seq orders a deck's calls, which overtake each other: one older than the last taken is dropped
// (0: unordered, always taken), so the slot never ends on a ticket the deck no longer selects.
func (m *Manager) ShowTicket(ctx context.Context, container, ref, note string, seq int64) error {
	if !containerRe.MatchString(container) {
		return errors.New("bad container name")
	}
	if len(ref) > maxTicketRef || strings.ContainsFunc(ref, textsafe.Unsafe) || !utf8.ValidString(ref) {
		return errors.New("bad ticket reference")
	}
	if len(note) > maxTicketNote || !utf8.ValidString(note) {
		return errors.New("the note is too long")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if seq != 0 {
		m.smu.Lock()
		stale := seq < m.ticketSeqs[container]
		if !stale {
			m.ticketSeqs[container] = seq
		}
		m.smu.Unlock()
		if stale {
			return nil
		}
	}
	s, err := m.session(ctx, container)
	if err != nil {
		return err
	}
	ps, err := listPanes(ctx, s, container)
	if err != nil {
		return err
	}
	if ref == "" && note == "" {
		m.setTicket(container, ticketView{})
		_, slot, ok := deckAndSlot(ps)
		if !ok || slot.Role != roleTicket {
			return nil
		}
		if ph, ok := byRole(ps, rolePlace); ok {
			if err := m.show(ctx, s, container, ps, ph, false); err != nil {
				return err
			}
		}
		if ps, err = listPanes(ctx, s, container); err != nil {
			return err
		}
		m.publish(container, ps)
		return nil
	}
	p, ok := byRole(ps, roleTicket)
	switch {
	case !ok:
		out, err := s.Run(ctx, append([]string{"new-window", "-d", "-t", "=" + container + ":", "-n", roleTicket, "-P", "-F", "#{pane_id}", "--"},
			m.ticketCmd(container)...)...)
		if err != nil {
			return err
		}
		if len(out) != 1 {
			return errors.New("tmux did not print the ticket pane's id")
		}
		if _, err := s.Run(ctx, "set-option", "-p", "-t", out[0], optRole, roleTicket); err != nil {
			return err
		}
		if ps, err = listPanes(ctx, s, container); err != nil {
			return err
		}
		if p, ok = byRole(ps, roleTicket); !ok {
			return errors.New("the new ticket pane is gone")
		}
	case p.Dead:
		// A ticket pane that keeps dying is started again with the same backoff as any pane.
		if !m.respawnDue(container, p.ID) {
			return errors.New("the ticket pane keeps exiting: try again in a moment")
		}
		if _, err := s.Run(ctx, append([]string{"respawn-pane", "-k", "-t", p.ID, "--"}, m.ticketCmd(container)...)...); err != nil {
			return err
		}
	}
	// What the pane renders is set before it takes the slot: it never shows the previous ticket.
	m.setTicket(container, ticketView{ref: ref, note: note})
	if err := m.show(ctx, s, container, ps, p, false); err != nil {
		return err
	}
	if ps, err = listPanes(ctx, s, container); err != nil {
		return err
	}
	m.publish(container, ps)
	return nil
}

// setTicket records the container's ticket view and tells the watchers.
func (m *Manager) setTicket(container string, v ticketView) {
	m.smu.Lock()
	if m.tickets[container] == v {
		m.smu.Unlock()
		return
	}
	m.tickets[container] = v
	m.smu.Unlock()
	m.feed.bump()
}

// slotTicket follows the slot: the ticket shows while the ticket pane holds it; anything else in
// the slot ends it (the ticket is forgotten, so a later swap never brings an old one back).
func (m *Manager) slotTicket(container string, inSlot bool) {
	m.smu.Lock()
	v := m.tickets[container]
	m.smu.Unlock()
	switch {
	case inSlot && !v.in && (v.ref != "" || v.note != ""):
		v.in = true
		m.setTicket(container, v)
	case !inSlot && v != ticketView{}:
		m.setTicket(container, ticketView{})
	}
}

// ticketOf is what the container's snapshot says of the ticket shown: nothing unless the ticket
// pane holds the slot.
func (m *Manager) ticketOf(container string) (ref, note string) {
	m.smu.Lock()
	defer m.smu.Unlock()
	if v := m.tickets[container]; v.in {
		return v.ref, v.note
	}
	return "", ""
}
