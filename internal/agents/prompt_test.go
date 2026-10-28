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

// An agent knows its own label and the rule for names (#84): labels in text the user reads,
// names in tool calls.
func TestFirstPromptNamesTheLabelAndTheRule(t *testing.T) {
	p := firstPrompt(store.Agent{Name: "dev-1", Role: "dev", Label: "fix names"}, "", "", true)
	for _, want := range []string{`You are the lea agent dev-1 (role dev), shown to the user as "fix names".`,
		"use its label from agents_list", "address tools by name"} {
		if !strings.Contains(p, want) {
			t.Fatalf("missing %q in %q", want, p)
		}
	}
	if p := firstPrompt(store.Agent{Name: "dev-1", Role: "dev"}, "", "", true); strings.Contains(p, "shown to the user as") ||
		!strings.Contains(p, "use its label from agents_list") {
		t.Fatalf("unlabelled prompt %q", p)
	}
}
