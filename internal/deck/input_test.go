package deck

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// inputAt finds the input modal in the view: its title line, its field line, its buttons line.
func inputAt(t *testing.T, m Model, title string) (top, field, buttons int, ls []string) {
	t.Helper()
	ls = viewLines(m)
	top, field, buttons = -1, -1, -1
	for i, l := range ls {
		switch {
		case strings.Contains(l, title):
			top = i
		case top >= 0 && field < 0 && strings.Contains(l, "▍"):
			field = i
		case strings.Contains(l, "[enter] Save") && strings.Contains(l, "[esc] Cancel"):
			buttons = i
		}
	}
	if top < 0 || field < 0 || buttons < 0 {
		t.Fatalf("no %q modal in the view:\n%s", title, strings.Join(ls, "\n"))
	}
	return top, field, buttons, ls
}

// noStatus checks the line above the bottom border is blank: the field is in the modal only.
func noStatus(t *testing.T, ls []string) {
	t.Helper()
	if s := strings.Trim(ls[len(ls)-2], "│ "); s != "" {
		t.Fatalf("the status line says %q:\n%s", s, strings.Join(ls, "\n"))
	}
	for _, p := range []string{"name: ", "folder: ", "value: "} {
		if strings.Contains(strings.Join(ls, "\n"), p) {
			t.Fatalf("the old prompt %q is still drawn:\n%s", p, strings.Join(ls, "\n"))
		}
	}
}

// e on an agent opens the Rename agent modal holding the field; the status line stays empty.
func TestERenamesInAModal(t *testing.T) {
	api := &fakeAPI{}
	m := sized(loaded(t, api, "lea-a/a", labelled("a", "old"), ag("b", "idle", 1)), 34, 14)
	m, _ = step(t, m, key("e"))
	m = typeKeys(t, m, "x")
	top, field, buttons, ls := inputAt(t, m, "Rename agent")
	if !strings.Contains(ls[field], "oldx▍") || !(top < field && field < buttons) {
		t.Fatalf("title %d field %d buttons %d:\n%s", top, field, buttons, strings.Join(ls, "\n"))
	}
	noStatus(t, ls)
	if len(ls) != 14 {
		t.Fatalf("%d lines", len(ls))
	}
	for i, l := range ls {
		if lipgloss.Width(l) != 34 {
			t.Fatalf("line %d is %d wide: %q", i, lipgloss.Width(l), l)
		}
	}
}

// e on a folder says Rename folder; f says New folder.
func TestFolderFieldsOpenAModal(t *testing.T) {
	st := &memStore{l: Layout{Folders: []Folder{{ID: "f1", Name: "web"}}}}
	m := sized(withFolders(t, st, ag("a", "idle", 1)), 34, 14)
	m.selected = folderKey("f1")
	m, _ = step(t, m, key("e"))
	_, field, _, ls := inputAt(t, m, "Rename folder")
	if !strings.Contains(ls[field], "web▍") {
		t.Fatalf("field %q", ls[field])
	}
	noStatus(t, ls)
	m, _ = step(t, m, key("esc"))

	m.selected = "lea-a/a"
	m, _ = step(t, m, key("f"))
	_, field, _, ls = inputAt(t, m, "New folder")
	if !strings.Contains(ls[field], "folder 2▍") {
		t.Fatalf("field %q", ls[field])
	}
	noStatus(t, ls)
}

// A click on Save saves, on Cancel cancels, outside the box cancels; a click inside does nothing.
func TestTheInputModalMouse(t *testing.T) {
	press := func(x, y int) tea.MouseMsg {
		return tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}
	}
	at := func(m Model, label string) (int, int) {
		_, _, buttons, ls := inputAt(t, m, "Rename agent")
		x := strings.Index(ls[buttons], label)
		return lipgloss.Width(ls[buttons][:x]) + 1, buttons
	}
	open := func(api *fakeAPI) Model {
		m := sized(loaded(t, api, "lea-a/a", labelled("a", "old"), ag("b", "idle", 1)), 34, 14)
		m, _ = step(t, m, key("e"))
		return m
	}

	api := &fakeAPI{}
	m := open(api)
	_, field, _, _ := inputAt(t, m, "Rename agent")
	m, cmd := step(t, m, press(10, field))
	if cmd != nil || m.editFor != "lea-a/a" {
		t.Fatalf("click in the box: edit %q", m.editFor)
	}
	m, cmd = step(t, m, press(at(m, "Save")))
	if cmd == nil || m.editFor != "" {
		t.Fatalf("click Save: edit %q", m.editFor)
	}
	cmd()
	if got := api.got(); len(got) != 1 || got[0] != `label lea-a/a "old"` {
		t.Fatalf("calls %q", got)
	}

	api = &fakeAPI{}
	m = open(api)
	m, cmd = step(t, m, press(at(m, "Cancel")))
	if cmd != nil || m.editFor != "" || len(api.got()) != 0 {
		t.Fatalf("click Cancel: edit %q calls %q", m.editFor, api.got())
	}

	m = open(api)
	m, cmd = step(t, m, press(10, 1))
	if cmd != nil || m.editFor != "" || len(api.got()) != 0 {
		t.Fatalf("click outside: edit %q calls %q", m.editFor, api.got())
	}
}

// An empty field still clears the label.
func TestTheInputModalSavesAnEmptyField(t *testing.T) {
	api := &fakeAPI{}
	m := sized(loaded(t, api, "lea-a/a", labelled("a", "old")), 34, 14)
	m, _ = step(t, m, key("e"))
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyCtrlU})
	m, cmd := step(t, m, key("enter"))
	if cmd == nil || m.editFor != "" {
		t.Fatalf("enter: edit %q", m.editFor)
	}
	cmd()
	if got := api.got(); len(got) != 1 || got[0] != `label lea-a/a ""` {
		t.Fatalf("calls %q", got)
	}
}

// A text setting's field is the same modal, titled with the setting; a click outside cancels.
func TestASettingFieldOpensAModal(t *testing.T) {
	st := someSettings()
	m := sized(withSettings(t, &fakeAPI{}, st), 40, 20)
	m, _ = step(t, m, key(","))
	for range 3 {
		m, _ = step(t, m, key("down"))
	}
	m, _ = step(t, m, key("enter"))
	m = typeIn(t, m, "/a.oga")
	_, field, _, ls := inputAt(t, m, "Edit Done sound")
	if !strings.Contains(ls[field], "/a.oga▍") {
		t.Fatalf("field %q", ls[field])
	}
	noStatus(t, ls)
	m, _ = step(t, m, tea.MouseMsg{X: 10, Y: 1, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if m.setEditing || !m.setOpen || len(st.sets) != 0 {
		t.Fatalf("click outside: editing %v open %v sets %q", m.setEditing, m.setOpen, st.sets)
	}
}
