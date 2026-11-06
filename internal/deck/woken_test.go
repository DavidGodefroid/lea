package deck

import (
	"strings"
	"testing"
)

// An agent lea woke for a message in its inbox (#297) says so, over its own task status, until
// the woken turn ends: the user knows why it started.
func TestAWokenAgentSaysLeaWokeIt(t *testing.T) {
	for _, state := range []string{"working", "idle"} {
		a := ag("a", state, clock.Unix())
		a.Woken = true
		a.Status = "writing tests"
		s := strings.Join(viewLines(sized(loaded(t, &fakeAPI{}, "", a), 64, 8)), "\n")
		if !strings.Contains(s, "woken by lea") || strings.Contains(s, "writing tests") {
			t.Errorf("%s: want the wake in\n%s", state, s)
		}
	}
	a := ag("a", "working", clock.Unix())
	a.Status = "writing tests"
	if s := strings.Join(viewLines(sized(loaded(t, &fakeAPI{}, "", a), 64, 8)), "\n"); strings.Contains(s, "woken by lea") {
		t.Errorf("an agent lea did not wake:\n%s", s)
	}
	p := ag("a", "working", clock.Unix())
	p.Woken, p.Paused = true, true
	if s := strings.Join(viewLines(sized(loaded(t, &fakeAPI{}, "", p), 64, 8)), "\n"); !strings.Contains(s, "paused") {
		t.Errorf("a paused agent shows paused first:\n%s", s)
	}
}
