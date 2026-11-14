package agents

import (
	"strings"
	"testing"

	"github.com/DavidGodefroid/lea/internal/grants"
	"github.com/DavidGodefroid/lea/internal/store"
)

// The first prompt states the task contract the deck's colours read (#72): next while waiting,
// blocked_on when a human must act, both cleared when done.
func TestFirstPromptStatesTheTaskContract(t *testing.T) {
	p := firstPrompt(store.Agent{Name: "dev-1", Role: "dev"}, "", "", true, nil)
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
	p := firstPrompt(store.Agent{Name: "dev-1", Role: "dev"}, "", "", true, nil)
	if !strings.Contains(p, "lea stops the containers you started (compose stacks, pods) while you are idle") ||
		!strings.Contains(p, "start them again before you use them") || !strings.Contains(p, "through lea heavy -- <command>") {
		t.Fatalf("no idle stacks rule in %q", p)
	}
}

// An agent on a branch that lea allow deliver covers knows the forms lea runs without asking
// (#271), with its own branch in them; one covered for some only is told the lea allow command
// for the rest, one covered for none only that (#319); one with no branch is not told.
func TestFirstPromptNamesTheGrantForms(t *testing.T) {
	dev := store.Agent{Name: "dev-1", Role: "dev", Branch: "fix/9", Repo: "lea"}
	p := firstPrompt(dev, "/p/lea", "/p/.lea/worktrees/lea/dev-1", true, []string{"push", "pr", "merge"})
	for _, want := range []string{"git push -u origin fix/9", "gh pr create --head fix/9 --title T --body-file F",
		"gh pr merge <number> --squash --delete-branch", "glab mr merge <number> --squash --remove-source-branch --yes", "without -R"} {
		if !strings.Contains(p, want) {
			t.Fatalf("missing %q in %q", want, p)
		}
	}
	if strings.ContainsAny(p, "\n\r\t") {
		t.Fatalf("the prompt is not one plain line: %q", p)
	}
	if strings.Contains(p, "lea allow deliver") {
		t.Fatalf("covered, yet told to ask: %q", p)
	}
	if p := firstPrompt(store.Agent{Name: "dev-1", Role: "dev"}, "", "", true, nil); strings.Contains(p, "gh pr merge") || strings.Contains(p, "lea allow") {
		t.Fatalf("no branch, yet grant forms: %q", p)
	}
	p = firstPrompt(dev, "/p/lea", "/p/.lea/worktrees/lea/dev-1", true, nil)
	if strings.Contains(p, "git push") || strings.Contains(p, "gh pr") ||
		!strings.Contains(p, "Pushing, opening or merging a PR asks the user; tell them lea allow deliver --repo lea lets you do it without asking.") {
		t.Fatalf("not covered: %q", p)
	}
	p = firstPrompt(dev, "/p/lea", "/p/.lea/worktrees/lea/dev-1", true, []string{"push"})
	if !strings.Contains(p, "git push -u origin fix/9") || strings.Contains(p, "gh pr merge") || !strings.Contains(p, "lea allow deliver --repo lea lets you") {
		t.Fatalf("push only: %q", p)
	}
}

// TestCovered: what of the deliver preset the grants of the agent's lea cover as it starts.
func TestCovered(t *testing.T) {
	a := store.Agent{Key: "lea-a/dev", Container: "lea-a", Repo: "lea", Branch: "fix/9"}
	g := func(action, repo, agent string) grants.Grant {
		return grants.Grant{Container: "lea-a", Action: action, Repo: repo, Agent: agent, Pin: "github.com/o/lea", Expires: grants.Standing}
	}
	gs := []grants.Grant{g(grants.CommitIdentity, "lea", ""), g(grants.Merge, "lea", "lea-a/lead"), g(grants.PRCreate, "other", "")}
	if c := Covered(a, []string{a.Key}, gs, 1); strings.Join(c, " ") != "push" {
		t.Fatalf("no lead: %v", c)
	}
	if c := Covered(a, []string{a.Key, "lea-a/lead"}, gs, 1); strings.Join(c, " ") != "push merge" {
		t.Fatalf("under the lead: %v", c)
	}
	b := a
	b.Container = "lea-b"
	if c := Covered(b, []string{b.Key}, gs, 1); len(c) != 0 {
		t.Fatalf("another lea: %v", c)
	}
}

// An agent creates its worktree branch with no upstream (#381): a branch made from origin/main
// would otherwise track main.
func TestFirstPromptCreatesTheWorktreeWithNoTrack(t *testing.T) {
	p := firstPrompt(store.Agent{Name: "dev-1", Role: "dev", Branch: "fix/9"}, "/p/lea", "/p/.lea/worktrees/lea/dev-1", true, nil)
	if !strings.Contains(p, "git worktree add --no-track -b fix/9 /p/.lea/worktrees/lea/dev-1 <base>") {
		t.Fatalf("no --no-track worktree form in %q", p)
	}
}

// An agent knows its own label and the rule for names (#84): labels in text the user reads,
// names in tool calls.
func TestFirstPromptNamesTheLabelAndTheRule(t *testing.T) {
	p := firstPrompt(store.Agent{Name: "dev-1", Role: "dev", Label: "fix names"}, "", "", true, nil)
	for _, want := range []string{`You are the lea agent dev-1 (role dev), shown to the user as "fix names".`,
		"use its label from agents_list", "address tools by name"} {
		if !strings.Contains(p, want) {
			t.Fatalf("missing %q in %q", want, p)
		}
	}
	if p := firstPrompt(store.Agent{Name: "dev-1", Role: "dev"}, "", "", true, nil); strings.Contains(p, "shown to the user as") ||
		!strings.Contains(p, "use its label from agents_list") {
		t.Fatalf("unlabelled prompt %q", p)
	}
}

// An agent recognises an egress refusal and tells the user the host command, never allowing a
// domain itself (#345).
func TestFirstPromptExplainsEgressRefusals(t *testing.T) {
	p := firstPrompt(store.Agent{Name: "dev-1", Role: "dev"}, "", "", true, nil)
	for _, want := range []string{"CONNECT tunnel failed, response 403", "X-Squid-Error: ERR_LEA_EGRESS",
		"run on the host: lea egress allow HOST (applies live, no restart; there is no one-off form: it stays until lea egress deny HOST)", "you cannot allow it yourself", "Do not retry, switch mirrors or work around the proxy"} {
		if !strings.Contains(p, want) {
			t.Fatalf("missing %q in %q", want, p)
		}
	}
	if strings.ContainsAny(p, "$`") {
		t.Fatalf("the prompt holds a shell expansion: %q", p)
	}
}

// An agent whose Lock client finds no console asks lea (lock_hint) what the user must do, and
// never suggests lox up or a command that restarts a lock (#343).
func TestFirstPromptSendsToLockHint(t *testing.T) {
	p := firstPrompt(store.Agent{Name: "dev-1", Role: "dev"}, "", "", true, nil)
	for _, want := range []string{"finds no console, call lock_hint and report its hint as is", "never tell the user to run lox up or lox approve"} {
		if !strings.Contains(p, want) {
			t.Fatalf("missing %q in %q", want, p)
		}
	}
}
