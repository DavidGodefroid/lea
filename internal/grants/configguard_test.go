package grants

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// guardRun runs the guard line of a pinned script (its first line) in a clean environment.
func guardRun(t *testing.T, root, script string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, shPath, "-c", strings.SplitN(script, "\n", 2)[0])
	cmd.Env = []string{"PATH=/usr/bin:/bin", "GIT_CONFIG_GLOBAL=/dev/null", "HOME=" + root}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// gitSet adds each key/value pair of kv to dir's repository config.
func gitSet(t *testing.T, dir string, kv []string) {
	t.Helper()
	for i := 0; i+1 < len(kv); i += 2 {
		if out, err := exec.Command(gitPath, "-C", dir, "config", "--add", kv[i], kv[i+1]).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
}

// gitUnset removes the keys of kv (every other word) from dir's repository config.
func gitUnset(dir string, kv []string) {
	for i := 0; i < len(kv); i += 2 {
		exec.Command(gitPath, "-C", dir, "config", "--unset-all", kv[i]).Run()
	}
}

// TestConfigGuardNamesTheKeyOrTheFailure (#413): git config's exit 0 (a key matched: named), 1
// (nothing matched: the call goes on) and 128 (git failed: said so, with git's message) are told
// apart; lea's own keys pass, anything else beside or under them does not.
func TestConfigGuardNamesTheKeyOrTheFailure(t *testing.T) {
	root, wt, _ := fixture(t)
	guard := configGuard(wt, "github.com/someone/lea")
	for _, c := range []struct {
		name string
		kv   []string
		want string // "" passes
	}{
		{"nothing (exit 1)", nil, ""},
		{"hooksPath, overridden by the pins", []string{"core.hooksPath", ".git/hooks"}, ""},
		{"pushurl to the grant's repository", []string{"remote.origin.pushurl", "https://github.com/Someone/Lea"}, ""},
		{"pushurl with .git", []string{"remote.origin.pushurl", "https://github.com/someone/lea.git"}, ""},
		{"lea's keys together", []string{"core.hooksPath", ".git/hooks", "remote.origin.pushurl", "https://github.com/someone/lea/"}, ""},
		{"pushurl elsewhere (exit 0)", []string{"remote.origin.pushurl", "https://evil.example/r"}, "sets remote.origin.pushurl, which"},
		{"one pushurl of two elsewhere", []string{"remote.origin.pushurl", "https://github.com/someone/lea", "remote.origin.pushurl", "https://github.com/someone/lea.evil"}, "sets remote.origin.pushurl,"},
		{"another repository of the owner", []string{"remote.origin.pushurl", "https://github.com/someone/lea2"}, "sets remote.origin.pushurl,"},
		{"another remote's pushurl", []string{"remote.fork.pushurl", "https://github.com/someone/lea"}, "sets remote.fork.pushurl,"},
		{"a risky key beside lea's", []string{"core.hooksPath", ".git/hooks", "filter.x.clean", "touch " + root + "/pwned"}, "sets filter.x.clean,"},
		{"two risky keys", []string{"include.path", "/tmp/x", "core.sshCommand", "touch " + root + "/pwned"}, "sets core.sshcommand include.path,"}, // in the file's order
	} {
		gitSet(t, wt, c.kv)
		out, err := guardRun(t, root, guard)
		if c.want == "" && err != nil {
			t.Errorf("%s: refused: %v %s", c.name, err, out)
		}
		if c.want != "" && (err == nil || !strings.Contains(out, "lea: grant not applied: the repository git config "+c.want)) {
			t.Errorf("%s: %v %q, want %q", c.name, err, out, c.want)
		}
		gitUnset(wt, c.kv)
	}
	// Exit 128: the worktree's gitdir link is broken; nothing in a config redirects anything.
	broken := filepath.Join(root, "broken")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, ".git"), []byte("gitdir: "+filepath.Join(root, "gone")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := guardRun(t, root, configGuard(broken, "github.com/someone/lea"))
	if err == nil || !strings.Contains(out, "lea: grant not applied: git config failed in the worktree: ") || !strings.Contains(out, "fatal:") ||
		strings.Contains(out, "redirects") {
		t.Fatalf("broken worktree: %v %q", err, out)
	}
	notPwned(t, root)
	// No pin: no pushurl passes.
	gitSet(t, wt, []string{"remote.origin.pushurl", "https://github.com/someone/lea"})
	if out, err := guardRun(t, root, configGuard(wt, "")); err == nil || !strings.Contains(out, "sets remote.origin.pushurl,") {
		t.Fatalf("no pin: %v %q", err, out)
	}
	if p := caseGlob("https://github.com/a$b"); p != `""` {
		t.Fatalf("caseGlob kept a character a pin never has: %s", p)
	}
}

// TestPinnedPushSameDecisionInEveryWorktree (#413): two agents, two worktrees of one repository
// (a main one and a linked one sharing its .git/config), the same push: the same decision, both
// with lea's own keys (they pass) and with a risky one (both refused, the key named).
func TestPinnedPushSameDecisionInEveryWorktree(t *testing.T) {
	root, wt, _ := fixture(t)
	other := filepath.Join(root, ".lea", "worktrees", "lea", "dev-3")
	if out, err := exec.Command(gitPath, "-C", wt, "worktree", "add", "-q", "-b", "feat/9-y", other).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	exec.Command(gitPath, "-C", wt, "branch", "feat/73-x").Run()
	_, g := pinnedCaller(root, wt)
	scripts := map[string]string{}
	for _, a := range []struct{ wt, branch string }{{wt, "feat/73-x"}, {other, "feat/9-y"}} {
		c, _ := pinnedCaller(root, a.wt)
		c.Branch = a.branch
		push := Command(Request{Prog: "git", Args: []string{"push", "-u", "origin", a.branch}}, c, g)
		if push == "" {
			t.Fatalf("%s: no pinned push", a.wt)
		}
		scripts[a.wt] = lastWord(t, push)
	}
	for _, kv := range [][]string{
		{"core.hooksPath", ".git/hooks", "remote.origin.pushurl", "https://github.com/Someone/Lea.git"},
		{"url.https://evil.example/.insteadOf", "https://github.com/"},
	} {
		// Set from the linked worktree: it lands in the shared .git/config.
		gitSet(t, other, kv)
		var outs []string
		for _, w := range []string{wt, other} {
			out, err := guardRun(t, root, scripts[w])
			outs = append(outs, fmt.Sprintf("%v %s", err == nil, out))
		}
		if outs[0] != outs[1] {
			t.Fatalf("%v: two decisions: %q vs %q", kv, outs[0], outs[1])
		}
		if pass := len(kv) == 4; pass != strings.HasPrefix(outs[0], "true") {
			t.Fatalf("%v: %q", kv, outs[0])
		}
		if len(kv) == 2 && !strings.Contains(outs[0], "sets url.https://evil.example/.insteadof,") {
			t.Fatalf("refusal does not name the key: %q", outs[0])
		}
		gitUnset(other, kv)
	}
	notPwned(t, root)
}
