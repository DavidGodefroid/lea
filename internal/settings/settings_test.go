package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type world struct {
	root, cwd, user string
	env             map[string]string
}

func newWorld(t *testing.T, org string) *world {
	t.Helper()
	dir := t.TempDir()
	w := &world{root: dir + "/root", cwd: dir, user: dir + "/config/lea/lea.conf",
		env: map[string]string{"HOME": dir, "XDG_CONFIG_HOME": dir + "/config"}}
	os.MkdirAll(w.root+"/config", 0o755)
	if err := os.WriteFile(w.root+"/config/lea.conf", []byte(org), 0o644); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *world) store() Store {
	return Store{Root: w.root, Cwd: w.cwd, Getenv: func(k string) string { return w.env[k] }}
}

func (w *world) userConf(t *testing.T, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(w.user), 0o700)
	if err := os.WriteFile(w.user, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func find(t *testing.T, ss []Setting, key string) Setting {
	t.Helper()
	for _, s := range ss {
		if s.Key == key {
			return s
		}
	}
	t.Fatalf("no setting %s", key)
	return Setting{}
}

func list(t *testing.T, w *world) []Setting {
	t.Helper()
	ss, err := w.store().List()
	if err != nil {
		t.Fatal(err)
	}
	return ss
}

// Every setting has a group, a name, a help line and says when it applies; the list keeps the
// catalog's order, grouped.
func TestTheCatalogIsComplete(t *testing.T) {
	w := newWorld(t, "LEA_APPROVAL_TIMEOUT=60\n")
	ss := list(t, w)
	if len(ss) != len(catalog) {
		t.Fatalf("%d settings, want %d", len(ss), len(catalog))
	}
	seen := map[string]bool{}
	for i, s := range ss {
		if s.Group == "" || s.Name == "" || s.Help == "" || s.Apply == "" {
			t.Errorf("%s is incomplete: %+v", s.Key, s)
		}
		if seen[s.Group] && ss[i-1].Group != s.Group {
			t.Errorf("group %s is split", s.Group)
		}
		seen[s.Group] = true
		if s.Kind == Choice && len(s.Choices) < 2 {
			t.Errorf("%s: choices %v", s.Key, s.Choices)
		}
	}
	if s := find(t, ss, "LEA_SOUND"); !s.Live || s.Apply != ApplyNow {
		t.Errorf("LEA_SOUND applies %q live=%v", s.Apply, s.Live)
	}
}

// A value comes from the defaults (lea's own config/lea.conf or the built-in default), the user
// file, or the environment.
func TestValueAndSource(t *testing.T) {
	w := newWorld(t, "LEA_APPROVAL_TIMEOUT=60\n")
	w.userConf(t, "LEA_APPROVALS=pane\nLEA_SOUND=0\n")
	ss := list(t, w)
	for key, want := range map[string][2]string{
		"LEA_APPROVALS":        {"pane", SourceFile},
		"LEA_SOUND":            {"0", SourceFile},
		"LEA_APPROVAL_TIMEOUT": {"60", SourceDefault},
		"LEA_AUTOUPDATE":       {"1", SourceDefault},
		"LEA_CLIPBOARD":        {"1", SourceDefault},
	} {
		if s := find(t, ss, key); s.Value != want[0] || s.Source != want[1] || s.Locked != "" {
			t.Errorf("%s = %q from %q (locked %q), want %v", key, s.Value, s.Source, s.Locked, want)
		}
	}
}

// The deck runs under the launcher, which exports every key: only the protected keys it names in
// LEA_ENV_KEYS came from the user's environment. Those are read-only, with the reason.
func TestProtectedKeysFromTheEnvironmentAreReadOnly(t *testing.T) {
	w := newWorld(t, "LEA_APPROVAL_TIMEOUT=60\n")
	w.userConf(t, "LEA_APPROVALS=pane\nLEA_APPROVAL_TIMEOUT=30\n")
	w.env["LEA_APPROVALS"] = "pane"     // exported by the launcher, from the file
	w.env["LEA_APPROVAL_TIMEOUT"] = "5" // the user's own
	w.env["LEA_ENV_KEYS"] = "LEA_APPROVAL_TIMEOUT"
	ss := list(t, w)
	if s := find(t, ss, "LEA_APPROVALS"); s.Source != SourceFile || s.Locked != "" {
		t.Errorf("LEA_APPROVALS from %q locked %q", s.Source, s.Locked)
	}
	s := find(t, ss, "LEA_APPROVAL_TIMEOUT")
	if s.Value != "5" || s.Source != SourceEnvironment || !strings.Contains(s.Locked, "environment") {
		t.Fatalf("LEA_APPROVAL_TIMEOUT = %+v", s)
	}
	if err := w.store().Set("LEA_APPROVAL_TIMEOUT", "40"); err == nil || !strings.Contains(err.Error(), "environment") {
		t.Errorf("Set on a locked key: %v", err)
	}
	if b, _ := os.ReadFile(w.user); strings.Contains(string(b), "40") {
		t.Errorf("the file changed:\n%s", b)
	}
}

// Set validates the value, writes it to the user file and List reads it back.
func TestSetValidatesAndWrites(t *testing.T) {
	w := newWorld(t, "LEA_APPROVAL_TIMEOUT=60\n")
	st := w.store()
	for key, bad := range map[string][]string{
		"LEA_APPROVALS":        {"", "both"},
		"LEA_APPROVAL_TIMEOUT": {"0", "601", "05", "x", "", "+5"},
		"LEA_SOUND":            {"2", "on"},
		"LEA_GIT_SSH":          {"h:0", "bad host:22"},
		"LEA_NOPE":             {"1"},
		"LEA_SOUND_DONE":       {"a\nb"},
	} {
		for _, v := range bad {
			if err := st.Set(key, v); err == nil {
				t.Errorf("%s=%q accepted", key, v)
			}
		}
	}
	if _, err := os.Stat(w.user); err == nil {
		t.Fatal("a refused value wrote the file")
	}
	for key, v := range map[string]string{
		"LEA_APPROVALS": "pane", "LEA_APPROVAL_TIMEOUT": "120", "LEA_SOUND": "0",
		"LEA_GIT_SSH": "github.com:22", "LEA_SOUND_DONE": "/it's.oga", "LEA_OTEL_UPSTREAM": "",
	} {
		if err := st.Set(key, v); err != nil {
			t.Fatalf("%s=%q: %v", key, v, err)
		}
	}
	ss := list(t, w)
	for key, v := range map[string]string{"LEA_APPROVALS": "pane", "LEA_APPROVAL_TIMEOUT": "120", "LEA_SOUND": "0",
		"LEA_GIT_SSH": "github.com:22", "LEA_SOUND_DONE": "/it's.oga"} {
		if s := find(t, ss, key); s.Value != v || s.Source != SourceFile {
			t.Errorf("%s = %q from %q, want %q", key, s.Value, s.Source, v)
		}
	}
}

// The projects directory goes through lea init's checks: an existing directory, never /, the
// home directory or one of its ancestors, never overlapping ~/.claude or a credential directory,
// saved as its physical path.
func TestProjectsDirPassesTheInitGuards(t *testing.T) {
	w := newWorld(t, "")
	st := w.store()
	os.MkdirAll(w.cwd+"/.claude/projects", 0o700)
	os.MkdirAll(w.cwd+"/.ssh/keys", 0o700)
	for _, v := range []string{"relative", "/", w.cwd, filepath.Dir(w.cwd), w.cwd + "/.claude/projects", w.cwd + "/.ssh",
		w.cwd + "/.ssh/keys", w.cwd + "/nope"} {
		if err := st.Set("LEA_PROJECTS_DIR", v); err == nil {
			t.Errorf("LEA_PROJECTS_DIR=%q accepted", v)
		}
	}
	if _, err := os.Stat(w.user); err == nil {
		t.Fatal("a refused projects directory wrote the file")
	}
	real, _ := filepath.EvalSymlinks(t.TempDir())
	link := t.TempDir() + "/projects"
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := st.Set("LEA_PROJECTS_DIR", link); err != nil {
		t.Fatal(err)
	}
	if s := find(t, list(t, w), "LEA_PROJECTS_DIR"); s.Value != real {
		t.Errorf("LEA_PROJECTS_DIR = %q, want the physical %q", s.Value, real)
	}
}

// A user file the launcher would refuse is reported, never overwritten blindly.
func TestABrokenUserFileIsReported(t *testing.T) {
	w := newWorld(t, "")
	w.userConf(t, "LEA_APPROVALS=$(boom)\n")
	if _, err := w.store().List(); err == nil {
		t.Error("List accepted a broken file")
	}
}
