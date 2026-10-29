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
		Note: "fix the row", Body: []string{"UPDATE t SET a=1", "WHERE id=7", "-- line 3", "-- line 4"}, Warnings: []string{"1 row"}}
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
	m := readable(loaded(t, &fakeAPI{}, "lea-a/b", ticketed("b", true)))
	v := m.View()
	for _, w := range []string{"locksql crm", "write", "PRODUCTION", "unverified", "db / crm · write", "fix the row",
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
