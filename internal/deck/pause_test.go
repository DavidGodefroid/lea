package deck

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func (f *fakeAPI) SetPaused(ctx context.Context, k string, paused bool) error {
	f.rec(fmt.Sprintf("paused %s %v", k, paused))
	return f.err
}

func paused(a Agent) Agent { a.Paused = true; return a }

// p pauses only once y confirms it: text meant for the previewed agent and typed on the deck must
// not freeze it (#240). A paused agent resumes at once: resuming freezes nothing.
func TestPPausesTheSelectedAgentAfterYAndResumesAPausedOne(t *testing.T) {
	api := &fakeAPI{}
	m := loaded(t, api, "lea-a/a", ag("a", "working", 1), paused(ag("b", "working", 1)))
	m, cmd := step(t, m, key("p"))
	if cmd != nil {
		t.Fatal("p pauses at once, with no confirmation")
	}
	if !strings.Contains(m.status, "pause a?") {
		t.Fatalf("status %q", m.status)
	}
	m, cmd = step(t, m, key("y"))
	if cmd == nil {
		t.Fatal("y does not pause")
	}
	cmd()
	m, _ = step(t, m, key("j"))
	_, cmd = step(t, m, key("p"))
	if cmd == nil {
		t.Fatal("p on a paused agent does nothing")
	}
	cmd()
	if got := api.got(); len(got) != 2 || got[0] != "paused lea-a/a true" || got[1] != "paused lea-a/b false" {
		t.Fatalf("calls=%q", got)
	}
}

// Typing a word with a p on the deck pauses nothing: any key but y cancels the question, and does
// nothing else.
func TestAStrayPPausesNothing(t *testing.T) {
	api := &fakeAPI{}
	m := loaded(t, api, "lea-a/a", ag("a", "working", 1), ag("b", "working", 1))
	for _, k := range []string{"p", "l", "e", "a", "s", "e"} {
		var cmd tea.Cmd
		if m, cmd = step(t, m, key(k)); cmd != nil && k == "l" {
			t.Fatalf("%s after p runs a command", k)
		}
		if cmd != nil && k == "p" {
			cmd()
		}
		if k == "l" && m.status != "" {
			t.Fatalf("the question stays: %q", m.status)
		}
	}
	for _, c := range api.got() {
		if strings.HasPrefix(c, "paused") {
			t.Fatalf("calls=%q", api.got())
		}
	}
}

// enter on a paused agent resumes it before focusing it, and says so: never a frozen pane that
// swallows what the user types (#240).
func TestEnterOnAPausedAgentResumesItAndSaysSo(t *testing.T) {
	api := &fakeAPI{}
	m := loaded(t, api, "lea-a/a", paused(ag("a", "working", 1)))
	m, cmd := step(t, m, key("enter"))
	if cmd == nil {
		t.Fatal("enter does nothing")
	}
	cmd()
	if got := api.got(); len(got) != 2 || got[0] != "paused lea-a/a false" || got[1] != "show lea-a/a true" {
		t.Fatalf("calls=%q", got)
	}
	if !strings.Contains(m.status, "resumed a") {
		t.Fatalf("status %q", m.status)
	}
}

// A paused agent in the slot says so on the deck, and how to resume it.
func TestAPreviewedPausedAgentSaysHowToResume(t *testing.T) {
	api := &fakeAPI{}
	m := loaded(t, api, "lea-a/a", ag("a", "working", 1), paused(ag("b", "working", 1)))
	m, _ = step(t, m, key("j"))
	m, _ = step(t, m, showMsg{m.seq})
	if !strings.Contains(m.status, "b is paused: p resumes it") {
		t.Fatalf("status %q", m.status)
	}
	m, _ = step(t, m, key("k"))
	if m.status != "" {
		t.Fatalf("the hint stays on another row: %q", m.status)
	}
}

// A shell is no agent, and an ended or stopped agent has no process to freeze.
func TestPIgnoresShellsAndEndedAgents(t *testing.T) {
	for _, a := range []Agent{shell("sh-1"), ag("a", "done", 1), ag("a", "stopped", 1)} {
		api := &fakeAPI{}
		m := loaded(t, api, a.Key, a)
		if _, cmd := step(t, m, key("p")); cmd != nil {
			cmd()
		}
		if got := api.got(); len(got) != 0 {
			t.Fatalf("%s %s: calls=%q", a.Name, a.State, got)
		}
	}
}

func TestTheMenuOffersPauseOrResume(t *testing.T) {
	has := func(a Agent, label string) bool {
		for _, it := range menuItems(a, false) {
			if it.label == label && it.key == "p" {
				return true
			}
		}
		return false
	}
	if a := ag("a", "working", 1); !has(a, "Pause") || has(a, "Resume") {
		t.Fatalf("running agent menu: %+v", menuItems(a, false))
	}
	if a := paused(ag("a", "working", 1)); !has(a, "Resume") || has(a, "Pause") {
		t.Fatalf("paused agent menu: %+v", menuItems(a, false))
	}
	for _, a := range []Agent{shell("sh-1"), ag("a", "done", 1), ag("a", "stopped", 1)} {
		if has(a, "Pause") || has(a, "Resume") {
			t.Fatalf("%s %s menu: %+v", a.Name, a.State, menuItems(a, false))
		}
	}
}

func TestTheMenusPauseItemPausesTheRow(t *testing.T) {
	api := &fakeAPI{}
	m := threeRows(t, api)
	m.openMenu("lea-a/b")
	_, cmd := step(t, m, key("p"))
	if cmd == nil {
		t.Fatal("no call")
	}
	cmd()
	if got := api.got(); len(got) != 1 || got[0] != "paused lea-a/b true" {
		t.Fatalf("calls=%q", got)
	}
}

// A paused agent shows paused, with its own mark, and does not spin.
func TestAPausedAgentShowsPaused(t *testing.T) {
	m := sized(loaded(t, &fakeAPI{}, "lea-a/a", paused(ag("a", "working", 1))), 34, 10)
	if l := m.label(m.agents[0]); l != "paused" {
		t.Fatalf("label %q", l)
	}
	if m.working() {
		t.Fatal("a paused agent counts as working: the spinner runs")
	}
	if m.agents[0].Status = "writing tests"; m.shortStatus(m.agents[0]) != "paused" {
		t.Fatalf("short status %q", m.shortStatus(m.agents[0]))
	}
	s := strings.Join(viewLines(m), "\n")
	if !strings.Contains(s, "paused") || !strings.Contains(s, "‖") {
		t.Fatalf("view:\n%s", s)
	}
	if !strings.Contains(strings.Join(helpLines, "\n"), "pause (y)") {
		t.Fatal("the key list lacks p")
	}
}
