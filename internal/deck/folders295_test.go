package deck

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// #295: how folders and their agents look. A folder's header is a section header: its name in
// bold in its colour, its agent count and a compact status summary. Its agents' rows show no
// guide glyph; the indentation alone shows the belonging. One blank line separates two folders.
// A folded folder is its header alone.

// twoFolderDeck is a deck with folder web (a working, b idle) and folder ops (c idle), then d at
// the root.
func twoFolderDeck(t *testing.T) (*memStore, Model) {
	t.Helper()
	st := &memStore{l: Layout{Folders: []Folder{
		{ID: "f1", Name: "web", Agents: []string{"lea-a/a", "lea-a/b"}},
		{ID: "f2", Name: "ops", Agents: []string{"lea-a/c"}}}}}
	return st, withFolders(t, st, ag("a", "working", 9_990), ag("b", "idle", 9_990), ag("c", "idle", 9_990), ag("d", "idle", 9_990))
}

// An expanded folder: its header is one line with the name, the count and the summary; its
// agents' rows and detail lines carry no guide glyph; one blank line separates it from the next
// folder. The layout holds from the narrowest to the widest deck.
func TestExpandedFolderLook(t *testing.T) {
	for _, width := range []int{24, 34, 60} {
		_, m := twoFolderDeck(t)
		m = sized(m, width, 30)
		m.selected = "lea-a/d"
		inner := m.innerWidth()
		ls, owners := m.rowLines(inner)
		if len(ls) != 11 { // web, a(2), b(2), blank, ops, c(2), d(2)
			t.Fatalf("width %d: %d lines: %q", width, len(ls), ls)
		}
		for i, l := range ls {
			if w := lipgloss.Width(l); w != inner {
				t.Fatalf("width %d: line %d is %d cells, not %d: %q", width, i, w, inner, cells(l))
			}
			if strings.Contains(cells(l), "╎") {
				t.Fatalf("width %d: line %d shows a guide: %q", width, i, cells(l))
			}
		}
		head, sep := cells(ls[0]), cells(ls[5])
		if !strings.HasPrefix(head, " ▾ web") || !strings.Contains(head, "2 ◐1 ✓1") {
			t.Fatalf("width %d: header %q", width, head)
		}
		if strings.TrimSpace(sep) != "" || owners[5] != (lineOwner{"", -1}) {
			t.Fatalf("width %d: separator %q owner %+v", width, sep, owners[5])
		}
		if h := cells(ls[6]); !strings.HasPrefix(h, " ▾ ops") || !strings.Contains(h, "1 ✓1") {
			t.Fatalf("width %d: ops header %q", width, h)
		}
	}
}

// A folded folder is its header alone: the fold arrow, the name, the count and the summary on
// one line.
func TestFoldedFolderIsOneSummedUpLine(t *testing.T) {
	st := &memStore{l: Layout{Folders: []Folder{{ID: "f1", Name: "web", Folded: true, Agents: []string{"lea-a/a", "lea-a/b"}}}}}
	m := withFolders(t, st, ag("a", "working", 9_990), ag("b", "idle", 9_990), ag("c", "idle", 9_990))
	m.selected = "lea-a/c"
	ls := m.rows(m.innerWidth())
	if len(ls) != 3 { // web's header, c's two lines
		t.Fatalf("%d lines: %q", len(ls), ls)
	}
	if h := cells(ls[0]); !strings.HasPrefix(h, " ▸ web") || !strings.Contains(h, "2 ◐1 ✓1") {
		t.Fatalf("folded header %q", h)
	}
}

// An agent that needs the user stands out in its folder's header, folded or not: the blocked
// mark and its count in the blocked colour, at a glance among the calmer states.
func TestAFolderThatNeedsYouStandsOut(t *testing.T) {
	truecolour(t)
	st := &memStore{l: Layout{Folders: []Folder{{ID: "f1", Name: "web", Agents: []string{"lea-a/a", "lea-a/b"}}}}}
	m := withFolders(t, st, withTask(ag("a", "working", 9_990), false, true), ag("b", "idle", 9_990))
	m.selected = "lea-a/b"
	if h := m.rows(m.innerWidth())[0]; !strings.Contains(h, sgrOf(theme.Blocked)+"m●1") {
		t.Fatalf("header %q", h)
	}
	m = m.toggleFold(folderKey("f1"))
	h := m.rows(m.innerWidth())[0]
	if !strings.Contains(cells(h), "▸ web") || !strings.Contains(h, sgrOf(theme.Blocked)+"m●1") {
		t.Fatalf("folded header %q", h)
	}
}

// An approval card of an agent in a folder is indented once, with no decoration.
func TestApprovalCardInAFolderIsPlain(t *testing.T) {
	st := &memStore{l: Layout{Folders: []Folder{{ID: "f1", Name: "web", Agents: []string{"lea-a/a"}}}}}
	a := ag("a", "blocked", 9_990)
	a.Approval = &Approval{ID: 1, Tool: "Bash", Summary: "rm -rf /"}
	m := withFolders(t, st, a)
	ls := m.rows(m.innerWidth())
	if len(ls) != 5 { // the header, the row's two lines, the card's two lines
		t.Fatalf("%d lines: %q", len(ls), ls)
	}
	for i, l := range ls[3:] {
		if r := []rune(cells(l)); string(r[:6]) != "      " || r[6] == ' ' {
			t.Fatalf("card line %d not indented once: %q", i, cells(l))
		}
	}
	if !strings.Contains(cells(ls[3]), "⚠ Bash: rm -rf /") {
		t.Fatalf("card %q", cells(ls[3]))
	}
}

// ASCII mode: the same layout with no box, guide or summary glyph outside ASCII.
func TestFolderLookASCII(t *testing.T) {
	st := &memStore{l: Layout{Folders: []Folder{
		{ID: "f1", Name: "web", Agents: []string{"lea-a/a"}},
		{ID: "f2", Name: "ops", Folded: true, Agents: []string{"lea-a/b"}}}}}
	m := withFolders(t, st, ag("a", "working", 9_990), ag("b", "idle", 9_990))
	m = sized(m, 24, 30)
	m.ascii = true
	ls := m.rows(m.innerWidth())
	if len(ls) != 5 { // web, a(2), blank, ops folded
		t.Fatalf("%d lines: %q", len(ls), ls)
	}
	if h := cells(ls[0]); !strings.HasPrefix(h, " v web") || !strings.Contains(h, "1 ~1") {
		t.Fatalf("web header %q", h)
	}
	if h := cells(ls[4]); !strings.HasPrefix(h, " > ops") || !strings.Contains(h, "1 =1") {
		t.Fatalf("ops header %q", h)
	}
	for i, l := range ls {
		for _, r := range cells(l) {
			if r > 0x7e {
				t.Fatalf("line %d not ASCII: %q", i, cells(l))
			}
		}
	}
}

// A monochrome terminal (NO_COLOR: lipgloss's Ascii profile) keeps the whole layout: the states
// read from the glyphs alone and not one escape sequence is emitted.
func TestFolderLookWithoutColour(t *testing.T) {
	prof := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(3) // termenv.Ascii, the uncoloured profile
	t.Cleanup(func() { lipgloss.SetColorProfile(prof) })
	_, m := twoFolderDeck(t)
	m.selected = "lea-a/d"
	out := m.View()
	if strings.Contains(out, "\x1b[") {
		t.Fatalf("colour on a monochrome terminal:\n%q", out)
	}
	s := strings.Join(strings.Split(out, "\n"), "\n")
	for _, w := range []string{" ▾ web", "2 ◐1 ✓1", " ▾ ops", "1 ✓1"} {
		if !strings.Contains(s, w) {
			t.Fatalf("no-colour view lacks %q:\n%s", w, s)
		}
	}
}

// The mouse maps the new lines: a click on a folder's header folds it, one on the blank line
// between folders selects nothing and folds nothing, one on an agent's row inside a folder
// selects the agent.
func TestMouseHitsTheNewLines(t *testing.T) {
	st, m := twoFolderDeck(t)
	// web's header y1, a y2-3, b y4-5, the blank line y6, ops y7, c y8-9, d y10-11.
	m, _ = step(t, m, click(tea.MouseButtonLeft, 1))
	if m.selected != folderKey("f1") || !st.l.Folders[0].Folded {
		t.Fatalf("click on web's header: selected %q saved %+v", m.selected, st.l)
	}
	m, _ = step(t, m, click(tea.MouseButtonLeft, 1)) // unfold again
	sel := m.selected
	m, _ = step(t, m, click(tea.MouseButtonLeft, 6))
	if m.selected != sel || st.l.Folders[0].Folded || st.l.Folders[1].Folded {
		t.Fatalf("click on the separator: selected %q saved %+v", m.selected, st.l)
	}
	m, _ = step(t, m, click(tea.MouseButtonLeft, 2))
	if m.selected != "lea-a/a" {
		t.Fatalf("click on a: selected %q", m.selected)
	}
	m, _ = step(t, m, click(tea.MouseButtonLeft, 8))
	if m.selected != "lea-a/c" {
		t.Fatalf("click on c: selected %q", m.selected)
	}
}

// TestFolderLookExamples prints the deck as text captures for review: an expanded folder, a
// folded one, and ASCII mode at the narrowest deck width.
func TestFolderLookExamples(t *testing.T) {
	show := func(title string, m Model) {
		fmt.Printf("--- %s ---\n%s\n", title, cells(m.View()))
	}
	_, m := twoFolderDeck(t)
	m.selected = "lea-a/a"
	show("expanded folder, width 34", m)

	st := &memStore{l: Layout{Folders: []Folder{
		{ID: "f1", Name: "web", Folded: true, Agents: []string{"lea-a/a", "lea-a/b"}},
		{ID: "f2", Name: "ops", Agents: []string{"lea-a/c"}}}}}
	f := withFolders(t, st, withTask(ag("a", "working", 9_990), false, true), ag("b", "idle", 9_990), ag("c", "working", 9_990))
	f.selected = "lea-a/c"
	show("folded folder (one agent needs you), width 34", f)

	_, a := twoFolderDeck(t)
	a = sized(a, 24, 30)
	a.ascii = true
	a.selected = "lea-a/d"
	show("ASCII, width 24", a)
}
