package launcher

import (
	"errors"
	"strings"
	"testing"

	km "github.com/DavidGodefroid/lockkit/managed"

	"github.com/DavidGodefroid/lea/internal/managed"
)

// The launcher's tests never read the machine's managed file: each one that needs one sets it
// (withManaged).
func init() { loadManaged = func() (*managed.Policy, error) { return nil, nil } }

// withManaged makes LoadConfig read the managed file body (version 1 added) for the test.
func withManaged(t *testing.T, body string) {
	t.Helper()
	f, err := km.Parse([]byte("version = 1\norganisation = \"ICTools\"\n" + body))
	if err != nil {
		t.Fatal(err)
	}
	p, err := managed.FromFile(f)
	if err != nil {
		t.Fatal(err)
	}
	old := loadManaged
	loadManaged = func() (*managed.Policy, error) { return p, nil }
	t.Cleanup(func() { loadManaged = old })
}

// No managed file: the configuration is the user's, no warning, no organisation.
func TestNoManagedFileChangesNothing(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_APPROVALS=pane\nLEA_AUTOUPDATE=0\nLEA_EGRESS_ALLOW=default,deb.debian.org\nLEA_LOCKHTTP=1\n")
	c, err := LoadConfig(w.getenv, w.root, w.proj)
	if err != nil {
		t.Fatal(err)
	}
	if c.Managed() != nil || len(c.ManagedWarnings()) != 0 || c.Get(ManagedOrgVar) != "" {
		t.Fatalf("managed %v, warnings %v", c.Managed(), c.ManagedWarnings())
	}
	if c.Get("LEA_APPROVALS") != "pane" || c.Get("LEA_AUTOUPDATE") != "0" || c.EgressAllow() != "default,deb.debian.org" {
		t.Fatalf("approvals %s autoupdate %s egress %s", c.Get("LEA_APPROVALS"), c.Get("LEA_AUTOUPDATE"), c.EgressAllow())
	}
}

// A managed file lea refuses (badly owned, invalid, a bad signature) is a refusal to start.
func TestARefusedManagedFileRefusesToStart(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	old := loadManaged
	t.Cleanup(func() { loadManaged = old })
	loadManaged = func() (*managed.Policy, error) {
		return nil, errors.New("managed: /etc/lockfamily/managed.toml must be owned by root (it is owned by uid 1000)")
	}
	if _, err := LoadConfig(w.getenv, w.root, w.proj); err == nil || !strings.Contains(err.Error(), "owned by root") {
		t.Fatalf("err = %v", err)
	}
	if rc := w.launcherOrStatus(); rc == 0 {
		t.Fatal("lea started")
	}
}

// launcherOrStatus is 0 when LoadConfig loads, else 1.
func (w *world) launcherOrStatus() int {
	if _, err := LoadConfig(w.getenv, w.root, w.proj); err != nil {
		return 1
	}
	return 0
}

// approvals and autoupdate are enforced: the user's value, from the file or the environment, is
// ignored with a warning that cites the managed key; a value equal to it warns nothing.
func TestManagedEnforcedValues(t *testing.T) {
	withManaged(t, "[lea]\napprovals = \"deck\"\nautoupdate = true\n")
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_APPROVALS=pane\nLEA_AUTOUPDATE=0\n")
	c, err := LoadConfig(w.getenv, w.root, w.proj)
	if err != nil {
		t.Fatal(err)
	}
	if c.Get("LEA_APPROVALS") != "deck" || c.Get("LEA_AUTOUPDATE") != "1" {
		t.Fatalf("approvals %s autoupdate %s", c.Get("LEA_APPROVALS"), c.Get("LEA_AUTOUPDATE"))
	}
	ws := strings.Join(c.ManagedWarnings(), "\n")
	for _, want := range []string{"LEA_APPROVALS=pane is ignored: blocked by company policy: lea.approvals = deck",
		"LEA_AUTOUPDATE=0 is ignored: blocked by company policy: lea.autoupdate = 1"} {
		if !strings.Contains(ws, want) {
			t.Errorf("warnings lack %q:\n%s", want, ws)
		}
	}
	if c.Get(ManagedOrgVar) != "ICTools" {
		t.Errorf("org %q", c.Get(ManagedOrgVar))
	}
	if v, rule, ok := c.ManagedValue("LEA_APPROVALS"); !ok || v != "deck" || rule != "lea.approvals" {
		t.Errorf("ManagedValue %s %s %v", v, rule, ok)
	}
	// The environment's protected LEA_APPROVALS loses too.
	writeUserConf(t, w, "")
	w.env["LEA_APPROVALS"] = "pane"
	if c, err = LoadConfig(w.getenv, w.root, w.proj); err != nil || c.Get("LEA_APPROVALS") != "deck" {
		t.Fatalf("%v %s", err, c.Get("LEA_APPROVALS"))
	}
	delete(w.env, "LEA_APPROVALS")
	writeUserConf(t, w, "LEA_APPROVALS=deck\n")
	if c, err = LoadConfig(w.getenv, w.root, w.proj); err != nil || len(c.ManagedWarnings()) != 0 {
		t.Fatalf("%v %v", err, c.ManagedWarnings())
	}
	// The launcher prints them.
	writeUserConf(t, w, "LEA_APPROVALS=pane\n")
	w.launcher().Main([]string{"egress", "bogus"})
	if !strings.Contains(w.errb.String(), "warning: LEA_APPROVALS=pane is ignored: blocked by company policy") {
		t.Errorf("stderr: %s", w.errb.String())
	}
}

func TestManagedEgress(t *testing.T) {
	m := []string{"github.com", "corp.example", "api.openai.com"}
	for _, c := range []struct {
		user, eff, dropped string
	}{
		{"", "github.com,api.openai.com", "corp.example"},
		{"default", "github.com,api.openai.com", ""},
		{"default,corp.example,deb.debian.org", "github.com,corp.example,api.openai.com", "deb.debian.org"},
		{"*", "github.com,corp.example,api.openai.com", ""},
		{"git.corp.example", "git.corp.example", ""},
		{"deb.debian.org", "anthropic.com,claude.ai,claude.com", "deb.debian.org"},
	} {
		eff, dropped := managedEgress(c.user, m)
		if c.user == "" {
			// "" is lea's default list: corp.example is not in it, nothing the user wrote is dropped.
			c.dropped = ""
		}
		if eff != c.eff || strings.Join(dropped, ",") != c.dropped {
			t.Errorf("%q: %q dropped %q; want %q dropped %q", c.user, eff, dropped, c.eff, c.dropped)
		}
	}
	// "default" in the managed list is lea's default list.
	if eff, _ := managedEgress("deb.debian.org,github.com", []string{"default"}); eff != "github.com" {
		t.Errorf("managed default: %q", eff)
	}
}

// The proxy gets the user's allowlist within egress_allow; lea egress allow refuses a domain
// outside it, and lea egress list says who bounds it.
func TestManagedEgressAllow(t *testing.T) {
	withManaged(t, "[lea]\negress_allow = [\"github.com\", \"corp.example\"]\n")
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_EGRESS_ALLOW=default,corp.example,deb.debian.org\n")
	c, err := LoadConfig(w.getenv, w.root, w.proj)
	if err != nil {
		t.Fatal(err)
	}
	if c.EgressAllow() != "github.com,corp.example" || c.Get("LEA_EGRESS_ALLOW") != "default,corp.example,deb.debian.org" {
		t.Fatalf("effective %q, user %q", c.EgressAllow(), c.Get("LEA_EGRESS_ALLOW"))
	}
	if ws := strings.Join(c.ManagedWarnings(), "\n"); !strings.Contains(ws, "LEA_EGRESS_ALLOW: deb.debian.org ignored: blocked by company policy: lea.egress_allow = github.com,corp.example") {
		t.Fatalf("warnings %s", ws)
	}
	w.env["FAKE_DOCKER_OUT_INSPECT__F"] = "true"
	if rc := w.launcher().Main([]string{"egress", "allow", "pkg.example"}); rc == 0 ||
		!strings.Contains(w.errb.String(), "blocked by company policy: lea.egress_allow = github.com,corp.example (pkg.example is not in it)") {
		t.Fatalf("rc %d: %s", rc, w.errb.String())
	}
	if conf, _ := userConf(t, w); strings.Contains(conf, "pkg.example") || w.hasCall("docker [exec]") {
		t.Fatalf("a refused domain was written or applied: %s %v", conf, w.calls())
	}
	// A domain within it is allowed, and the proxy gets the bounded list.
	writeUserConf(t, w, "LEA_EGRESS_ALLOW=deb.debian.org\n")
	if rc := w.launcher().Main([]string{"egress", "allow", "git.corp.example"}); rc != 0 {
		t.Fatalf("rc %d: %s", rc, w.errb.String())
	}
	if !w.hasCall("[LEA_EGRESS_ALLOW=git.corp.example] [lea-proxy]") {
		t.Fatalf("not the bounded list:\n%s", strings.Join(w.calls(), "\n"))
	}
	writeUserConf(t, w, "LEA_EGRESS_ALLOW=default,corp.example,deb.debian.org\n")
	// The model API stays allowed whatever the list.
	if err := c.managedEgressRefuses("api.anthropic.com"); err != nil {
		t.Fatal(err)
	}
	w.out.Reset()
	if rc := w.launcher().Main([]string{"egress", "list"}); rc != 0 || !strings.Contains(w.out.String(), "Managed by ICTools") ||
		!strings.Contains(w.out.String(), "LEA_EGRESS_ALLOW within lea.egress_allow") || strings.Contains(w.out.String(), "deb.debian.org") {
		t.Fatalf("rc %d:\n%s", rc, w.out.String())
	}
}

// LockHTTP replaces lea-proxy's allowlist: refused under egress_allow, unless the managed file
// bounds the gate itself ([lockhttp]).
func TestManagedEgressRefusesLockHTTP(t *testing.T) {
	withManaged(t, "[lea]\negress_allow = [\"github.com\"]\n")
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_LOCKHTTP=1\n")
	if _, err := LoadConfig(w.getenv, w.root, w.proj); err == nil || !strings.Contains(err.Error(), "LEA_LOCKHTTP=1 is refused: blocked by company policy: lea.egress_allow") {
		t.Fatalf("err = %v", err)
	}
	withManaged(t, "[lea]\negress_allow = [\"github.com\"]\n[lockhttp.bounds]\n")
	if _, err := LoadConfig(w.getenv, w.root, w.proj); err != nil {
		t.Fatalf("with [lockhttp]: %v", err)
	}
}

// lea config import beyond the managed file is refused as a whole, nothing written.
func TestManagedConfigImportRefuses(t *testing.T) {
	withManaged(t, "[lea]\napprovals = \"deck\"\nautoupdate = true\negress_allow = [\"github.com\"]\n")
	for name, c := range map[string]struct{ in, msg string }{
		"approvals":  {`{"lea_config_format":1,"keys":{"LEA_APPROVALS":"pane"}}`, "blocked by company policy: lea.approvals = deck"},
		"egress":     {`{"lea_config_format":1,"keys":{"LEA_EGRESS_ALLOW":"github.com,deb.debian.org"}}`, "deb.debian.org not in it"},
		"egress any": {`{"lea_config_format":1,"keys":{"LEA_EGRESS_ALLOW":"*"}}`, "'*' opens the web"},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t, repoRoot(t))
			w.writeConf("lea.conf", "LEA_SOUND=0\n")
			before := w.snapshot()
			code, _, errs := w.config("", "import", w.shareFile(c.in), "--yes", "--allow-egress", "--allow-host-keys", "--allow-code", "--allow-lock")
			if code == 0 || !strings.Contains(errs, "import refused, nothing written") || !strings.Contains(errs, c.msg) {
				t.Fatalf("status %d, stderr %q, want %q", code, errs, c.msg)
			}
			if !sameSnapshot(before, w.snapshot()) {
				t.Fatal("a refused import wrote a file")
			}
		})
	}
	// Within the bounds, it imports.
	w := newWorld(t, repoRoot(t))
	w.writeConf("lea.conf", "LEA_SOUND=0\n")
	if code, _, errs := w.config("", "import", w.shareFile(`{"lea_config_format":1,"keys":{"LEA_APPROVALS":"deck","LEA_EGRESS_ALLOW":"github.com"}}`),
		"--yes", "--allow-egress"); code != 0 {
		t.Fatalf("status %d: %s", code, errs)
	}
}

// lea doctor names who manages lea and its rules.
func TestManagedDoctor(t *testing.T) {
	withManaged(t, "[lea]\napprovals = \"deck\"\n")
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_APPROVALS=pane\n")
	l := w.launcher()
	l.managedDoctor()
	out := w.out.String()
	for _, want := range []string{"ok    Managed by ICTools (sha256:", "ok    managed lea.approvals = deck", "warn  managed: LEA_APPROVALS=pane is ignored"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// claude_enforced sets Claude Code settings in every agent's managed settings: lea config claude
// cannot change them, and what lea hands the entrypoint carries them over the user's.
func TestManagedClaudeEnforced(t *testing.T) {
	withManaged(t, "[lea.claude_enforced]\ndefaultMode = \"plan\"\nmodel = \"opus\"\ndisableBypassPermissionsMode = \"disable\"\n")
	w := newWorld(t, repoRoot(t))
	l := w.launcher()
	if rc := l.Main([]string{"config", "claude", "defaultMode", "acceptEdits"}); rc == 0 ||
		!strings.Contains(w.errb.String(), "blocked by company policy: lea.claude_enforced.defaultMode = plan") {
		t.Fatalf("rc %d: %s", rc, w.errb.String())
	}
	if rc := l.Main([]string{"config", "claude", "model", "--unset"}); rc == 0 || !strings.Contains(w.errb.String(), "lea.claude_enforced.model = opus") {
		t.Fatalf("rc %d: %s", rc, w.errb.String())
	}
	if rc := l.Main([]string{"config", "claude", "language", "French"}); rc != 0 {
		t.Fatalf("a key the file leaves alone: rc %d: %s", rc, w.errb.String())
	}
	user := &claudeConfig{DefaultMode: "acceptEdits", Settings: map[string]any{"model": "sonnet", "language": "French"}}
	got := string(claudeHanded(nil, l.Cfg.withManagedClaude(user)))
	for _, want := range []string{`"defaultMode": "plan"`, `"model": "opus"`, `"language": "French"`} {
		if !strings.Contains(got, want) {
			t.Errorf("handed lacks %s:\n%s", want, got)
		}
	}
	if user.DefaultMode != "acceptEdits" || user.Settings["model"] != "sonnet" {
		t.Errorf("the user's configuration changed: %+v", user)
	}
	w.out.Reset()
	if rc := l.Main([]string{"config", "claude"}); rc != 0 || !strings.Contains(w.out.String(), "company policy (lea.claude_enforced)") {
		t.Fatalf("rc %d:\n%s", rc, w.out.String())
	}
}

// Fail closed: a Claude setting lea cannot enforce, or that would loosen lea, is refused.
func TestManagedClaudeEnforcedRefuses(t *testing.T) {
	for in, want := range map[string]string{
		`hooks = "x"`:      "is lea's to keep",
		`statusLine = "x"`: "is lea's to keep",
		`skipDangerousModePermissionPrompt = true`: "is not a setting lea can enforce",
		`defaultMode = "bypassPermissions"`:        "bypassPermissions is lea's to refuse",
		`disableBypassPermissionsMode = "allow"`:   `can only be "disable"`,
		`alwaysThinkingEnabled = "yes"`:            "is true or false",
		`model = 1`:                                "is a string",
	} {
		f, err := km.Parse([]byte("version = 1\n[lea.claude_enforced]\n" + in + "\n"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := managed.FromFile(f); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", in, err, want)
		}
	}
}
