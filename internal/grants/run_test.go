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
	// A push names the grant's repository and the full ref, whatever origin says.
	push := Command(Request{Action: CommitIdentity, Prog: "git", Args: []string{"push", "-u", "origin", "feat/73-x"}}, c, g)
	if !strings.Contains(push, `'push' '-u' 'https://github.com/someone/lea.git' 'refs/heads/feat/73-x:refs/heads/feat/73-x'`) {
		t.Fatalf("push %s", push)
	}
	del := Command(Request{Action: DeleteOwnBranch, Prog: "git", Args: []string{"push", "origin", "--delete", "feat/73-x"}}, c, g)
	if !strings.Contains(del, `'push' 'https://github.com/someone/lea.git' '--delete' 'refs/heads/feat/73-x'`) {
		t.Fatalf("delete %s", del)
	}
	for _, bad := range [][]string{{"push", "origin", "main"}, {"push", "origin"}, {"push", "-u", "--delete", "origin", "x"}} {
		if cmd := Command(Request{Prog: "git", Args: bad}, c, g); cmd != "" {
			t.Fatalf("%v: %s", bad, cmd)
		}
	}
	// A redirecting or program-running key in the repository config stops every pinned call.
	for _, kv := range [][2]string{{"url.https://evil.example/.insteadOf", "https://github.com/"},
		{"include.path", "/tmp/x"}, {"filter.x.clean", "touch " + root + "/pwned"}, {"remote.origin.pushurl", "https://evil.example/r"},
		{"credential.https://github.com.helper", "!touch " + root + "/pwned"}, {"remote.origin.push", "refs/heads/x:refs/heads/main"},
		{"remote.origin.mirror", "true"}} {
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
