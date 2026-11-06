package keyproxy

import (
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestTreeGitPushEndToEnd runs real git pushes from a tree container through the forge
// forwarder to a real git http-backend (#110, re-review B2): what a script's git push sends
// (no grant token) never reaches GitHub, nor does a push to a production branch with any token;
// the pinned push of the agent's own branch with its token does, once.
func TestTreeGitPushEndToEnd(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	r := newTreeRig(t, true)
	r.def = "develop"
	r.prod["develop"] = "release\n"
	root := t.TempDir()
	bare := filepath.Join(root, "someone", "shop.git")
	run := func(dir string, args ...string) (string, error) {
		cmd := exec.Command(git, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
			"HTTP_PROXY=", "HTTPS_PROXY=", "http_proxy=", "https_proxy=", "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@x",
			"GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@x")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := run(root, "init", "-q", "--bare", bare); err != nil {
		t.Fatal(out)
	}
	run(bare, "config", "http.receivepack", "true")
	r.mu.Lock()
	r.git = &cgi.Handler{Path: git, Args: []string{"http-backend"}, Root: "/",
		Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}}
	r.mu.Unlock()
	wt := t.TempDir()
	run(wt, "init", "-q", "-b", "feat/x")
	os.WriteFile(filepath.Join(wt, "f"), []byte("x"), 0o644)
	run(wt, "add", "f")
	if out, err := run(wt, "commit", "-qm", "x"); err != nil {
		t.Fatal(out)
	}
	url := r.fw.URL + "/someone/shop.git"
	push := func(tok, dst string) (string, error) {
		args := []string{"-c", "http.extraHeader=Host: lea-proxy:4401"}
		if tok != "" {
			args = append(args, "-c", "http.extraHeader="+GrantHeader+": "+tok)
		}
		return run(wt, append(args, "push", url, "HEAD:refs/heads/"+dst)...)
	}
	remote := func(ref string) bool {
		_, err := run(bare, "rev-parse", "--verify", "-q", ref)
		return err == nil
	}
	// A script's push: no token.
	if out, err := push("", "feat/x"); err == nil || remote("refs/heads/feat/x") {
		t.Fatalf("a push without a token went through: %s", out)
	}
	// A production branch, with a token for it (the daemon never mints one for it anyway).
	for _, b := range []string{"main", "develop", "release"} {
		if out, err := push(r.pushGrant(t, "refs/heads/"+b), b); err == nil || remote("refs/heads/"+b) {
			t.Fatalf("a push to %s went through: %s", b, out)
		}
	}
	// Another ref's token.
	if out, err := push(r.pushGrant(t, "refs/heads/feat/y"), "feat/x"); err == nil || remote("refs/heads/feat/x") {
		t.Fatalf("a push with another ref's token went through: %s", out)
	}
	tok := r.pushGrant(t, "refs/heads/feat/x")
	if out, err := push(tok, "feat/x"); err != nil || !remote("refs/heads/feat/x") {
		t.Fatalf("the pinned push failed: %v %s", err, out)
	}
	// Its token again, for a new commit.
	os.WriteFile(filepath.Join(wt, "f"), []byte("y"), 0o644)
	run(wt, "commit", "-qam", "y")
	before, _ := run(bare, "rev-parse", "refs/heads/feat/x")
	if out, err := push(tok, "feat/x"); err == nil {
		t.Fatalf("a replayed token went through: %s", out)
	}
	if after, _ := run(bare, "rev-parse", "refs/heads/feat/x"); after != before {
		t.Fatal("the replay moved the branch")
	}
}
