package agents

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/DavidGodefroid/lea/internal/grants"
	"github.com/DavidGodefroid/lea/internal/roles"
	"github.com/DavidGodefroid/lea/internal/store"
)

// firstPrompt is the first prompt of an agent started with a brief: one plain line (tmux and argv
// carry no control character) that sends the agent to the brief waiting in its inbox. The brief
// itself is a message, so it is stored, attributed and wrapped like any other.
// repo, when set, is the repository's directory: the agent starts in the projects directory.
// covered are the capabilities of the deliver preset that cover the agent as it starts (Covered).
func firstPrompt(a store.Agent, repo, worktree string, brief bool, covered []string) string {
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
		// --no-track (#381): a branch made from origin/main would track main.
		fmt.Fprintf(&b, " (create it with git worktree add --no-track -b %s %s <base> if it does not exist), never in the shared checkout.", a.Branch, worktree)
		// Grants (#271, #319): the forms lea runs without asking, for what covers the agent when it
		// starts; otherwise the lea allow command the user runs. grants_list stays the live truth.
		b.WriteString(deliveryForms(a, covered))
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
	// Heavy jobs (#132): the container's slots hold the host's CPUs.
	b.WriteString(" Run test suites, static analysis, builds and browsers through lea heavy -- <command>: it waits for a free slot" +
		" and caps the command's CPUs and workers.")
	// Egress refusals (#345): clients hide the proxy's page behind a CONNECT 403, and only the user
	// may allow a domain, on the host.
	b.WriteString(" When a download fails with CONNECT tunnel failed, response 403 or 403 Forbidden through lea-proxy (the HTTPS_PROXY of your environment)," +
		" lea-proxy refused the host: curl -sI https://HOST/ shows X-Squid-Error: ERR_LEA_EGRESS when it is not in the egress allowlist." +
		" Do not retry, switch mirrors or work around the proxy: tell the user in one line, as \"lea-proxy refused HOST: not in the egress allowlist." +
		" To allow it, run on the host: lea egress allow HOST (applies live, no restart; there is no one-off form: it stays until lea egress deny HOST).\", and wait; you cannot allow it yourself.")
	// A lock without a socket (#343): lea knows its state, the agent must not guess a command.
	b.WriteString(" When a Lock's client (locksql, lockmq, lockfile, locklog) finds no console, call lock_hint and report its hint as is;" +
		" never tell the user to run lox up or lox approve, or to restart or recreate a lock.")
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

// deliveryForms is the first prompt's sentence on pushing and PRs: the forms of the capabilities
// in covered, and for the others the lea allow command that lets the agent run them.
func deliveryForms(a store.Agent, covered []string) string {
	has := map[string]bool{}
	for _, c := range covered {
		has[c] = true
	}
	var gh, glab []string
	if has["push"] {
		gh = append(gh, "git push -u origin "+a.Branch)
		glab = append(glab, "git push -u origin "+a.Branch)
	}
	if has["pr"] {
		gh = append(gh, "gh pr create --head "+a.Branch+" --title T --body-file F")
		glab = append(glab, "glab mr create --source-branch "+a.Branch+" --title T --yes")
	}
	if has["merge"] {
		gh = append(gh, "gh pr merge <number> --squash --delete-branch")
		glab = append(glab, "glab mr merge <number> --squash --remove-source-branch --yes")
	}
	allow := "lea allow deliver"
	if hintRepo.MatchString(a.Repo) {
		allow += " --repo " + a.Repo
	}
	if len(gh) == 0 {
		return " Pushing, opening or merging a PR asks the user; tell them " + allow + " lets you do it without asking."
	}
	s := fmt.Sprintf(" The user allowed you (lea allow), so lea runs these forms without asking, each as one plain command from your worktree's root,"+
		" without -R: %s (GitLab: %s).", strings.Join(gh, ", "), strings.Join(glab, ", "))
	if len(gh) < 3 {
		s += " The rest of pushing, opening and merging a PR asks the user; " + allow + " lets you do it without asking."
	}
	return s
}

// hintRepo is a repository name the prompt may print.
var hintRepo = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// Covered is what of the deliver preset (push, pr, merge) the active grants gs of a's lea cover
// for a when it starts, a's lineage its key then its parents' (nearest first).
func Covered(a store.Agent, lineage []string, gs []grants.Grant, now int64) []string {
	c := grants.Caller{Key: a.Key, Container: a.Container, Repo: a.Repo, Branch: a.Branch, Lineage: lineage}
	var out []string
	for _, name := range []string{"push", "pr", "merge"} {
		capb, _ := grants.CapabilityNamed(name)
		if grants.Covers(capb.Action, c, gs, now) {
			out = append(out, name)
		}
	}
	return out
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
