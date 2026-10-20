package deck

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/charmbracelet/lipgloss"
)

func asking(name string, id int64) Agent {
	a := ag(name, "idle", 1)
	a.LockAsk = &LockAsk{ID: id, Tool: "locksql", Profile: "prod"}
	return a
}

// readable is m a lockAskDelay after the snapshot that showed its cards.
func readable(m Model) Model {
	later := clock.Add(lockAskDelay)
	m.now = func() time.Time { return later }
	return m
}

// O opens the console the agent asked for (through the daemon, as L would), d declines. The
// answer goes to the request's id taken when the key was pressed.
func TestLockAskKeys(t *testing.T) {
	for k, want := range map[string]string{"O": "lock-decide lea-a 5 true", "d": "lock-decide lea-a 5 false"} {
		api := &fakeAPI{}
		m := readable(loaded(t, api, "lea-a/b", asking("b", 5), ag("c", "idle", 1)))
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
	// a and A answer approvals only: never a lock request.
	api := &fakeAPI{}
	m := readable(loaded(t, api, "lea-a/b", asking("b", 5)))
	for _, k := range []string{"a", "A"} {
		var cmd tea.Cmd
		if m, cmd = step(t, m, key(k)); cmd != nil {
			t.Fatalf("%s answered a lock request", k)
		}
	}
	if !strings.Contains(m.status, "O opens the locksql/prod console") {
		t.Fatalf("status %q", m.status)
	}
	// On a row without a request, O and d do nothing.
	m = readable(loaded(t, api, "lea-a/c", asking("b", 5), ag("c", "idle", 1)))
	for _, k := range []string{"O", "d"} {
		if _, cmd := step(t, m, key(k)); cmd != nil {
			t.Fatalf("%s on a row without a card made a call", k)
		}
	}
	if len(api.got()) != 0 {
		t.Fatalf("calls=%q", api.got())
	}
}

// The review's case: a lock request waits behind an approval; a pressed twice allows the
// approval, then (the approval gone, the lock card in view) opens nothing. O right after the card
// came into view does nothing either; a second later it opens.
func TestALockCardIsReadBeforeItOpens(t *testing.T) {
	api := &fakeAPI{}
	both := carded("b", 7, false)
	both.LockAsk = &LockAsk{ID: 5, Tool: "locksql", Profile: "prod"}
	m := loaded(t, api, "lea-a/b", both)
	m, cmd := step(t, m, key("a"))
	cmd()
	alone := asking("b", 5)
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Shown: "lea-a/b", Agents: []Agent{alone}}})
	if _, cmd = step(t, m, key("a")); cmd != nil {
		t.Fatal("a second a opened the console")
	}
	m, cmd = step(t, m, key("O"))
	if cmd != nil || !strings.Contains(m.status, "read the locksql/prod request first") {
		t.Fatalf("O at once: call=%v status=%q", cmd != nil, m.status)
	}
	// The delay counts from when the card came into view, not from the request.
	at := clock.Add(lockAskDelay - time.Millisecond)
	m.now = func() time.Time { return at }
	if _, cmd = step(t, m, key("O")); cmd != nil {
		t.Fatal("O before the delay")
	}
	m = readable(m)
	if _, cmd = step(t, m, key("O")); cmd == nil {
		t.Fatal("O after the delay made no call")
	}
	cmd()
	if got := api.got(); len(got) != 2 || got[0] != "decide 7 allow" || got[1] != "lock-decide lea-a 5 true" {
		t.Fatalf("calls=%q", got)
	}
	// A card hidden again (an approval in front of it) starts its delay again when it shows.
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 9, Shown: "lea-a/b", Agents: []Agent{both}}})
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 10, Shown: "lea-a/b", Agents: []Agent{alone}}})
	if _, cmd = step(t, m, key("O")); cmd != nil {
		t.Fatal("O right after the card showed again")
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
	if !strings.Contains(s, "⚠ open the locksql/prod console?") || !strings.Contains(s, "[O]pen [d]ecline") {
		t.Fatalf("card:\n%s", s)
	}
	if !strings.Contains(s, "asks for a console") || !strings.HasPrefix(ls[len(ls)-1], "╰─ O open console · d decline") {
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
