package deck

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func (f *fakeAPI) HubDecide(ctx context.Context, c, instance, id string, approved bool, retype string) error {
	f.rec(fmt.Sprintf("hub-decide %s %s/%s %v %q", c, instance, id, approved, retype))
	return f.err
}

func (f *fakeAPI) HubReveal(ctx context.Context, c, instance, id string) (HubClear, error) {
	f.rec(fmt.Sprintf("hub-reveal %s %s/%s", c, instance, id))
	return f.clear, f.err
}

func (f *fakeAPI) HubRenew(ctx context.Context, c, instance, id string) (HubRenewed, error) {
	f.rec(fmt.Sprintf("hub-renew %s %s/%s", c, instance, id))
	return f.renewed, f.err
}

func ticketed(name string, prod bool) Agent {
	a := ag(name, "working", 1)
	h := HubCard{ID: "T1", Instance: "crm", Lock: "locksql", Risk: "write", Header: "db / crm · write",
		Note: "fix the row", Body: []string{"UPDATE t SET a=1", "WHERE id=7"}, Warnings: []string{"1 row"}, Claimed: "b"}
	if prod {
		h.Production, h.Retype = true, "crm"
	}
	a.Hub = []HubCard{h}
	return a
}

// a approves the selected agent's oldest hub ticket, d denies it, through the daemon; not before
// the card was in view for lockAskDelay; a ticket needs no approval or lock request in front.
func TestHubCardKeys(t *testing.T) {
	for k, want := range map[string]string{"a": `hub-decide lea-a crm/T1 true ""`, "d": `hub-decide lea-a crm/T1 false ""`} {
		api := &fakeAPI{}
		m := readable(loaded(t, api, "lea-a/b", ticketed("b", false)))
		m, cmd := step(t, m, key(k))
		if cmd == nil {
			t.Fatalf("%s: no call", k)
		}
		step(t, m, cmd())
		if got := api.got(); len(got) != 1 || got[0] != want {
			t.Fatalf("%s: calls=%q", k, got)
		}
	}
	api := &fakeAPI{}
	m := loaded(t, api, "lea-a/b", ticketed("b", false))
	if m, cmd := step(t, m, key("a")); cmd != nil || !strings.Contains(m.status, "read the locksql ticket first") {
		t.Fatalf("answered before the delay: status %q", m.status)
	}
	if rank(ticketed("b", false), nil) != 0 || m.label(ticketed("b", false)) != "Lock ticket" {
		t.Fatal("a hub ticket does not ask for the user")
	}
}

// A production ticket asks for its name: a opens the field, enter approves with what was typed,
// esc cancels; d denies without it.
func TestHubProductionTicketNeedsItsNameTyped(t *testing.T) {
	api := &fakeAPI{}
	m := readable(loaded(t, api, "lea-a/b", ticketed("b", true)))
	m, cmd := step(t, m, key("a"))
	if cmd != nil || m.hubFor == "" {
		t.Fatalf("no field for the name (cmd %v)", cmd != nil)
	}
	if v := m.View(); !strings.Contains(v, "type crm:") {
		t.Fatalf("field not shown:\n%s", v)
	}
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.hubFor != "" || len(api.got()) != 0 {
		t.Fatal("esc did not cancel")
	}
	// A wrong name is refused in the deck: nothing reaches the daemon.
	m, _ = step(t, m, key("a"))
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Crm")})
	m, cmd = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || !strings.Contains(m.status, "does not match") || len(api.got()) != 0 {
		t.Fatalf("a wrong name was sent: status %q, calls %q", m.status, api.got())
	}
	m, _ = step(t, m, key("a"))
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("crm")})
	m, cmd = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter did not approve")
	}
	step(t, m, cmd())
	if got := api.got(); len(got) != 1 || got[0] != `hub-decide lea-a crm/T1 true "crm"` {
		t.Fatalf("calls=%q", got)
	}
}

// The card shows the Lock, the instance, the risk, production, an unverified requester, the
// Lock's masked screen (three lines of it; v shows them all) and its warnings.
func TestHubCardView(t *testing.T) {
	a := ticketed("b", true)
	a.Hub[0].Body = append(a.Hub[0].Body, "-- line 3", "-- line 4")
	m := readable(loaded(t, &fakeAPI{}, "lea-a/b", a))
	v := m.View()
	for _, w := range []string{"locksql crm", "write", "PRODUCTION", `unverified: claims "b"`, "db / crm · write", "fix the row",
		"UPDATE t SET a=1", "1 row", "[a]pprove [d]eny [v]iew"} {
		if !strings.Contains(v, w) {
			t.Errorf("missing %q in\n%s", w, v)
		}
	}
	if strings.Contains(v, "-- line 4") {
		t.Errorf("the whole screen shows before v:\n%s", v)
	}
	m, _ = step(t, m, key("v"))
	if v := m.View(); !strings.Contains(v, "-- line 4") {
		t.Errorf("v did not show the whole screen:\n%s", v)
	}
}

// A ticket longer than the folded card (more lines, or a line wider than the deck) is approved only
// once v showed all of it; nothing of it is cut, it wraps. One too long for the deck, one the
// daemon had to cut, or a production one with no name to type, is refused: lock approve answers
// it; d still denies.
func TestHubTicketIsReadWholeBeforeApproval(t *testing.T) {
	long := ticketed("b", false)
	long.Hub[0].Body = []string{"UPDATE t SET a=1 WHERE id=7 OR 1=1; " + strings.Repeat("-- ", 30) + "DROP TABLE t"}
	api := &fakeAPI{}
	m := readable(loaded(t, api, "lea-a/b", long))
	if strings.Contains(m.View(), "DROP") {
		t.Fatal("the folded card shows the whole line")
	}
	if m2, cmd := step(t, m, key("a")); cmd != nil || !strings.Contains(m2.status, "v shows the whole ticket") {
		t.Fatalf("approved unread: status %q", m2.status)
	}
	m, _ = step(t, m, key("v"))
	if v := m.View(); !strings.Contains(v, "DROP") || !strings.Contains(v, "TABLE t") {
		t.Fatalf("v does not show the whole line:\n%s", m.View())
	}
	if _, cmd := step(t, m, key("a")); cmd == nil {
		t.Fatal("not approved after v")
	}
	for name, a := range map[string]Agent{"cut": ticketed("b", false), "huge": ticketed("b", false), "prod": ticketed("b", true)} {
		switch name {
		case "cut":
			a.Hub[0].Truncated = true
		case "huge":
			a.Hub[0].Body = nil
			for i := 0; i < 40; i++ {
				a.Hub[0].Body = append(a.Hub[0].Body, "line")
			}
		case "prod":
			a.Hub[0].Retype = ""
		}
		m := readable(loaded(t, &fakeAPI{}, "lea-a/b", a))
		m, _ = step(t, m, key("v"))
		if m2, cmd := step(t, m, key("a")); cmd != nil || !strings.Contains(m2.status, "lock approve") {
			t.Fatalf("%s: approved from the deck, status %q", name, m2.status)
		}
		if _, cmd := step(t, m, key("d")); cmd == nil {
			t.Fatalf("%s: d does not deny", name)
		}
	}
}

// The whole card must have been on screen: a ticket whose card (with the Lock's header and the
// agent's note) is taller than the deck is never approved from it, even after v.
func TestHubTicketTallerThanTheDeckGoesToLockApprove(t *testing.T) {
	tall := ticketed("b", false)
	tall.Hub[0].Note = strings.Repeat("n", 240)
	tall.Hub[0].Body = nil
	for i := 0; i < 18; i++ {
		tall.Hub[0].Body = append(tall.Hub[0].Body, "-- ok")
	}
	tall.Hub[0].Body = append(tall.Hub[0].Body, "DROP hidden")
	api := &fakeAPI{}
	m := readable(loaded(t, api, "lea-a/b", tall))
	m, _ = step(t, m, tea.WindowSizeMsg{Width: 36, Height: 30})
	m, _ = step(t, m, key("v"))
	if strings.Contains(m.View(), "DROP hidden") {
		t.Fatalf("the test's card fits:\n%s", m.View())
	}
	if !strings.Contains(m.status, "lock approve") {
		t.Fatalf("status %q", m.status)
	}
	if m, cmd := step(t, m, key("a")); cmd != nil || !strings.Contains(m.status, "lock approve") {
		t.Fatalf("approved a ticket not all on screen: status %q", m.status)
	}
	// Tall enough: v shows it all, a approves.
	m, _ = step(t, m, tea.WindowSizeMsg{Width: 36, Height: 60})
	m, _ = step(t, m, key("v"))
	m, _ = step(t, m, key("v"))
	if !strings.Contains(m.View(), "DROP hidden") {
		t.Fatalf("not all shown at height 60:\n%s", m.View())
	}
	if _, cmd := step(t, m, key("a")); cmd == nil {
		t.Fatalf("not approved at height 60: status %q", m.status)
	}
}

// o (and Ctrl-O) asks the daemon for the ticket's clear values and shows them on the card, marked
// apart from the masked screen and cleaned; o again hides them, and they go when the card changes
// or the selection moves off it, or a minute later. Not before the card was in view for
// lockAskDelay.
func TestHubRevealShowsTheClearValuesUntilTheCardChanges(t *testing.T) {
	for _, k := range []tea.KeyMsg{key("o"), {Type: tea.KeyCtrlO}} {
		api := &fakeAPI{clear: HubClear{Header: "db / crm · write", Body: []string{"UPDATE t SET mail='ann@example.com\x1b[2J'"}}}
		m := wide(t, loaded(t, api, "lea-a/b", ticketed("b", false), ag("c", "idle", 2)))
		if m2, cmd := step(t, m, k); cmd != nil || !strings.Contains(m2.status, "read the locksql ticket first") {
			t.Fatalf("%s: revealed before the delay: %q", k, m2.status)
		}
		m = readable(m)
		m, cmd := step(t, m, k)
		if cmd == nil {
			t.Fatalf("%s: no call", k)
		}
		m, tick := step(t, m, cmd())
		if got := api.got(); len(got) != 1 || got[0] != "hub-reveal lea-a crm/T1" {
			t.Fatalf("calls %q", got)
		}
		v := m.View()
		if !strings.Contains(v, "CLEAR VALUES") || !strings.Contains(v, "ann@example.com") || strings.Contains(v, "\x1b[2J") {
			t.Fatalf("%s: clear values not shown, or not cleaned:\n%s", k, v)
		}
		if tick == nil {
			t.Fatal("no timeout")
		}
		// The timeout of this reveal hides it; o hides it too.
		m2, _ := step(t, m, revealEndMsg{m.revealSeq})
		if strings.Contains(m2.View(), "ann@example.com") {
			t.Fatal("still shown after the timeout")
		}
		m2, _ = step(t, m, key("o"))
		if strings.Contains(m2.View(), "ann@example.com") {
			t.Fatal("o did not hide them")
		}
		// The selection moves to another row: gone, also when it comes back.
		m2, _ = step(t, m, key("j"))
		m2, _ = step(t, m2, key("k"))
		if strings.Contains(m2.View(), "ann@example.com") {
			t.Fatal("still shown after the selection moved")
		}
		// The ticket goes from the snapshot (decided elsewhere): gone.
		m2, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Shown: "lea-a/b", Agents: []Agent{ag("b", "working", 1)}}})
		if m2.revealed != nil {
			t.Fatal("kept after the ticket went")
		}
	}
	// The daemon's refusal is one line in the status, nothing shows.
	api := &fakeAPI{err: errors.New("no clear values: the lock does not pin the deck's facade key (run lock up)")}
	m := readable(loaded(t, api, "lea-a/b", ticketed("b", false)))
	m, cmd := step(t, m, key("o"))
	m, _ = step(t, m, cmd())
	if m.revealed != nil || !strings.Contains(m.status, "facade key") {
		t.Fatalf("status %q", m.status)
	}
	// Elsewhere o still picks a folder.
	m = readable(loaded(t, &fakeAPI{}, "lea-a/c", ag("c", "idle", 1)))
	if m, _ := step(t, m, key("o")); !m.menuPicker {
		t.Fatal("o no longer moves a row to a folder")
	}
}

// An expired ticket shows on its agent's row labelled expired, ranks below what needs a
// decision, cannot be decided or revealed, and r asks the daemon to renew it; the hub's answer
// says whether it approved the renewal at once.
func TestHubExpiredTicketIsRenewedWithR(t *testing.T) {
	a := ticketed("b", false)
	a.Hub[0].Expired = true
	for _, auto := range []bool{true, false} {
		api := &fakeAPI{renewed: HubRenewed{Ticket: "T2", AutoApproved: auto}}
		m := wide(t, readable(loaded(t, api, "lea-a/b", a)))
		if m.label(a) != "Lock ticket expired" || rank(a, nil) == 0 {
			t.Fatalf("label %q rank %d", m.label(a), rank(a, nil))
		}
		if v := m.View(); !strings.Contains(v, "EXPIRED") || !strings.Contains(v, "[r] asks the lock again") {
			t.Fatalf("card:\n%s", v)
		}
		for _, k := range []string{"a", "d", "o"} {
			if _, cmd := step(t, m, key(k)); cmd != nil {
				t.Fatalf("%s acted on an expired ticket", k)
			}
		}
		m, cmd := step(t, m, key("r"))
		if cmd == nil || m.roleFor != "" {
			t.Fatal("r did not renew")
		}
		m, _ = step(t, m, cmd())
		if got := api.got(); len(got) != 1 || got[0] != "hub-renew lea-a crm/T1" {
			t.Fatalf("calls %q", got)
		}
		if want := map[bool]string{true: "renewed as ticket T2 and approved", false: "asks again with ticket T2"}[auto]; !strings.Contains(m.status, want) {
			t.Fatalf("status %q", m.status)
		}
	}
	// Elsewhere r still sets the role.
	m := readable(loaded(t, &fakeAPI{}, "lea-a/b", ticketed("b", false)))
	if m, _ := step(t, m, key("r")); m.roleFor == "" {
		t.Fatal("r no longer sets the role")
	}
}

// A renewal ticket says what it renews and what changed since; it is approved like any ticket.
func TestHubRenewalCardShowsItsChanges(t *testing.T) {
	a := ticketed("b", false)
	a.Hub[0].Renews = &HubRenewal{Of: "T1", Reason: "approval-expired", Checked: true,
		Changes: []HubChange{{"schema", "changed"}, {"rows\u202e", "new"}}}
	a.Hub[0].ID = "T2"
	api := &fakeAPI{}
	m := wide(t, readable(loaded(t, api, "lea-a/b", a)))
	v := m.View()
	for _, w := range []string{"renews T1: its approval expired before the run", "changed since then:", "schema: changed", "rows?: new"} {
		if !strings.Contains(v, w) {
			t.Errorf("missing %q in\n%s", w, v)
		}
	}
	if strings.ContainsRune(v, '\u202e') {
		t.Error("bidi control shown")
	}
	a.Hub[0].Renews = &HubRenewal{Of: "T1"}
	if v := wide(t, readable(loaded(t, api, "lea-a/b", a))).View(); !strings.Contains(v, "could not compare") {
		t.Errorf("unchecked renewal:\n%s", v)
	}
	m, cmd := step(t, m, key("a"))
	if cmd == nil {
		t.Fatalf("renewal not approved: %q", m.status)
	}
	step(t, m, cmd())
	if got := api.got(); len(got) != 1 || got[0] != `hub-decide lea-a crm/T2 true ""` {
		t.Fatalf("calls %q", got)
	}
}

// wide gives the deck room for a whole card on one line each.
func wide(t *testing.T, m Model) Model {
	t.Helper()
	m, _ = step(t, m, tea.WindowSizeMsg{Width: 120, Height: 60})
	return m
}

// r may approve at once (the hub re-approves an unchanged renewal in the user's name, also of a
// ticket that expired undecided): it is read like a. A cut, too long or too tall expired ticket
// is never renewed from the deck; a long one only once v showed it whole.
func TestHubRenewNeedsTheTicketReadLikeAnApproval(t *testing.T) {
	expired := func() Agent {
		a := ticketed("b", false)
		a.Hub[0].Expired = true
		return a
	}
	for name, prep := range map[string]func(*Agent){
		"cut": func(a *Agent) { a.Hub[0].Truncated = true },
		"too long": func(a *Agent) {
			for i := 0; i < 40; i++ {
				a.Hub[0].Body = append(a.Hub[0].Body, "line")
			}
		},
	} {
		a := expired()
		prep(&a)
		api := &fakeAPI{}
		m := wide(t, readable(loaded(t, api, "lea-a/b", a)))
		m, _ = step(t, m, key("v"))
		if m2, cmd := step(t, m, key("r")); cmd != nil || !strings.Contains(m2.status, "lock approve") {
			t.Fatalf("%s: renewed from the deck, status %q", name, m2.status)
		}
	}
	// Taller than the deck.
	tall := expired()
	api := &fakeAPI{}
	m := readable(loaded(t, api, "lea-a/b", tall))
	m, _ = step(t, m, tea.WindowSizeMsg{Width: 60, Height: 8})
	if m2, cmd := step(t, m, key("r")); cmd != nil || !strings.Contains(m2.status, "lock approve") {
		t.Fatalf("taller than the deck: renewed, status %q", m2.status)
	}
	// Long: v first.
	long := expired()
	long.Hub[0].Body = append(long.Hub[0].Body, "-- line 3", "-- line 4")
	m = wide(t, readable(loaded(t, api, "lea-a/b", long)))
	if v := m.View(); !strings.Contains(v, "approved at once if nothing changed") {
		t.Fatalf("the card does not say r may approve:\n%s", v)
	}
	if m2, cmd := step(t, m, key("r")); cmd != nil || !strings.Contains(m2.status, "v shows the whole ticket") {
		t.Fatalf("renewed unread: status %q", m2.status)
	}
	m, _ = step(t, m, key("v"))
	if _, cmd := step(t, m, key("r")); cmd == nil {
		t.Fatal("not renewed after v")
	}
	if got := api.got(); len(got) != 0 {
		t.Fatalf("calls %q", got)
	}
}

// A wheel or a click that moves the selection drops the clear values; they are drawn on the
// selected card only.
func TestHubRevealGoesWhenTheMouseMovesTheSelection(t *testing.T) {
	reveal := func(t *testing.T) Model {
		api := &fakeAPI{clear: HubClear{Body: []string{"mail=" + "ann@example.com"}}}
		m := wide(t, readable(loaded(t, api, "lea-a/b", ticketed("b", false), ag("c", "idle", 2))))
		m, cmd := step(t, m, key("o"))
		m, _ = step(t, m, cmd())
		if !strings.Contains(m.View(), "ann@example.com") {
			t.Fatal("not shown")
		}
		return m
	}
	m := reveal(t)
	m, _ = step(t, m, tea.MouseMsg{Y: 1, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	if m.selected == "lea-a/b" || m.revealed != nil || strings.Contains(m.View(), "ann@example.com") {
		t.Fatalf("still shown after the wheel (selected %s)", m.selected)
	}
	// Drawn on the selected card only, even while kept.
	m = reveal(t)
	m.selected = "lea-a/c"
	if strings.Contains(m.View(), "ann@example.com") {
		t.Fatal("drawn on a card not selected")
	}
}

// A renewal input the Lock could not check now says so.
func TestHubRenewalSaysWhatWasNotChecked(t *testing.T) {
	a := ticketed("b", false)
	a.Hub[0].Renews = &HubRenewal{Of: "T1", Checked: true, Changes: []HubChange{{"schema", "unchecked"}}}
	if v := wide(t, readable(loaded(t, &fakeAPI{}, "lea-a/b", a))).View(); !strings.Contains(v, "schema: not checked now; the run checks it") {
		t.Fatalf("card:\n%s", v)
	}
}
