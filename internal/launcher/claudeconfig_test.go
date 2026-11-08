package launcher

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// handed is the file lea hands the entrypoint (HostPermissionsPath) at the next start, decoded.
func handed(t *testing.T, w *world, st string) map[string]any {
	t.Helper()
	mounts(t, w, st)
	var m map[string]any
	b := w.read(st + "/claude/" + ID(w.proj) + ".permissions.json")
	if err := json.Unmarshal([]byte(b), &m); err != nil {
		t.Fatalf("%v in %s", err, b)
	}
	return m
}

func (w *world) claudeConf() string { return w.confDir() + "/claude.json" }

// claude runs `lea config claude ARGS` and fails the test on a non-zero status.
func (w *world) claude(args ...string) string {
	w.t.Helper()
	code, out, errs := w.config("", append([]string{"claude"}, args...)...)
	if code != 0 {
		w.t.Fatalf("lea config claude %v: %d %s", args, code, errs)
	}
	return out + errs
}

// lea config claude defaultMode auto outranks the host's settings.json and the image; --unset
// falls back to the host's, then to the image's (none: Claude Code's own default).
func TestConfigClaudeDefaultMode(t *testing.T) {
	w, src, _, st := layerWorld(t)
	os.WriteFile(src+"/settings.json", []byte(`{"permissions":{"defaultMode":"plan"}}`), 0o644)
	msg := w.claude("defaultMode", "auto")
	if !strings.Contains(msg, "applies to lea containers started from now on") {
		t.Errorf("no word on when it applies: %q", msg)
	}
	var stored map[string]any
	if err := json.Unmarshal([]byte(w.read(w.claudeConf())), &stored); err != nil || stored["defaultMode"] != "auto" {
		t.Fatalf("stored %v %v", stored, err)
	}
	if fi, _ := os.Stat(w.claudeConf()); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}
	if m := handed(t, w, st); m["defaultMode"] != "auto" {
		t.Fatalf("defaultMode %v, want auto over the host's plan", m["defaultMode"])
	}
	w.claude("defaultMode", "--unset")
	if m := handed(t, w, st); m["defaultMode"] != "plan" {
		t.Fatalf("--unset: %v, want the host's plan", m["defaultMode"])
	}
	os.WriteFile(src+"/settings.json", []byte(`{}`), 0o644)
	if m := handed(t, w, st); m["defaultMode"] != nil {
		t.Fatalf("--unset, no host mode: %v, want none (the image's)", m["defaultMode"])
	}
}

// allow, ask, deny and additionalDirectories: add and remove change lea's own entries only, an
// entry is added once, and a rule of the image or the host can be removed.
func TestConfigClaudeLists(t *testing.T) {
	w, src, _, st := layerWorld(t)
	os.WriteFile(src+"/settings.json", []byte(`{"permissions":{"allow":["Bash(ls)","Bash(npm test)"],"deny":["Read(./.env)"]}}`), 0o644)
	w.claude("allow", "add", "Bash(make *)")
	if msg := w.claude("allow", "add", "Bash(make *)"); !strings.Contains(msg, "already") {
		t.Errorf("a second add: %q", msg)
	}
	w.claude("allow", "remove", "Bash(go *)")     // the image's
	w.claude("allow", "remove", "Bash(npm test)") // the host's
	w.claude("ask", "add", "Bash(git push *)")
	w.claude("deny", "add", "Bash(rm -rf *)")
	w.claude("additionalDirectories", "add", "/srv/data")
	m := handed(t, w, st)
	if g := strList(m["allow"]); !reflect.DeepEqual(g, []string{"Bash(make *)", "Bash(ls)"}) {
		t.Errorf("allow %v", g)
	}
	if g := strList(m["ask"]); !reflect.DeepEqual(g, []string{"Bash(git push *)"}) {
		t.Errorf("ask %v", g)
	}
	if g := strList(m["deny"]); !reflect.DeepEqual(g, []string{"Bash(rm -rf *)", "Read(./.env)"}) {
		t.Errorf("deny %v", g)
	}
	if g := strList(m["additionalDirectories"]); !reflect.DeepEqual(g, []string{"/srv/data"}) {
		t.Errorf("additionalDirectories %v", g)
	}
	rm, _ := m["remove"].(map[string]any)
	if g := strList(rm["allow"]); !reflect.DeepEqual(g, []string{"Bash(go *)", "Bash(npm test)"}) {
		t.Errorf("remove.allow %v", g)
	}
	// Removing lea's own entry drops it; adding a removed one back undoes the removal.
	w.claude("allow", "remove", "Bash(make *)")
	w.claude("allow", "add", "Bash(go *)")
	m = handed(t, w, st)
	if g := strList(m["allow"]); !reflect.DeepEqual(g, []string{"Bash(ls)"}) {
		t.Errorf("allow after remove %v", g)
	}
	rm, _ = m["remove"].(map[string]any)
	if g := strList(rm["allow"]); !reflect.DeepEqual(g, []string{"Bash(npm test)"}) {
		t.Errorf("remove.allow after add back %v", g)
	}
	// An entry that is nowhere: nothing to remove, nothing written.
	before := w.snapshot()
	if code, _, errs := w.config("", "claude", "deny", "remove", "Bash(nowhere)"); code == 0 || !strings.Contains(errs, "not one of") {
		t.Errorf("remove of nothing: %d %q", code, errs)
	}
	if !sameSnapshot(before, w.snapshot()) {
		t.Error("a refused remove wrote the configuration")
	}
	// --unset drops lea's changes to one list.
	w.claude("allow", "--unset")
	m = handed(t, w, st)
	if g := strList(m["allow"]); !reflect.DeepEqual(g, []string{"Bash(ls)", "Bash(npm test)"}) {
		t.Errorf("allow after --unset %v", g)
	}
	if rm, _ := m["remove"].(map[string]any); rm["allow"] != nil {
		t.Errorf("remove.allow after --unset %v", rm["allow"])
	}
}

// env and Claude Code's plain preferences reach the entrypoint's file too.
func TestConfigClaudeEnvAndPreferences(t *testing.T) {
	w, _, _, st := layerWorld(t)
	w.claude("env", "MY_TOOL_HOME", "/opt/tool")
	w.claude("model", "opus")
	w.claude("alwaysThinkingEnabled", "true")
	m := handed(t, w, st)
	if env, _ := m["env"].(map[string]any); env["MY_TOOL_HOME"] != "/opt/tool" {
		t.Errorf("env %v", m["env"])
	}
	s, _ := m["settings"].(map[string]any)
	if s["model"] != "opus" || s["alwaysThinkingEnabled"] != true {
		t.Errorf("settings %v", m["settings"])
	}
	w.claude("env", "MY_TOOL_HOME", "--unset")
	w.claude("model", "--unset")
	m = handed(t, w, st)
	if m["env"] != nil {
		t.Errorf("env after --unset %v", m["env"])
	}
	if s, _ := m["settings"].(map[string]any); s["model"] != nil || s["alwaysThinkingEnabled"] != true {
		t.Errorf("settings after --unset %v", m["settings"])
	}
}

// Invalid values, unknown keys and the keys lea enforces are refused with their reason, and the
// stored configuration stays as it was.
func TestConfigClaudeRefuses(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.claude("defaultMode", "plan")
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"defaultMode", "foo"}, "not a permission mode"},
		{[]string{"defaultMode", "bypassPermissions"}, "bypass skips permission requests"},
		{[]string{"defaultMode"}, "Usage:"},
		{[]string{"hooks", "x"}, "lea's hooks are lea"},
		{[]string{"allowManagedPermissionRulesOnly", "false"}, "widen its own rules"},
		{[]string{"disableBypassPermissionsMode", "x"}, "bypass skips permission requests"},
		{[]string{"disableAllHooks", "true"}, "lea's hooks are lea"},
		{[]string{"statusLine", "x"}, "status line"},
		{[]string{"env", "LEA_BUS_SOCK", "/tmp/x"}, "lea's own variables"},
		{[]string{"env", "SSH_AUTH_SOCK", "/tmp/x"}, "git delivery"},
		{[]string{"env", "GH_CONFIG_DIR", "/tmp/x"}, "git delivery"},
		{[]string{"env", "OTEL_EXPORTER_OTLP_ENDPOINT", "http://x"}, "telemetry"},
		{[]string{"env", "CLAUDE_CODE_ENABLE_TELEMETRY", "0"}, "telemetry"},
		{[]string{"env", "DISABLE_UPDATES", "0"}, "pinned Claude Code version"},
		{[]string{"env", "HTTPS_PROXY", "http://x"}, "lea-proxy"},
		{[]string{"env", "1BAD", "x"}, "not a variable name"},
		{[]string{"env", "GOOD", "a\nb"}, "one line"},
		{[]string{"allow", "add", ""}, "empty"},
		{[]string{"allow", "add", strings.Repeat("x", 1025)}, "1024"},
		{[]string{"allow", "push", "x"}, "Usage:"},
		{[]string{"alwaysThinkingEnabled", "yes"}, "true or false"},
		{[]string{"model", ""}, "empty"},
		{[]string{"permissions", "x"}, "defaultMode, allow, ask, deny, additionalDirectories"},
		{[]string{"apiKeyHelper", "x"}, "not a setting lea config claude knows"},
	} {
		before := w.snapshot()
		code, _, errs := w.config("", append([]string{"claude"}, c.args...)...)
		if code == 0 || !strings.Contains(errs, c.want) {
			t.Errorf("%q: %d %q, want a refusal saying %q", c.args, code, errs, c.want)
		}
		if !sameSnapshot(before, w.snapshot()) {
			t.Errorf("%q: the configuration changed", c.args)
		}
	}
	// A stored file lea cannot read is never overwritten.
	w.writeConf("claude.json", "not json")
	if code, _, errs := w.config("", "claude", "defaultMode", "auto"); code == 0 || !strings.Contains(errs, "claude.json") {
		t.Errorf("a broken claude.json: %d %q", code, errs)
	}
	if w.read(w.claudeConf()) != "not json" {
		t.Error("a broken claude.json was overwritten")
	}
}

// A start with a claude.json lea cannot read fails, instead of starting without the user's rules.
func TestConfigClaudeBrokenFileFailsTheStart(t *testing.T) {
	w, _, _, st := layerWorld(t)
	w.writeConf("claude.json", `{"deny":7}`)
	if _, err := w.launcher().claudeMounts(w.home, w.proj, st); err == nil || !strings.Contains(err.Error(), "claude.json") {
		t.Fatalf("a broken claude.json: %v", err)
	}
}

// lea config claude with no argument prints every effective key with its source.
func TestConfigClaudeShow(t *testing.T) {
	w, src, _, _ := layerWorld(t)
	os.WriteFile(src+"/settings.json", []byte(`{"permissions":{"allow":["Bash(npm test)"],"defaultMode":"plan"}}`), 0o644)
	w.claude("defaultMode", "auto")
	w.claude("allow", "add", "Bash(make *)")
	w.claude("allow", "remove", "Bash(staticcheck *)")
	w.claude("env", "MY_TOOL_HOME", "/opt/tool")
	w.claude("model", "opus")
	out := w.claude()
	lines := strings.Split(out, "\n")
	has := func(fields ...string) bool {
		for _, l := range lines {
			ok := true
			for _, f := range fields {
				ok = ok && strings.Contains(l, f)
			}
			if ok {
				return true
			}
		}
		return false
	}
	for _, c := range [][]string{
		{"defaultMode", "auto", "lea config claude"},
		{"allow", "Bash(go *)", "lea default"},
		{"allow", "Bash(make *)", "lea config claude"},
		{"allow", "Bash(npm test)", "host ~/.claude/settings.json"},
		{"allow", "Bash(staticcheck *)", "removed by lea config claude"},
		{"env MY_TOOL_HOME", "/opt/tool", "lea config claude"},
		{"env LEA_BUS_SOCK", "/run/lea/bus/bus.sock", "lea, enforced"},
		{"model", "opus", "lea config claude"},
		{"hooks", "PreToolUse", "lea, enforced"},
		{"allowManagedPermissionRulesOnly", "true", "lea, enforced"},
		{"disableBypassPermissionsMode", "disable", "lea, enforced"},
	} {
		if !has(c...) {
			t.Errorf("no line with %q in\n%s", c, out)
		}
	}
	if has("defaultMode", "plan") {
		t.Errorf("the host's plan shows though lea config claude outranks it:\n%s", out)
	}
	if !strings.Contains(out, w.claudeConf()) {
		t.Errorf("the stored file is not named:\n%s", out)
	}
}

// lea config export leaves Claude's base settings on the workstation, --include-personal too.
func TestConfigExportSkipsClaudeSettings(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.writeConf("lea.conf", "LEA_LOCKSQL=0\n")
	w.claude("defaultMode", "auto")
	w.claude("allow", "add", "Bash(make *)")
	for _, a := range [][]string{{"export"}, {"export", "--include-personal"}} {
		code, out, errs := w.config("", a...)
		if code != 0 {
			t.Fatalf("%v: %d %s", a, code, errs)
		}
		if strings.Contains(out, "make *") || strings.Contains(out, "defaultMode") {
			t.Errorf("%v exported Claude's settings: %s", a, out)
		}
	}
}

// lea config --help and lea config claude --help name the subcommand.
func TestConfigClaudeHelp(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	for _, a := range [][]string{{"--help"}, {"claude", "--help"}} {
		code, out, _ := w.config("", a...)
		if code != 0 || !strings.Contains(out, "lea config claude") {
			t.Errorf("%v: %d %q", a, code, out)
		}
	}
}
