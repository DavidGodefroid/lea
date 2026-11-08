package grants

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// inner is a pinned command with the sh -c script's quoting undone, for matching its words.
func inner(cmd string) string { return strings.ReplaceAll(cmd, "'\\''", "'") }

func releaseGrant(tag string) Grant {
	g := all(ReleaseCreate)
	g.Branch, g.Tag = "", tag
	return g
}

// TestReleaseCreateDecide: a release-create grant covers exactly gh release create <tag>
// --verify-tag [--title T] [--notes-file F | --generate-notes] [--prerelease] [--latest], one
// plain command at the root of the caller's own worktree, for a semver tag the grant's glob
// matches (#353).
func TestReleaseCreateDecide(t *testing.T) {
	every := []Grant{releaseGrant("v0.15.*"), all(Merge), all(CommitIdentity), all(ReleaseTag)}
	away := dev
	away.OwnWorktree, away.Worktree = false, ""
	other := dev
	other.Repo = "other"
	stranger := dev
	stranger.Lineage = []string{"lea-a/dev-9", "lea-a/claude-3"}
	scoped := releaseGrant("v0.15.*")
	scoped.Agent = "lea-a/claude-1"
	expired := releaseGrant("v0.15.*")
	expired.Expires = now - 1
	revoked := releaseGrant("v0.15.*")
	revoked.Revoked = now - 1
	cases := []struct {
		name, cmd, want string
		c               Caller
		gs              []Grant
	}{
		{"notes file", "gh release create v0.15.0 --verify-tag --notes-file notes.md", Allow, dev, every},
		{"generate notes", "gh release create v0.15.0 --verify-tag --generate-notes", Allow, dev, every},
		{"all flags", "gh release create v0.15.1-rc.1 --verify-tag --title 'v0.15.1 rc' -F docs/notes.md --prerelease --latest", Allow, dev, every},
		{"flags first", "gh release create --verify-tag --generate-notes v0.15.0", Allow, dev, every},
		{"no notes", "gh release create v0.15.0 --verify-tag", Allow, dev, every},
		{"any semver tag", "gh release create v3.0.0 --verify-tag", Allow, dev, []Grant{releaseGrant("")}},
		{"lead's subtree", "gh release create v0.15.0 --verify-tag", Allow, dev, []Grant{scoped}},
		{"outside the glob", "gh release create v0.16.0 --verify-tag --generate-notes", "", dev, every},
		{"no grant", "gh release create v0.15.0 --verify-tag", "", dev, []Grant{all(Merge), all(ReleaseTag)}},
		{"other repo", "gh release create v0.15.0 --verify-tag", "", other, every},
		{"away from the worktree", "gh release create v0.15.0 --verify-tag", "", away, every},
		{"other agent's subtree", "gh release create v0.15.0 --verify-tag", "", stranger, []Grant{scoped}},
		{"expired", "gh release create v0.15.0 --verify-tag", "", dev, []Grant{expired}},
		{"revoked", "gh release create v0.15.0 --verify-tag", "", dev, []Grant{revoked}},
		{"-R", "gh release create v0.15.0 --verify-tag -R o/other", "", dev, every},
		{"--repo", "gh release create v0.15.0 --verify-tag --repo o/lea", "", dev, every},
		{"no verify-tag", "gh release create v0.15.0 --generate-notes", "", dev, every},
		{"draft", "gh release create v0.15.0 --verify-tag --draft", "", dev, every},
		{"target", "gh release create v0.15.0 --verify-tag --target main", "", dev, every},
		{"asset", "gh release create v0.15.0 --verify-tag dist/lea.tar.gz", "", dev, every},
		{"not semver", "gh release create release-1 --verify-tag", "", dev, every},
		{"no tag", "gh release create --verify-tag --generate-notes", "", dev, every},
		{"notes and generate", "gh release create v0.15.0 --verify-tag -F notes.md --generate-notes", "", dev, every},
		{"notes text", "gh release create v0.15.0 --verify-tag --notes hi", "", dev, every},
		{"notes stdin", "gh release create v0.15.0 --verify-tag --notes-file -", "", dev, every},
		{"notes outside", "gh release create v0.15.0 --verify-tag --notes-file ../notes.md", "", dev, every},
		{"notes absolute", "gh release create v0.15.0 --verify-tag --notes-file /etc/passwd", "", dev, every},
		{"notes in .git", "gh release create v0.15.0 --verify-tag --notes-file .git/HEAD", "", dev, every},
		{"twice", "gh release create v0.15.0 --verify-tag --verify-tag", "", dev, every},
		{"latest=false", "gh release create v0.15.0 --verify-tag --latest=false", "", dev, every},
		{"chained", "gh release create v0.15.0 --verify-tag && true", "", dev, every},
		{"edit", "gh release edit v0.15.0 --verify-tag", "", dev, every},
		{"delete", "gh release delete v0.15.0 --yes", "", dev, every},
		{"upload", "gh release upload v0.15.0 dist/x", "", dev, every},
		{"delete-asset", "gh release delete-asset v0.15.0 x", "", dev, every},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := Decide("Bash", bash(tc.cmd), tc.c, tc.gs, now, false)
			if d.Verdict != tc.want {
				t.Fatalf("%q: verdict %q (%s), want %q", tc.cmd, d.Verdict, d.Reason, tc.want)
			}
			if d.Verdict != Allow {
				return
			}
			cmd := inner(d.Command)
			if d.Request.Action != ReleaseCreate || !strings.HasPrefix(d.Command, "'"+envPath+"' '-i' ") ||
				!strings.Contains(cmd, "'-R' 'github.com/o/lea'") || !strings.Contains(cmd, forgeGHConfig) ||
				!strings.Contains(cmd, "'--verify-tag'") || !strings.Contains(d.Request.Target, "github.com/o/lea release v") {
				t.Fatalf("%q: %+v", tc.cmd, d)
			}
		})
	}
}

// TestReleaseCreateNotesFile: the notes file is opened once, checked to be a plain file of the
// worktree outside .git, and handed to gh on its stdin, as gh pr create's body file is.
func TestReleaseCreateNotesFile(t *testing.T) {
	d := Decide("Bash", bash("gh release create v0.15.0 --verify-tag --notes-file notes.md"), dev, []Grant{releaseGrant("v0.15.*")}, now, false)
	if d.Verdict != Allow {
		t.Fatalf("verdict %q (%s)", d.Verdict, d.Reason)
	}
	for _, want := range []string{"exec 3< '/p/.lea/worktrees/lea/dev-2/notes.md'", "'--notes-file' '-'", "<&3", "/proc/self/fd/3"} {
		if !strings.Contains(inner(d.Command), want) {
			t.Fatalf("command lacks %q:\n%s", want, d.Command)
		}
	}
}

// TestReleaseCreateHint: an agent whose release-create grant does not cover the call learns the
// form it covers, its tag glob and grants_request; one with no grant learns grants_request.
func TestReleaseCreateHint(t *testing.T) {
	d := Decide("Bash", bash("gh release create v0.16.0 --verify-tag --generate-notes"), dev, []Grant{releaseGrant("v0.15.*")}, now, false)
	for _, want := range []string{"release-create", "gh release create <tag> --verify-tag", "v0.15.*", "grants_request"} {
		if !strings.Contains(d.Hint, want) {
			t.Fatalf("hint lacks %q: %q", want, d.Hint)
		}
	}
	d = Decide("Bash", bash("gh release create v0.16.0 --verify-tag"), dev, []Grant{all(Merge)}, now, false)
	for _, want := range []string{"release-create", "grants_request"} {
		if !strings.Contains(d.Hint, want) {
			t.Fatalf("no-grant hint lacks %q: %q", want, d.Hint)
		}
	}
}

func TestValidTagGlob(t *testing.T) {
	for _, g := range []string{"v0.15.*", "v1.2.3", "v*", "v2.*.*"} {
		if !ValidTagGlob(g) {
			t.Errorf("%q refused", g)
		}
	}
	for _, g := range []string{"0.15.*", "*", "v0.15.?", "v0 15", "v/x", "v..1", "v" + strings.Repeat("1", 70)} {
		if ValidTagGlob(g) {
			t.Errorf("%q accepted", g)
		}
	}
}

// TestPinnedReleaseCreateRuns runs the release-create script against a fake gh: gh runs with
// lea's gh config and placeholder token, pinned to the grant's repository, and gets the notes
// file's content on its stdin only when it is a plain file of the worktree.
func TestPinnedReleaseCreateRuns(t *testing.T) {
	needTools(t)
	if _, err := os.Stat("/usr/bin/realpath"); err != nil {
		t.Skip(err)
	}
	root := t.TempDir()
	wt := filepath.Join(root, ".lea", "worktrees", "lea", "dev-2")
	secret := filepath.Join(root, "secret")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	for p, s := range map[string]string{filepath.Join(wt, "notes.md"): "notes", secret: "token"} {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(secret, filepath.Join(wt, "out.md")); err != nil {
		t.Fatal(err)
	}
	c := dev
	c.Worktree = wt
	for _, tc := range []struct{ args, want string }{
		{"--notes-file notes.md", "release create v0.15.0 --verify-tag --notes-file - -R github.com/o/lea\nnotes"},
		{"--generate-notes", "release create v0.15.0 --verify-tag --generate-notes -R github.com/o/lea\n"},
		{"--notes-file out.md", ""},
	} {
		d := Decide("Bash", bash("gh release create v0.15.0 --verify-tag "+tc.args), c, []Grant{releaseGrant("v0.15.*")}, now, false)
		if d.Verdict != Allow {
			t.Fatalf("%s: %+v", tc.args, d)
		}
		dir := t.TempDir()
		fake := filepath.Join(dir, "gh")
		log := filepath.Join(dir, "log")
		body := "#!/bin/sh\necho \"$GH_TOKEN|$GH_CONFIG_DIR\" >> " + log + ".env\necho \"$*\" >> " + log + "\ncat >> " + log + "\n"
		if err := os.WriteFile(fake, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		script := strings.ReplaceAll(lastWord(t, d.Command), "'"+ghPath+"'", "'"+fake+"'")
		out, err := exec.Command(shPath, "-c", script).CombinedOutput()
		got, _ := os.ReadFile(log)
		if string(got) != tc.want || (err == nil) != (tc.want != "") {
			t.Fatalf("%s: calls %q, want %q, err %v, out %s", tc.args, got, tc.want, err, out)
		}
		if env, _ := os.ReadFile(log + ".env"); tc.want != "" && string(env) != ghPlaceholder+"|"+forgeGHConfig+"\n" {
			t.Fatalf("%s: gh ran with %q", tc.args, env)
		}
	}
}
