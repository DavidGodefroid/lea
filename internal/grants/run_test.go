package grants

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestPinnedGitRuns runs the pinned commands on a real repository: a planted hook never runs; a
// repository config that redirects or runs programs, or an origin that is not the grant's
// repository, stops the call before git does anything.
func TestPinnedGitRuns(t *testing.T) {
	for _, p := range []string{gitPath, envPath, "/bin/sh"} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("%s: %v", p, err)
		}
	}
	root := t.TempDir()
	wt := filepath.Join(root, ".lea", "worktrees", "lea", "dev-2")
	sh := func(dir, script string) (string, error) {
		cmd := exec.Command("/bin/sh", "-c", script)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := sh(root, "git init -q "+wt+" && cd "+wt+" && git config user.name t && git config user.email t@t"+
		" && git remote add origin https://github.com/Someone/Lea.git && echo a > a && git add a"+
		" && printf '#!/bin/sh\\ntouch "+root+"/pwned\\n' > .git/hooks/pre-commit && chmod +x .git/hooks/pre-commit"); err != nil {
		t.Fatalf("setup: %v %s", err, out)
	}
	c := dev
	c.Worktree, c.Env = wt, map[string]string{"HOME": root}
	g := all(CommitIdentity)
	g.GitHub = "github.com/someone/lea"
	commit := Command(Request{Action: CommitIdentity, Prog: "git", Args: []string{"commit", "-m", "it's"}}, c, g)
	if out, err := sh(root, commit); err != nil {
		t.Fatalf("pinned commit: %v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(root, "pwned")); err == nil {
		t.Fatal("the planted pre-commit hook ran")
	}
	if out, _ := sh(wt, "git log --format=%s"); strings.TrimSpace(out) != "it's" {
		t.Fatalf("log %q", out)
	}
	// Origin is the grant's repository (any case): the guard lets the push through to git, which
	// then fails offline; another origin stops it in the guard.
	push := Command(Request{Action: CommitIdentity, Prog: "git", Args: []string{"push", "origin", "feat/73-x"}}, c, g)
	g2 := g
	g2.GitHub = "github.com/other/lea"
	other := Command(Request{Action: CommitIdentity, Prog: "git", Args: []string{"push", "origin", "feat/73-x"}}, c, g2)
	if out, err := sh(root, other); err == nil || !strings.Contains(out, "lea: grant not applied") {
		t.Fatalf("push to an origin that is not the grant's: %v %s", err, out)
	}
	if out, _ := sh(root, push); strings.Contains(out, "lea: grant not applied") {
		t.Fatalf("push to the grant's origin stopped by the guard: %s", out)
	}
	// A redirecting or program-running key in the repository config stops every pinned call.
	for _, kv := range [][2]string{{"url.https://evil.example/.insteadOf", "https://github.com/"},
		{"include.path", "/tmp/x"}, {"filter.x.clean", "touch " + root + "/pwned"}, {"remote.origin.pushurl", "https://evil.example/r"},
		{"credential.https://github.com.helper", "!touch " + root + "/pwned"}} {
		if out, err := sh(wt, "git config '"+kv[0]+"' '"+kv[1]+"'"); err != nil {
			t.Fatalf("%v %s", err, out)
		}
		if out, err := sh(root, commit); err == nil || !strings.Contains(out, "lea: grant not applied") {
			t.Fatalf("%s: commit ran: %v %s", kv[0], err, out)
		}
		if out, err := sh(wt, "git config --unset-all '"+kv[0]+"'"); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "pwned")); err == nil {
		t.Fatal("a planted program ran")
	}
}
