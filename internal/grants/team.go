package grants

import (
	"encoding/json"
	"path"
	"strings"
)

// TeamBranch is a branch a lead may push for its team (#406): the recorded branch of one of its
// direct children that no live agent works on any more (the daemon builds the list from its rows,
// never from anything the agent says). Key and Name are the child's, Repo its repository: its
// worktree is <projects>/.lea/worktrees/<Repo>/<Name>.
type TeamBranch struct {
	Key, Name, Repo, Branch string
}

// team is the caller a lead's push runs as when its call runs at the root of the worktree of one
// of its Team branches: that child's repository, branch and worktree, the child's key before the
// lead's lineage. ok false otherwise.
func (c Caller) team() (Caller, bool) {
	for _, t := range c.Team {
		if t.Branch == "" || !BranchName(t.Branch) || Protected(t.Branch) || !InWorktree(c.Cwd, t.Repo, t.Name) {
			continue
		}
		wt := strings.TrimSuffix(c.Cwd, "/")
		return Caller{Key: c.Key, Container: c.Container, Repo: t.Repo, Branch: t.Branch,
			Lineage: append([]string{t.Key}, c.Lineage...), OwnWorktree: true, Worktree: wt, Cwd: c.Cwd,
			Env: c.Env, Proxy: c.Proxy}, true
	}
	return Caller{}, false
}

// teamPush is the decision on a lead's push of a team branch: allowed only when, run as the
// child (team), the call is exactly a push that a grant covers and lea pins. Anything else (a
// commit, a merge, a delete, another branch) keeps the lead's own decision.
func teamPush(tool string, input json.RawMessage, c Caller, gs []Grant, now int64) (Decision, bool) {
	t, ok := c.team()
	if !ok || tool != "Bash" {
		return Decision{}, false
	}
	d := decide(tool, input, t, gs, now, false)
	if d.Verdict != Allow || d.Request.Action != CommitIdentity || d.Request.Prog != "git" ||
		len(d.Request.Args) == 0 || d.Request.Args[0] != "push" {
		return Decision{}, false
	}
	d.Reason += ", for the team branch " + t.Branch
	return d, true
}

// teamHint tells a lead denied a push which of its children's branches it may push, and from
// where; "" when it has none.
func teamHint(c Caller) string {
	var bs []string
	for _, t := range c.Team {
		if BranchName(t.Branch) && !Protected(t.Branch) {
			bs = append(bs, t.Branch+" (run it from "+path.Join(".lea/worktrees", t.Repo, t.Name)+")")
		}
	}
	if len(bs) == 0 {
		return ""
	}
	return ". A lead pushes a branch of its finished children from that child's worktree root, as git push [-u] origin <branch>: " +
		strings.Join(bs, ", ")
}
