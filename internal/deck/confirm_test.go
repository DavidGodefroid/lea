package deck

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// stopping is three rows with b selected and the stop modal open over them.
func stopping(t *testing.T, api *fakeAPI) Model {
	t.Helper()
	m := sized(loaded(t, api, "lea-a/b", ag("a", "idle", 1), ag("b", "idle", 1), ag("c", "idle", 1)), 34, 20)
	m, cmd := step(t, m, key("x"))
	if cmd != nil || m.confirm != "lea-a/b" {
		t.Fatalf("x: confirm %q", m.confirm)
	}
	return m
}

// modalAt finds the modal's lines in the view: its title line, its buttons line.
func modalAt(t *testing.T, m Model) (title, buttons int, ls []string) {
	t.Helper()
	ls = viewLines(m)
	title, buttons = -1, -1
	for i, l := range ls {
		if strings.Contains(l, "Stop agent") {
			title = i
		}
		if strings.Contains(l, "[y] Confirm") && strings.Contains(l, "[n/esc] Cancel") {
			buttons = i
		}
	}
	if title < 0 || buttons < 0 {
		t.Fatalf("no modal in the view:\n%s", strings.Join(ls, "\n"))
	}
	return title, buttons, ls
}

// The stop confirmation is a modal over the agent list: a title, what will happen, the two
// buttons; the focus starts on Cancel. The status line says nothing of it.
func TestStopOpensAModalOverTheList(t *testing.T) {
	m := stopping(t, &fakeAPI{})
	title, buttons, ls := modalAt(t, m)
	all := strings.Join(ls, "\n")
	if !strings.Contains(all, "Stop b?") || !strings.Contains(all, "Its process ends") {
		t.Fatalf("modal does not say what will happen:\n%s", all)
	}
	if buttons <= title || title < 1 || buttons >= len(ls)-1 {
		t.Fatalf("title at %d buttons at %d:\n%s", title, buttons, all)
	}
	if !strings.Contains(ls[buttons], "›[n/esc] Cancel") || strings.Contains(ls[buttons], "›[y] Confirm") {
		t.Fatalf("focus is not on Cancel: %q", ls[buttons])
	}
	if strings.Contains(all, "y to confirm") || m.status != "" {
		t.Fatalf("the status line still asks: status %q\n%s", m.status, all)
	}
	if len(ls) != 20 {
		t.Fatalf("%d lines", len(ls))
	}
	for i, l := range ls {
		if lipgloss.Width(l) != 34 {
			t.Fatalf("line %d is %d wide: %q", i, lipgloss.Width(l), l)
		}
	}
}

func TestTheModalConfirmsWithY(t *testing.T) {
	api := &fakeAPI{}
	m, cmd := step(t, stopping(t, api), key("y"))
	if cmd == nil || m.confirm != "" {
		t.Fatalf("y: confirm %q", m.confirm)
	}
	cmd()
	if got := api.got(); len(got) != 1 || got[0] != "stop lea-a/b" {
		t.Fatalf("calls %q", got)
	}
	if strings.Contains(strings.Join(viewLines(m), "\n"), "Stop agent") {
		t.Fatal("modal still drawn after y")
	}
}

func TestTheModalCancelsWithNEscAndEnterOnCancel(t *testing.T) {
	for _, k := range []tea.KeyMsg{key("n"), {Type: tea.KeyEsc}, key("enter"), key("ctrl+c")} {
		api := &fakeAPI{}
		m, cmd := step(t, stopping(t, api), k)
		if cmd != nil || m.confirm != "" || len(api.got()) != 0 {
			t.Fatalf("%q: confirm %q calls %q", k.String(), m.confirm, api.got())
		}
	}
}

// No other key confirms: the modal stays open and nothing is called, j or a digit do not move.
func TestOtherKeysDoNotConfirm(t *testing.T) {
	api := &fakeAPI{}
	m := stopping(t, api)
	for _, k := range []string{"x", "a", "Y", "j", "1", " ", "q", "?"} {
		var cmd tea.Cmd
		m, cmd = step(t, m, key(k))
		if cmd != nil || m.confirm != "lea-a/b" || m.selected != "lea-a/b" || m.help || len(api.got()) != 0 {
			t.Fatalf("%q: cmd %v confirm %q selected %q calls %q", k, cmd != nil, m.confirm, m.selected, api.got())
		}
	}
}

// The arrows and tab move the focus; enter picks the focused button.
func TestTheFocusMovesAndEnterPicksIt(t *testing.T) {
	api := &fakeAPI{}
	m, _ := step(t, stopping(t, api), tea.KeyMsg{Type: tea.KeyLeft})
	if _, b, ls := modalAt(t, m); !strings.Contains(ls[b], "›[y] Confirm") {
		t.Fatalf("left did not focus Confirm: %q", ls[b])
	}
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyTab})
	if _, b, ls := modalAt(t, m); !strings.Contains(ls[b], "›[n/esc] Cancel") {
		t.Fatalf("tab did not move back to Cancel: %q", ls[b])
	}
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyRight})
	m, cmd := step(t, m, key("enter"))
	if cmd == nil || m.confirm != "" {
		t.Fatalf("enter on Confirm: confirm %q", m.confirm)
	}
	cmd()
	if got := api.got(); len(got) != 1 || got[0] != "stop lea-a/b" {
		t.Fatalf("calls %q", got)
	}
}

// A click on a button picks it; a click elsewhere in the box does nothing, outside it cancels.
func TestTheModalWorksWithTheMouse(t *testing.T) {
	at := func(m Model, label string) (int, int) {
		_, b, ls := modalAt(t, m)
		x := strings.Index(ls[b], label)
		return lipgloss.Width(ls[b][:x]) + 1, b
	}
	press := func(x, y int) tea.MouseMsg {
		return tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}
	}

	api := &fakeAPI{}
	m := stopping(t, api)
	x, y := at(m, "Cancel")
	m, cmd := step(t, m, press(x, y))
	if cmd != nil || m.confirm != "" || len(api.got()) != 0 {
		t.Fatalf("click Cancel: confirm %q calls %q", m.confirm, api.got())
	}

	m = stopping(t, api)
	title, _, _ := modalAt(t, m)
	m, cmd = step(t, m, press(10, title+1))
	if cmd != nil || m.confirm != "lea-a/b" {
		t.Fatalf("click in the box: confirm %q", m.confirm)
	}
	m, cmd = step(t, m, tea.MouseMsg{Y: 1, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	if cmd != nil || m.confirm != "lea-a/b" || m.selected != "lea-a/b" {
		t.Fatalf("wheel: confirm %q selected %q", m.confirm, m.selected)
	}
	x, y = at(m, "Confirm")
	m, cmd = step(t, m, press(x, y))
	if cmd == nil || m.confirm != "" {
		t.Fatalf("click Confirm: confirm %q", m.confirm)
	}
	cmd()
	if got := api.got(); len(got) != 1 || got[0] != "stop lea-a/b" {
		t.Fatalf("calls %q", got)
	}

	api = &fakeAPI{}
	m = stopping(t, api)
	m, cmd = step(t, m, press(5, 19)) // the last body line, below the box
	if cmd != nil || m.confirm != "" || m.selected != "lea-a/b" || len(api.got()) != 0 {
		t.Fatalf("click outside: confirm %q selected %q calls %q", m.confirm, m.selected, api.got())
	}
}

// The modal fits a short deck and an ASCII terminal.
func TestTheModalFitsSmallAndASCII(t *testing.T) {
	m := stopping(t, &fakeAPI{})
	m.ascii, m.height = true, 9
	ls := viewLines(m)
	all := strings.Join(ls, "\n")
	if len(ls) != 9 || !strings.Contains(all, "[y] Confirm") || !strings.Contains(all, ">[n/esc] Cancel") {
		t.Fatalf("view:\n%s", all)
	}
	for i, l := range ls {
		if lipgloss.Width(l) != 34 {
			t.Fatalf("line %d is %d wide: %q", i, lipgloss.Width(l), l)
		}
		for _, r := range l {
			if r > 0x7e {
				t.Fatalf("line %d is not ASCII: %q", i, l)
			}
		}
	}
}
