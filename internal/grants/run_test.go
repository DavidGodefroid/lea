package grants

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// hostile is a shell prelude as an agent could plant in Claude's shell snapshot: every name the
// pinned command might look up runs the agent's code instead.
const hostile = `test(){ touch "$PWNED"; return 0; }
git(){ touch "$PWNED"; }
gh(){ touch "$PWNED"; }
env(){ touch "$PWNED"; }
alias env='touch "$PWNED"'
shopt -s expand_aliases
`

func needTools(t *testing.T) {
	for _, p := range []string{gitPath, envPath, shPath, mktempPath, rmPath, "/bin/bash"} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("%s: %v", p, err)
		}
	}
}

// fixture is a repository at <root>/.lea/worktrees/lea/dev-2 with a staged file, a planted
// pre-commit hook and a clean filter that would touch <root>/pwned.
func fixture(t *testing.T) (root, wt string, run func(dir, script string) (string, error)) {
	needTools(t)
	root = t.TempDir()
	wt = filepath.Join(root, ".lea", "worktrees", "lea", "dev-2")
	pwned := filepath.Join(root, "pwned")
	run = func(dir, script string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "/bin/bash", "-c", hostile+script)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "PWNED="+pwned, "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	setup := exec.Command("/bin/sh", "-c", "git init -q "+wt+" && cd "+wt+" && git config user.name t && git config user.email t@t"+
		" && git remote add origin https://github.com/Someone/Lea.git && echo a > a && git add a && git commit -qm init"+
		" && echo b > a && git add a && printf '* filter=x\\n' > .gitattributes"+
		" && printf '#!/bin/sh\\ntouch "+pwned+"\\n' > .git/hooks/pre-commit && chmod +x .git/hooks/pre-commit")
	setup.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := setup.CombinedOutput(); err != nil {
		t.Fatalf("setup: %v %s", err, out)
	}
	return root, wt, run
}

func pinnedCaller(root, wt string) (Caller, Grant) {
	c := dev
	c.Worktree, c.Env = wt, map[string]string{"HOME": root}
	g := all(CommitIdentity)
	g.GitHub = "github.com/someone/lea"
	return c, g
}

func notPwned(t *testing.T, root string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, "pwned")); err == nil {
		t.Fatal("the agent's code ran")
	}
}

// TestPinnedCommitIgnoresTheAgentsShell: the pinned commit runs in a fresh shell (the agent's
// functions and aliases never run) and through plumbing (the planted hook never runs).
func TestPinnedCommitIgnoresTheAgentsShell(t *testing.T) {
	root, wt, run := fixture(t)
	c, g := pinnedCaller(root, wt)
	// The hostile shell is live: the agent's own "git" runs its code.
	run(root, "git status")
	if _, err := os.Stat(filepath.Join(root, "pwned")); err != nil {
		t.Fatal("the hostile prelude is not active")
	}
	os.Remove(filepath.Join(root, "pwned"))
	commit := Command(Request{Prog: "git", Args: []string{"commit", "-m", "it's"}}, c, g)
	if out, err := run(root, commit); err != nil || !strings.Contains(out, "[lea grant] committed") {
		t.Fatalf("pinned commit: %v %s", err, out)
	}
	notPwned(t, root)
	if out, _ := exec.Command(gitPath, "-C", wt, "log", "-1", "--format=%s").Output(); strings.TrimSpace(string(out)) != "it's" {
		t.Fatalf("log %q", out)
	}
}

// TestPinnedCommitRunsNoFilterWhileConfigChanges is the reviewer's TOCTOU exploit: a loop toggles
// a clean filter in .git/config while commits run. The guard may refuse some; none runs it.
func TestPinnedCommitRunsNoFilterWhileConfigChanges(t *testing.T) {
	root, wt, run := fixture(t)
	c, g := pinnedCaller(root, wt)
	commit := Command(Request{Prog: "git", Args: []string{"commit", "-m", "x"}}, c, g)
	if err := os.WriteFile(filepath.Join(wt, "a"), []byte("c\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		cfg := filepath.Join(wt, ".git", "config")
		base, _ := os.ReadFile(cfg)
		evil := append(append([]byte{}, base...), []byte("[filter \"x\"]\n\tclean = touch "+filepath.Join(root, "pwned")+"\n")...)
		for i := 0; ; i++ {
			select {
			case <-stop:
				os.WriteFile(cfg, base, 0o644)
				return
			default:
			}
			if i%2 == 0 {
				os.WriteFile(cfg, evil, 0o644)
			} else {
				os.WriteFile(cfg, base, 0o644)
			}
		}
	}()
	for i := 0; i < 20; i++ {
		run(root, commit)
	}
	close(stop)
	wg.Wait()
	notPwned(t, root)
}

// TestPinnedCommitRefusesARiskyConfig: a redirecting or program-running key stops the call.
func TestPinnedCommitRefusesARiskyConfig(t *testing.T) {
	root, wt, run := fixture(t)
	c, g := pinnedCaller(root, wt)
	commit := Command(Request{Prog: "git", Args: []string{"commit", "-m", "x"}}, c, g)
	for _, kv := range [][2]string{{"url.https://evil.example/.insteadOf", "https://github.com/"},
		{"include.path", "/tmp/x"}, {"filter.x.clean", "touch " + root + "/pwned"}, {"remote.origin.pushurl", "https://evil.example/r"},
		{"credential.https://github.com.helper", "!touch " + root + "/pwned"}, {"remote.origin.push", "refs/heads/x:refs/heads/main"},
		{"remote.origin.mirror", "true"}, {"http.sslCAInfo", "/tmp/evil-ca.pem"}, {"http.https://github.com/.curloptResolve", "github.com:443:1.2.3.4"},
		{"http.cookieFile", "/tmp/c"}} {
		if out, err := exec.Command(gitPath, "-C", wt, "config", kv[0], kv[1]).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
		if out, err := run(root, commit); err == nil || !strings.Contains(out, "lea: grant not applied") {
			t.Fatalf("%s: commit ran: %v %s", kv[0], err, out)
		}
		exec.Command(gitPath, "-C", wt, "config", "--unset-all", kv[0]).Run()
	}
	notPwned(t, root)
}

// TestPinnedPushNeverReadsTheRepositoryConfig: the push runs from a bare repository of lea's,
// so a url.insteadOf in the worktree's config (planted after the guard) cannot send it to the
// agent's repository; it names the grant's URL and the full ref.
func TestPinnedPushNeverReadsTheRepositoryConfig(t *testing.T) {
	root, wt, _ := fixture(t)
	c, g := pinnedCaller(root, wt)
	evil := filepath.Join(root, "evil.git")
	if out, err := exec.Command(gitPath, "init", "-q", "--bare", evil).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	push := Command(Request{Prog: "git", Args: []string{"push", "-u", "origin", "feat/73-x"}}, c, g)
	if !strings.Contains(push, "github.com/someone/lea.git") || !strings.Contains(push, "refs/heads/feat/73-x") || !strings.Contains(push, "--git-dir") {
		t.Fatalf("push %s", push)
	}
	exec.Command(gitPath, "-C", wt, "branch", "feat/73-x").Run()
	// The guard ran on a clean config; the agent plants the redirection right after it.
	script := strings.SplitN(lastWord(t, push), "\n", 2)[1]
	if out, err := exec.Command(gitPath, "-C", wt, "config", "url."+evil+".insteadOf", "https://github.com/").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	env := []string{"PATH=/usr/bin:/bin", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "HOME=" + root}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, shPath, "-c", script)
	cmd.Env = env
	cmd.CombinedOutput() // reaches github.com or fails offline: either way not the agent's repo
	if out, _ := exec.Command(gitPath, "--git-dir", evil, "for-each-ref").Output(); len(out) != 0 {
		t.Fatalf("the push went to the agent's repository: %s", out)
	}
	notPwned(t, root)
	for _, bad := range [][]string{{"push", "origin", "main"}, {"push", "origin"}, {"push", "-u", "--delete", "origin", "x"}} {
		if cmd := Command(Request{Prog: "git", Args: bad}, c, g); cmd != "" {
			t.Fatalf("%v: %s", bad, cmd)
		}
	}
}

// lastWord is the script an outer pinned command hands to /bin/sh -c, as the shell reads it.
func lastWord(t *testing.T, outer string) string {
	t.Helper()
	out, err := exec.Command("/bin/sh", "-c", "set -- "+outer+`; eval "s=\${$#}"; printf %s "$s"`).Output()
	if err != nil {
		t.Fatalf("read %s: %v", outer, err)
	}
	return string(out)
}
