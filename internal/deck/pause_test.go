package deck

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func (f *fakeAPI) SetPaused(ctx context.Context, k string, paused bool) error {
	f.rec(fmt.Sprintf("paused %s %v", k, paused))
	return f.err
}

func paused(a Agent) Agent { a.Paused = true; return a }

func TestPPausesTheSelectedAgentAndResumesAPausedOne(t *testing.T) {
	api := &fakeAPI{}
	m := loaded(t, api, "lea-a/a", ag("a", "working", 1), paused(ag("b", "working", 1)))
	m, cmd := step(t, m, key("p"))
	if cmd == nil {
		t.Fatal("p on a running agent does nothing")
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
		for _, it := range menuItems(a) {
			if it.label == label && it.key == "p" {
				return true
			}
		}
		return false
	}
	if a := ag("a", "working", 1); !has(a, "Pause") || has(a, "Resume") {
		t.Fatalf("running agent menu: %+v", menuItems(a))
	}
	if a := paused(ag("a", "working", 1)); !has(a, "Resume") || has(a, "Pause") {
		t.Fatalf("paused agent menu: %+v", menuItems(a))
	}
	for _, a := range []Agent{shell("sh-1"), ag("a", "done", 1), ag("a", "stopped", 1)} {
		if has(a, "Pause") || has(a, "Resume") {
			t.Fatalf("%s %s menu: %+v", a.Name, a.State, menuItems(a))
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
	if !strings.Contains(strings.Join(helpLines, "\n"), "pause or resume") {
		t.Fatal("the key list lacks p")
	}
}
