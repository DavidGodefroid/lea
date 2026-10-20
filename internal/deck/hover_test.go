package deck

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func hover(y int) tea.MouseMsg {
	return tea.MouseMsg{X: 5, Y: y, Button: tea.MouseButtonNone, Action: tea.MouseActionMotion}
}

func bottomLine(m Model) string { ls := viewLines(m); return ls[len(ls)-1] }

// A long label is cut in its row; the pointer over the row shows it whole, with the agent's
// name, role, profile and state, in the bottom border (#29).
func TestHoverShowsTheFullName(t *testing.T) {
	long := ag("dev-7", "working", 1)
	long.Label, long.Role = "refactor-billing-export", "lead"
	m := sized(loaded(t, &fakeAPI{}, "lea-a/a", ag("a", "idle", 1), long), 34, 12)
	if s := strings.Join(viewLines(m), "\n"); strings.Contains(s, long.Label) {
		t.Fatalf("the row already shows the whole label:\n%s", s)
	}
	m, cmd := step(t, m, hover(4)) // the long row's second line
	if m.hoverFor != "lea-a/dev-7" || cmd == nil {
		t.Fatalf("hover %q", m.hoverFor)
	}
	if b := bottomLine(m); !strings.Contains(b, "refactor-billing-export · ") || len([]rune(b)) != 34 {
		t.Fatalf("bottom %q", b)
	}
	if got := hoverText(long); got != "refactor-billing-export · dev-7 · lead · claude · working" {
		t.Fatalf("hover text %q", got)
	}
	// Elsewhere (below the rows, the frame) the keys come back.
	m, _ = step(t, m, hover(9))
	if m.hoverFor != "" || !strings.Contains(bottomLine(m), "? keys") {
		t.Fatalf("hover %q bottom %q", m.hoverFor, bottomLine(m))
	}
}

// The pointer may leave the pane without a word to the deck: the hover fades after a while.
func TestHoverFades(t *testing.T) {
	m := threeRows(t, &fakeAPI{})
	m, _ = step(t, m, hover(1))
	stale := m.hoverSeq
	m, _ = step(t, m, hover(3))
	m, _ = step(t, m, hoverEndMsg{stale})
	if m.hoverFor != "lea-a/b" {
		t.Fatalf("an older fade cleared the hover: %q", m.hoverFor)
	}
	m, _ = step(t, m, hoverEndMsg{m.hoverSeq})
	if m.hoverFor != "" {
		t.Fatalf("hover %q", m.hoverFor)
	}
}

// The bottom line keeps its hints while a menu, a field, the key list or the modal is open, and a
// drag is no hover.
func TestHoverGivesWayToTheOpenThings(t *testing.T) {
	m := threeRows(t, &fakeAPI{})
	m, _ = step(t, m, click(tea.MouseButtonRight, 1))
	m, _ = step(t, m, hover(9))
	if b := bottomLine(m); !strings.Contains(b, "esc close") {
		t.Fatalf("menu open: %q", b)
	}
	m, _ = step(t, m, key("esc"))
	m, _ = step(t, m, key("e"))
	m, _ = step(t, m, hover(3))
	if b := bottomLine(m); !strings.Contains(b, "enter save") {
		t.Fatalf("rename open: %q", b)
	}
	m, _ = step(t, m, key("esc"))
	m, _ = step(t, m, tea.MouseMsg{Y: 5, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion})
	if m.hoverFor != "" {
		t.Fatalf("a drag hovers %q", m.hoverFor)
	}
}

// The name and the label are the agents' own text: what could drive the terminal (escapes,
// controls, bidi overrides) is drawn as '?', never sent.
func TestHoverTextIsSafe(t *testing.T) {
	bad := ag("x", "idle", 1)
	bad.Label = "a\x1b]52;c;cHduZWQ=\x07b‮c"
	bad.Role = "dev\x1b[2J"
	m := sized(loaded(t, &fakeAPI{}, "lea-a/x", bad), 34, 12)
	m, _ = step(t, m, hover(1))
	v := m.View()
	for _, w := range []string{"\x1b]", "\x07", "‮", "\x1b[2J"} {
		if strings.Contains(v, w) {
			t.Fatalf("view holds %q", w)
		}
	}
	if b := bottomLine(m); !strings.Contains(b, "a?]52;c;cHduZWQ=?b?c") {
		t.Fatalf("bottom %q", b)
	}
}
