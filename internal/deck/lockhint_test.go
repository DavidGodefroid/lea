package deck

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// bodyText is the deck's rows as one line of words: the borders and the padding gone.
func bodyText(m Model) string {
	ls := viewLines(m)
	var words []string
	for _, l := range ls[1 : len(ls)-1] {
		words = append(words, strings.Fields(strings.Trim(l, "│|"))...)
	}
	return strings.Join(words, " ")
}

// TestDeckRestartHint: a lock lox up runs that this container does not mount (the daemon's
// Snapshot.LocksAdded) shows, verbatim, on the selected agent; none without it.
func TestDeckRestartHint(t *testing.T) {
	const want = "lock orders added: restart lea to use it (lea stop, then lea)"
	m := sized(loaded(t, &fakeAPI{}, "lea-a/b", ag("a", "idle", 1), ag("b", "working", 1)), 34, 20)
	if strings.Contains(bodyText(m), "added:") {
		t.Fatalf("a hint without a lock added:\n%s", strings.Join(viewLines(m), "\n"))
	}
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Shown: "lea-a/b", Agents: []Agent{ag("a", "idle", 1), ag("b", "working", 1)},
		LocksAdded: []string{"orders"}}})
	if got := bodyText(m); !strings.Contains(got, want) || strings.Count(got, "added:") != 1 {
		t.Fatalf("hint not shown once, verbatim:\n%s", strings.Join(viewLines(m), "\n"))
	}
	for i, l := range viewLines(m) {
		if w := lipgloss.Width(l); w != 34 {
			t.Fatalf("line %d is %d wide: %q", i, w, l)
		}
	}
	// A name the daemon should never send is not shown.
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 9, Shown: "lea-a/b", Agents: []Agent{ag("b", "working", 1)},
		LocksAdded: []string{"Bad\x1b[2Jname"}}})
	if strings.Contains(bodyText(m), "added:") {
		t.Fatalf("a bad name shown:\n%s", strings.Join(viewLines(m), "\n"))
	}
}
