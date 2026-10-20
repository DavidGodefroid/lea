package deck

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestUpdateNoticeText(t *testing.T) {
	cases := []struct{ file, running, want string }{
		{"v1.3.0 installed\n", "v1.2.3", "lea v1.3.0 installed: relaunch"},
		{"v2.0.0 major\n", "v1.2.3", "lea v2.0.0 (major): lea update"},
		{"v1.3.0 available\n", "v1.2.3", "lea v1.3.0: run lea update"},
		// the deck already runs it, or a later one: no notice
		{"v1.3.0 installed\n", "v1.3.0", ""},
		{"v1.3.0 installed\n", "v1.10.0", ""},
		{"v2.0.0 major\n", "v2.0.1", ""},
		// a dev build, a missing or unexpected file: no notice
		{"v1.3.0 installed\n", "dev", ""},
		{"", "v1.2.3", ""},
		{"v1.3.0 hacked \x1b[31m\n", "v1.2.3", ""},
		{"1.3.0 installed\n", "v1.2.3", ""},
	}
	for _, c := range cases {
		if got := UpdateNotice(c.file, c.running); got != c.want {
			t.Errorf("UpdateNotice(%q, %q) = %q, want %q", c.file, c.running, got, c.want)
		}
	}
}

// The deck shows the notice on its status line, read again on each snapshot; a status (an
// error, a question) takes the line while it lasts.
func TestTheDeckShowsTheUpdateNotice(t *testing.T) {
	notice := "lea v1.3.0 installed: relaunch"
	cur := ""
	m := newModel(&fakeAPI{}).WithNotice(func() string { return cur })
	m, _ = step(t, m, tea.WindowSizeMsg{Width: Width, Height: 10})
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 7, Agents: []Agent{ag("a", "idle", 1)}}})
	if strings.Contains(sgr.ReplaceAllString(m.View(), ""), "lea v1.3.0") {
		t.Fatal("a notice with no notice file")
	}
	cur = notice
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Agents: []Agent{ag("a", "idle", 2)}}})
	if v := sgr.ReplaceAllString(m.View(), ""); !strings.Contains(v, notice) {
		t.Fatalf("no notice:\n%s", v)
	}
	m.status = "stop a? y/n"
	if v := sgr.ReplaceAllString(m.View(), ""); strings.Contains(v, notice) || !strings.Contains(v, "stop a?") {
		t.Fatalf("the status does not take the line:\n%s", v)
	}
}
