package launcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGuardRefusesWhatTheShellRefuses(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	l := w.launcher()
	os.MkdirAll(w.home+"/.claude/x", 0o755)
	os.Symlink(w.home, w.dir+"/homelink")
	refused := map[string]string{
		"/":                          "it is / or contains your home directory",
		w.home:                       "it is / or contains your home directory",
		w.dir:                        "it is / or contains your home directory",
		w.home + "/.claude":          "it overlaps",
		w.home + "/.claude/x":        "it overlaps",
		w.data:                       "it overlaps",
		l.Root:                       "it overlaps",
		physDir(w.dir + "/homelink"): "contains your home directory",
	}
	for p, want := range refused {
		if err := l.guardProjects(p); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", p, err, want)
		}
	}
	os.MkdirAll(w.home+"2", 0o755) // a sibling with a common prefix
	if err := l.guardProjects(w.home + "2"); err != nil {
		t.Fatalf("sibling refused: %v", err)
	}
	if err := l.guardProjects(w.proj); err != nil {
		t.Fatalf("projects refused: %v", err)
	}
}

func TestCheckPathsRefusesPlantedSymlinks(t *testing.T) {
	for _, rel := range []string{"state/kube", "host/sessions/ID", "host/scan/ID.start", "state/claude/ID"} {
		w := newWorld(t, repoRoot(t))
		l := w.launcher()
		p := w.data + "/lea/" + strings.ReplaceAll(rel, "ID", w.id())
		os.MkdirAll(p[:strings.LastIndex(p, "/")], 0o755)
		os.Symlink(w.dir, p)
		if err := l.checkPaths(w.proj); err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Errorf("%s: err = %v", rel, err)
		}
	}
}

func TestProjectsDirComesFromTheConfiguration(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	delete(w.env, "LEA_PROJECTS_DIR")
	if _, err := w.launcher().projectsDir(); err == nil || err.Error() != "no projects directory: run 'lea init', or set LEA_PROJECTS_DIR" {
		t.Fatalf("err = %v", err)
	}
	w.env["LEA_PROJECTS_DIR"] = w.dir + "/nope"
	if _, err := w.launcher().projectsDir(); err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("err = %v", err)
	}
}

// The lea installation, a directory inside it and one containing it cannot be projects dirs.
func TestGuardRefusesTheInstallation(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	l := w.launcher()
	l.Root = w.dir + "/inst/lea"
	os.MkdirAll(l.Root+"/libexec", 0o755)
	for _, p := range []string{l.Root, l.Root + "/libexec", w.dir + "/inst"} {
		if err := l.guardProjects(p); err == nil || !strings.Contains(err.Error(), "it overlaps "+l.Root) {
			t.Errorf("%s: err = %v", p, err)
		}
	}
}

// A symlink planted on a path the launcher mounts, writes or removes stops lea shell before any
// docker call, and nothing is written or removed through it (open.bats symlink refusals).
func TestPlantedSymlinksStopTheSessionBeforeDocker(t *testing.T) {
	for _, rel := range []string{"state/claude.json", "state/claude/ID", "state/kube", "host/sessions/ID", "host/scan/ID.start",
		"HOME/.claude/projects/SLUG"} {
		t.Run(strings.ReplaceAll(rel, "/", "_"), func(t *testing.T) {
			w := newWorld(t, repoRoot(t))
			running(w)
			p := w.data + "/lea/" + strings.ReplaceAll(rel, "ID", w.id())
			if strings.HasPrefix(rel, "HOME") {
				p = w.home + strings.ReplaceAll(strings.TrimPrefix(rel, "HOME"), "SLUG", Slug(w.proj))
			}
			target := w.dir + "/target"
			os.MkdirAll(target, 0o755)
			os.WriteFile(target+"/999999", []byte("keep"), 0o600)
			if strings.HasSuffix(rel, ".json") || strings.HasSuffix(rel, ".start") {
				target += "/999999"
			}
			os.Remove(p)
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.Symlink(target, p)
			if got := w.launcher().Main([]string{"shell"}); got == 0 || !strings.Contains(w.errb.String(), p+" is a symbolic link") {
				t.Fatalf("status %d: %q", got, w.errb.String())
			}
			if w.hasCall("docker") {
				t.Fatalf("docker was called:\n%s", strings.Join(w.calls(), "\n"))
			}
			ents, _ := os.ReadDir(w.dir + "/target")
			if len(ents) != 1 || readTrim(w.dir+"/target/999999") != "keep" {
				t.Fatalf("written or removed through the symlink: %v", ents)
			}
		})
	}
}
