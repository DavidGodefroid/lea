package agents

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// A hub ticket the deck shows takes the slot in a pane of its own (lea deck --ticket), made on
// first use, without moving the keyboard; the snapshot names it with the deck's note while it is
// there. An agent shown next ends it; a hide puts the placeholder back (#284).
func TestShowTicketTakesTheSlot(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	a := e.spawn(t, "a1")
	if err := e.m.ShowTicket(bg, C, "crm/T1", "", 0); err != nil {
		t.Fatal(err)
	}
	tp := s.byRole(roleTicket)
	if tp == nil || s.slot() != tp || !reflect.DeepEqual(tp.cmd, []string{leaBin, "deck", "--ticket", C}) {
		t.Fatalf("ticket pane=%+v slot=%+v", tp, s.slot())
	}
	if s.logged("select-pane -t " + tp.id) {
		t.Fatal("showing a ticket moved the keyboard")
	}
	snap, _ := e.m.Watch(bg, C, 0, time.Second)
	if snap.Ticket != "crm/T1" || snap.TicketNote != "" || snap.Shown != "" {
		t.Fatalf("snapshot ticket=%q note=%q shown=%q", snap.Ticket, snap.TicketNote, snap.Shown)
	}
	// Another ticket, then a refusal under it: the same pane.
	if err := e.m.ShowTicket(bg, C, "crm/T2", "refused", 0); err != nil {
		t.Fatal(err)
	}
	snap, _ = e.m.Watch(bg, C, 0, time.Second)
	if snap.Ticket != "crm/T2" || snap.TicketNote != "refused" || s.byRole(roleTicket) != tp || s.slot() != tp {
		t.Fatalf("second ticket: %q %q", snap.Ticket, snap.TicketNote)
	}
	if err := e.m.Focus(bg, a.Key, false); err != nil {
		t.Fatal(err)
	}
	snap, _ = e.m.Watch(bg, C, 0, time.Second)
	if s.slot() != s.byAgent("a1") || snap.Ticket != "" || snap.TicketNote != "" {
		t.Fatalf("agent shown: slot=%+v ticket=%q", s.slot(), snap.Ticket)
	}
	// Hidden while it holds the slot: the placeholder comes back.
	if err := e.m.ShowTicket(bg, C, "crm/T1", "", 0); err != nil {
		t.Fatal(err)
	}
	if err := e.m.ShowTicket(bg, C, "", "", 0); err != nil {
		t.Fatal(err)
	}
	if snap, _ = e.m.Watch(bg, C, 0, time.Second); s.slot() != s.byRole(rolePlace) || snap.Ticket != "" {
		t.Fatalf("hidden: slot=%+v ticket=%q", s.slot(), snap.Ticket)
	}
	// A hide while an agent holds the slot leaves it there.
	if err := e.m.Focus(bg, a.Key, false); err != nil {
		t.Fatal(err)
	}
	if err := e.m.ShowTicket(bg, C, "", "", 0); err != nil || s.slot() != s.byAgent("a1") {
		t.Fatalf("hide moved the agent: %v", err)
	}
	// A note alone (the approve-all report).
	if err := e.m.ShowTicket(bg, C, "", "2 approved", 0); err != nil {
		t.Fatal(err)
	}
	if snap, _ = e.m.Watch(bg, C, 0, time.Second); s.slot() != tp || snap.TicketNote != "2 approved" {
		t.Fatalf("note alone: %q", snap.TicketNote)
	}
	if err := e.m.ShowTicket(bg, C, "crm/"+strings.Repeat("a", maxTicketRef-4), "", 0); err != nil {
		t.Fatalf("a ref of the longest length: %v", err)
	}
	for _, bad := range []string{"crm/\x1b[2J", "crm/" + strings.Repeat("a", maxTicketRef-3), "crm/\xff"} {
		if err := e.m.ShowTicket(bg, C, bad, "", 0); err == nil {
			t.Fatalf("bad ref %q accepted", bad)
		}
	}
	if err := e.m.ShowTicket(bg, "Bad Name", "crm/T1", "", 0); err == nil {
		t.Fatal("bad container accepted")
	}
}

// A dead ticket pane is started again when a ticket is shown, with the backoff of any pane, and
// by the layout repair.
func TestADeadTicketPaneIsRespawned(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	if err := e.m.ShowTicket(bg, C, "crm/T1", "", 0); err != nil {
		t.Fatal(err)
	}
	tp := s.byRole(roleTicket)
	respawn := "respawn-pane -k -t " + tp.id + " -- " + leaBin + " deck --ticket " + C
	s.locked(func() { tp.dead = true })
	if err := e.m.ShowTicket(bg, C, "crm/T2", "", 0); err != nil {
		t.Fatal(err)
	}
	if tp.dead || !s.logged(respawn) {
		t.Fatalf("not respawned: %q", s.log)
	}
	// Dead again at once: the backoff holds the next respawn.
	s.locked(func() { tp.dead = true })
	mark := s.mark()
	if err := e.m.ShowTicket(bg, C, "crm/T1", "", 0); err == nil || !strings.Contains(err.Error(), "keeps exiting") {
		t.Fatalf("err=%v", err)
	}
	for _, l := range s.since(mark) {
		if l == respawn {
			t.Fatal("respawned within the backoff")
		}
	}
	// The layout repair respawns it once the backoff allows.
	at := time.Now().Add(time.Hour)
	e.m.mu.Lock()
	e.m.clock = func() time.Time { return at }
	e.m.mu.Unlock()
	mark = s.mark()
	if err := e.m.Ensure(bg, C, ""); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range s.since(mark) {
		found = found || l == respawn
	}
	if !found || tp.dead {
		t.Fatalf("the repair did not respawn the ticket pane: %q", s.since(mark))
	}
}

// A new agent shown in the slot ends the ticket: the snapshot never names a ticket while another
// pane holds the slot (#284).
func TestANewAgentShownEndsTheTicket(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	if err := e.m.ShowTicket(bg, C, "crm/T1", "", 0); err != nil {
		t.Fatal(err)
	}
	if snap, _ := e.m.Watch(bg, C, 0, time.Second); snap.Ticket != "crm/T1" {
		t.Fatalf("ticket=%q", snap.Ticket)
	}
	e.spawn(t, "a1")
	snap, _ := e.m.Watch(bg, C, 0, time.Second)
	if s.slot() != s.byAgent("a1") || snap.Ticket != "" {
		t.Fatalf("slot=%+v ticket=%q", s.slot(), snap.Ticket)
	}
	// An agent another agent starts (hidden) leaves the ticket in the slot.
	if err := e.m.ShowTicket(bg, C, "crm/T1", "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.New(bg, NewRequest{Container: C, Name: "a2", Hidden: true}); err != nil {
		t.Fatal(err)
	}
	if snap, _ := e.m.Watch(bg, C, 0, time.Second); snap.Ticket != "crm/T1" {
		t.Fatalf("hidden spawn: ticket=%q", snap.Ticket)
	}
}

// Calls that overtake each other: one older than the last taken is dropped, so the slot stays on
// the ticket the deck selected last.
func TestShowTicketDropsAStaleCall(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	if err := e.m.ShowTicket(bg, C, "crm/T2", "", 20); err != nil {
		t.Fatal(err)
	}
	if err := e.m.ShowTicket(bg, C, "crm/T1", "", 10); err != nil {
		t.Fatal(err)
	}
	if snap, _ := e.m.Watch(bg, C, 0, time.Second); snap.Ticket != "crm/T2" {
		t.Fatalf("ticket=%q, want the latest call's", snap.Ticket)
	}
	if err := e.m.ShowTicket(bg, C, "", "", 15); err != nil {
		t.Fatal(err)
	}
	if snap, _ := e.m.Watch(bg, C, 0, time.Second); snap.Ticket != "crm/T2" {
		t.Fatalf("a stale hide took effect: ticket=%q", snap.Ticket)
	}
	if err := e.m.ShowTicket(bg, C, "", "", 30); err != nil {
		t.Fatal(err)
	}
	if snap, _ := e.m.Watch(bg, C, 0, time.Second); snap.Ticket != "" {
		t.Fatalf("hide: ticket=%q", snap.Ticket)
	}
}
