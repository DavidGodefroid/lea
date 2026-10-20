package launcher

import (
	"os"
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
