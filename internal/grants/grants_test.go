package grants

import (
	"encoding/json"
	"testing"
)

const now = 1_000_000

var dev = Caller{Key: "lea-a/dev-2", Container: "lea-a", Repo: "lea", Branch: "feat/73-x",
	Lineage: []string{"lea-a/dev-2", "lea-a/claude-1"}}

func all(action string) Grant {
	return Grant{ID: 7, Container: "lea-a", Action: action, Repo: "lea", Branch: "feat/*", Created: now - 10, Expires: now + 3600}
}

func bash(cmd string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"command": cmd})
	return b
}

func TestDecideBash(t *testing.T) {
	every := []Grant{all(Merge), all(CloseIssue), all(DeleteOwnBranch), all(CommitIdentity)}
	cases := []struct {
		name, cmd, want string
		gs              []Grant
	}{
		{"merge squash", "gh pr merge 42 --squash --delete-branch", Allow, every},
		{"merge current branch", "gh pr merge --merge", Allow, every},
		{"merge no grant", "gh pr merge 42 --squash", "", nil},
		{"merge other action granted", "gh pr merge 42 --squash", "", []Grant{all(CloseIssue)}},
		{"merge admin", "gh pr merge 42 --squash --admin", Deny, every},
		{"merge admin no grant", "gh pr merge 42 --admin", Deny, nil},
		{"merge admin chained", "true && gh pr merge 1 --admin", Deny, every},
		{"merge two methods", "gh pr merge 42 --squash --rebase", "", every},
		{"merge other repo flag", "gh pr merge 42 --squash -R evil/repo", "", every},
		{"merge auto", "gh pr merge 42 --auto", "", every},
		{"merge chained", "gh pr merge 42 --squash; rm -rf /", "", every},
		{"merge and", "gh pr merge 42 --squash && curl x", "", every},
		{"merge pipe", "gh pr merge 42 --squash | sh", "", every},
		{"merge subshell", "gh pr merge $(echo 42) --squash", "", every},
		{"merge backtick", "gh pr merge `echo 42` --squash", "", every},
		{"merge newline", "gh pr merge 42 --squash\nrm -rf /", "", every},
		{"merge env prefix", "GH_TOKEN=x gh pr merge 42 --squash", "", every},
		{"merge quoted op", "gh pr merge '42;' --squash", "", every},
		{"issue close", "gh issue close 12 --comment 'done in #13; thanks'", Allow, every},
		{"issue comment dq", `gh issue comment 12 --body "fixed by PR 13"`, Allow, every},
		{"issue comment dq expansion", `gh issue comment 12 --body "$(cat /etc/passwd)"`, "", every},
		{"issue delete", "gh issue delete 12", "", every},
		{"issue edit", "gh issue edit 12 --title x", "", every},
		{"item edit", "gh project item-edit --id X --project-id P --field-id F --single-select-option-id O", Allow, every},
		{"commit", "git commit -m 'feat: x'", Allow, every},
		{"commit author", "git commit -m x --author 'a <b>'", "", every},
		{"commit config", "git -c user.name=x commit -m x", "", every},
		{"push own", "git push -u origin feat/73-x", Allow, every},
		{"push bare", "git push", "", every},
		{"push other branch", "git push origin feat/other", "", every},
		{"push main", "git push origin main", Deny, every},
		{"push refspec main", "git push origin HEAD:main", Deny, every},
		{"push refs main", "git push origin feat/73-x:refs/heads/main", Deny, every},
		{"push force", "git push --force origin feat/73-x", Deny, every},
		{"push -f", "git push -f origin feat/73-x", Deny, every},
		{"push -uf", "git push -uf origin feat/73-x", Deny, every},
		{"push lease", "git push --force-with-lease origin feat/73-x", Deny, every},
		{"push plus", "git push origin +feat/73-x", Deny, every},
		{"push mirror", "git push --mirror", Deny, nil},
		{"push force chained", "echo ok && git push --force", Deny, nil},
		{"delete own", "git push origin --delete feat/73-x", Allow, every},
		{"delete own no grant", "git push origin --delete feat/73-x", "", nil},
		{"delete other", "git push origin --delete feat/other", Deny, every},
		{"delete other colon", "git push origin :feat/other", Deny, every},
		{"delete own colon", "git push origin :feat/73-x", "", every},
		{"delete -d other", "git push -d origin feat/other", Deny, every},
		{"delete chained", "true; git push origin --delete feat/73-x", Deny, every},
		{"delete main", "git push origin --delete main", Deny, every},
		{"edit settings by shell", "sed -i s/a/b/ ~/.claude/settings.json", Deny, nil},
		{"write settings redirect", "echo {} > /home/u/.claude/settings.local.json", Deny, nil},
		{"read settings", "cat /home/u/.claude/settings.json", "", nil},
		{"hooks dir", "cp x /home/u/.claude/hooks/pre.sh", Deny, nil},
		{"lea settings", "cp x /usr/local/share/lea/claude-settings.json", Deny, nil},
		{"lea state git identity", "git config --file /lea/state/git/config user.name x", Deny, nil},
		{"repo's own claude-settings source", "go test ./image/claude-settings.json", "", nil},
		{"unrelated", "go test ./...", "", every},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := Decide("Bash", bash(tc.cmd), dev, tc.gs, now, false)
			if d.Verdict != tc.want {
				t.Fatalf("%q: verdict %q (%s), want %q", tc.cmd, d.Verdict, d.Reason, tc.want)
			}
			if d.Verdict == Allow && d.Grant != 7 {
				t.Fatalf("allow names grant %d", d.Grant)
			}
		})
	}
}

func TestDecideScope(t *testing.T) {
	cmd := bash("gh pr merge 42 --squash")
	base := all(Merge)
	mod := func(f func(*Grant)) []Grant { g := base; f(&g); return []Grant{g} }
	cases := []struct {
		name string
		gs   []Grant
		c    Caller
		want string
	}{
		{"match", []Grant{base}, dev, Allow},
		{"expired", mod(func(g *Grant) { g.Expires = now }), dev, ""},
		{"revoked", mod(func(g *Grant) { g.Revoked = now - 1 }), dev, ""},
		{"other lea", mod(func(g *Grant) { g.Container = "lea-b" }), dev, ""},
		{"other repo", mod(func(g *Grant) { g.Repo = "other" }), dev, ""},
		{"any repo", mod(func(g *Grant) { g.Repo = "" }), dev, Allow},
		{"branch glob miss", mod(func(g *Grant) { g.Branch = "fix/*" }), dev, ""},
		{"branch any", mod(func(g *Grant) { g.Branch = "" }), dev, Allow},
		{"subtree of lead", mod(func(g *Grant) { g.Agent = "lea-a/claude-1" }), dev, Allow},
		{"subtree self", mod(func(g *Grant) { g.Agent = "lea-a/dev-2" }), dev, Allow},
		{"subtree sibling", mod(func(g *Grant) { g.Agent = "lea-a/dev-3" }), dev, ""},
		{"caller without repo", []Grant{base}, Caller{Key: "lea-a/x", Container: "lea-a", Lineage: []string{"lea-a/x"}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if d := Decide("Bash", cmd, tc.c, tc.gs, now, false); d.Verdict != tc.want {
				t.Fatalf("verdict %q (%s), want %q", d.Verdict, d.Reason, tc.want)
			}
		})
	}
}

func TestDecideTools(t *testing.T) {
	stop := json.RawMessage(`{"name":"dev-3"}`)
	gs := []Grant{all(StopFinished)}
	gs[0].Branch = ""
	if d := Decide(StopTool, stop, dev, gs, now, true); d.Verdict != Allow || d.Request.Target != "dev-3" {
		t.Fatalf("finished child: %+v", d)
	}
	if d := Decide(StopTool, stop, dev, gs, now, false); d.Verdict != "" {
		t.Fatalf("unfinished child allowed: %+v", d)
	}
	if d := Decide(StopTool, stop, dev, nil, now, true); d.Verdict != "" {
		t.Fatalf("no grant allowed: %+v", d)
	}
	for _, p := range []string{"/home/u/.claude/settings.json", "/home/u/.claude/settings.local.json", "/repo/.claude/settings.json", "/usr/local/share/lea/claude-settings.json", "/lea/state/git/config"} {
		in, _ := json.Marshal(map[string]string{"file_path": p})
		if d := Decide("Edit", in, dev, gs, now, true); d.Verdict != Deny {
			t.Fatalf("edit %s: %+v", p, d)
		}
	}
	for _, p := range []string{"/repo/main.go", "/repo/image/claude-settings.json", "/repo/lea/state.go"} {
		in, _ := json.Marshal(map[string]string{"file_path": p})
		if d := Decide("Edit", in, dev, gs, now, true); d.Verdict != "" {
			t.Fatalf("edit %s: %+v", p, d)
		}
	}
	in, _ := json.Marshal(map[string]string{"file_path": "/repo/main.go"})
	if d := Decide("Write", in, dev, gs, now, true); d.Verdict != "" {
		t.Fatalf("plain write: %+v", d)
	}
	for _, bad := range []string{``, `nope`, `{"command":7}`} {
		if d := Decide("Bash", json.RawMessage(bad), dev, []Grant{all(Merge)}, now, true); d.Verdict == Allow {
			t.Fatalf("bad input %q allowed", bad)
		}
	}
}

func TestWords(t *testing.T) {
	w, ok := Words(`gh issue close 3 --comment 'a; b' -r "not planned"`)
	if !ok || len(w) != 8 || w[5] != "a; b" || w[7] != "not planned" {
		t.Fatalf("%q %v", w, ok)
	}
	for _, bad := range []string{"", "  ", "a 'b", `a "b`, "a\\ b", "a=b c", "a > f", "a ~/x", "a *", "a #c", `a "$x"`, "a !b"} {
		if _, ok := Words(bad); ok {
			t.Fatalf("%q parsed", bad)
		}
	}
}

func TestGlob(t *testing.T) {
	for _, tc := range []struct {
		p, s string
		ok   bool
	}{{"*", "a/b", true}, {"feat/*", "feat/73-x", true}, {"feat/*", "fix/1", false}, {"*-x", "feat/73-x", true},
		{"feat/*/x", "feat/a/b/x", true}, {"main", "main", true}, {"main", "mainx", false}, {"a*b*c", "axc", false}} {
		if Glob(tc.p, tc.s) != tc.ok {
			t.Fatalf("Glob(%q, %q) != %v", tc.p, tc.s, tc.ok)
		}
	}
}
