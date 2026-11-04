package deck

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// The panel's title names its keys: out of the panel how to open it and what then, in it how to
// move, answer, approve all and leave (#256, #284).
func TestHubPanelTitleNamesItsKeys(t *testing.T) {
	q := []HubCard{queueTicket("T1", "", false, false), queueTicket("T2", "", false, false)}
	m := withQueue(t, &fakeAPI{}, q, ag("b", "working", 1))
	m.width = 200
	// Y works in the panel only: the title out of it does not offer it.
	if v := m.View(); !strings.Contains(v, "H open · ↑↓ select · a approve · d deny") || strings.Contains(v, "Y approve all") || strings.Contains(v, "details") {
		t.Fatalf("title out of the panel:\n%s", v)
	}
	m, _ = step(t, m, key("H"))
	if v := m.View(); !strings.Contains(v, "↑↓ select · a approve · d deny · Y approve all · esc leave") {
		t.Fatalf("title in the panel:\n%s", v)
	}
	m.panel, m.help = false, true
	if v := m.View(); !strings.Contains(v, "hub: approve all") {
		t.Fatalf("? does not name Y:\n%s", v)
	}
}

// A click on a ticket's line moves the selection into the panel, on that ticket, and shows it
// in the slot; a click on the title enters the panel; a click on the selected ticket changes
// nothing.
func TestHubPanelClick(t *testing.T) {
	q := []HubCard{queueTicket("T1", "", false, false), queueTicket("T2", "lea-a/b", true, false)}
	api := &fakeAPI{}
	m := withQueue(t, api, q, ag("b", "working", 1))
	m, cmd := step(t, m, click(tea.MouseButtonLeft, 3)) // T2's line, under the title
	if !m.panel || m.panelSel != "crm/T2" || cmd == nil {
		t.Fatalf("click on T2: panel %v on %q", m.panel, m.panelSel)
	}
	run(t, m, cmd)
	if got := api.got(); len(got) != 1 || got[0] != `hub-show lea-a "crm/T2" ""` {
		t.Fatalf("calls %q", got)
	}
	if _, cmd := step(t, m, click(tea.MouseButtonLeft, 3)); cmd != nil {
		t.Fatal("a second click on the selected ticket asked something")
	}
	m, _ = step(t, m, key("esc"))
	m, _ = step(t, m, click(tea.MouseButtonLeft, 1)) // the title
	if !m.panel || m.panelSel != "crm/T1" {
		t.Fatalf("click on the title: panel %v on %q", m.panel, m.panelSel)
	}
}
