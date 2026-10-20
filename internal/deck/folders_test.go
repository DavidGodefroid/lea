package deck

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// memStore is a LayoutStore in memory; saves counts the writes.
type memStore struct {
	l     Layout
	err   error
	saves int
}

func (s *memStore) Load() (Layout, error) { return cloneLayout(s.l), s.err }
func (s *memStore) Save(l Layout) error {
	if err := l.check(); err != nil {
		return err
	}
	s.l, s.saves = cloneLayout(l), s.saves+1
	return nil
}

// withFolders is the deck of agents as, with the folders of st.
func withFolders(t *testing.T, st *memStore, as ...Agent) Model {
	t.Helper()
	m := sized(newModel(&fakeAPI{}).WithLayout(st), 34, 30)
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 7, Shown: as[0].Key, Agents: as}})
	return m
}

func typed(t *testing.T, m Model, keys ...string) Model {
	t.Helper()
	for _, k := range keys {
		m, _ = step(t, m, key(k))
	}
	return m
}

func order(m Model) string {
	var out []string
	for _, e := range m.entries(false) {
		if e.agent < 0 {
			out = append(out, "["+m.layout.Folders[e.in].Name+"]")
		} else {
			out = append(out, m.agents[e.agent].Name)
		}
	}
	return strings.Join(out, " ")
}

func TestFNewFolderIsNamedAndSaved(t *testing.T) {
	st := &memStore{}
	m := withFolders(t, st, ag("a", "idle", 1), ag("b", "idle", 1))
	m = typed(t, m, "f")
	if !isFolderKey(m.editFor) || m.selected != m.editFor {
		t.Fatalf("f: edit %q selected %q", m.editFor, m.selected)
	}
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyCtrlU})
	m = typed(t, m, "w", "e", "b", " ", "x")
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m, _ = step(t, m, key("enter"))
	if len(st.l.Folders) != 1 || st.l.Folders[0].Name != "web x" {
		t.Fatalf("saved %+v", st.l)
	}
	if got := order(m); got != "[web x] a b" {
		t.Fatalf("order %q", got)
	}
	if s := strings.Join(viewLines(m), "\n"); !strings.Contains(s, "▾ web x") {
		t.Fatalf("no folder row:\n%s", s)
	}
}

// A folder name goes through textsafe: an escape or invisible character typed or loaded never
// reaches the terminal.
func TestFolderNamesNeverCarryEscapes(t *testing.T) {
	st := &memStore{}
	m := withFolders(t, st, ag("a", "idle", 1))
	m = typed(t, m, "f")
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyCtrlU})
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x\x1b[31m\u202ey")})
	m, _ = step(t, m, key("enter"))
	if n := st.l.Folders[0].Name; n != "x[31my" {
		t.Fatalf("name %q", n)
	}
	for _, bad := range []string{"\x1b]0;pwn\x07", "a\u200bb", "", "   ", strings.Repeat("x", maxLabel+1)} {
		if validFolderName(bad) {
			t.Errorf("%q accepted", bad)
		}
		if err := (Layout{Folders: []Folder{{ID: "f1", Name: bad}}}).check(); err == nil {
			t.Errorf("layout with %q accepted", bad)
		}
	}
}

func TestOMovesAnAgentIntoAFolderAndOut(t *testing.T) {
	st := &memStore{l: Layout{Folders: []Folder{{ID: "f1", Name: "web"}, {ID: "f2", Name: "ops"}}}}
	m := withFolders(t, st, ag("a", "idle", 1), ag("b", "idle", 1))
	if got := order(m); got != "[web] [ops] a b" {
		t.Fatalf("order %q", got)
	}
	m.selected = "lea-a/b"
	m = typed(t, m, "o")
	if !m.menuPicker || m.menuFor != "lea-a/b" {
		t.Fatal("o opened no picker")
	}
	s := strings.Join(viewLines(m), "\n")
	for _, w := range []string{"web", "ops", "New folder"} {
		if !strings.Contains(s, w) {
			t.Fatalf("picker lacks %q:\n%s", w, s)
		}
	}
	m = typed(t, m, "2") // ops
	if got := order(m); got != "[web] [ops] b a" || st.l.Folders[1].Agents[0] != "lea-a/b" {
		t.Fatalf("order %q, saved %+v", got, st.l)
	}
	if m.selected != "lea-a/b" {
		t.Fatalf("selected %q", m.selected)
	}
	m = typed(t, m, "o", "0") // no folder
	if got := order(m); got != "[web] [ops] a b" || len(st.l.Folders[1].Agents) != 0 {
		t.Fatalf("order %q, saved %+v", got, st.l)
	}
}

func TestPickerNewFolderTakesTheAgent(t *testing.T) {
	st := &memStore{}
	m := withFolders(t, st, ag("a", "idle", 1), ag("b", "idle", 1))
	m.selected = "lea-a/b"
	m = typed(t, m, "o", "f")
	m, _ = step(t, m, key("enter")) // keeps the default name
	if got := order(m); got != "[folder 1] b a" {
		t.Fatalf("order %q", got)
	}
}

// Delete puts the folder's agents back at the root and never stops one.
func TestDeletingAFolderNeverStopsItsAgents(t *testing.T) {
	st := &memStore{l: Layout{Folders: []Folder{{ID: "f1", Name: "web", Agents: []string{"lea-a/b"}}}}}
	api := &fakeAPI{}
	m := sized(newModel(api).WithLayout(st), 34, 30)
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 7, Shown: "lea-a/a", Agents: []Agent{ag("a", "idle", 1), ag("b", "working", 1)}}})
	m.selected = folderKey("f1")
	m = typed(t, m, "x")
	if got := order(m); got != "a b" || len(st.l.Folders) != 0 {
		t.Fatalf("order %q, saved %+v", got, st.l)
	}
	for _, c := range api.got() {
		if strings.HasPrefix(c, "stop") || strings.HasPrefix(c, "remove") {
			t.Fatalf("deleting a folder called %q", c)
		}
	}
	if m.confirm != "" {
		t.Fatal("deleting a folder asked to stop an agent")
	}
}

func TestEnterFoldsAndJKSkipTheHiddenAgents(t *testing.T) {
	st := &memStore{l: Layout{Folders: []Folder{{ID: "f1", Name: "web", Agents: []string{"lea-a/a", "lea-a/b"}}}}}
	m := withFolders(t, st, ag("a", "idle", 1), ag("b", "idle", 1), ag("c", "idle", 1))
	m.selected = folderKey("f1")
	m = typed(t, m, "enter")
	if !st.l.Folders[0].Folded || order(m) != "[web] c" {
		t.Fatalf("order %q folded %v", order(m), st.l.Folders[0].Folded)
	}
	if s := strings.Join(viewLines(m), "\n"); !strings.Contains(s, "▸ web") {
		t.Fatalf("no folded arrow:\n%s", s)
	}
	m = typed(t, m, "j")
	if m.selected != "lea-a/c" {
		t.Fatalf("j went to %q", m.selected)
	}
	m = typed(t, m, "k", "l")
	if st.l.Folders[0].Folded || order(m) != "[web] a b c" {
		t.Fatalf("l: order %q", order(m))
	}
}

// A folded folder's dot is its agents' worst state: red when one needs the user, the spinner
// while one works, green once none works any more.
func TestAFoldedFoldersDotAggregatesItsAgents(t *testing.T) {
	st := &memStore{l: Layout{Folders: []Folder{{ID: "f1", Name: "web", Folded: true, Agents: []string{"lea-a/a", "lea-a/b"}}}}}
	for _, c := range []struct {
		a, b, want string
	}{
		{"idle", "done", "idle"},
		{"idle", "working", "working"},
		{"working", "blocked", "blocked"},
	} {
		m := withFolders(t, st, ag("a", c.a, 9_990), ag("b", c.b, 9_990), ag("c", "idle", 1))
		if got, _ := m.folderState(0); got != c.want {
			t.Errorf("%s+%s: %q, want %q", c.a, c.b, got, c.want)
		}
		g, _ := stateMark(c.want, 9_990, clock.Unix(), 0, false)
		if line := viewLines(m)[1]; !strings.Contains(line, g+" ▸ web") {
			t.Errorf("%s+%s: line %q lacks %q", c.a, c.b, line, g)
		}
	}
	// An idle agent whose subagents still run is working; a paused one does not count.
	sub := ag("a", "idle", 9_990)
	sub.Subagents = 1
	p := ag("b", "working", 9_990)
	p.Paused = true
	m := withFolders(t, st, sub, p)
	if got, _ := m.folderState(0); got != "working" {
		t.Errorf("subagents: %q", got)
	}
	sub.Subagents = 0
	m = withFolders(t, st, sub, p)
	if got, _ := m.folderState(0); got != "idle" {
		t.Errorf("paused: %q", got)
	}
}

func TestJKReorderFoldersAndAgentsInAFolder(t *testing.T) {
	st := &memStore{l: Layout{Folders: []Folder{{ID: "f1", Name: "web", Agents: []string{"lea-a/a", "lea-a/b"}}, {ID: "f2", Name: "ops"}}}}
	m := withFolders(t, st, ag("a", "idle", 1), ag("b", "idle", 1), ag("c", "idle", 1))
	m.selected = folderKey("f2")
	m = typed(t, m, "K")
	if got := order(m); got != "[ops] [web] a b c" {
		t.Fatalf("K on a folder: %q", got)
	}
	m.selected = "lea-a/a"
	m = typed(t, m, "J")
	if got := order(m); got != "[ops] [web] b a c" || st.l.Folders[1].Agents[0] != "lea-a/b" {
		t.Fatalf("J in a folder: %q, saved %+v", got, st.l)
	}
	m.selected = "lea-a/c"
	before := st.saves
	m = typed(t, m, "K")
	if got := order(m); got != "[ops] [web] b a c" || st.saves != before {
		t.Fatalf("K at the root: %q", got)
	}
}

func TestTheFolderMenu(t *testing.T) {
	st := &memStore{l: Layout{Folders: []Folder{{ID: "f1", Name: "web"}, {ID: "f2", Name: "ops"}}}}
	m := withFolders(t, st, ag("a", "idle", 1))
	m, _ = step(t, m, click(tea.MouseButtonRight, 2)) // ops's line
	if m.menuFor != folderKey("f2") {
		t.Fatalf("menu %q", m.menuFor)
	}
	s := strings.Join(viewLines(m), "\n")
	for _, w := range []string{"Fold", "Rename", "Move up", "Move down", "Delete"} {
		if !strings.Contains(s, w) {
			t.Fatalf("folder menu lacks %q:\n%s", w, s)
		}
	}
	m = typed(t, m, "K") // Move up
	if got := order(m); got != "[ops] [web] a" {
		t.Fatalf("Move up: %q", got)
	}
	m, _ = step(t, m, click(tea.MouseButtonRight, 1))
	m = typed(t, m, "e")
	if m.editFor != folderKey("f2") {
		t.Fatalf("Rename: edit %q", m.editFor)
	}
	if line := viewLines(m)[len(viewLines(m))-2]; !strings.Contains(line, "folder: ops") {
		t.Fatalf("field %q", line)
	}
}

// ctrl + right click makes a folder; over an agent, the agent goes in.
func TestCtrlRightClickMakesAFolder(t *testing.T) {
	st := &memStore{}
	m := withFolders(t, st, ag("a", "idle", 1), ag("b", "idle", 1))
	m, _ = step(t, m, tea.MouseMsg{Y: 3, Button: tea.MouseButtonRight, Action: tea.MouseActionPress, Ctrl: true}) // b
	m, _ = step(t, m, key("enter"))
	if got := order(m); got != "[folder 1] b a" {
		t.Fatalf("over b: %q", got)
	}
	m, _ = step(t, m, tea.MouseMsg{Y: 25, Button: tea.MouseButtonRight, Action: tea.MouseActionPress, Ctrl: true})
	m, _ = step(t, m, key("esc"))
	if got := order(m); got != "[folder 1] b [folder 2] a" {
		t.Fatalf("below the rows: %q", got)
	}
}

// space finds an agent that needs the user inside a folded folder, and unfolds it.
func TestSpaceUnfoldsTheFolderOfTheAgentThatNeedsYou(t *testing.T) {
	st := &memStore{l: Layout{Folders: []Folder{{ID: "f1", Name: "web", Folded: true, Agents: []string{"lea-a/b"}}}}}
	m := withFolders(t, st, ag("a", "idle", 1), ag("b", "blocked", 1))
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeySpace})
	if m.selected != "lea-a/b" || st.l.Folders[0].Folded {
		t.Fatalf("selected %q folded %v", m.selected, st.l.Folders[0].Folded)
	}
}

// A show from elsewhere of an agent in a folded folder selects the folder's row.
func TestAShownAgentInAFoldedFolderSelectsTheFolder(t *testing.T) {
	st := &memStore{l: Layout{Folders: []Folder{{ID: "f1", Name: "web", Folded: true, Agents: []string{"lea-a/b"}}}}}
	m := withFolders(t, st, ag("a", "idle", 1), ag("b", "idle", 1))
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Shown: "lea-a/b", Agents: []Agent{ag("a", "idle", 1), ag("b", "idle", 1)}}})
	if m.selected != folderKey("f1") {
		t.Fatalf("selected %q", m.selected)
	}
	// A folder's row shows no agent: a pending delayed show is dropped.
	if _, cmd := m.move(0); cmd != nil {
		t.Fatalf("selecting a folder schedules a show")
	}
}

// An agent removed for good leaves its folder at the next save; the selection survives its
// folder's agent going.
func TestARemovedAgentLeavesItsFolder(t *testing.T) {
	st := &memStore{l: Layout{Folders: []Folder{{ID: "f1", Name: "web", Agents: []string{"lea-a/a", "lea-a/b"}}}}}
	m := withFolders(t, st, ag("a", "idle", 1), ag("b", "idle", 1))
	m.selected = "lea-a/b"
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Agents: []Agent{ag("a", "idle", 1)}}})
	if m.selected != "lea-a/a" {
		t.Fatalf("selected %q", m.selected)
	}
	m = typed(t, m, "k", "enter") // fold: a save
	if got := st.l.Folders[0].Agents; len(got) != 1 || got[0] != "lea-a/a" {
		t.Fatalf("saved %q", got)
	}
}

func TestShellsStayOutOfFolders(t *testing.T) {
	sh := Agent{Key: "\x00shell:lea-a/sh-1", Name: "sh-1", Kind: KindShell, State: "running"}
	st := &memStore{l: Layout{Folders: []Folder{{ID: "f1", Name: "web"}}}}
	m := withFolders(t, st, ag("a", "idle", 1), sh)
	m.selected = sh.Key
	m = typed(t, m, "o")
	if m.menuFor != "" {
		t.Fatal("o opened a picker on a shell")
	}
}

func TestFolderCountIsCapped(t *testing.T) {
	st := &memStore{}
	m := withFolders(t, st, ag("a", "idle", 1))
	for range maxFolders {
		m = typed(t, m, "f", "enter")
	}
	m = typed(t, m, "f")
	if len(m.layout.Folders) != maxFolders || m.editFor != "" || !strings.Contains(m.status, "at most") {
		t.Fatalf("%d folders, status %q", len(m.layout.Folders), m.status)
	}
}

// The file store: a round trip, a missing file, and every file it refuses.
func TestFileStore(t *testing.T) {
	dir := t.TempDir()
	p, ok := LayoutPath(dir, "lea-a")
	if !ok || p != filepath.Join(dir, "deck", "lea-a.json") {
		t.Fatalf("path %q %v", p, ok)
	}
	for _, bad := range []string{"", "../x", "a/b", "A", ".x", "lea-a.json/.."} {
		if _, ok := LayoutPath(dir, bad); ok {
			t.Errorf("container %q gives a path", bad)
		}
	}
	s := FileStore{Path: p}
	if l, err := s.Load(); err != nil || len(l.Folders) != 0 {
		t.Fatalf("missing file: %+v %v", l, err)
	}
	want := Layout{Folders: []Folder{{ID: "f1", Name: "web", Folded: true, Agents: []string{"lea-a/a"}}}}
	if err := s.Save(want); err != nil {
		t.Fatal(err)
	}
	for path, mode := range map[string]os.FileMode{p: 0o600, filepath.Dir(p): 0o700} {
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != mode {
			t.Fatalf("%s: %v %v", path, fi.Mode(), err)
		}
	}
	got, err := s.Load()
	if err != nil || got.Folders[0].Name != "web" || !got.Folders[0].Folded || got.Folders[0].Agents[0] != "lea-a/a" {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	if ents, _ := os.ReadDir(filepath.Dir(p)); len(ents) != 1 {
		t.Fatalf("temporary files left: %v", ents)
	}

	write := func(v any) {
		b, _ := json.Marshal(v)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, v := range map[string]any{
		"bad id":         Layout{Folders: []Folder{{ID: "../x", Name: "a"}}},
		"same id":        Layout{Folders: []Folder{{ID: "f1", Name: "a"}, {ID: "f1", Name: "b"}}},
		"escape":         Layout{Folders: []Folder{{ID: "f1", Name: "\x1b[2J"}}},
		"bad agent":      Layout{Folders: []Folder{{ID: "f1", Name: "a", Agents: []string{"\x1b"}}}},
		"agent twice":    Layout{Folders: []Folder{{ID: "f1", Name: "a", Agents: []string{"c/a"}}, {ID: "f2", Name: "b", Agents: []string{"c/a"}}}},
		"not json":       "{",
		"too many":       Layout{Folders: make([]Folder, maxFolders+1)},
		"oversized file": strings.Repeat(" ", maxLayoutBytes+1),
	} {
		if s, ok := v.(string); ok {
			os.WriteFile(p, []byte(s), 0o600)
		} else {
			write(v)
		}
		if _, err := s.Load(); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
	// A symlink is never followed, to read or to write.
	os.Remove(p)
	target := filepath.Join(dir, "elsewhere")
	os.WriteFile(target, []byte(`{"folders":[]}`), 0o600)
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); err == nil {
		t.Error("loaded through a symlink")
	}
	if err := s.Save(want); err == nil {
		t.Error("saved over a symlink")
	}
	if b, _ := os.ReadFile(target); string(b) != `{"folders":[]}` {
		t.Errorf("the symlink's target changed: %s", b)
	}
	// A layout the deck would never write is not written either.
	if err := s.Save(Layout{Folders: []Folder{{ID: "f1", Name: "\x07"}}}); err == nil {
		t.Error("saved a bad layout")
	}
}

// A layout that cannot be read is said, and the deck starts with no folder.
func TestAnUnreadableLayoutIsSaid(t *testing.T) {
	m := newModel(&fakeAPI{}).WithLayout(&memStore{err: errors.New("deck layout refused: x")})
	if len(m.layout.Folders) != 0 || !strings.Contains(m.status, "folders not loaded") {
		t.Fatalf("status %q", m.status)
	}
}
