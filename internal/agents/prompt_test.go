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

// An agent knows lea stops its containers while it is idle (#132), and starts them again itself.
func TestFirstPromptSaysIdleStacksStop(t *testing.T) {
	p := firstPrompt(store.Agent{Name: "dev-1", Role: "dev"}, "", "", true)
	if !strings.Contains(p, "lea stops the containers you started (compose stacks, pods) while you are idle") ||
		!strings.Contains(p, "start them again before you use them") || !strings.Contains(p, "through lea heavy -- <command>") {
		t.Fatalf("no idle stacks rule in %q", p)
	}
}

// An agent on a branch knows the forms a delivery grant runs without asking (#271), with its own
// branch in them; one with no branch is not told.
func TestFirstPromptNamesTheGrantForms(t *testing.T) {
	p := firstPrompt(store.Agent{Name: "dev-1", Role: "dev", Branch: "fix/9"}, "/p/lea", "/p/.lea/worktrees/lea/dev-1", true)
	for _, want := range []string{"git push -u origin fix/9", "gh pr create --head fix/9 --title T --body-file F",
		"gh pr merge <number> --squash --delete-branch", "glab mr merge <number> --squash --remove-source-branch --yes", "without -R"} {
		if !strings.Contains(p, want) {
			t.Fatalf("missing %q in %q", want, p)
		}
	}
	if strings.ContainsAny(p, "\n\r\t") {
		t.Fatalf("the prompt is not one plain line: %q", p)
	}
	if p := firstPrompt(store.Agent{Name: "dev-1", Role: "dev"}, "", "", true); strings.Contains(p, "gh pr merge") {
		t.Fatalf("no branch, yet grant forms: %q", p)
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

// A tower's first prompt names the control-tower skill; a tower's lead names the project-lead
// skill and reports to its tower; a lead's child reports to its lead (#110).
func TestFirstPromptOfTheTowersTree(t *testing.T) {
	tw := firstPrompt(store.Agent{Name: "tw", Role: "tower"}, "", "", true)
	if !strings.Contains(tw, "/lea:control-tower") || !strings.Contains(tw, "may start lead agents") {
		t.Fatalf("tower prompt %q", tw)
	}
	l := firstPrompt(store.Agent{Name: "lead-1", Role: "lead", Parent: "lea-a/tw"}, "shop", "", true)
	if !strings.Contains(l, "Your tower is tw") || !strings.Contains(l, "/lea:project-lead") || strings.Contains(l, "Your lead is") {
		t.Fatalf("lead prompt %q", l)
	}
	d := firstPrompt(store.Agent{Name: "dev-1", Role: "dev", Parent: "lea-a/lead-1"}, "shop", "", true)
	if !strings.Contains(d, "Your lead is lead-1") || strings.Contains(d, "project-lead") {
		t.Fatalf("dev prompt %q", d)
	}
}
