package deck

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// run runs cmd, and each command of a batch, and returns the messages they gave.
func run(t *testing.T, m Model, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if b, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range b {
			out = append(out, run(t, m, c)...)
		}
		return out
	}
	if msg == nil {
		return nil
	}
	return []tea.Msg{msg}
}

// answered runs cmd and takes in only the slot calls' answers (not a watch's snapshot).
func answered(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	for _, msg := range run(t, m, cmd) {
		if a, ok := msg.(ticketShownMsg); ok {
			m, _ = step(t, m, a)
		}
	}
	return m
}

// showing is the deck once the daemon says the slot shows ref, ticketDelay later.
func showing(t *testing.T, m Model, ref string) Model {
	t.Helper()
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: m.version + 1, Agents: m.agents, HubQueue: m.hubQueue, Ticket: ref}})
	at := m.now().Add(ticketDelay)
	m.now = func() time.Time { return at }
	return m
}

// Selecting a ticket in the panel shows it in the slot, moving shows the next one, and leaving the
// panel gives the slot back to the selected agent (#284).
func TestHubPanelShowsTheTicketInTheSlot(t *testing.T) {
	q := []HubCard{queueTicket("T1", "", false, false), queueTicket("T2", "", false, false)}
	api := &fakeAPI{}
	m := withQueue(t, api, q, ag("b", "working", 1))
	m, cmd := step(t, m, key("H"))
	run(t, m, cmd)
	m, cmd = step(t, m, key("j"))
	run(t, m, cmd)
	if _, cmd := step(t, m, key("j")); cmd != nil {
		t.Fatal("j past the last ticket asked something")
	}
	m, cmd = step(t, m, key("esc"))
	run(t, m, cmd)
	// Leaving: a hide first (its number drops a late ticket call), then the agent.
	want := []string{`hub-show lea-a "crm/T1" ""`, `hub-show lea-a "crm/T2" ""`, `hub-show lea-a "" ""`, "show lea-a/b false"}
	if got := api.got(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("calls %q, want %q", got, want)
	}
	if m.panel || m.ticketAsked.on {
		t.Fatal("esc did not leave the panel")
	}
	// No agent to give the slot back to: the ticket is hidden.
	api = &fakeAPI{}
	m = withQueue(t, api, q)
	m, cmd = step(t, m, key("H"))
	run(t, m, cmd)
	m, cmd = step(t, m, key("esc"))
	run(t, m, cmd)
	if got := api.got(); len(got) != 2 || got[1] != `hub-show lea-a "" ""` {
		t.Fatalf("calls %q", got)
	}
}

// a approves the ticket the slot shows, d denies it, at once: no unfold, no read-first step, also
// for a long ticket; not before the daemon says the slot shows it, nor within ticketDelay, nor
// twice (#284).
func TestHubPanelAnswersTheTicketShown(t *testing.T) {
	q := []HubCard{queueTicket("T1", "", false, false), queueTicket("T2", "", false, false)}
	for i := 0; i < 60; i++ {
		q[0].Body = append(q[0].Body, "line")
	}
	for k, want := range map[string]string{"a": `hub-decide lea-a panel crm/T1 true ""`, "d": `hub-decide lea-a panel crm/T1 false ""`} {
		api := &fakeAPI{}
		m := withQueue(t, api, q, ag("b", "working", 1))
		m, _ = step(t, m, key("H"))
		if m2, cmd := step(t, m, key(k)); cmd != nil || !strings.Contains(m2.status, "not shown yet") {
			t.Fatalf("%s before the slot shows it: %q", k, m2.status)
		}
		m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Agents: m.agents, HubQueue: q, Ticket: "crm/T1"}})
		if m2, cmd := step(t, m, key(k)); cmd != nil || !strings.Contains(m2.status, "just showed") {
			t.Fatalf("%s within ticketDelay: %q", k, m2.status)
		}
		m = showing(t, m, "crm/T1")
		m, cmd := step(t, m, key(k))
		if cmd == nil {
			t.Fatalf("%s: no call (%q)", k, m.status)
		}
		for _, msg := range run(t, m, cmd) {
			m, _ = step(t, m, msg)
		}
		if got := api.got(); len(got) != 1 || got[0] != want {
			t.Fatalf("%s: calls %q", k, got)
		}
		if m2, cmd := step(t, m, key(k)); cmd != nil || !strings.Contains(m2.status, "answered") {
			t.Fatalf("%s twice: %q", k, m2.status)
		}
		// The ticket decided, the next one slides under the selection: a waits for the slot again.
		m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 20, Agents: m.agents, HubQueue: q[1:], Ticket: "crm/T1"}})
		if m.panelSel != "crm/T2" {
			t.Fatalf("selection on %q", m.panelSel)
		}
		if m2, cmd := step(t, m, key(k)); cmd != nil && len(run(t, m2, cmd)) > 0 || m2.status != "the ticket is not shown yet: look at it on the right" {
			t.Fatalf("%s answered the next ticket: %q", k, m2.status)
		}
	}
}

// A production ticket whose queue item asks for a name still asks for it, and the ticket in the
// slot says so (#289); one that does not is approved at once.
func TestHubPanelProductionRetype(t *testing.T) {
	api := &fakeAPI{}
	m := withQueue(t, api, []HubCard{queueTicket("T1", "", false, true)}, ag("b", "working", 1))
	m, _ = step(t, m, key("H"))
	m = showing(t, m, "crm/T1")
	m, cmd := step(t, m, key("a"))
	if m.hubFor != "crm/T1" || !strings.Contains(m.View(), "type crm:") {
		t.Fatalf("no field for the name:\n%s", m.View())
	}
	run(t, m, cmd)
	if got := api.got(); len(got) != 1 || got[0] != fmt.Sprintf("hub-show lea-a %q %q", "crm/T1", retypeNote("crm")) {
		t.Fatalf("the slot does not say the name is awaited: calls %q", got)
	}
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("crm")})
	m, cmd = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	run(t, m, cmd)
	if got := decisions(api.got()); len(got) != 1 || got[0] != `hub-decide lea-a panel crm/T1 true "crm"` {
		t.Fatalf("calls %q", got)
	}
	api = &fakeAPI{}
	h := queueTicket("T1", "", false, true)
	h.Retype = ""
	m = withQueue(t, api, []HubCard{h}, ag("b", "working", 1))
	m, _ = step(t, m, key("H"))
	m = showing(t, m, "crm/T1")
	_, cmd = step(t, m, key("a"))
	run(t, m, cmd)
	if got := decisions(api.got()); len(got) != 1 || got[0] != `hub-decide lea-a panel crm/T1 true ""` {
		t.Fatalf("no retype: calls %q", got)
	}
}

// A hub refusal shows in the slot, under the ticket, with the fix; the next ticket shows without it.
func TestHubPanelRefusalShowsInTheSlot(t *testing.T) {
	q := []HubCard{queueTicket("T1", "", false, true), queueTicket("T2", "", false, false)}
	q[0].Retype = ""
	api := &fakeAPI{}
	m := withQueue(t, api, q, ag("b", "working", 1))
	m, _ = step(t, m, key("H"))
	m = showing(t, m, "crm/T1")
	m, cmd := step(t, m, key("a"))
	api.err = errors.New("the typed name does not match: approving this ticket needs it typed exactly")
	for _, msg := range run(t, m, cmd) {
		m, cmd = step(t, m, msg)
	}
	api.err = nil
	run(t, m, cmd)
	got := api.got()
	last := got[len(got)-1]
	if !strings.HasPrefix(last, `hub-show lea-a "crm/T1" "Not answered: the typed name does not match`) ||
		!strings.Contains(last, "lox up updates it") || !strings.Contains(last, "lox approve") {
		t.Fatalf("calls %q", got)
	}
	m, cmd = step(t, m, key("j"))
	run(t, m, cmd)
	if got := api.got(); got[len(got)-1] != `hub-show lea-a "crm/T2" ""` {
		t.Fatalf("the refusal follows the next ticket: %q", got)
	}
}

// Y approves every pending, non-expired ticket after one y naming the count and the production
// count; a ticket that needs its name typed is left out and listed; the hub's refusals are listed
// with their reason in the slot (#284).
func TestHubPanelApproveAll(t *testing.T) {
	q := []HubCard{queueTicket("T1", "", false, false), queueTicket("T2", "", false, true), queueTicket("T3", "", false, true),
		queueTicket("T4", "", false, false), queueTicket("T5", "", false, false)}
	q[1].Retype = ""      // production, no name asked
	q[3].Expired = true   // nothing decides it
	q[4].ID = "T5-orphan" // the hub refuses it
	api := &fakeAPI{}
	m := withQueue(t, api, q, ag("b", "working", 1))
	m, cmd := step(t, m, key("H"))
	run(t, m, cmd)
	m, _ = step(t, m, key("Y"))
	if m.status != "approve 3 tickets, 1 production? y/n (1 skipped)" {
		t.Fatalf("status %q", m.status)
	}
	if m2, cmd := step(t, m, key("n")); cmd != nil || m2.allFor != nil || m2.status != "nothing approved" {
		t.Fatalf("n: %q", m2.status)
	}
	m, cmd = step(t, m, key("y"))
	api.failOn = "T5-orphan"
	for _, msg := range run(t, m, cmd) {
		m, cmd = step(t, m, msg)
	}
	run(t, m, cmd)
	got := api.got()
	want := []string{`hub-decide lea-a panel crm/T1 true ""`, `hub-decide lea-a panel crm/T2 true ""`, `hub-decide lea-a panel crm/T5-orphan true ""`}
	if len(got) < 5 || strings.Join(got[1:4], "|") != strings.Join(want, "|") {
		t.Fatalf("calls %q", got)
	}
	if m.status != "approved 2, 1 skipped, 1 refused: see the right pane" {
		t.Fatalf("status %q", m.status)
	}
	report := got[4]
	for _, w := range []string{"Approve all: 2 approved.", "locksql crm T3: needs its name typed", "locksql crm T5-orphan: this ticket's lock no longer exists", "lox approve"} {
		if !strings.Contains(report, w) {
			t.Errorf("report: missing %q in %q", w, report)
		}
	}
	// Nothing left to approve at once.
	m = withQueue(t, &fakeAPI{}, q[2:4], ag("b", "working", 1))
	m, _ = step(t, m, key("H"))
	if m, _ = step(t, m, key("Y")); m.allFor != nil || !strings.Contains(m.status, "no ticket to approve at once: 1 need a on each") {
		t.Fatalf("status %q", m.status)
	}
}

// The ticket pane shows the whole ticket the snapshot names: the Lock's screen, the agent's note,
// instance, production, whom it is for, created, expires and the keys, then the deck's note.
func TestTicketPaneShowsTheWholeTicket(t *testing.T) {
	h := queueTicket("T1", "lea-a/b", false, true)
	h.Note, h.Warnings = "fix the row", []string{"1 row"}
	for i := 0; i < 30; i++ {
		h.Body = append(h.Body, "line")
	}
	snap := Snapshot{Version: 3, Agents: []Agent{ag("b", "working", 1)}, HubQueue: []HubCard{h}, Ticket: "crm/T1", TicketNote: "Not answered: x\nlox up"}
	p := ticketPane{api: &fakeAPI{}, container: "lea-a", now: func() time.Time { return clock }}
	nm, _ := p.Update(tea.WindowSizeMsg{Width: 100, Height: 80})
	nm, _ = nm.Update(snapshotMsg{snap: snap})
	v := nm.View()
	for _, w := range []string{"⚠ locksql crm · write", "PRODUCTION", "db / crm · write", "UPDATE t SET a=1", "1 row",
		"agent's note: fix the row", "instance: crm", "production: yes", "requested by: claims: b (unverified)",
		"created: ", "(5m ago)", "expires: ", "(15m left)", "ticket: T1", "a approve · d deny · Y approve all",
		"approving asks for crm", "Not answered: x", "lox up"} {
		if !strings.Contains(v, w) {
			t.Errorf("missing %q in\n%s", w, v)
		}
	}
	if strings.Count(v, "  line") != 30 {
		t.Errorf("the screen is folded:\n%s", v)
	}
	// Too tall for the pane: it scrolls.
	nm, _ = nm.Update(tea.WindowSizeMsg{Width: 100, Height: 10})
	if v := nm.View(); !strings.Contains(v, "more lines: the wheel scrolls") {
		t.Fatalf("no scroll mark:\n%s", v)
	}
	nm, _ = nm.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	if nm.(ticketPane).top != 3 {
		t.Fatalf("wheel: top %d", nm.(ticketPane).top)
	}
	// Decided meanwhile, nothing shown, a note alone.
	for snap, w := range map[*Snapshot]string{
		{Version: 4, Ticket: "crm/T1"}:             "no longer pending",
		{Version: 5}:                               "No ticket shown",
		{Version: 6, TicketNote: "Approve all: 2"}: "Approve all: 2",
	} {
		nm, _ = nm.Update(snapshotMsg{snap: *snap})
		if v := nm.View(); !strings.Contains(v, w) {
			t.Errorf("missing %q in\n%s", w, v)
		}
	}
}

// r on an expired panel ticket waits for the slot to show it, as a does, and never renews a
// ticket lea had to cut (#284).
func TestHubPanelRenewWaitsForTheSlot(t *testing.T) {
	q := []HubCard{queueTicket("T1", "", false, false)}
	q[0].Expired = true
	api := &fakeAPI{}
	m := later(withQueue(t, api, q, ag("b", "working", 1)))
	m, _ = step(t, m, key("H"))
	m = later(m)
	if m2, cmd := step(t, m, key("r")); cmd != nil || !strings.Contains(m2.status, "not shown yet") {
		t.Fatalf("r before the slot shows it: %q", m2.status)
	}
	m = showing(t, m, "crm/T1")
	_, cmd := step(t, m, key("r"))
	run(t, m, cmd)
	if got := api.got(); got[len(got)-1] != "hub-renew lea-a panel crm/T1" {
		t.Fatalf("calls %q", got)
	}
	q[0].Truncated = true
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 30, Agents: m.agents, HubQueue: q, Ticket: "crm/T1"}})
	n := len(api.got())
	m2, cmd := step(t, m, key("r"))
	run(t, m2, cmd)
	if !strings.Contains(m2.status, "cut this ticket") || len(api.got()) != n+1 || strings.HasPrefix(api.got()[n], "hub-renew") {
		t.Fatalf("r on a cut ticket: %q, calls %q", m2.status, api.got()[n:])
	}
}

// The slot is asked again when it does not show what the deck asked: after a failed call, or when
// the snapshot says it shows something else (a restarted daemon, a dead pane, another deck), at
// most once a retryDelay; each call's number grows (#284).
func TestHubPanelAsksTheSlotAgain(t *testing.T) {
	q := []HubCard{queueTicket("T1", "", false, false), queueTicket("T2", "", false, false)}
	api := &fakeAPI{err: errors.New("daemon busy")}
	m := withQueue(t, api, q, ag("b", "working", 1))
	m, cmd := step(t, m, key("H"))
	m = answered(t, m, cmd)
	if !strings.Contains(m.status, "daemon busy") {
		t.Fatalf("status %q", m.status)
	}
	if _, cmd := step(t, m, frameMsg{}); answered(t, m, cmd).ticketBusy || len(api.got()) != 1 {
		t.Fatalf("asked again within retryDelay: %q", api.got())
	}
	api.err = nil
	at := m.now().Add(retryDelay)
	m.now = func() time.Time { return at }
	m, cmd = step(t, m, frameMsg{})
	m = answered(t, m, cmd)
	m = showing(t, m, "crm/T1")
	if got := api.got(); len(got) != 2 || got[1] != `hub-show lea-a "crm/T1" ""` {
		t.Fatalf("after a failure: calls %q", got)
	}
	if _, cmd := step(t, m, key("a")); cmd == nil {
		t.Fatalf("a once the slot shows it: %q", m.status)
	}
	// The slot lost the ticket (the daemon restarted): asked again, then a waits for it.
	at = m.now().Add(retryDelay)
	m.now = func() time.Time { return at }
	m, cmd = step(t, m, snapshotMsg{snap: Snapshot{Version: 40, Agents: m.agents, HubQueue: q}})
	if cmd == nil {
		t.Fatal("no call")
	}
	m = answered(t, m, cmd)
	if got := api.got(); got[len(got)-1] != `hub-show lea-a "crm/T1" ""` {
		t.Fatalf("not asked again: %q", got)
	}
	if m2, _ := step(t, m, key("a")); !strings.Contains(m2.status, "not shown yet") {
		t.Fatalf("a while the slot lost the ticket: %q", m2.status)
	}
	// Calls carry growing numbers, so the daemon drops one that arrives late.
	m, cmd = step(t, m, key("j"))
	run(t, m, cmd)
	for i := 1; i < len(api.seqs); i++ {
		if api.seqs[i] <= api.seqs[i-1] {
			t.Fatalf("sequence numbers %v", api.seqs)
		}
	}
}

// Y's question holds only in its context: a click, leaving the panel or a queue that changes what
// it counted drops it; y approves only the tickets still pending then (#284).
func TestHubPanelApproveAllQuestionExpires(t *testing.T) {
	q := []HubCard{queueTicket("T1", "", false, false), queueTicket("T2", "", false, false)}
	start := func() (Model, *fakeAPI) {
		api := &fakeAPI{}
		m := withQueue(t, api, q, ag("b", "working", 1))
		m, _ = step(t, m, key("H"))
		m, _ = step(t, m, key("Y"))
		if m.allFor == nil {
			t.Fatal("no question")
		}
		return m, api
	}
	m, _ := start()
	if m, _ = step(t, m, click(tea.MouseButtonLeft, 3)); m.allFor != nil {
		t.Fatal("a click kept the question")
	}
	m, _ = start()
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 30, Agents: m.agents, HubQueue: append(q, queueTicket("T3", "", false, true))}})
	if m.allFor != nil || !strings.Contains(m.status, "Y counts again") {
		t.Fatalf("a new ticket kept the question: %q", m.status)
	}
	m, _ = start()
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 30, Agents: m.agents}})
	if m.allFor != nil {
		t.Fatal("the emptied queue kept the question")
	}
	// A ticket gone between Y and y (the snapshot not seen yet) is not approved, and said.
	m, api := start()
	m.hubQueue = q[1:]
	m, cmd := step(t, m, key("y"))
	for _, msg := range run(t, m, cmd) {
		m, _ = step(t, m, msg)
	}
	if got := api.got(); len(got) != 1 || got[0] != `hub-decide lea-a panel crm/T2 true ""` {
		t.Fatalf("calls %q", got)
	}
	if m.status != "approved 1, 1 skipped: see the right pane" || !strings.Contains(m.hubReport, "locksql crm T1: gone from the queue") {
		t.Fatalf("status %q report %q", m.status, m.hubReport)
	}
}

// A reference twice in the queue: the deck answers neither, Y leaves both out, and the pane shows
// the first and says so (#284).
func TestHubPanelDuplicateReference(t *testing.T) {
	q := []HubCard{queueTicket("T1", "", false, false), queueTicket("T1", "", false, true)}
	q[1].Retype = ""
	m := withQueue(t, &fakeAPI{}, q, ag("b", "working", 1))
	m, _ = step(t, m, key("H"))
	m = showing(t, m, "crm/T1")
	if m2, cmd := step(t, m, key("a")); cmd != nil || !strings.Contains(m2.status, "shows twice") {
		t.Fatalf("a on a duplicate: %q", m2.status)
	}
	if m2, _ := step(t, m, key("Y")); m2.allFor != nil {
		t.Fatal("Y counted a duplicate")
	}
	p := ticketPane{now: func() time.Time { return clock }, width: 120, height: 60, snap: Snapshot{HubQueue: q, Ticket: "crm/T1"}}
	if v := p.View(); !strings.Contains(v, "shows twice") || strings.Contains(v, "PRODUCTION") {
		t.Fatalf("the pane does not show the first, flagged:\n%s", v)
	}
}

// The warnings and the facts come before the Lock's screen: a long screen never hides them.
func TestTicketPaneFactsBeforeTheScreen(t *testing.T) {
	h := queueTicket("T1", "", false, false)
	h.Warnings = []string{"drops a table"}
	for i := 0; i < 200; i++ {
		h.Body = append(h.Body, "line")
	}
	p := ticketPane{now: func() time.Time { return clock }, width: 120, height: 20, snap: Snapshot{HubQueue: []HubCard{h}, Ticket: "crm/T1", TicketNote: "refused"}}
	v := p.View()
	for _, w := range []string{"drops a table", "instance: crm", "production: no", "requested by:", "refused"} {
		if !strings.Contains(v, w) {
			t.Errorf("missing %q in the first screen:\n%s", w, v)
		}
	}
}

// A key the ticket pane does not use, typed while it has the keyboard, says where the keys act
// (#289): before, a there did nothing and said nothing. A scroll key does not; a new ticket drops it.
func TestTicketPaneSaysTheDeckAnswers(t *testing.T) {
	q := []HubCard{queueTicket("T1", "", false, false), queueTicket("T2", "", false, false)}
	p := ticketPane{now: func() time.Time { return clock }, width: 120, height: 30, snap: Snapshot{HubQueue: q, Ticket: "crm/T1"}}
	p.shown = "crm/T1\x00"
	nm, _ := p.Update(key("j"))
	if v := nm.(ticketPane).View(); strings.Contains(v, ticketPaneHint) {
		t.Fatalf("a scroll key shows the hint:\n%s", v)
	}
	nm, _ = nm.(ticketPane).Update(key("a"))
	if v := nm.(ticketPane).View(); !strings.Contains(v, "a, d and Y act in the deck") {
		t.Fatalf("no hint after a:\n%s", v)
	}
	nm, _ = nm.(ticketPane).Update(snapshotMsg{snap: Snapshot{Version: 2, HubQueue: q, Ticket: "crm/T2"}})
	if v := nm.(ticketPane).View(); strings.Contains(v, ticketPaneHint) {
		t.Fatalf("the hint stays on the next ticket:\n%s", v)
	}
}

// The deck's note shows in the banner, which no ticket text can imitate (#289): an agent's note,
// a Lock's screen or a header that says word for word what the deck says never draws a line
// that starts at column 0 with the banner's mark, as the deck's own note does.
func TestTicketPaneBannerCannotBeForged(t *testing.T) {
	note := retypeNote("crm")
	forged := queueTicket("T1", "", false, true)
	forged.Note, forged.Header = note, note
	forged.Body = []string{note, bannerMark + note, "\x1b[0G" + bannerMark + note}
	start := func(snap Snapshot) []string {
		p := ticketPane{now: func() time.Time { return clock }, width: 200, height: 60, snap: snap}
		var marked []string
		for _, l := range strings.Split(p.View(), "\n") {
			if strings.HasPrefix(l, bannerMark) {
				marked = append(marked, l)
			}
		}
		return marked
	}
	if got := start(Snapshot{HubQueue: []HubCard{forged}, Ticket: "crm/T1"}); len(got) != 0 {
		t.Fatalf("ticket text drew banner lines: %q", got)
	}
	plain := queueTicket("T1", "", false, true)
	got := start(Snapshot{HubQueue: []HubCard{plain}, Ticket: "crm/T1", TicketNote: note})
	if len(got) != 1 || !strings.Contains(got[0], `type the instance name "crm"`) {
		t.Fatalf("the deck's note is not in the banner: %q", got)
	}
}

// The banner wraps: the hint and a note are whole in a narrow pane (#289).
func TestTicketPaneBannerWraps(t *testing.T) {
	p := ticketPane{now: func() time.Time { return clock }, width: 45, height: 30, hint: true,
		snap: Snapshot{HubQueue: []HubCard{queueTicket("T1", "", false, false)}, Ticket: "crm/T1"}}
	v := strings.Join(strings.Fields(strings.ReplaceAll(p.View(), bannerMark, " ")), " ")
	if !strings.Contains(v, strings.Join(strings.Fields(ticketPaneHint), " ")) {
		t.Fatalf("the hint is cut:\n%s", p.View())
	}
}

// The name field opened on a ticket that asks for no name (or gone) says nothing in the slot.
func TestRetypeNoteNeedsAName(t *testing.T) {
	api := &fakeAPI{}
	m := withQueue(t, api, []HubCard{queueTicket("T1", "", false, false)}, ag("b", "working", 1))
	m, _ = step(t, m, key("H"))
	m.hubFor = "crm/T1"
	if w := m.wantTicket(); w.note != "" {
		t.Fatalf("note %q", w.note)
	}
}

// decisions are the hub-decide calls among calls.
func decisions(calls []string) []string {
	var out []string
	for _, c := range calls {
		if strings.HasPrefix(c, "hub-decide ") {
			out = append(out, c)
		}
	}
	return out
}
