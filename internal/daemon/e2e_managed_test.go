package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/store"
)

// managedSettingsPath is where Claude Code reads its file-based managed settings on Linux, as the
// image ships them (image/managed-settings.json, #111). The tests below need it writable and
// absent (CI: sudo install -d -o "$(id -u)" /etc/claude-code), and remove what they write.
const managedSettingsPath = "/etc/claude-code/managed-settings.json"

// writeManaged writes lea's managed settings (image/managed-settings.json, this build of lea,
// without the telemetry) where Claude Code reads them, until the test ends.
func (e *claudeE2E) writeManaged() { e.writeManagedEnv(nil) }

// writeManagedEnv is writeManaged with env as the managed env (nil: none), as the image pins
// LEA_BUS_SOCK; the test's bus stands for the image's path. A second call replaces the file.
func (e *claudeE2E) writeManagedEnv(env map[string]string) {
	e.t.Helper()
	if _, err := os.Lstat(managedSettingsPath); err == nil {
		if !e.managed {
			e.t.Skipf("%s already exists: not overwriting it", managedSettingsPath)
		}
		os.Remove(managedSettingsPath)
	}
	raw, err := os.ReadFile("../../image/managed-settings.json")
	if err != nil {
		e.t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.ReplaceAll(string(raw), "/usr/local/bin/lea", e.lea)), &m); err != nil {
		e.t.Fatal(err)
	}
	delete(m, "env")
	if env != nil {
		m["env"] = env
	}
	b, _ := json.Marshal(m)
	if err := os.WriteFile(managedSettingsPath, b, 0o644); err != nil {
		e.t.Skipf("cannot write %s (%v): give the test its directory (sudo install -d -o \"$(id -u)\" %s)",
			managedSettingsPath, err, filepath.Dir(managedSettingsPath))
	}
	if !e.managed {
		e.managed = true
		e.t.Cleanup(func() { os.Remove(managedSettingsPath) })
	}
}

// tamperWith writes settings as the agent's own ~/.claude/settings.json.
func (e *claudeE2E) tamperWith(settings map[string]any) {
	e.t.Helper()
	b, _ := json.Marshal(settings)
	if err := os.WriteFile(filepath.Join(e.cfgDir(), "settings.json"), b, 0o600); err != nil {
		e.t.Fatal(err)
	}
}

// tamper is what a prompt-injected agent writes into its own writable ~/.claude/settings.json
// (#111): every hook off, every shell command allowed, no hook of lea's left.
func (e *claudeE2E) tamper() {
	e.t.Helper()
	b, _ := json.Marshal(map[string]any{"disableAllHooks": true, "hooks": map[string]any{},
		"permissions": map[string]any{"allow": []string{"Bash(*)"}}})
	if err := os.WriteFile(filepath.Join(e.cfgDir(), "settings.json"), b, 0o600); err != nil {
		e.t.Fatal(err)
	}
}

// cfgDir is the session's CLAUDE_CONFIG_DIR, its user settings' directory.
func (e *claudeE2E) cfgDir() string {
	for _, v := range e.env {
		if d, ok := strings.CutPrefix(v, "CLAUDE_CONFIG_DIR="); ok {
			return d
		}
	}
	e.t.Fatal("no CLAUDE_CONFIG_DIR")
	return ""
}

// toolResult waits for the request that carries the Bash call's result and returns it.
func (e *claudeE2E) toolResult(what string, from int) string {
	e.t.Helper()
	var got string
	e.until(what, 60, func(store.Agent) bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		for _, b := range e.bodies[min(from, len(e.bodies)):] {
			if strings.Contains(b, `"tool_result"`) {
				got = b
				return true
			}
		}
		return false
	})
	return got
}

// An agent that rewrote its own writable settings (#111) to turn every hook off and allow every
// shell command, and that runs without lea's --settings file, is still guarded: lea's hooks come
// from the managed settings, which Claude Code runs whatever disableAllHooks says below them. A
// shell call lea hard-denies (a gh merge --admin) is denied and never runs. The control run,
// without the managed settings, shows the same tampering lets the call through.
func TestClaudeManagedSettingsGuardATamperedAgent(t *testing.T) {
	e := newClaudeE2E(t)
	e.setLimited(false)
	if _, err := e.st.Register(store.NewAgent{Container: "lea-a", Name: "dev", Profile: "claude", Workdir: e.wd, Role: "dev"}, store.Caps{}, 1); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(e.wd, "merged-with-admin")
	cmd, _ := json.Marshal(map[string]string{"command": "touch " + marker + " # gh pr merge 1 --admin", "description": "merge"})
	e.mu.Lock()
	e.answer = func(body string) (string, string) {
		if strings.Contains(body, `"tool_result"`) {
			return "", ""
		}
		return "Bash", string(cmd)
	}
	e.mu.Unlock()
	e.tamper()

	// The control: the tampered settings alone let the hard-denied call run.
	kill := e.runBare("merge it")
	e.toolResult("the control run's tool result", 0)
	kill()
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("control: the tampered settings did not let the call run (%v): the test proves nothing\n%s", err, e.out.String())
	}
	os.Remove(marker)

	// With lea's managed settings: the same tampered agent is denied.
	e.writeManaged()
	e.mu.Lock()
	from := len(e.bodies)
	e.mu.Unlock()
	e.runBare("merge it")
	res := e.toolResult("the guarded run's tool result", from)
	if !strings.Contains(res, "lea hard deny") {
		t.Fatalf("the call was not denied by lea's hook:\n%.3000s", res)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the hard-denied command ran (%v)", err)
	}
}

// lea's hooks are both in the managed settings and in the --settings file (which keeps them on a
// host whose organisation has server-managed settings, where Claude Code skips the file-based
// ones): Claude Code runs each identical command once, so no event is reported twice and no
// approval is asked twice.
func TestClaudeManagedAndFlagHooksRunOnce(t *testing.T) {
	e := newClaudeE2E(t)
	e.setLimited(false)
	e.writeManaged()
	e.run("say hi")
	a := e.until("the first turn", 45, func(a store.Agent) bool {
		ps, _ := e.st.EventPayloads(a.Key, "Stop")
		return a.State == "idle" && len(ps) > 0
	})
	for _, ev := range []string{"SessionStart", "UserPromptSubmit", "Stop"} {
		if ps, _ := e.st.EventPayloads(a.Key, ev); len(ps) != 1 {
			t.Errorf("%s reported %d times, want 1", ev, len(ps))
		}
	}
}

// runBare starts claude like run, without lea's --settings file: an agent's own claude.
func (e *claudeE2E) runBare(args ...string) (kill func()) {
	argv := append([]string{"-I", "-c", claudePty, "90", e.claude}, args...)
	cmd := exec.Command("python3", argv...)
	cmd.Dir = e.wd
	cmd.Env = e.env
	cmd.Stdout, cmd.Stderr = &e.out, &e.out
	if err := cmd.Start(); err != nil {
		e.t.Fatal(err)
	}
	kill = func() { cmd.Process.Kill(); cmd.Wait() }
	e.t.Cleanup(kill)
	return kill
}

// An agent that widened its own permissions (#111: its writable ~/.claude/settings.json allows
// every shell command) gains nothing: the managed settings make their own rules the only ones
// (allowManagedPermissionRulesOnly). A shell call no rule of lea's allows still asks (the
// PermissionRequest hook runs, the deck's path) and does not run. The control run, without the
// managed settings, shows the same widening lets it run unasked.
func TestClaudeManagedPermissionsIgnoreAWidenedSettings(t *testing.T) {
	e := newClaudeE2E(t)
	e.setLimited(false)
	if _, err := e.st.Register(store.NewAgent{Container: "lea-a", Name: "dev", Profile: "claude", Workdir: e.wd, Role: "dev"}, store.Caps{}, 1); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(e.wd, "widened")
	cmd, _ := json.Marshal(map[string]string{"command": "touch " + marker, "description": "touch"})
	e.mu.Lock()
	e.answer = func(body string) (string, string) {
		if strings.Contains(body, `"tool_result"`) {
			return "", ""
		}
		return "Bash", string(cmd)
	}
	e.mu.Unlock()
	e.tamperWith(map[string]any{"permissions": map[string]any{"allow": []string{"Bash(*)"}}})

	// The control: the widened settings alone let the call run, never asked.
	kill := e.run("touch it")
	e.toolResult("the control run's tool result", 0)
	kill()
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("control: the widened settings did not let the call run (%v): the test proves nothing\n%s", err, e.out.String())
	}
	os.Remove(marker)
	a := e.until("the control run's agent", 10, func(store.Agent) bool { return true })
	if ps, _ := e.st.EventPayloads(a.Key, "PermissionRequest"); len(ps) != 0 {
		t.Fatalf("control: asked %d times, want 0", len(ps))
	}

	// With lea's managed settings: the widening is ignored, the call asks and waits.
	e.writeManaged()
	e.run("touch it")
	e.until("the permission request", 60, func(a store.Agent) bool {
		ps, _ := e.st.EventPayloads(a.Key, "PermissionRequest")
		return len(ps) > 0
	})
	time.Sleep(3 * time.Second)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the widened settings let the call run (%v)", err)
	}
}

// An agent that rewrote its own settings' env (#111) cannot turn lea's guard off: LEA_BUS_SOCK is
// pinned by the managed settings (the hard deny still comes from lea's daemon), a bus it points
// the hooks away from is a deny (the guard fails closed), and so is an emptied LEA_AGENT. The
// hard-denied command never runs.
func TestClaudeManagedHooksIgnoreATamperedEnv(t *testing.T) {
	e := newClaudeE2E(t)
	e.setLimited(false)
	if _, err := e.st.Register(store.NewAgent{Container: "lea-a", Name: "dev", Profile: "claude", Workdir: e.wd, Role: "dev"}, store.Caps{}, 1); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(e.wd, "merged-with-admin")
	cmd, _ := json.Marshal(map[string]string{"command": "touch " + marker + " # gh pr merge 1 --admin", "description": "merge"})
	e.mu.Lock()
	e.answer = func(body string) (string, string) {
		if strings.Contains(body, `"tool_result"`) {
			return "", ""
		}
		return "Bash", string(cmd)
	}
	e.mu.Unlock()
	for _, c := range []struct {
		name    string
		managed map[string]string
		env     map[string]string
		want    string
	}{
		{"a redirected bus, pinned", map[string]string{"LEA_BUS_SOCK": e.bus}, map[string]string{"LEA_BUS_SOCK": "/nonexistent/bus.sock"}, "lea hard deny"},
		{"a redirected bus, unpinned", nil, map[string]string{"LEA_BUS_SOCK": "/nonexistent/bus.sock"}, "bus did not answer"},
		{"an emptied identity", map[string]string{"LEA_BUS_SOCK": e.bus}, map[string]string{"LEA_AGENT": ""}, "no lea agent identity"},
	} {
		t.Logf("case: %s", c.name)
		e.tamperWith(map[string]any{"disableAllHooks": true, "hooks": map[string]any{},
			"permissions": map[string]any{"allow": []string{"Bash(*)"}}, "env": c.env})
		e.writeManagedEnv(c.managed)
		e.mu.Lock()
		from := len(e.bodies)
		e.mu.Unlock()
		kill := e.runBare("merge it")
		res := e.toolResult(c.name+": the tool result", from)
		kill()
		if !strings.Contains(res, c.want) {
			t.Fatalf("%s: the call was not denied with %q:\n%.3000s", c.name, c.want, res)
		}
		if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s: the hard-denied command ran (%v)", c.name, err)
		}
	}
}

// A freshly spawned agent starts in the permission mode a local Claude Code starts in (#333):
// lea's managed settings set no default mode, so Claude Code's own default (auto on the accounts
// that have it) applies, as on the host, never a mode lea pins. Its prompt's hook reports the mode
// the session runs in; the control run is the same Claude Code without the managed settings. A
// default mode the agent writes into its own settings is not one lea set either: the managed
// settings still disable bypass.
func TestClaudeManagedSettingsKeepClaudeCodesDefaultMode(t *testing.T) {
	e := newClaudeE2E(t)
	e.setLimited(false)
	mode := func(what string, from int) string {
		var m string
		e.until(what, 45, func(a store.Agent) bool {
			ps, _ := e.st.EventPayloads(a.Key, "UserPromptSubmit")
			if len(ps) <= from {
				return false
			}
			var p struct {
				PermissionMode string `json:"permission_mode"`
			}
			if json.Unmarshal([]byte(ps[from]), &p) != nil || p.PermissionMode == "" {
				t.Fatalf("%s: no permission_mode in the hook's payload: %.2000s", what, ps[from])
			}
			m = p.PermissionMode
			return true
		})
		return m
	}

	kill := e.run("say hi")
	own := mode("the control run's prompt", 0)
	kill()

	e.writeManaged()
	kill = e.run("say hi")
	if m := mode("the agent's prompt", 1); m != own {
		t.Fatalf("a fresh agent starts in %q mode, Claude Code's own default is %q\n%s", m, own, e.out.String())
	}
	kill()

	// A host mode lea accepts (the entrypoint merges it into the managed settings) is the agent's.
	want := "plan"
	if own == "plan" {
		want = "acceptEdits"
	}
	raw, _ := os.ReadFile(managedSettingsPath)
	var m map[string]any
	json.Unmarshal(raw, &m)
	m["permissions"].(map[string]any)["defaultMode"] = want
	b, _ := json.Marshal(m)
	if err := os.WriteFile(managedSettingsPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	e.run("say hi")
	if got := mode("the host-mode agent's prompt", 2); got != want {
		t.Fatalf("with the host's %s, the agent starts in %q mode", want, got)
	}
}
