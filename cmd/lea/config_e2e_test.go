package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestConfigShareBinary builds lea into a scratch checkout and carries a configuration from one
// workstation (HOME A) to another (HOME B) with the real binary: lea config export on A, lea config
// import on B. B's shell (lea_load_config) then resolves A's shared keys, B keeps its projects
// dir, a second import changes nothing, and a file trying to inject shell is refused unwritten.
func TestConfigShareBinary(t *testing.T) {
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
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	type host struct{ home, proj string }
	mk := func(name string) host {
		h := host{home: root + "/" + name, proj: root + "/" + name + "/projects"}
		if err := os.MkdirAll(h.proj, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(h.home+"/.config/lea", 0o700); err != nil {
			t.Fatal(err)
		}
		return h
	}
	env := func(h host) []string {
		return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + h.home, "XDG_CONFIG_HOME=" + h.home + "/.config",
			"XDG_DATA_HOME=" + h.home + "/.local/share", "LC_ALL=C"}
	}
	lea := func(h host, stdin string, args ...string) (string, string, int) {
		t.Helper()
		c := exec.Command(bin, args...)
		c.Dir, c.Env, c.Stdin = h.proj, env(h), strings.NewReader(stdin)
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
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	shared := `printf '%s|' "$LEA_GIT_SSH" "$LEA_APPROVALS" "$LEA_APPROVAL_TIMEOUT" "$LEA_LOCKSQL" "$LEA_EGRESS_ALLOW" "$LEA_IMAGE"`
	resolved := func(h host, what string) string {
		t.Helper()
		c := exec.Command("sh", "-c", `set -eu; LEA_ROOT='`+root+`'; for f in "$LEA_ROOT"/libexec/lib/*.sh; do . "$f"; done; lea_load_config; `+what)
		c.Dir, c.Env = h.proj, env(h)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("lea_load_config: %v %s", err, out)
		}
		return string(out)
	}

	a, b := mk("a"), mk("b")
	key := testHostKey(t)
	write(a.home+"/.config/lea/git_known_hosts", "git.corp.example "+key+"\n")
	write(a.home+"/.config/lea/internal-hosts", "git.corp.example 443\n")
	write(a.home+"/.config/lea/lea.conf", "LEA_PROJECTS_DIR='"+a.proj+"'\nLEA_GIT_SSH='git.corp.example:22'\n"+
		"LEA_APPROVALS=pane\nLEA_APPROVAL_TIMEOUT=120\nLEA_LOCKSQL=0\nLEA_EGRESS_ALLOW='github.com'\nLEA_IMAGE=ghcr.io/corp/lea:1\n"+
		"LEA_INTERNAL_HOSTS='"+a.home+"/.config/lea/internal-hosts'\n")
	write(b.home+"/.config/lea/lea.conf", "LEA_PROJECTS_DIR='"+b.proj+"'\n")

	team, errs, code := lea(a, "", "config", "export")
	if code != 0 || strings.Contains(team, a.home) {
		t.Fatalf("export: %d %q %s", code, errs, team)
	}
	write(root+"/team.json", team)
	all := []string{"config", "import", root + "/team.json", "--yes", "--allow-egress", "--allow-host-keys", "--allow-code"}
	if _, errs, code := lea(b, "", all...); code != 0 {
		t.Fatalf("import: %d %s", code, errs)
	}
	if ra, rb := resolved(a, shared), resolved(b, shared); ra != rb || !strings.Contains(rb, "pane|120|0|") {
		t.Errorf("A resolves %q, B %q", ra, rb)
	}
	if got := resolved(b, `printf '%s' "$LEA_PROJECTS_DIR"`); got != b.proj {
		t.Errorf("B's projects dir is %q", got)
	}
	if got := resolved(b, `cat "$LEA_GIT_KNOWN_HOSTS" "$LEA_INTERNAL_HOSTS"`); got != "git.corp.example "+key+"\ngit.corp.example 443\n" {
		t.Errorf("B's files: %q", got)
	}
	conf, _ := os.ReadFile(b.home + "/.config/lea/lea.conf")
	if out, _, code := lea(b, "", all...); code != 0 || !strings.Contains(out, "nothing to change") {
		t.Fatalf("second import: %d %q", code, out)
	}
	if again, _ := os.ReadFile(b.home + "/.config/lea/lea.conf"); string(again) != string(conf) {
		t.Error("the second import rewrote lea.conf")
	}

	// From stdin, and an injection attempt: refused, nothing written, nothing run.
	evil := `{"lea_config_format":1,"keys":{"LEA_K8S_NAMESPACE":"x'$(touch ` + root + `/pwned)'"}}`
	if _, errs, code := lea(b, evil, "config", "import", "-", "--yes"); code == 0 || !strings.Contains(errs, "LEA_K8S_NAMESPACE") {
		t.Fatalf("injection: %d %q", code, errs)
	}
	resolved(b, "true")
	if fileExists(root + "/pwned") {
		t.Fatal("the imported value ran")
	}
	if now, _ := os.ReadFile(b.home + "/.config/lea/lea.conf"); string(now) != string(conf) {
		t.Error("a refused import wrote lea.conf")
	}
}

func testHostKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var b []byte
	for _, s := range [][]byte{[]byte("ssh-ed25519"), pub} {
		b = binary.BigEndian.AppendUint32(b, uint32(len(s)))
		b = append(b, s...)
	}
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(b)
}
