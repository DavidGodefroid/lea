package deck

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// The panel's title names its keys: out of the panel how to open it and what then, in it how to
// move, see the details and leave (#256).
func TestHubPanelTitleNamesItsKeys(t *testing.T) {
	q := []HubCard{queueTicket("T1", "", false, false), queueTicket("T2", "", false, false)}
	m := withQueue(t, &fakeAPI{}, q, ag("b", "working", 1))
	if v := m.View(); !strings.Contains(v, "H open · ↑↓ select · ⏎ details · a approve · d deny") || strings.Contains(v, "H to act") {
		t.Fatalf("title out of the panel:\n%s", v)
	}
	m, _ = step(t, m, key("H"))
	if v := m.View(); !strings.Contains(v, "↑↓ select · ⏎ details · esc leave") {
		t.Fatalf("title in the panel:\n%s", v)
	}
}

// A click on a ticket's line moves the selection into the panel, on that ticket; a click on the
// title enters the panel; a click on the selected ticket opens its details, a second closes them.
func TestHubPanelClick(t *testing.T) {
	q := []HubCard{queueTicket("T1", "", false, false), queueTicket("T2", "lea-a/b", true, false)}
	m := withQueue(t, &fakeAPI{}, q, ag("b", "working", 1))
	m, _ = step(t, m, click(tea.MouseButtonLeft, 3)) // T2's line, under the title
	if !m.panel || m.panelSel != "crm/T2" || m.panelDetail {
		t.Fatalf("click on T2: panel %v on %q, details %v", m.panel, m.panelSel, m.panelDetail)
	}
	m, _ = step(t, m, click(tea.MouseButtonLeft, 3))
	if !m.panelDetail || !strings.Contains(m.View(), "requested by: waiting: b (verified)") {
		t.Fatalf("click on the selected T2: details %v\n%s", m.panelDetail, m.View())
	}
	m, _ = step(t, m, click(tea.MouseButtonLeft, 3))
	if m.panelDetail {
		t.Fatal("a second click left the details open")
	}
	m, _ = step(t, m, key("esc"))
	m, _ = step(t, m, click(tea.MouseButtonLeft, 1)) // the title
	if !m.panel || m.panelSel != "crm/T1" {
		t.Fatalf("click on the title: panel %v on %q", m.panel, m.panelSel)
	}
}

// Enter on a selected ticket shows its details: whom it is for, when it was asked and how long
// ago, when it expires, under its card; moving to another ticket closes them.
func TestHubPanelDetails(t *testing.T) {
	q := []HubCard{queueTicket("T1", "", false, true), queueTicket("T2", "", false, false)}
	q[1].Expires = 0
	q[0].Note = "fix the row"
	m := withQueue(t, &fakeAPI{}, q, ag("b", "working", 1))
	m, _ = step(t, m, key("H"))
	if v := m.View(); strings.Contains(v, "requested by:") {
		t.Fatalf("details before enter:\n%s", v)
	}
	m, _ = step(t, m, key("enter"))
	v := m.View()
	for _, w := range []string{"requested by: no agent", "created: ", "(5m ago)", "expires: ", "(15m left)",
		"agent: fix the row", "PRODUCTION", "approving asks for crm", "UPDATE t SET a=1"} {
		if !strings.Contains(v, w) {
			t.Errorf("details: missing %q in\n%s", w, v)
		}
	}
	m, _ = step(t, m, key("down"))
	if m.panelDetail || strings.Contains(m.View(), "requested by:") {
		t.Fatalf("details stay on another ticket:\n%s", m.View())
	}
	m, _ = step(t, m, key("enter"))
	if v := m.View(); !strings.Contains(v, "expires: never") {
		t.Fatalf("a ticket with no expiry:\n%s", v)
	}
}
