package deck

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// cells is s without its colours.
func cells(s string) string { return sgr.ReplaceAllString(s, "") }

// An agent in a folder is shifted right under its folder's row, its detail line too; a root agent
// is not. Every row keeps the deck's width, in ASCII and at narrow widths alike: the name is
// truncated, down to nothing at the narrowest deck.
func TestAgentInAFolderIsIndented(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ascii bool
		width int
	}{{"unicode", false, 34}, {"ascii", true, 34}, {"narrow", false, 24}, {"narrow-ascii", true, 24}, {"narrowest", false, 20}} {
		t.Run(tc.name, func(t *testing.T) {
			st := &memStore{l: Layout{Folders: []Folder{{ID: "f1", Name: "web", Agents: []string{"lea-a/alpha-agent-with-a-long-name"}}}}}
			m := withFolders(t, st, ag("alpha-agent-with-a-long-name", "idle", 1), ag("bravo", "idle", 1))
			m = sized(m, tc.width, 30)
			m.ascii = tc.ascii
			m.selected = "lea-a/bravo"
			inner := m.innerWidth()
			ls := m.rows(inner)
			for i, l := range ls {
				if w := lipgloss.Width(l); w != inner {
					t.Fatalf("row %d is %d cells, not %d: %q", i, w, inner, cells(l))
				}
			}
			guide := "╎"
			if tc.ascii {
				guide = ":"
			}
			in1, in2, root1, root2 := []rune(cells(ls[1])), []rune(cells(ls[2])), []rune(cells(ls[3])), []rune(cells(ls[4]))
			// The folder's agent: guide, then its glyph under the folder's arrow and its name under
			// the folder's name; its detail starts there too.
			if string(in1[:4]) != " "+guide+"  " || in1[4] == ' ' || in1[5] != ' ' || (tc.width > 20 && !strings.HasPrefix(string(in1[6:]), "al")) {
				t.Fatalf("folder agent not indented: %q", string(in1))
			}
			if string(in2[:6]) != " "+guide+"    " || in2[6] == ' ' {
				t.Fatalf("folder agent's detail not indented: %q", string(in2))
			}
			if root1[2] == ' ' || !strings.HasPrefix(string(root1[4:]), "b") {
				t.Fatalf("root agent indented: %q", string(root1))
			}
			if string(root2[:4]) != "    " || root2[4] == ' ' {
				t.Fatalf("root agent's detail indented: %q", string(root2))
			}
		})
	}
}
