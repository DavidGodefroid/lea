package deck

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func (f *fakeAPI) HubDecide(ctx context.Context, c, instance, id string, approved bool, retype string) error {
	f.rec(fmt.Sprintf("hub-decide %s %s/%s %v %q", c, instance, id, approved, retype))
	return f.err
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
