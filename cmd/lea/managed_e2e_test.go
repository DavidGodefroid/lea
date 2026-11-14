package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestManagedBinary builds lea with the lockfamily_managed_test tag (the only build that may
// read a managed file elsewhere than /etc/lockfamily, LEA_TEST_MANAGED) and runs the real binary
// under a company's managed file (#399): without one nothing changes; with one, lea egress allow
// beyond egress_allow and lea config import beyond approvals are refused and write nothing; a
// managed file lea refuses keeps both the launcher and the daemon from starting.
func TestManagedBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	if fileExists("/run/lea/ready") {
		t.Skip("running inside lea")
	}
	root := t.TempDir()
	for _, d := range []string{"config", "libexec"} {
		if out, err := exec.Command("cp", "-R", "../../"+d, root+"/"+d).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	bin := root + "/build/lea"
	if out, err := exec.Command("go", "build", "-tags", "lockfamily_managed_test", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	home, proj := root+"/home", root+"/home/projects"
	for _, d := range []string{proj, home + "/.config/lea", root + "/etc"} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, s string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	conf := home + "/.config/lea/lea.conf"
	userConf := "LEA_PROJECTS_DIR='" + proj + "'\nLEA_APPROVALS=deck\nLEA_EGRESS_ALLOW='default'\n"
	write(conf, userConf)
	managedPath := root + "/etc/managed.toml"
	lea := func(managed bool, stdin string, args ...string) (string, string, int) {
		t.Helper()
		c := exec.Command(bin, args...)
		c.Dir, c.Stdin = proj, strings.NewReader(stdin)
		// No docker on PATH: lea egress finds no running proxy.
		c.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home, "XDG_CONFIG_HOME=" + home + "/.config",
			"XDG_DATA_HOME=" + home + "/.local/share", "XDG_RUNTIME_DIR=" + root + "/run", "LC_ALL=C"}
		if managed {
			c.Env = append(c.Env, "LEA_TEST_MANAGED="+managedPath)
		}
		var o, e strings.Builder
		c.Stdout, c.Stderr = &o, &e
		code := 0
		if err := c.Run(); err != nil {
			ee, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatal(err)
			}
			code = ee.ExitCode()
		}
		return o.String(), e.String(), code
	}

	// No managed file (the path names none): lea egress allow works as before.
	if _, errs, code := lea(true, "", "egress", "allow", "pkg.example"); code != 0 {
		t.Fatalf("no managed file: %d %s", code, errs)
	}
	if b, _ := os.ReadFile(conf); !strings.Contains(string(b), "LEA_EGRESS_ALLOW='default,pkg.example'") {
		t.Fatalf("lea.conf: %s", b)
	}
	write(conf, userConf)

	write(managedPath, "version = 1\norganisation = \"ICTools\"\n[lea]\napprovals = \"deck\"\negress_allow = [\"github.com\", \"corp.example\"]\n")
	if _, errs, code := lea(true, "", "egress", "allow", "pkg.example"); code == 0 ||
		!strings.Contains(errs, "blocked by company policy: lea.egress_allow = github.com,corp.example (pkg.example is not in it)") {
		t.Fatalf("egress allow: %d %s", code, errs)
	}
	if _, errs, code := lea(true, "", "egress", "allow", "git.corp.example"); code != 0 {
		t.Fatalf("egress allow within: %d %s", code, errs)
	}
	out, errs, code := lea(true, "", "egress", "list")
	if code != 0 || !strings.Contains(out, "Managed by ICTools (sha256:") || strings.Contains(out, "registry.npmjs.org") ||
		!strings.Contains(out, "git.corp.example") {
		t.Fatalf("egress list: %d %s %s", code, out, errs)
	}
	write(conf, userConf)
	write(root+"/team.json", `{"lea_config_format":1,"keys":{"LEA_APPROVALS":"pane"}}`)
	if _, errs, code := lea(true, "", "config", "import", root+"/team.json", "--yes"); code == 0 ||
		!strings.Contains(errs, "import refused, nothing written") || !strings.Contains(errs, "lea.approvals = deck") {
		t.Fatalf("import: %d %s", code, errs)
	}
	if b, _ := os.ReadFile(conf); string(b) != userConf {
		t.Fatalf("a refused import wrote lea.conf: %s", b)
	}
	// The user's own pane is ignored with a warning.
	write(conf, strings.Replace(userConf, "LEA_APPROVALS=deck", "LEA_APPROVALS=pane", 1))
	if _, errs, _ := lea(true, "", "egress", "list"); !strings.Contains(errs, "warning: LEA_APPROVALS=pane is ignored: blocked by company policy: lea.approvals = deck") {
		t.Fatalf("no warning: %s", errs)
	}

	// Fail closed: a [lea] key lea does not know, or a file that is not TOML, stops lea.
	for _, bad := range []string{"version = 1\n[lea]\nno_such_rule = true\n", "version = 1\n[lea\n", "version = 2\n"} {
		write(managedPath, bad)
		if _, errs, code := lea(true, "", "egress", "list"); code == 0 || !strings.Contains(errs, "managed") {
			t.Errorf("launcher with %q: %d %s", bad, code, errs)
		}
		if _, errs, code := lea(true, "", "daemon"); code != 1 || !strings.Contains(errs, "lea refuses to start") {
			t.Errorf("daemon with %q: %d %s", bad, code, errs)
		}
	}
}
