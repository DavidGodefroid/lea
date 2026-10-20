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
		for _, it := range menuItems(a, false) {
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

// Every right click opens a menu (#44): outside the rows (the empty column below them, the
// frame), it is the deck's own menu, under the last row, and the selection stays.
func TestARightClickOutsideTheRowsOpensTheDecksMenu(t *testing.T) {
	api := &fakeAPI{}
	for _, y := range []int{0, 7, 15, 19} { // top border, below the rows, bottom border
		m := threeRows(t, api)
		m, _ = step(t, m, click(tea.MouseButtonRight, y))
		if m.menuFor != deckMenu || m.selected != "lea-a/a" {
			t.Fatalf("y=%d: menu %q selected %q", y, m.menuFor, m.selected)
		}
		s := strings.Join(viewLines(m), "\n")
		for _, w := range []string{"New agent", "New shell", "Keys", "Detach", "enter pick · esc close"} {
			if !strings.Contains(s, w) {
				t.Fatalf("y=%d: deck menu lacks %q:\n%s", y, w, s)
			}
		}
	}
	m := threeRows(t, api)
	m, _ = step(t, m, click(tea.MouseButtonRight, 12))
	// The rows are lines 1-6, the deck's menu starts at 7 (New agent).
	if k, i := m.hit(7); k != deckMenu || i != 0 {
		t.Fatalf("line 7 is %q item %d", k, i)
	}
	m, cmd := step(t, m, click(tea.MouseButtonLeft, 7))
	if m.menuFor != "" || cmd == nil {
		t.Fatal("picking New agent did not close the menu and act")
	}
	cmd()
	if got := api.got(); len(got) != 1 || got[0] != "new lea-a" {
		t.Fatalf("calls %q", got)
	}
}

// With no agent yet, a right click anywhere offers to start one.
func TestARightClickOnAnEmptyDeckOpensTheDecksMenu(t *testing.T) {
	api := &fakeAPI{}
	m := sized(loaded(t, api, ""), 34, 20)
	m, _ = step(t, m, click(tea.MouseButtonRight, 5))
	if m.menuFor != deckMenu {
		t.Fatalf("menu %q", m.menuFor)
	}
	// Line 1 says there is no agent yet; the menu follows: New agent at 2, New shell at 3.
	if k, i := m.hit(3); k != deckMenu || i != 1 {
		t.Fatalf("line 3 is %q item %d", k, i)
	}
	m, _ = step(t, m, key("enter"))
	if m.menuFor != "" || m.status != "starting a new agent…" {
		t.Fatalf("menu %q status %q", m.menuFor, m.status)
	}
}

// A right click on the open menu keeps it open and moves its highlight there; on another row, it
// opens that row's menu.
func TestARightClickOnTheOpenMenuKeepsIt(t *testing.T) {
	m := threeRows(t, &fakeAPI{})
	m, _ = step(t, m, click(tea.MouseButtonRight, 1))
	m, _ = step(t, m, click(tea.MouseButtonRight, 5)) // a's menu: Show 3, Message 4, Thread 5
	if m.menuFor != "lea-a/a" || m.menuSel != 2 {
		t.Fatalf("menu %q sel %d", m.menuFor, m.menuSel)
	}
	m, _ = step(t, m, click(tea.MouseButtonRight, 1))
	if m.menuFor != "lea-a/a" || m.menuSel != 0 {
		t.Fatalf("a second right click on the row: menu %q sel %d", m.menuFor, m.menuSel)
	}
	_, own := m.rowLines(m.innerWidth())
	last := len(own) // c's second line
	m, _ = step(t, m, click(tea.MouseButtonRight, last))
	if m.menuFor != "lea-a/c" || m.selected != "lea-a/c" {
		t.Fatalf("menu %q selected %q", m.menuFor, m.selected)
	}
}

// The deck's menu stays open while snapshots come in, and stays on screen below many rows.
func TestTheDecksMenuOutlivesSnapshotsAndScrolling(t *testing.T) {
	var as []Agent
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"} {
		as = append(as, ag(n, "idle", 1))
	}
	m := sized(loaded(t, &fakeAPI{}, "lea-a/a", as...), 34, 16)
	m, _ = step(t, m, click(tea.MouseButtonRight, 15)) // the bottom border
	if m.menuFor != deckMenu {
		t.Fatalf("menu %q", m.menuFor)
	}
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Shown: "lea-a/a", Agents: as}})
	if m.menuFor != deckMenu {
		t.Fatal("a snapshot closed the deck's menu")
	}
	s := strings.Join(viewLines(m), "\n")
	if !strings.Contains(s, "New agent") || !strings.Contains(s, "Detach") {
		t.Fatalf("the deck's menu is off screen:\n%s", s)
	}
	m, _ = step(t, m, key("esc"))
	if m.menuFor != "" {
		t.Fatal("esc left the deck's menu open")
	}
}

// The deck's menu offers the sounds switch only when the host plays sounds.
func TestDeckMenuItems(t *testing.T) {
	got := func(sound string) string {
		var l []string
		for _, it := range deckMenuItems(sound) {
			l = append(l, it.label+"="+it.key)
		}
		return strings.Join(l, ",")
	}
	if g := got(""); g != "New agent=n,New shell=s,New folder=f,Keys=?,Detach=q" {
		t.Fatalf("no player: %s", g)
	}
	if g := got("on"); !strings.Contains(g, "Mute sounds=S") {
		t.Fatalf("sounds on: %s", g)
	}
	if g := got("off"); !strings.Contains(g, "Sounds on=S") {
		t.Fatalf("sounds off: %s", g)
	}
}

// The deck's menu stays whole above the update notice, below many rows.
func TestTheDecksMenuFitsAboveTheNotice(t *testing.T) {
	var as []Agent
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"} {
		as = append(as, ag(n, "idle", 1))
	}
	m := sized(newModel(&fakeAPI{}), 34, 16).WithNotice(func() string { return "lea 9.9 is ready" })
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 7, Shown: "lea-a/a", Agents: as}})
	m, _ = step(t, m, click(tea.MouseButtonRight, 15))
	ls := viewLines(m)
	if len(ls) != 16 || !strings.Contains(strings.Join(ls, "\n"), "Detach") || !strings.Contains(ls[14], "9.9") {
		t.Fatalf("%d lines:\n%s", len(ls), strings.Join(ls, "\n"))
	}
}
