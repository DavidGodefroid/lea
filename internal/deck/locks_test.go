package deck

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func asking(name string, id int64) Agent {
	a := ag(name, "idle", 1)
	a.LockAsk = &LockAsk{ID: id, Tool: "locksql", Profile: "prod"}
	return a
}

// a opens the console the agent asked for (through the daemon, as L would), d declines; A does
// nothing. The answer goes to the request's id taken when the key was pressed.
func TestLockAskKeys(t *testing.T) {
	for k, want := range map[string]string{"a": "lock-decide lea-a 5 true", "d": "lock-decide lea-a 5 false"} {
		api := &fakeAPI{}
		m := loaded(t, api, "lea-a/b", asking("b", 5), ag("c", "idle", 1))
		m, cmd := step(t, m, key(k))
		if cmd == nil {
			t.Fatalf("%s: no call", k)
		}
		m.agents[0].LockAsk = &LockAsk{ID: 6, Tool: "locksql", Profile: "staging"}
		m, _ = step(t, m, cmd())
		if got := api.got(); len(got) != 1 || got[0] != want {
			t.Fatalf("%s: calls=%q", k, got)
		}
		if k == "d" && m.status != "lock request declined" {
			t.Fatalf("status %q", m.status)
		}
	}
	api := &fakeAPI{}
	m := loaded(t, api, "lea-a/b", asking("b", 5))
	if _, cmd := step(t, m, key("A")); cmd != nil {
		t.Fatal("A answered a lock request")
	}
	// On a row without a request, a and d do nothing.
	m = loaded(t, api, "lea-a/c", asking("b", 5), ag("c", "idle", 1))
	for _, k := range []string{"a", "d"} {
		if _, cmd := step(t, m, key(k)); cmd != nil {
			t.Fatalf("%s on a row without a card made a call", k)
		}
	}
}

// An approval card is answered first: a lock request waits behind it.
func TestApprovalComesBeforeALockAsk(t *testing.T) {
	api := &fakeAPI{}
	a := carded("b", 7, false)
	a.LockAsk = &LockAsk{ID: 5, Tool: "locksql", Profile: "prod"}
	m := loaded(t, api, "lea-a/b", a)
	_, cmd := step(t, m, key("a"))
	cmd()
	if got := api.got(); len(got) != 1 || got[0] != "decide 7 allow" {
		t.Fatalf("calls=%q", got)
	}
}

func TestViewShowsTheLockAskCard(t *testing.T) {
	m := sized(loaded(t, &fakeAPI{}, "lea-a/b", asking("b", 5), ag("c", "idle", 1)), 40, 10)
	ls := viewLines(m)
	s := strings.Join(ls, "\n")
	if !strings.Contains(s, "⚠ open the locksql/prod console?") || !strings.Contains(s, "[a] open [d]ecline") {
		t.Fatalf("card:\n%s", s)
	}
	if !strings.Contains(s, "asks for a console") || !strings.HasPrefix(ls[len(ls)-1], "╰─ a open console · d decline") {
		t.Fatalf("label or hint:\n%s", s)
	}
	for i, l := range ls {
		if lipgloss.Width(l) != 40 {
			t.Fatalf("line %d is %d wide: %q", i, lipgloss.Width(l), l)
		}
	}
	// Whatever a snapshot carries, no control character reaches the terminal.
	a := asking("b", 5)
	a.LockAsk.Tool, a.LockAsk.Profile = "lock\x1b[2Jsql", "pr\x07od‮"
	raw := sized(loaded(t, &fakeAPI{}, "lea-a/b", a), 40, 8).View()
	if strings.Contains(raw, "\x07") || strings.Contains(raw, "‮") || strings.Contains(sgr.ReplaceAllString(raw, ""), "\x1b") {
		t.Fatalf("control characters reach the terminal: %q", raw)
	}
}

// space finds an agent that asks for a console: it needs the user.
func TestSpaceFindsALockAsk(t *testing.T) {
	m := loaded(t, &fakeAPI{}, "lea-a/a", ag("a", "idle", 1), asking("b", 5))
	m, _ = step(t, m, key(" "))
	if m.selected != "lea-a/b" {
		t.Fatalf("selected %q", m.selected)
	}
}
