package deck

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// #241: the folder gestures a user tries first, colours, and agents that follow their lead.

// webLayout is one folder, web, holding a, then b at the root.
func webLayout(t *testing.T) (*memStore, Model) {
	t.Helper()
	st := &memStore{l: Layout{Folders: []Folder{{ID: "f1", Name: "web", Agents: []string{"lea-a/a"}}}}}
	return st, withFolders(t, st, ag("a", "idle", 1), ag("b", "idle", 1))
}

// A left click on a folder's row folds it, a second one unfolds it, and each is saved.
func TestClickOnAFolderRowTogglesIt(t *testing.T) {
	st, m := webLayout(t)
	m, _ = step(t, m, click(tea.MouseButtonLeft, 1)) // the folder's row
	if m.selected != folderKey("f1") || !st.l.Folders[0].Folded || order(m) != "[web] b" {
		t.Fatalf("first click: selected %q saved %+v order %q", m.selected, st.l, order(m))
	}
	m, _ = step(t, m, click(tea.MouseButtonLeft, 1))
	if st.l.Folders[0].Folded || order(m) != "[web] a b" {
		t.Fatalf("second click: saved %+v order %q", st.l, order(m))
	}
	// A click on an agent's row only selects it.
	m, _ = step(t, m, click(tea.MouseButtonLeft, 2))
	if m.selected != "lea-a/a" || st.l.Folders[0].Folded {
		t.Fatalf("click on a: selected %q saved %+v", m.selected, st.l)
	}
}

// h or left on an agent inside a folder selects the folder's row and folds it; on an agent at
// the root it does nothing.
func TestHFromInsideAFolderFoldsIt(t *testing.T) {
	for _, k := range []string{"h", "left"} {
		st, m := webLayout(t)
		m.selected = "lea-a/a"
		m = typed(t, m, k)
		if m.selected != folderKey("f1") || !st.l.Folders[0].Folded {
			t.Fatalf("%s: selected %q saved %+v", k, m.selected, st.l)
		}
		m.selected = "lea-a/b"
		m = typed(t, m, k)
		if m.selected != "lea-a/b" {
			t.Fatalf("%s at the root: selected %q", k, m.selected)
		}
	}
}

// u takes the selected agent out of its folder in one key; on a root agent or a shell it does
// nothing.
func TestUMovesAnAgentOutOfItsFolder(t *testing.T) {
	st := &memStore{l: Layout{Folders: []Folder{{ID: "f1", Name: "web", Agents: []string{"lea-a/a"}}}}}
	sh := ag("sh", "idle", 1)
	sh.Kind = KindShell
	m := withFolders(t, st, ag("a", "idle", 1), ag("b", "idle", 1), sh)
	m.selected = "lea-a/a"
	m = typed(t, m, "u")
	if m.selected != "lea-a/a" || m.pos("lea-a/a") < 0 || len(st.l.Folders[0].Agents) != 0 || order(m) != "[web] a b sh" {
		t.Fatalf("u: selected %q saved %+v order %q", m.selected, st.l, order(m))
	}
	saves := st.saves
	for _, k := range []string{"lea-a/b", "lea-a/sh"} {
		m.selected = k
		m = typed(t, m, "u")
	}
	if st.saves != saves || order(m) != "[web] a b sh" {
		t.Fatalf("u on a root row or a shell: %d saves, order %q", st.saves-saves, order(m))
	}
}

// The agent's menu says where o moves it, and offers u only when it is in a folder.
func TestAgentMenuOffersRemoveFromFolderOnlyInAFolder(t *testing.T) {
	labels := func(items []menuItem) string {
		var out []string
		for _, it := range items {
			out = append(out, it.label+"="+it.key)
		}
		return strings.Join(out, ",")
	}
	in, root := labels(menuItems(ag("a", "idle", 1), true)), labels(menuItems(ag("a", "idle", 1), false))
	if !strings.Contains(in, "Remove from folder=u") || !strings.Contains(in, "Move to another folder…=o") {
		t.Fatalf("in a folder: %s", in)
	}
	if strings.Contains(root, "=u") || !strings.Contains(root, "Move to folder…=o") {
		t.Fatalf("at the root: %s", root)
	}
	_, m := webLayout(t)
	m, _ = step(t, m, click(tea.MouseButtonRight, 2)) // a's menu
	if m.menuFor != "lea-a/a" {
		t.Fatalf("menu for %q", m.menuFor)
	}
	m = typed(t, m, "u") // picked from the menu
	if m.folderOf("lea-a/a") >= 0 || m.menuFor != "" {
		t.Fatal("Remove from folder left a in its folder")
	}
}

// x on a folder's row asks first: n and esc keep the folder, y deletes it and stops no agent.
func TestDeletingAFolderAsksFirst(t *testing.T) {
	for _, no := range []string{"n", "esc"} {
		st, m := webLayout(t)
		m.selected = folderKey("f1")
		m = typed(t, m, "x")
		if m.confirm != folderKey("f1") {
			t.Fatalf("x on a folder asked nothing (confirm %q)", m.confirm)
		}
		s := strings.Join(viewLines(m), "\n")
		if !strings.Contains(s, "Delete folder web?") || !strings.Contains(s, "none is stopped") || !strings.Contains(s, "y delete") {
			t.Fatalf("modal:\n%s", s)
		}
		m = typed(t, m, no)
		if m.confirm != "" || len(st.l.Folders) != 1 || order(m) != "[web] a b" {
			t.Fatalf("%s: confirm %q saved %+v", no, m.confirm, st.l)
		}
	}
	api := &fakeAPI{}
	st := &memStore{l: Layout{Folders: []Folder{{ID: "f1", Name: "web", Agents: []string{"lea-a/a"}}}}}
	m := sized(newModel(api).WithLayout(st), 34, 30)
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 7, Shown: "lea-a/a", Agents: []Agent{ag("a", "working", 1), ag("b", "idle", 1)}}})
	m.selected = folderKey("f1")
	m, cmd := step(t, typed(t, m, "x"), key("y"))
	if cmd != nil || len(st.l.Folders) != 0 || order(m) != "a b" || len(api.got()) != 0 {
		t.Fatalf("y: saved %+v order %q calls %v", st.l, order(m), api.got())
	}
}

// The status line lists a folder row's keys, and hints u on an agent in a folder; on a root
// agent it says nothing. The key list has u and enter's fold.
func TestFolderKeysAreHinted(t *testing.T) {
	_, m := webLayout(t)
	m.status = ""
	m.selected = folderKey("f1")
	if s := m.statusLine(m.innerWidth()); !strings.Contains(s, "enter fold") || !strings.Contains(s, "e name") {
		t.Fatalf("folder row: %q", s)
	}
	wide := m
	wide.width = 80
	if s := wide.statusLine(wide.innerWidth()); !strings.Contains(s, "x delete") || !strings.Contains(s, "J K move") || !strings.Contains(s, "c colour") {
		t.Fatalf("folder row, wide: %q", s)
	}
	m.selected = "lea-a/a"
	if s := m.statusLine(m.innerWidth()); !strings.Contains(s, "u out of folder") {
		t.Fatalf("agent in a folder: %q", s)
	}
	m.selected = "lea-a/b"
	if s := m.statusLine(m.innerWidth()); s != "" {
		t.Fatalf("root agent: %q", s)
	}
	m.help = true
	s := strings.Join(viewLines(m), "\n")
	for _, w := range []string{"f o u", "in, out", "enter       show / fold folder"} {
		if !strings.Contains(s, w) {
			t.Fatalf("help lacks %q:\n%s", w, s)
		}
	}
}

// truecolour draws in 24-bit colour on a dark background for the test, as a terminal would.
func truecolour(t *testing.T) {
	t.Helper()
	prof, dark := lipgloss.ColorProfile(), lipgloss.HasDarkBackground()
	lipgloss.SetColorProfile(0) // termenv.TrueColor, without importing termenv
	lipgloss.SetHasDarkBackground(true)
	t.Cleanup(func() { lipgloss.SetColorProfile(prof); lipgloss.SetHasDarkBackground(dark) })
}

// sgrOf is the 24-bit foreground SGR of a Catppuccin colour, dark shade.
func sgrOf(c lipgloss.AdaptiveColor) string {
	var r, g, b int
	fmt.Sscanf(c.Dark, "#%02x%02x%02x", &r, &g, &b)
	return fmt.Sprintf("38;2;%d;%d;%d", r, g, b)
}

// c on a folder's row (or Colour… in its menu) picks its colour: the arrow, the name and the
// guides of its agents take it, the dot keeps its state's; 0 clears it.
func TestCPicksAFolderColour(t *testing.T) {
	truecolour(t)
	st := &memStore{l: Layout{Folders: []Folder{{ID: "f1", Name: "web", Agents: []string{"lea-a/a"}}}}}
	blocked := withTask(ag("a", "idle", 1), false, true)
	m := withFolders(t, st, blocked, ag("b", "idle", 1))
	m.selected = folderKey("f1")
	m = typed(t, m, "c")
	if !m.menuPicker || m.menuFor != folderKey("f1") {
		t.Fatal("c opened no colour picker")
	}
	pink, _ := folderColour("pink")
	if s := m.View(); !strings.Contains(s, "No colour") || !strings.Contains(s, sgrOf(pink)) {
		t.Fatalf("picker:\n%q", s)
	}
	m = typed(t, m, "1") // pink
	if st.l.Folders[0].Color != "pink" || m.menuFor != "" {
		t.Fatalf("saved %+v", st.l)
	}
	m.selected = "lea-a/b" // no selection background on the rows looked at
	ls := m.rows(m.innerWidth())
	if !strings.Contains(ls[0], sgrOf(pink)+"m▾") || !strings.Contains(ls[0], "1;"+sgrOf(pink)+"mweb") {
		t.Fatalf("folder row %q", ls[0])
	}
	if !strings.Contains(ls[0], sgrOf(theme.Blocked)) {
		t.Fatalf("the folder's dot lost its state colour: %q", ls[0])
	}
	if !strings.Contains(ls[1], sgrOf(pink)+"m╎") || strings.Contains(ls[3], sgrOf(pink)) {
		t.Fatalf("guides: a %q, b %q", ls[1], ls[3])
	}
	// The folder's menu has Colour…; its No colour (0) clears it.
	m, _ = step(t, m, click(tea.MouseButtonRight, 1))
	if s := m.View(); !strings.Contains(s, "Colour…") {
		t.Fatalf("folder menu:\n%s", s)
	}
	m = typed(t, m, "c", "0")
	if st.l.Folders[0].Color != "" || strings.Contains(m.rows(m.innerWidth())[0], sgrOf(pink)) {
		t.Fatalf("no colour: saved %+v", st.l)
	}
}

// The colour is saved by name and survives a restart; an unknown name is refused like any bad
// layout; a layout without colour still loads.
func TestFolderColourIsSavedByName(t *testing.T) {
	p := filepath.Join(t.TempDir(), "deck", "lea-a.json")
	fs := FileStore{Path: p}
	if err := fs.Save(Layout{Folders: []Folder{{ID: "f1", Name: "web", Color: "teal"}}}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), `"color": "teal"`) {
		t.Fatalf("file %s", b)
	}
	if l, err := fs.Load(); err != nil || l.Folders[0].Color != "teal" {
		t.Fatalf("load %+v %v", l, err)
	}
	if err := os.WriteFile(p, []byte(`{"folders":[{"id":"f1","name":"web","color":"#ff0000"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Load(); !errors.Is(err, errBadLayout) {
		t.Fatalf("unknown colour loaded: %v", err)
	}
	if err := os.WriteFile(p, []byte(`{"folders":[{"id":"f1","name":"web"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if l, err := fs.Load(); err != nil || l.Folders[0].Color != "" {
		t.Fatalf("no colour: %+v %v", l, err)
	}
	if err := (Layout{Saved: -1}).check(); err == nil {
		t.Fatal("negative saved time accepted")
	}
}

// spawned is agent name started by lead (a key, "" for the user) at created.
func spawned(name, lead string, created int64) Agent {
	a := launched(ag(name, "working", 1), created)
	a.Parent = lead
	return a
}

// An agent started by a lead in a folder lands at the end of that folder, saved; one whose lead
// is at the root, or that the user started, stays at the root. Once the user moves it out, later
// snapshots leave it there.
func TestSpawnedAgentsFollowTheirLead(t *testing.T) {
	st := &memStore{l: Layout{Saved: 9_000, Folders: []Folder{{ID: "f1", Name: "web", Agents: []string{"lea-a/lead", "lea-a/x"}}}}}
	lead, x, other := launched(ag("lead", "idle", 1), 100), launched(ag("x", "idle", 1), 100), launched(ag("other", "idle", 1), 100)
	m := withFolders(t, st, lead, x, other)
	if order(m) != "[web] lead x other" {
		t.Fatalf("order %q", order(m))
	}
	agents := []Agent{lead, x, other, spawned("kid", "lea-a/lead", 9_990), spawned("rootkid", "lea-a/other", 9_990), spawned("mine", "", 9_990)}
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Agents: agents}})
	if got := order(m); got != "[web] lead x kid other rootkid mine" {
		t.Fatalf("order %q", got)
	}
	if fa := st.l.Folders[0].Agents; len(fa) != 3 || fa[2] != "lea-a/kid" {
		t.Fatalf("saved %+v", st.l)
	}
	m.selected = "lea-a/kid"
	m = typed(t, m, "u")
	for v := int64(9); v < 11; v++ {
		m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: v, Agents: agents}})
	}
	if got := order(m); got != "[web] lead x other kid rootkid mine" {
		t.Fatalf("moved back: order %q", got)
	}
}

// An agent a lead started while no deck was attached is filed on the next attach: it is newer
// than the layout's last save. A layout written before #241 (no saved time) files nothing on
// that first attach.
func TestAgentsSpawnedWhileDetachedAreFiledOnAttach(t *testing.T) {
	later := func() time.Time { return time.Unix(20_000, 0) }
	lead := launched(ag("lead", "idle", 1), 100)
	for _, c := range []struct {
		saved int64
		filed bool
	}{{10_000, true}, {0, false}} {
		st := &memStore{l: Layout{Saved: c.saved, Folders: []Folder{{ID: "f1", Name: "web", Agents: []string{"lea-a/lead"}}}}}
		m := sized(New(&fakeAPI{}, "lea-a", false, later).WithLayout(st), 34, 30)
		m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 7, Agents: []Agent{lead, spawned("kid", "lea-a/lead", 15_000)}}})
		if filed := m.folderOf("lea-a/kid") == 0; filed != c.filed {
			t.Fatalf("saved %d: kid filed %v, saved %+v", c.saved, filed, st.l)
		}
		if c.filed && (len(st.l.Folders[0].Agents) != 2 || st.l.Saved != 20_000) {
			t.Fatalf("saved %d: layout %+v", c.saved, st.l)
		}
	}
}
