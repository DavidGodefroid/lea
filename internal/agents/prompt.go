package agents

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/DavidGodefroid/lea/internal/roles"
	"github.com/DavidGodefroid/lea/internal/store"
)

// firstPrompt is the first prompt of an agent started with a brief: one plain line (tmux and argv
// carry no control character) that sends the agent to the brief waiting in its inbox. The brief
// itself is a message, so it is stored, attributed and wrapped like any other.
// repo, when set, is the repository's directory: the agent starts in the projects directory.
func firstPrompt(a store.Agent, repo, worktree string, brief bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are the lea agent %s (role %s)", a.Name, a.Role)
	if l, err := CheckLabel(a.Label); err == nil && l != "" && l == a.Label {
		fmt.Fprintf(&b, ", shown to the user as %q", l)
	}
	b.WriteString(".")
	if brief {
		b.WriteString(" Your brief is waiting: read it now with the msg_inbox tool, then work on it.")
	}
	if repo != "" && a.Branch == "" {
		fmt.Fprintf(&b, " Your repository is %s: work in it.", repo)
	}
	if a.Branch != "" {
		fmt.Fprintf(&b, " Work on branch %s in your own git worktree %s", a.Branch, worktree)
		if repo != "" {
			fmt.Fprintf(&b, " of the repository %s", repo)
		}
		b.WriteString(" (create it with git worktree add if it does not exist), never in the shared checkout.")
	}
	if r, err := roles.Get(a.Role); err == nil && r.CanSpawn {
		fmt.Fprintf(&b, " As a %s you may start %s agents with agents_spawn and stop your own with agents_stop.", r.Name, strings.Join(r.SpawnRoles, " and "))
	} else {
		b.WriteString(" When you need another agent, ask a lead with agents_request_spawn.")
	}
	b.WriteString(" Report progress with task_update: set next while something is still to happen (CI, a review, a reply)," +
		" blocked_on when a human must act, and clear both once your work is done.")
	// Idle stacks (#132): what the agent started stops while it waits.
	b.WriteString(" lea stops the containers you started (compose stacks, pods) while you are idle: start them again before you use them" +
		" (docker compose up -d, podman start).")
	// The deck shows an agent's label, the user may rename it at any time (#84).
	b.WriteString(" When you mention an agent in text the user reads (task_update, results your lead relays), use its label from agents_list" +
		" when it has one, looked up fresh since the user may rename it; address tools by name, never by label.")
	if a.Parent != "" {
		// The lead that started the agent waits for its answer: task_update alone reaches the
		// board, never the lead's inbox.
		parent := a.Parent[strings.LastIndex(a.Parent, "/")+1:]
		fmt.Fprintf(&b, " Your lead is %s: when your work is done, and whenever you are blocked or need a decision,"+
			" tell it with msg_send (to %s, kind result or request), at most 5 lines: the outcome, its evidence, the open question.", parent, parent)
	}
	return b.String()
}

// taskTitle is the first non-blank line of a brief, on one clean line of at most 80 characters.
func taskTitle(brief string) string {
	for _, l := range strings.Split(brief, "\n") {
		if t := strings.Join(strings.Fields(l), " "); t != "" {
			return oneLineCap(strings.Map(safeRune, t), 80)
		}
	}
	return ""
}

// worktreePath is where an agent on a branch keeps its git worktree (spec A14):
// <projects>/.lea/worktrees/<repo>/<agent>, repo being the agent's repo, else its directory's name.
func worktreePath(projects, workdir string, a store.Agent) string {
	repo := a.Repo
	if repo == "" {
		repo = filepath.Base(workdir)
	}
	return filepath.Join(projects, ".lea", "worktrees", repo, a.Name)
}
