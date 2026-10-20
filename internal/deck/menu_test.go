package deck

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func click(b tea.MouseButton, y int) tea.MouseMsg {
	return tea.MouseMsg{Y: y, Button: b, Action: tea.MouseActionPress}
}

// Rows are two lines each, below the top border: a at lines 1-2, b at 3-4, c at 5-6.
func threeRows(t *testing.T, api *fakeAPI) Model {
	return sized(loaded(t, api, "lea-a/a", ag("a", "idle", 1), ag("b", "idle", 1), ag("c", "idle", 1)), 34, 20)
}

func TestALeftClickSelectsTheRowUnderThePointer(t *testing.T) {
	m := threeRows(t, &fakeAPI{})
	m, cmd := step(t, m, click(tea.MouseButtonLeft, 4))
	if m.selected != "lea-a/b" || cmd == nil {
		t.Fatalf("selected %q", m.selected)
	}
	m, _ = step(t, m, click(tea.MouseButtonLeft, 18))
	if m.selected != "lea-a/b" {
		t.Fatalf("a click below the rows moved the selection to %q", m.selected)
	}
}

func TestTheWheelMovesTheSelection(t *testing.T) {
	m := threeRows(t, &fakeAPI{})
	m, _ = step(t, m, tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	if m.selected != "lea-a/b" {
		t.Fatalf("selected %q", m.selected)
	}
	m, _ = step(t, m, tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	if m.selected != "lea-a/a" {
		t.Fatalf("selected %q", m.selected)
	}
}

// A right click opens the row's menu under it; a click on an item runs it on that row.
func TestARightClickOpensTheRowsMenu(t *testing.T) {
	api := &fakeAPI{}
	m := threeRows(t, api)
	m, _ = step(t, m, click(tea.MouseButtonRight, 3))
	if m.menuFor != "lea-a/b" || m.selected != "lea-a/b" {
		t.Fatalf("menu %q selected %q", m.menuFor, m.selected)
	}
	s := strings.Join(viewLines(m), "\n")
	for _, w := range []string{"Show", "Message", "Thread", "Rename", "Role", "Stop", "enter pick · esc close"} {
		if !strings.Contains(s, w) {
			t.Fatalf("menu lacks %q:\n%s", w, s)
		}
	}
	// b's lines are 3-4, its menu 5 (Show), 6 (Message) … c moves down below it.
	if k, i := m.hit(6); k != "lea-a/b" || i != 1 {
		t.Fatalf("line 6 is %q item %d", k, i)
	}
	m, cmd := step(t, m, click(tea.MouseButtonLeft, 6))
	if m.menuFor != "" || cmd == nil {
		t.Fatal("picking Message did not close the menu and act")
	}
	cmd()
	if got := api.got(); len(got) != 1 || got[0] != "popup lea-a/b true" {
		t.Fatalf("calls %q", got)
	}
}

func TestTheMenuWorksWithTheKeys(t *testing.T) {
	m := threeRows(t, &fakeAPI{})
	m, _ = step(t, m, click(tea.MouseButtonRight, 5))
	m, _ = step(t, m, key("j"))
	m, _ = step(t, m, key("j"))
	m, _ = step(t, m, key("j")) // Show, Message, Thread, Rename
	m, _ = step(t, m, key("enter"))
	if m.menuFor != "" || m.editFor != "lea-a/c" {
		t.Fatalf("menu %q edit %q", m.menuFor, m.editFor)
	}
	m, _ = step(t, m, key("esc"))
	m, _ = step(t, m, click(tea.MouseButtonRight, 1))
	m, _ = step(t, m, key("x")) // an item's own key picks it
	if m.menuFor != "" || m.confirm != "lea-a/a" {
		t.Fatalf("menu %q confirm %q", m.menuFor, m.confirm)
	}
	m, _ = step(t, m, key("n"))
	m, _ = step(t, m, click(tea.MouseButtonRight, 1))
	m, _ = step(t, m, key("esc"))
	if m.menuFor != "" {
		t.Fatal("esc left the menu open")
	}
}

// A click outside the menu closes it and does nothing else.
func TestAClickElsewhereClosesTheMenu(t *testing.T) {
	m := threeRows(t, &fakeAPI{})
	m, _ = step(t, m, click(tea.MouseButtonRight, 1))
	m, _ = step(t, m, click(tea.MouseButtonLeft, 19))
	if m.menuFor != "" || m.selected != "lea-a/a" {
		t.Fatalf("menu %q selected %q", m.menuFor, m.selected)
	}
}

// A row waiting for an approval offers it first; a shell offers show, close and the settings.
func TestMenuItemsFollowTheRow(t *testing.T) {
	got := func(a Agent) string {
		var l []string
		for _, it := range menuItems(a) {
			l = append(l, it.label)
		}
		return strings.Join(l, ",")
	}
	if g := got(carded("b", 7, true)); !strings.HasPrefix(g, "Allow,Deny,Always allow,Show") {
		t.Fatalf("carded: %s", g)
	}
	if g := got(carded("b", 7, false)); strings.Contains(g, "Always") {
		t.Fatalf("no always rule offered: %s", g)
	}
	if g := got(shell("sh-1")); g != "Show,Close,Settings" {
		t.Fatalf("shell: %s", g)
	}
}
