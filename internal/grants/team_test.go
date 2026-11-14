package grants

import (
	"strings"
	"testing"
)

// lead is a lead with no branch of its own, whose finished child dev-1 left feat/406-x.
var lead = Caller{Key: "lea-a/claude-1", Container: "lea-a", Lineage: []string{"lea-a/claude-1"},
	Cwd: "/p/.lea/worktrees/lea/dev-1", Env: dev.Env, Proxy: LeaProxy,
	Team: []TeamBranch{{Key: "lea-a/dev-1", Name: "dev-1", Repo: "lea", Branch: "feat/406-x"}}}

// TestLeadPushesTeamBranch: with a push grant, a lead pushes its finished child's branch from that
// child's worktree, pinned as the child's own push would be; nothing else widens (#406).
func TestLeadPushesTeamBranch(t *testing.T) {
	push := all(CommitIdentity)
	away := lead
	away.Cwd = "/p/lea"
	main := lead
	main.Team = []TeamBranch{{Key: "lea-a/dev-1", Name: "dev-1", Repo: "lea", Branch: "main"}}
	other := lead
	other.Team = nil // another lead's child: the daemon never lists it
	cases := []struct {
		name, cmd string
		c         Caller
		gs        []Grant
		want      string
	}{
		{"team branch", "git push origin feat/406-x", lead, []Grant{push}, Allow},
		{"team branch -u", "git push -u origin feat/406-x", lead, []Grant{push}, Allow},
		{"team branch no grant", "git push origin feat/406-x", lead, nil, Deny},
		{"team branch other repo grant", "git push origin feat/406-x", lead, []Grant{{ID: 8, Container: "lea-a", Action: CommitIdentity,
			Repo: "other", Pin: "github.com/o/other", Created: now - 10, Expires: now + 3600}}, Deny},
		{"team branch outside glob", "git push origin feat/406-x", lead, []Grant{{ID: 8, Container: "lea-a", Action: CommitIdentity,
			Repo: "lea", Branch: "fix/*", Pin: "github.com/o/lea", Created: now - 10, Expires: now + 3600}}, Deny},
		{"other branch", "git push origin feat/other", lead, []Grant{push}, Deny},
		{"two branches", "git push origin feat/406-x feat/other", lead, []Grant{push}, Deny},
		{"main", "git push origin main", lead, []Grant{push}, Deny},
		{"team branch to main", "git push origin feat/406-x:refs/heads/main", lead, []Grant{push}, Deny},
		{"protected team branch", "git push origin main", main, []Grant{push}, Deny},
		{"force", "git push -f origin feat/406-x", lead, []Grant{push}, Deny},
		{"delete", "git push origin --delete feat/406-x", lead, []Grant{push, all(DeleteOwnBranch)}, Deny},
		{"from elsewhere", "git push origin feat/406-x", away, []Grant{push}, Deny},
		{"another lead's child", "git push origin feat/406-x", other, []Grant{push}, Deny},
		{"commit stays the child's", "git commit -m x", lead, []Grant{push}, ""},
		{"merge stays the child's", "gh pr merge feat/406-x --squash", lead, []Grant{push, all(Merge)}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := Decide("Bash", bash(tc.cmd), tc.c, tc.gs, now, false)
			if d.Verdict != tc.want {
				t.Fatalf("verdict %q (%s), want %q", d.Verdict, d.Reason, tc.want)
			}
			if d.Verdict == Allow && (!strings.Contains(d.Command, "/p/.lea/worktrees/lea/dev-1") ||
				!strings.Contains(d.Command, "refs/heads/feat/406-x") || d.Request.Target != "lea push feat/406-x") {
				t.Fatalf("command %q target %q", d.Command, d.Request.Target)
			}
		})
	}
}

// TestLeadPushHint: a lead denied a push it could run from its child's worktree is told where.
func TestLeadPushHint(t *testing.T) {
	away := lead
	away.Cwd = "/p/lea"
	d := Decide("Bash", bash("git push origin feat/406-x"), away, []Grant{all(CommitIdentity)}, now, false)
	if d.Verdict != Deny || !strings.Contains(d.Reason, "feat/406-x (run it from .lea/worktrees/lea/dev-1)") {
		t.Fatalf("verdict %q reason %q", d.Verdict, d.Reason)
	}
	solo := away
	solo.Team = nil
	if d := Decide("Bash", bash("git push origin feat/406-x"), solo, nil, now, false); d.Reason != "lea hard deny: "+pushOther {
		t.Fatalf("reason %q", d.Reason)
	}
}
