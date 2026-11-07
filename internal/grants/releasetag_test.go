package grants

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestReleaseTagDecide: a tag push is a hard deny, but for the one form a release-tag grant covers
// (#342): git push origin refs/tags/vX.Y.Z, one new semver tag, plainly, from the caller's own
// worktree, on the grant's repository.
func TestReleaseTagDecide(t *testing.T) {
	tag := all(ReleaseTag)
	tag.Branch = ""
	every := []Grant{tag, all(Merge), all(CommitIdentity)}
	away := dev
	away.OwnWorktree, away.Worktree = false, ""
	other := dev
	other.Repo = "other"
	cases := []struct {
		name, cmd, want string
		c               Caller
		gs              []Grant
	}{
		{"granted", "git push origin refs/tags/v1.2.3", Allow, dev, every},
		{"granted pre-release", "git push origin refs/tags/v1.2.3-rc.1", Allow, dev, every},
		{"no grant", "git push origin refs/tags/v1.2.3", Deny, dev, []Grant{all(Merge), all(CommitIdentity)}},
		{"other repo", "git push origin refs/tags/v1.2.3", Deny, other, every},
		{"away from the worktree", "git push origin refs/tags/v1.2.3", Deny, away, every},
		{"bare tag name", "git push origin v1.2.3", Deny, dev, every},
		{"tags", "git push --tags origin", Deny, dev, every},
		{"follow tags", "git push --follow-tags origin feat/73-x", Deny, dev, every},
		{"force", "git push -f origin refs/tags/v1.2.3", Deny, dev, every},
		{"force refspec", "git push origin +refs/tags/v1.2.3", Deny, dev, every},
		{"force flag", "git push --force origin refs/tags/v1.2.3", Deny, dev, every},
		{"two tags", "git push origin refs/tags/v1.2.3 refs/tags/v1.2.4", Deny, dev, every},
		{"refspec", "git push origin HEAD:refs/tags/v1.2.3", Deny, dev, every},
		{"not semver", "git push origin refs/tags/release-1", Deny, dev, every},
		{"leading zero", "git push origin refs/tags/v01.2.3", Deny, dev, every},
		{"no v", "git push origin refs/tags/1.2.3", Deny, dev, every},
		{"build metadata", "git push origin refs/tags/v1.2.3+b1", Deny, dev, every},
		{"other remote", "git push upstream refs/tags/v1.2.3", Deny, dev, every},
		{"delete", "git push origin --delete refs/tags/v1.2.3", Deny, dev, every},
		{"delete refspec", "git push origin :refs/tags/v1.2.3", Deny, dev, every},
		{"chained", "git push origin refs/tags/v1.2.3 && true", Deny, dev, every},
		{"env prefix", "GIT_TRACE=1 git push origin refs/tags/v1.2.3", Deny, dev, every},
		{"git option", "git -C /x push origin refs/tags/v1.2.3", Deny, dev, every},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := Decide("Bash", bash(tc.cmd), tc.c, tc.gs, now, false)
			if d.Verdict != tc.want {
				t.Fatalf("%q: verdict %q (%s), want %q", tc.cmd, d.Verdict, d.Reason, tc.want)
			}
			if d.Verdict == Allow && (d.Request.Action != ReleaseTag || !strings.Contains(d.Command, "refs/tags/v1.2.3") ||
				!strings.Contains(d.Command, "merge-base --is-ancestor") || strings.Contains(d.Command, "--force")) {
				t.Fatalf("%q: %+v", tc.cmd, d)
			}
		})
	}
	// The deny names the way to a grant.
	if d := Decide("Bash", bash("git push origin refs/tags/v1.2.3"), dev, nil, now, false); !strings.Contains(d.Reason, "grants_request") {
		t.Fatalf("no hint in %q", d.Reason)
	}
	// release-tag is the user's own choice: lea delivery never records it.
	for _, a := range DeliveryActions {
		if a == ReleaseTag {
			t.Fatal("release-tag is a delivery action")
		}
	}
}

// TestPinnedReleaseTag runs the pinned tag push against a local remote: a new tag on a commit of
// the remote's default branch goes (annotated as its tag object); a tag on a commit the default
// branch lacks, or one the remote already has, does not.
func TestPinnedReleaseTag(t *testing.T) {
	root, wt, _ := fixture(t)
	c, g := pinnedCaller(root, wt)
	g.Action = ReleaseTag
	remote := filepath.Join(root, "remote.git")
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(gitPath, args...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "GIT_CONFIG_GLOBAL=/dev/null", "HOME=" + root}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "--bare", remote)
	git("-C", wt, "push", "-q", remote, "HEAD:refs/heads/main")
	git("--git-dir", remote, "symbolic-ref", "HEAD", "refs/heads/main")
	side := git("-C", wt, "commit-tree", "HEAD^{tree}", "-p", "HEAD", "-m", "side")
	git("-C", wt, "tag", "v1.0.0")
	git("-C", wt, "tag", "-a", "-m", "release", "v1.1.0")
	git("-C", wt, "tag", "v2.0.0", side)
	push := func(tag string) (string, error) {
		d := Decide("Bash", bash("git push origin refs/tags/"+tag), c, []Grant{g}, now, false)
		if d.Verdict != Allow {
			t.Fatalf("%s: %+v", tag, d)
		}
		// The test's remote is a local path: the forwarder's URL and transport give way to it.
		s := strings.ReplaceAll(lastWord(t, d.Command), "http://lea-proxy:4401/someone/lea.git", remote)
		s = strings.ReplaceAll(s, "'protocol.allow=never'", "'protocol.allow=always'")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, shPath, "-c", s)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "HOME=" + root}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := push("v1.0.0"); err != nil {
		t.Fatalf("v1.0.0: %v %s", err, out)
	}
	if out, err := push("v1.1.0"); err != nil {
		t.Fatalf("v1.1.0: %v %s", err, out)
	}
	if typ := git("--git-dir", remote, "cat-file", "-t", "refs/tags/v1.1.0"); typ != "tag" {
		t.Fatalf("annotated tag went as a %s", typ)
	}
	out, err := push("v2.0.0")
	if err == nil || !strings.Contains(out, "not on the default branch") {
		t.Fatalf("a tag off the default branch went: %v %s", err, out)
	}
	if refs := git("--git-dir", remote, "for-each-ref", "refs/tags/v2.0.0"); refs != "" {
		t.Fatalf("remote has %s", refs)
	}
	// The tag exists: moving it on the remote needs a force push, which lea never runs.
	git("-C", wt, "tag", "-f", "-a", "-m", "again", "v1.0.0")
	if out, err := push("v1.0.0"); err == nil {
		t.Fatalf("an existing tag was moved: %s", out)
	}
	if got, want := git("--git-dir", remote, "rev-parse", "refs/tags/v1.0.0"), git("-C", wt, "rev-parse", "HEAD"); got != want {
		t.Fatalf("remote v1.0.0 = %s, want %s", got, want)
	}
	notPwned(t, root)
}
