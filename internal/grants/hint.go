package grants

import (
	"encoding/json"
	"regexp"
	"strings"
)

// near is a kind of call a grant covers in some forms only: the forms covered, by forge.
type near struct {
	re       *regexp.Regexp
	actions  []string
	gh, glab []string
}

// nears: what an agent most often writes for a merge, a PR or a push, matched loosely on the
// command with its quotes removed, so a chained or flagged spelling is recognised too. <branch>
// stands for the caller's branch.
var nears = []near{
	{regexp.MustCompile(`\bgh\s+pr\s+merge\b`), []string{Merge},
		[]string{"gh pr merge <number|<branch>> [--squash|--merge|--rebase] [--delete-branch]"}, nil},
	{regexp.MustCompile(`\bglab\s+mr\s+merge\b`), []string{Merge},
		nil, []string{"glab mr merge <number|<branch>> [--squash] [--remove-source-branch] [--yes]"}},
	{regexp.MustCompile(`\bgh\s+pr\s+create\b`), []string{PRCreate},
		[]string{"gh pr create --head <branch> --title T (--body B | --body-file F) [--base B] [--draft]"}, nil},
	{regexp.MustCompile(`\bglab\s+mr\s+create\b`), []string{PRCreate}, nil,
		[]string{"glab mr create --source-branch <branch> --title T [--description D] [--target-branch B] [--draft] [--remove-source-branch] [--yes]"}},
	{regexp.MustCompile(`\bgit\s+push\b`), []string{CommitIdentity, DeleteOwnBranch},
		[]string{"git push [-u] origin <branch>", "git push origin --delete <branch>"},
		[]string{"git push [-u] origin <branch>", "git push origin --delete <branch>"}},
}

// hint is, for a Bash call no grant allowed, the forms that a grant the caller holds for that
// kind of call would cover (#271): the call still takes the normal path, and the agent learns
// why it was not covered. "" when the caller holds no such grant.
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
			if _, _, gitlab := gitlabPin(g.GitHub); gitlab {
				forms = n.glab
			} else {
				forms = n.gh
			}
		}
		if len(held) == 0 || len(forms) == 0 {
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
		if c.Branch == "" {
			b.WriteString(" While another agent works on the same branch, no branch is yours and nothing is covered.")
		}
		return b.String()
	}
	return ""
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
