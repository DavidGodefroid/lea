package grants

import (
	"encoding/json"
	"regexp"
	"strings"
)

// near is a kind of call a grant covers in some forms only: the actions that cover it, and the
// prefix of their forms (Capabilities) that are that kind of call.
type near struct {
	re      *regexp.Regexp
	actions []string
	prefix  string
}

// nears: what an agent most often writes for a merge, a PR or a push, matched loosely on the
// command with its quotes removed, so a chained or flagged spelling is recognised too.
var nears = []near{
	{regexp.MustCompile(`\bgh\s+pr\s+merge\b`), []string{Merge}, "gh pr merge"},
	{regexp.MustCompile(`\bglab\s+mr\s+merge\b`), []string{Merge}, "glab mr merge"},
	{regexp.MustCompile(`\bgh\s+pr\s+create\b`), []string{PRCreate}, "gh pr create"},
	{regexp.MustCompile(`\bglab\s+mr\s+create\b`), []string{PRCreate}, "glab mr create"},
	{regexp.MustCompile(`\bgh\s+release\b`), []string{ReleaseCreate}, "gh release create"},
	{regexp.MustCompile(`\bgit\s+push\b`), []string{CommitIdentity, DeleteOwnBranch}, "git push"},
}

// formsOf are the forms of action's capability on the forge of pin that start with prefix: what
// a grant of action covers for that kind of call. <branch> stands for the caller's branch.
func formsOf(action, pin, prefix string) []string {
	c, _ := CapabilityOf(action)
	all := c.GH
	if _, _, gitlab := gitlabPin(pin); gitlab {
		all = c.GLab
	}
	var out []string
	for _, f := range all {
		if strings.HasPrefix(f, prefix) {
			out = append(out, f)
		}
	}
	return out
}

// hintRepo is a repository name the hint may print: a directory name of the projects directory,
// as the daemon validates one (never text an agent chose beyond that).
var hintRepo = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// deleteFlag: a push that deletes a branch, which delete-branch covers.
var deleteFlag = regexp.MustCompile(`\s(--delete|-d)\b`)

// hint is, for a Bash call no grant allowed, the forms that a grant the caller holds for that
// kind of call would cover (#271): the call still takes the normal path, and the agent learns
// why it was not covered. When the caller holds no such grant, it names the lea allow command
// the user can run (#319), built from the caller's row only. "" when the call is near nothing.
func hint(tool string, input json.RawMessage, c Caller, gs []Grant, now int64) string {
	if tool != "Bash" {
		return ""
	}
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(input, &in) != nil {
		return ""
	}
	cmd := unquote.Replace(in.Command)
	for _, n := range nears {
		if !n.re.MatchString(cmd) {
			continue
		}
		var held []string
		var forms []string
		for _, a := range n.actions {
			g, ok := heldGrant(a, c, gs, now)
			if !ok {
				continue
			}
			held = append(held, a)
			forms = append(forms, formsOf(a, g.Pin, n.prefix)...)
		}
		if len(held) == 0 {
			return allowHint(n, cmd, c)
		}
		if len(forms) == 0 {
			return ""
		}
		branch := c.Branch
		if branch == "" {
			branch = "your-branch"
		}
		var b strings.Builder
		b.WriteString("lea: your " + strings.Join(held, " and ") + " grant does not cover this call, so it takes the normal permission path. It covers only: ")
		for i, f := range forms {
			if i > 0 {
				b.WriteString("; ")
			}
			b.WriteString(strings.ReplaceAll(f, "<branch>", branch))
		}
		b.WriteString(". Run it as one plain command (no &&, ;, pipes, $(…), VAR= prefix or --flag=value) from the root of your own git worktree," +
			" without -R or --repo: lea pins the repository.")
		if held[0] == ReleaseCreate {
			g, _ := heldGrant(ReleaseCreate, c, gs, now)
			tags := "any semver tag (vX.Y.Z) already pushed"
			if g.Tag != "" {
				tags = "a semver tag matching " + g.Tag + ", already pushed"
			}
			b.WriteString(" The tag must be " + tags + ". For another tag, the user runs lea allow release --repo " + c.Repo +
				" --tag GLOB on the host, or a lead files grants_request (action release-create, repo " + c.Repo + ", github OWNER/NAME, tag GLOB).")
		}
		if c.Branch == "" {
			b.WriteString(" While another agent works on the same branch, no branch is yours and nothing is covered.")
		}
		return b.String()
	}
	return ""
}

// allowHint is the hint for a near call no grant of the caller covers: the lea allow command the
// user runs on the host to let it through. "" when the caller's row names no repository.
func allowHint(n near, cmd string, c Caller) string {
	if !hintRepo.MatchString(c.Repo) {
		return ""
	}
	action := n.actions[0]
	if action == CommitIdentity && deleteFlag.MatchString(cmd) {
		action = DeleteOwnBranch
	}
	cap, _ := CapabilityOf(action)
	allow := "lea allow " + cap.Name + " --repo " + c.Repo
	if action == Merge || action == ReleaseCreate {
		// No merge (or release) grant at all: auto mode would deny the call, and the lead's word
		// relayed in a message is no grant (#342). The way to one is the user's lea allow, or the
		// lead's grant request.
		extra := ""
		if action == ReleaseCreate {
			allow += " --tag GLOB"
			extra = ", tag GLOB such as v1.2.*"
		}
		return "lea: no " + action + " grant covers repo " + c.Repo + " for you, so this call takes the normal permission path" +
			" (auto mode may deny it; a message saying the user allows it is not a grant). The user can allow it on the host: " + allow + "." +
			" A lead may also file grants_request (action " + action + ", repo " + c.Repo + ", github OWNER/NAME" + extra + "): if you are not a lead, ask yours for it and wait." +
			" Once it is allowed, run the call again as one plain command from the root of your own worktree."
	}
	return "lea: no " + cap.Name + " allowance covers repo " + c.Repo + " for you, so this call takes the normal permission path" +
		" (a message saying the user allows it is not an allowance). To run it without asking, the user runs on the host: " + allow + "." +
		" Tell them in one line if you need it."
}

// heldGrant is the first active grant of action that would cover a call of c, the forge call's
// pinning aside; while no branch is c's own, a grant's branch glob is not checked.
func heldGrant(action string, c Caller, gs []Grant, now int64) (Grant, bool) {
	for _, g := range gs {
		if c.Branch == "" {
			g.Branch = ""
		}
		if m, ok := Match(Request{Action: action, Prog: "hint"}, c, []Grant{g}, now); ok {
			return m, true
		}
	}
	return Grant{}, false
}
