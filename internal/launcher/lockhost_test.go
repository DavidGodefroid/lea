package launcher

import (
	"os"
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

// The fake docker's answers for the lockfile-run volume: absent, or present with an owner.
const (
	volInspectLockfile = "FAKE_DOCKER_RC_VOLUME_INSPECT_AT_LOCKFILE_RUN"
	volOwner           = "FAKE_DOCKER_OUT_RUN___RM_AT__R"
)

// Off by default: no trace of lockmq or lockfile in the container.
func TestLockHostOffByDefault(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	for _, a := range lockArgs(t, w) {
		if strings.Contains(a, "lockmq") || strings.Contains(a, "lockfile") || a == "10010" || a == "10030" {
			t.Fatalf("lock tool in the run args: %s", a)
		}
	}
}

// On: each tool's socket volume on /run/<tool> and its own clients group, as locksql's; nothing
// of the host (no system.toml, no host directory, no console account's files).
func TestLockHostWired(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_LOCKMQ=1\nLEA_LOCKFILE=1\n")
	w.env[volInspectLockfile] = "1" // no volume yet: lea's image initialises it
	args := lockArgs(t, w)
	for _, m := range []string{"LEA_LOCKMQ=1", "LEA_LOCKFILE=1", "type=volume,src=lockmq-run,dst=/run/lockmq",
		"type=volume,src=lockfile-run,dst=/run/lockfile"} {
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

// A lockfile-run volume of lockfile before 0.3 (locksql's ids) leaves lockfile out until its
// launcher migrates it; lockmq is wired all the same. Lea never migrates it itself.
func TestLockfileVolumeOldIDs(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_LOCKMQ=1\nLEA_LOCKFILE=1\n")
	w.env[volOwner] = "10001:10000"
	args := lockArgs(t, w)
	if contains(args, "LEA_LOCKFILE=1") || !contains(args, "LEA_LOCKMQ=1") {
		t.Fatalf("args %v", args)
	}
	if !strings.Contains(w.errb.String(), "lockfile console") || !w.hasCall("stat") || w.hasCall("migrate-ids") {
		t.Errorf("warning %q, calls %v", w.errb.String(), w.calls())
	}
	if _, err := os.Stat(w.hostDir() + "/lockfile-run-ids"); err == nil {
		t.Error("an old volume was marked checked")
	}
}

// A volume with lockfile's own ids is wired, and not checked again.
func TestLockfileVolumeChecked(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_LOCKFILE=1\n")
	w.env[volOwner] = "10031:10030"
	if args := lockArgs(t, w); !contains(args, "LEA_LOCKFILE=1") {
		t.Fatalf("args %v", args)
	}
	w.env[volOwner] = "10001:10000"
	if args := lockArgs(t, w); !contains(args, "LEA_LOCKFILE=1") {
		t.Fatalf("checked again: %v\n%s", args, w.errb.String())
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
// socket volumes and clients groups, nothing of the host).
func TestRunArgsLocksOnMatchTheGoldenFile(t *testing.T) {
	w := newWorld(t, copyRoot(t))
	writeUserConf(t, w, "LEA_LOCKMQ=1\nLEA_LOCKFILE=1\n")
	w.env[volInspectLockfile] = "1"
	a, err := w.launcher().runArgs(w.proj, "172.18.0.5")
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "run-args", "lockmq and lockfile on", hostNeutral(normalize(w, strings.Join(a, "\n")+"\n")))
}
