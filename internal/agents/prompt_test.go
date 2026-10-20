package agents

import (
	"strings"
	"testing"

	"github.com/DavidGodefroid/lea/internal/store"
)

// The first prompt states the task contract the deck's colours read (#72): next while waiting,
// blocked_on when a human must act, both cleared when done.
func TestFirstPromptStatesTheTaskContract(t *testing.T) {
	p := firstPrompt(store.Agent{Name: "dev-1", Role: "dev"}, "", "", true)
	for _, want := range []string{"set next while something is still to happen", "blocked_on when a human must act", "clear both once your work is done"} {
		if !strings.Contains(p, want) {
			t.Fatalf("missing %q in %q", want, p)
		}
	}
	if strings.ContainsAny(p, "\n\r\t") {
		t.Fatalf("the prompt is not one plain line: %q", p)
	}
}
