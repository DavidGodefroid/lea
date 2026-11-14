package deck

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// The top border leads with the account's usage (#421): with the 5-hour reset time and the
// count when the column is wide enough, shorter forms when it is not; amber at or above
// budget_5h_pct; nothing while the usage is unknown.
func TestTopBorderShowsTheUsage(t *testing.T) {
	reset := time.Date(2026, 11, 14, 14, 5, 0, 0, time.Local).Unix()
	m := sized(loaded(t, &fakeAPI{}, "lea-a/a", ag("a", "idle", 1)), 34, 8)
	if top := viewLines(m)[0]; strings.Contains(top, "5h") || !strings.HasSuffix(top, " 1 agent ─╮") {
		t.Fatalf("unknown usage: %q", top)
	}
	m.usage = &Usage{FiveHour: &UsageWindow{Pct: 42, ResetsAt: reset}, SevenDay: &UsageWindow{Pct: 18}, Budget: 60}
	if top := viewLines(m)[0]; !strings.HasSuffix(top, " 5h 42% · 7d 18% ─╮") || lipgloss.Width(top) != 34 {
		t.Fatalf("narrow: %q", top)
	}
	if st := m.usageStyle(); st.GetForeground() != muted.GetForeground() {
		t.Fatal("below the budget: not muted")
	}
	wide := sized(m, 60, 8)
	if top := viewLines(wide)[0]; !strings.HasSuffix(top, " 5h 42% (14:05) · 7d 18% · 1 agent ─╮") || lipgloss.Width(top) != 60 {
		t.Fatalf("wide: %q", top)
	}
	m.usage.Over, m.usage.FiveHour.Pct = true, 64
	if st := m.usageStyle(); st.GetForeground() != lipgloss.NewStyle().Foreground(theme.Working).GetForeground() {
		t.Fatal("above the budget: not amber")
	}
	tiny := sized(m, 24, 8)
	if top := viewLines(tiny)[0]; !strings.HasSuffix(top, " 5h 64% ─╮") || lipgloss.Width(top) != 24 {
		t.Fatalf("tiny: %q", top)
	}
	// A snapshot without usage clears it.
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 9, Agents: []Agent{ag("a", "idle", 1)}}})
	if m.usage != nil || strings.Contains(viewLines(m)[0], "5h") {
		t.Fatalf("usage kept: %+v", m.usage)
	}
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 10, Agents: []Agent{ag("a", "idle", 1)}, Usage: &Usage{FiveHour: &UsageWindow{Pct: 70}}}})
	if !strings.Contains(viewLines(m)[0], "5h 70%") {
		t.Fatalf("snapshot usage not shown: %q", viewLines(m)[0])
	}
}
