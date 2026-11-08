package deck

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// #355: in a folder header's summary each state mark is set apart from its count by a space.
// The marks are East Asian Ambiguous width: a terminal that draws them wide spills them onto a
// digit glued to them.

// glued matches a state mark, Unicode or ASCII, touching a digit.
var glued = regexp.MustCompile(`[●◐◷✓!+~=][0-9]`)

// Six working agents in a folder: the header reads "6 ◐ 6", the moon never touches the six.
func TestFolderSummaryMarkStandsApartFromItsCount(t *testing.T) {
	keys := []string{"lea-a/a", "lea-a/b", "lea-a/c", "lea-a/d", "lea-a/e", "lea-a/f"}
	st := &memStore{l: Layout{Folders: []Folder{{ID: "f1", Name: "web", Agents: keys}}}}
	var as []Agent
	for _, k := range []string{"a", "b", "c", "d", "e", "f"} {
		as = append(as, ag(k, "working", 9_990))
	}
	m := withFolders(t, st, append(as, ag("z", "idle", 9_990))...)
	m.selected = "lea-a/z"
	for _, ascii := range []bool{false, true} {
		m.ascii = ascii
		want := "6 ◐ 6"
		if ascii {
			want = "6 + 6"
		}
		if h := cells(m.rows(m.innerWidth())[0]); !strings.Contains(h, want) || glued.MatchString(h) {
			t.Fatalf("ascii %v: header %q, want %q", ascii, h, want)
		}
	}
}

// Every state's mark, Unicode and ASCII, at every deck width: never glued to its count, the
// header exactly inner cells wide, the total always there, and narrowing drops the calmest
// state first: a state shows only when every worse one shows.
func TestFolderSummaryEveryMarkSpacedAtEveryWidth(t *testing.T) {
	marks := map[bool][]string{false: {"● 1", "◐ 1", "◷ 1", "✓ 1"}, true: {"! 1", "+ 1", "~ 1", "= 1"}}
	for _, ascii := range []bool{false, true} {
		for width := 24; width <= 60; width++ {
			st := &memStore{l: Layout{Folders: []Folder{{ID: "f1", Name: "web",
				Agents: []string{"lea-a/a", "lea-a/b", "lea-a/c", "lea-a/d"}}}}}
			m := withFolders(t, st, withTask(ag("a", "working", 9_990), false, true), ag("b", "working", 9_990),
				limited("c", clock.Unix()+60), ag("d", "idle", 9_990), ag("e", "idle", 9_990))
			m = sized(m, width, 40)
			m.ascii = ascii
			inner := m.innerWidth()
			l := m.rows(inner)[0]
			if w := lipgloss.Width(l); w != inner {
				t.Fatalf("ascii %v width %d: header is %d cells, not %d: %q", ascii, width, w, inner, cells(l))
			}
			h := cells(l)
			if glued.MatchString(h) {
				t.Fatalf("ascii %v width %d: a mark touches its count: %q", ascii, width, h)
			}
			if !strings.Contains(h, "4 "+marks[ascii][0]) {
				t.Fatalf("ascii %v width %d: header %q lacks the total and %q", ascii, width, h, marks[ascii][0])
			}
			shown := true
			for _, mk := range marks[ascii] {
				has := strings.Contains(h, mk)
				if has && !shown {
					t.Fatalf("ascii %v width %d: %q shows while a worse state was dropped: %q", ascii, width, mk, h)
				}
				shown = has
			}
			if width == 60 && !shown {
				t.Fatalf("ascii %v width 60: header %q drops a state", ascii, h)
			}
		}
	}
}
