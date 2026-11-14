package daemon

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DavidGodefroid/lea/internal/store"
)

// A lea agent (the real Claude Code with lea's hooks) with a push grant (which carries commit-identity) runs a plain
// git commit in its own worktree: lea's hook rewrites it to the pinned commit script, and Claude
// Code runs that script without asking (#393). The script used to clean up with rm -rf, which
// Claude Code's own safety check refuses even when a hook allowed the call: the commit waited
// for an answer nobody gave.
func TestClaudeGrantedCommitRunsWithoutAsking(t *testing.T) {
	e := newClaudeE2E(t)
	if _, err := os.Lstat(managedSettingsPath); err == nil {
		// In a lea agent's container: its managed settings send the hooks to the agent's own bus.
		t.Skipf("%s exists: Claude Code's hooks would not reach this test's daemon", managedSettingsPath)
	}
	e.setLimited(false)
	wt := filepath.Join(e.dir, ".lea", "worktrees", "fixture", "dev")
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", wt}, args...)...)
		c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %q: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	git("init", "-q", "-b", "feat/x")
	git("config", "user.name", "t")
	git("config", "user.email", "t@t")
	os.WriteFile(filepath.Join(wt, "a"), []byte("a\n"), 0o644)
	git("add", "a")
	git("commit", "-qm", "init")
	os.WriteFile(filepath.Join(wt, "a"), []byte("b\n"), 0o644)
	git("add", "a")
	// Claude Code trusts the worktree, as it does lea's.
	cfg := filepath.Join(e.dir, "cfg", ".claude.json")
	var seed map[string]any
	if b, err := os.ReadFile(cfg); err != nil || json.Unmarshal(b, &seed) != nil {
		t.Fatalf("seed: %v", err)
	}
	seed["projects"].(map[string]any)[wt] = map[string]any{"hasTrustDialogAccepted": true, "hasCompletedProjectOnboarding": true}
	b, _ := json.Marshal(seed)
	os.WriteFile(cfg, b, 0o600)
	e.wd = wt

	if _, err := e.st.Register(store.NewAgent{Container: "lea-a", Name: "dev", Profile: "claude", Workdir: e.dir, Role: "dev",
		Repo: "fixture", Branch: "feat/x"}, store.Caps{}, 1); err != nil {
		t.Fatal(err)
	}
	set, _ := json.Marshal(map[string]any{"container": "lea-a", "capabilities": []string{"push"},
		"repos": []map[string]string{{"repo": "fixture", "pin": "o/fixture"}}})
	if _, err := e.d.allowSet(set); err != nil {
		t.Fatal(err)
	}
	cmd, _ := json.Marshal(map[string]string{"command": `git commit -m "granted e2e"`, "description": "commit"})
	e.mu.Lock()
	e.answer = func(body string) (string, string) {
		if strings.Contains(body, `"tool_result"`) {
			return "", ""
		}
		return "Bash", string(cmd)
	}
	e.mu.Unlock()
	// As the agents' panes run it: a permission lea does not answer waits for the user.
	e.env = append(e.env, "LEA_APPROVALS=deck", "LEA_APPROVAL_TIMEOUT=120")
	e.run("--permission-mode", "default", "commit it")

	res := e.toolResult("the tool result of the granted commit", 0)
	if !strings.Contains(res, "[lea grant] committed") {
		t.Fatalf("the granted commit did not run:\n%.3000s\n%s", res, e.out.String())
	}
	if s := git("log", "-1", "--format=%s"); s != "granted e2e" {
		t.Fatalf("HEAD is %q", s)
	}
	if a := git("show", "HEAD:a"); a != "b" {
		t.Fatalf("committed a = %q", a)
	}
}
