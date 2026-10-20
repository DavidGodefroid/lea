package launcher

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The launcher tells its children which protected keys the environment set: the deck's settings
// screen shows them read-only, since the children see every exported key in their environment.
func TestEnvironNamesTheProtectedKeysTheEnvironmentSet(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_APPROVALS=pane\n")
	w.env["LEA_APPROVAL_TIMEOUT"] = "5"
	w.env["LEA_PREFIX"] = "env"
	c, err := LoadConfig(w.getenv, w.root, w.proj)
	if err != nil {
		t.Fatal(err)
	}
	env := strings.Join(c.Environ([]string{EnvKeysVar + "=LEA_PROJECTS_DIR"}), "\n") + "\n"
	if !strings.Contains(env, "\n"+EnvKeysVar+"=LEA_PROJECTS_DIR LEA_APPROVAL_TIMEOUT\n") || strings.Count(env, EnvKeysVar+"=") != 1 {
		t.Fatalf("Environ:\n%s", env)
	}
}

func TestUserConfigFileIsRelativeToTheWorkingDirectory(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	c, err := LoadConfig(w.getenv, w.root, w.proj)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.UserConfigFile(w.proj); got != filepath.Join(w.env["XDG_CONFIG_HOME"], "lea", "lea.conf") {
		t.Errorf("UserConfigFile = %q", got)
	}
	w.env["LEA_CONFIG"] = "rel.conf"
	os.WriteFile(filepath.Join(w.proj, "rel.conf"), nil, 0o600)
	c, _ = LoadConfig(w.getenv, w.root, w.proj)
	if got := c.UserConfigFile(w.proj); got != filepath.Join(w.proj, "rel.conf") {
		t.Errorf("UserConfigFile = %q", got)
	}
}

// Assignment says which file assigned a key.
func TestAssignmentNamesTheFile(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_APPROVALS=pane\n")
	c, err := LoadConfig(w.getenv, w.root, w.proj)
	if err != nil {
		t.Fatal(err)
	}
	if v, f, ok := c.Assignment("LEA_APPROVALS"); !ok || v != "pane" || f != c.UserConfigFile(w.proj) {
		t.Errorf("LEA_APPROVALS = %q %q %v", v, f, ok)
	}
	if _, f, ok := c.Assignment("LEA_APPROVAL_TIMEOUT"); !ok || f != filepath.Join(w.root, "config", "lea.conf") {
		t.Errorf("LEA_APPROVAL_TIMEOUT from %q %v", f, ok)
	}
	if _, _, ok := c.Assignment("LEA_NOPE"); ok {
		t.Error("LEA_NOPE assigned")
	}
}

// WriteUserConfig is lea_user_config_set: KEY='VALUE' replaces every KEY= line, keeps the others,
// leaves a private file, refuses a symbolic link, and reads back as written.
func TestWriteUserConfig(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	p := filepath.Join(w.env["XDG_CONFIG_HOME"], "lea", "lea.conf")
	if err := WriteUserConfig(p, "LEA_SOUND_DONE", "/it's here.oga"); err != nil {
		t.Fatal(err)
	}
	writeUserConf(t, w, "# mine\nLEA_APPROVALS=pane\nLEA_SOUND=1\nLEA_APPROVALS=deck\n")
	if err := WriteUserConfig(p, "LEA_APPROVALS", "pane"); err != nil {
		t.Fatal(err)
	}
	if err := WriteUserConfig(p, "LEA_SOUND_DONE", "/it's here.oga"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if want := "# mine\nLEA_SOUND=1\nLEA_APPROVALS='pane'\nLEA_SOUND_DONE='/it'\\''s here.oga'\n"; string(b) != want {
		t.Fatalf("file:\n%s\nwant:\n%s", b, want)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode())
	}
	c, err := LoadConfig(w.getenv, w.root, w.proj)
	if err != nil || c.Get("LEA_SOUND_DONE") != "/it's here.oga" {
		t.Fatalf("read back %q: %v", c.Get("LEA_SOUND_DONE"), err)
	}
	link := filepath.Join(w.dir, "link.conf")
	os.Symlink(p, link)
	if err := WriteUserConfig(link, "LEA_SOUND", "0"); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("symlink: %v", err)
	}
	for _, bad := range []string{"1LEA", "LEA X", ""} {
		if err := WriteUserConfig(p, bad, "1"); err == nil {
			t.Errorf("key %q accepted", bad)
		}
	}
	if err := WriteUserConfig(p, "LEA_SOUND", "a\nb"); err == nil {
		t.Error("a new line accepted")
	}
}

func TestCheckGitSSH(t *testing.T) {
	if err := CheckGitSSH("github.com:22 gitlab.com:22"); err != nil {
		t.Error(err)
	}
	if err := CheckGitSSH("h:0"); err == nil {
		t.Error("h:0 accepted")
	}
}

// Every line that sets the key for parseLine is replaced: indented and `export KEY=` ones too, so
// the file never keeps a contradicting line.
func TestWriteUserConfigReplacesEveryFormOfTheKey(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "export LEA_APPROVALS=deck\n  LEA_APPROVALS=pane\n\tLEA_SOUND=1\nLEA_APPROVALS_X=1\n# LEA_APPROVALS=x\n")
	p := filepath.Join(w.env["XDG_CONFIG_HOME"], "lea", "lea.conf")
	if err := WriteUserConfig(p, "LEA_APPROVALS", "pane"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if want := "\tLEA_SOUND=1\nLEA_APPROVALS_X=1\n# LEA_APPROVALS=x\nLEA_APPROVALS='pane'\n"; string(b) != want {
		t.Fatalf("file:\n%q\nwant:\n%q", b, want)
	}
}

// The shell sources the user config: a written value reads back literally under /bin/sh, and
// under the Go reader, whatever shell syntax it holds.
func TestWrittenValuesSourceLiterally(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	p := filepath.Join(w.env["XDG_CONFIG_HOME"], "lea", "lea.conf")
	for _, v := range []string{"it's", "$(id)", "`id`", "a; b", "$HOME", `"q"`, `\x`, "'; touch pwned; '", "a'b'c"} {
		if err := WriteUserConfig(p, "LEA_SOUND_DONE", v); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command("/bin/sh", "-c", `. "$1"; printf '%s' "$LEA_SOUND_DONE"`, "sh", p).CombinedOutput()
		if err != nil || string(out) != v {
			t.Errorf("sh reads %q (%v), want %q", out, err, v)
		}
		c, err := LoadConfig(w.getenv, w.root, w.proj)
		if err != nil || c.Get("LEA_SOUND_DONE") != v {
			t.Errorf("Go reads %q (%v), want %q", c.Get("LEA_SOUND_DONE"), err, v)
		}
	}
	if _, err := os.Stat(filepath.Join(w.proj, "pwned")); err == nil {
		t.Fatal("a value ran")
	}
}

// ReplaceUserConfig puts a whole file back at once: private, never through a symbolic link.
func TestReplaceUserConfig(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	p := filepath.Join(w.env["XDG_CONFIG_HOME"], "lea", "lea.conf")
	writeUserConf(t, w, "LEA_SOUND=1\n")
	if err := ReplaceUserConfig(p, []byte("LEA_SOUND=0\n")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "LEA_SOUND=0\n" {
		t.Fatalf("%q", b)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode())
	}
	link := filepath.Join(w.dir, "link.conf")
	os.Symlink(p, link)
	if err := ReplaceUserConfig(link, nil); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("symlink: %v", err)
	}
}

func TestCheckOtelUpstream(t *testing.T) {
	for _, ok := range []string{"", "otel.corp:4317", "10.0.0.1:4317"} {
		if err := CheckOtelUpstream(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"1.2.3.4,fork:80", "h:80,fork", "h;x:80", "h:080", "h:0", "h:65536", "'h':80", "h!:1", "h", ":80", "h:80:81", "h :80"} {
		if err := CheckOtelUpstream(bad); err == nil || !strings.Contains(err.Error(), "LEA_OTEL_UPSTREAM") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

// LoadConfig refuses an OTel collector the proxy would pass to socat as address options.
func TestLoadConfigRefusesABadOtelUpstream(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_OTEL_UPSTREAM='1.2.3.4,fork:80'\n")
	if _, err := LoadConfig(w.getenv, w.root, w.proj); err == nil ||
		err.Error() != "LEA_OTEL_UPSTREAM must be host:port with a plain host name, not '1.2.3.4,fork:80'" {
		t.Fatalf("err = %v", err)
	}
}
