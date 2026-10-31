package launcher

import (
	"strings"
	"testing"
)

func lockArgs(t *testing.T, w *world) []string {
	t.Helper()
	args, err := w.launcher().runArgs(w.proj, "172.18.0.5")
	if err != nil {
		t.Fatal(err)
	}
	return args
}

// Off by default: no trace of lockmq or lockfile in the container.
func TestLockHostOffByDefault(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	for _, a := range lockArgs(t, w) {
		if strings.Contains(a, "lockmq") || strings.Contains(a, "lockfile") || a == "10010" || a == "10030" {
			t.Fatalf("lock tool in the run args: %s", a)
		}
	}
}

// On: each tool's socket volume of lox up's lock on /run/<tool>, read-only, and its own clients
// group, as locksql's; nothing of the host (no system.toml, no host directory, no console
// account's files).
func TestLockHostWired(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_LOCKSQL=1\nLEA_LOCKMQ=1\nLEA_LOCKFILE=1\n")
	writeUp(t, w, lockUp("crm", "locksql")+","+lockUp("events", "lockmq")+","+lockUp("docs", "lockfile"))
	labels(w, "crm", "locksql", "socket")
	labels(w, "events", "lockmq", "socket")
	labels(w, "docs", "lockfile", "socket")
	args := lockArgs(t, w)
	for _, m := range []string{"LEA_LOCKMQ=1", "LEA_LOCKFILE=1", "type=volume,src=" + upProjectOf(w) + "_events-run,dst=/run/lockmq,readonly",
		"type=volume,src=" + upProjectOf(w) + "_docs-run,dst=/run/lockfile,readonly"} {
		if !contains(args, m) {
			t.Errorf("missing %q in %v", m, args)
		}
	}
	groups := map[string]bool{}
	for i, a := range args {
		if a == "--group-add" {
			groups[args[i+1]] = true
		}
		for _, bad := range []string{"/etc/lockmq", "/etc/lockfile", "/home/lockmq", "/home/lockfile", "lockfile-home", "LEA_LOCKMQ_UID", "LEA_LOCKFILE_UID", "LEA_LOCKMQ_GID", "LEA_LOCKFILE_GID"} {
			if strings.Contains(a, bad) {
				t.Errorf("host or console files reach the container: %s", a)
			}
		}
	}
	if !groups["10010"] || !groups["10030"] {
		t.Errorf("clients groups %v, want 10010 and 10030", groups)
	}
	if strings.Contains(w.errb.String(), "warning") {
		t.Errorf("warnings: %s", w.errb.String())
	}
}

// The consoles run hub-attached only, under lox up: a tool that is on, and has no lock of lox up
// for the projects directory, gets no socket (never the fixed <tool>-run volume of a console of
// its own launcher), and lea says to run `lox up`.
func TestLockHostNeedsLoxUp(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_LOCKSQL=1\nLEA_LOCKMQ=1\nLEA_LOCKFILE=1\n")
	writeUp(t, w, lockUp("crm", "locksql")+","+lockUp("events", "lockmq"))
	labels(w, "crm", "locksql", "socket")
	labels(w, "events", "lockmq", "socket")
	args := lockArgs(t, w)
	if !contains(args, "LEA_LOCKMQ=1") || contains(args, "LEA_LOCKFILE=1") || contains(args, "10030") {
		t.Fatalf("args %v", args)
	}
	for _, a := range args {
		if strings.Contains(a, "src=lockmq-run") || strings.Contains(a, "src=lockfile-run") {
			t.Errorf("fixed socket volume: %s", a)
		}
	}
	if e := w.errb.String(); !strings.Contains(e, "no lockfile lock of lox up") || !strings.Contains(e, "run `lox up`") || strings.Contains(e, "lockmq") {
		t.Errorf("warnings: %s", e)
	}
	// No up.json at all: neither.
	w = newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_LOCKSQL=1\nLEA_LOCKMQ=1\nLEA_LOCKFILE=1\n")
	args = lockArgs(t, w)
	if contains(args, "LEA_LOCKMQ=1") || contains(args, "LEA_LOCKFILE=1") || w.hasCall("stat") {
		t.Fatalf("args %v", args)
	}
	if e := w.errb.String(); strings.Count(e, "run `lox up`") != 3 {
		t.Errorf("warnings: %s", e)
	}
}

func TestLockHostConfig(t *testing.T) {
	for conf, want := range map[string]string{
		"LEA_LOCKMQ=yes\n":          "LEA_LOCKMQ must be 1",
		"LEA_LOCKFILE=2\n":          "LEA_LOCKFILE must be 1",
		"LEA_LOCKMQ_REF=latest\n":   "LEA_LOCKMQ_REF must be a lockmq release",
		"LEA_LOCKFILE_REF=main\n":   "LEA_LOCKFILE_REF must be a lockfile release",
		"LEA_LOCKFILE_REF=v0.1.0\n": "sandbox",
	} {
		w := newWorld(t, repoRoot(t))
		writeUserConf(t, w, conf)
		_, err := LoadConfig(w.getenv, w.root, w.proj)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", conf, err, want)
		}
	}
	w := newWorld(t, repoRoot(t))
	cfg, err := LoadConfig(w.getenv, w.root, w.proj)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LockHost("LEA_LOCKMQ") || cfg.LockHost("LEA_LOCKFILE") {
		t.Fatal("lockmq and lockfile must be off by default")
	}
}

func TestLockfileSafe(t *testing.T) {
	for v, want := range map[string]bool{"v0.1.0": false, "v0.1.1": true, "v0.2.0": true, "1.0.0": true, "v0.0.9": false, "x": false} {
		if lockfileSafe(v) != want {
			t.Errorf("lockfileSafe(%q) = %v", v, !want)
		}
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// TestRunArgsLocksOnMatchTheGoldenFile: the docker run arguments with lockmq and lockfile on (their
// lox up socket volumes and clients groups, nothing of the host).
func TestRunArgsLocksOnMatchTheGoldenFile(t *testing.T) {
	w := newWorld(t, copyRoot(t))
	writeUserConf(t, w, "LEA_LOCKSQL=1\nLEA_LOCKMQ=1\nLEA_LOCKFILE=1\n")
	writeUp(t, w, lockUp("crm", "locksql")+","+lockUp("events", "lockmq")+","+lockUp("docs", "lockfile"))
	labels(w, "crm", "locksql", "socket")
	labels(w, "events", "lockmq", "socket")
	labels(w, "docs", "lockfile", "socket")
	a, err := w.launcher().runArgs(w.proj, "172.18.0.5")
	if err != nil {
		t.Fatal(err)
	}
	// lox up's project derives from the projects directory's path.
	got := strings.ReplaceAll(strings.Join(a, "\n")+"\n", upProjectOf(w), "lockfamily-$PROJECT")
	checkGolden(t, "run-args", "lockmq and lockfile on", hostNeutral(normalize(w, got)))
}
