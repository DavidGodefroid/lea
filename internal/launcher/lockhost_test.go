package launcher

import (
	"os"
	"path/filepath"
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
// its own launcher), and lea says to add one (`lox add <tool>`).
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
	if e := w.errb.String(); !strings.Contains(e, "no such lock yet: `lox add lockfile`") || !strings.Contains(e, "(lea's agents get its socket once lea restarts: lea stop, then lea)") || strings.Contains(e, "lockmq") {
		t.Errorf("warnings: %s", e)
	}
	// No up.json at all: neither.
	w = newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_LOCKSQL=1\nLEA_LOCKMQ=1\nLEA_LOCKFILE=1\n")
	args = lockArgs(t, w)
	if contains(args, "LEA_LOCKMQ=1") || contains(args, "LEA_LOCKFILE=1") || w.hasCall("stat") {
		t.Fatalf("args %v", args)
	}
	if e := w.errb.String(); strings.Count(e, "no such lock yet: `lox add ") != 3 {
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
	if on := cfg.ToolsOn(w.proj, func(string, ...any) {}); on["lockmq"] || on["lockfile"] || on["locksql"] {
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

// A lock lox up runs for a tool the user turned off (LEA_<TOOL>=0): one line says how to turn it
// on; the agents get none of its socket.
func TestLockUpLockOfAToolThatIsOff(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_LOCKSQL=0\nLEA_LOCKMQ=0\n")
	writeUp(t, w, lockUp("crm", "locksql")+","+lockUp("events", "lockmq"))
	labels(w, "crm", "locksql", "socket")
	labels(w, "events", "lockmq", "socket")
	for _, a := range lockArgs(t, w) {
		if strings.Contains(a, upProjectOf(w)+"_") || a == "LEA_LOCKSQL=1" {
			t.Errorf("a socket of a tool that is off: %s", a)
		}
	}
	e := w.errb.String()
	if strings.Count(e, "locksql is off (LEA_LOCKSQL=0), though lox up runs a locksql lock here: `lea init --locksql` turns it on") != 1 ||
		strings.Count(e, "lockmq is off (LEA_LOCKMQ=0)") != 1 || strings.Contains(e, "lockfile") {
		t.Errorf("stderr: %s", e)
	}
}

// Interactive lea init's default "no" (lockmq, lockfile) writes nothing: a user assignment would
// count as an explicit opt-out, so a later lox add would never reach the agents.
func TestInitDefaultNoLeavesToolUnset(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	so, se, sc := shellEval(t, w.root, w.environ(), w.proj,
		"lea_load_config; lea__init_tty() { true; }; printf '\\n' | lea__init_lockhost lockmq; printf '\\n' | lea__init_lockhost lockfile")
	if sc != 0 || !strings.Contains(se, "through a lockmq console") || !strings.Contains(se, "through a lockfile console") {
		t.Fatalf("status %d\nstdout %s\nstderr %s", sc, so, se)
	}
	b, _ := os.ReadFile(filepath.Join(w.env["XDG_CONFIG_HOME"], "lea", "lea.conf"))
	if strings.Contains(string(b), "LEA_LOCKMQ") || strings.Contains(string(b), "LEA_LOCKFILE") {
		t.Fatalf("user config: %s", b)
	}
	writeUp(t, w, lockUp("events", "lockmq")+","+lockUp("docs", "lockfile"))
	cfg, err := LoadConfig(w.getenv, w.root, w.proj)
	if err != nil {
		t.Fatal(err)
	}
	if on := cfg.ToolsOn(w.proj, func(string, ...any) {}); !on["lockmq"] || !on["lockfile"] {
		t.Fatalf("after lox add: %v", on)
	}
}

// lea locksql, lea lockmq and lea lockfile take the launcher's decision (ToolsOn): a lock of lox
// up for the projects directory turns a tool on that the user's configuration does not set, and
// an explicit LEA_<TOOL>=0 keeps it off.
func TestToolStatusFollowsUpJSON(t *testing.T) {
	for tool, key := range map[string]string{"locksql": "LEA_LOCKSQL", "lockmq": "LEA_LOCKMQ", "lockfile": "LEA_LOCKFILE"} {
		w := newWorld(t, repoRoot(t))
		writeUp(t, w, lockUp("orders", tool))
		if st := w.launcher().Main([]string{tool}); st != 0 {
			t.Fatalf("%s: status %d: %s", tool, st, w.errb.String())
		}
		if e := w.errb.String(); !strings.Contains(e, tool+" is on for lea while lox up runs a "+tool+" lock here") || strings.Contains(e, " is off") {
			t.Errorf("%s with a lock of lox up: %s", tool, e)
		}
		w = newWorld(t, repoRoot(t))
		writeUp(t, w, lockUp("orders", tool))
		writeUserConf(t, w, key+"=0\n")
		if st := w.launcher().Main([]string{tool}); st != 0 {
			t.Fatalf("%s: status %d: %s", tool, st, w.errb.String())
		}
		if e := w.errb.String(); !strings.Contains(e, tool+" is off ("+key+"=0)") {
			t.Errorf("%s with %s=0: %s", tool, key, e)
		}
	}
}

// lea doctor checks the lock of a tool that a lock of lox up turns on, as the launcher does.
func TestDoctorFollowsUpJSON(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeUp(t, w, lockUp("orders", "locksql")+","+lockUp("events", "lockmq"))
	w.env["FAKE_DOCKER_OUT_VOLUME_LS"] = upProjectOf(w) + "_orders-run"
	w.launcher().Main([]string{"doctor"})
	if o := w.out.String(); !strings.Contains(o, "ok    locksql lock of lox up (socket volume "+upProjectOf(w)+"_orders-run)") ||
		!strings.Contains(o, "lockmq launcher") || strings.Contains(o, "lockfile") || strings.Contains(o, "locksql image") {
		t.Errorf("doctor: %s", o)
	}
}
