package launcher

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestFirstRunNotNeededMakesNoCall(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	if err := w.launcher().firstRun(); err != nil {
		t.Fatal(err)
	}
	if w.hasCall("docker [version]") || w.hasCall("lea-sh") {
		t.Fatalf("calls: %v", w.calls())
	}
	if want := "docker [image] [inspect] [-f] [{{.Id}}] [lea:dev]\ndocker [image] [inspect] [-f] [{{.Id}}] [lea-proxy:dev]"; strings.Join(w.calls(), "\n") != want {
		t.Fatalf("calls: %v", w.calls())
	}
}

func TestFirstRunDockerDownSaysHow(t *testing.T) {
	for os_, want := range map[string]string{
		"linux":  "Docker does not answer: start the Docker engine (sudo systemctl start docker), then run lea again",
		"darwin": "Docker does not answer: start Docker Desktop (open -a Docker), then run lea again",
	} {
		t.Run(os_, func(t *testing.T) {
			saved := hostOS
			hostOS = os_
			t.Cleanup(func() { hostOS = saved })
			w := newWorld(t, repoRoot(t))
			os.Remove(w.data + "/lea/state/claude.json")
			w.env["FAKE_DOCKER_RC_VERSION___FORMAT"] = "1"
			err := w.launcher().firstRun()
			if err == nil || err.Error() != want {
				t.Fatalf("firstRun() = %v, want %q", err, want)
			}
			if w.hasCall("docker [run]") || w.hasCall("lea-sh") {
				t.Fatalf("calls: %v", w.calls())
			}
		})
	}
}

// On a machine that is not set up, the cheap refusals answer before any docker call or init.
func TestCheapRefusalsComeBeforeTheFirstRun(t *testing.T) {
	for _, name := range []string{"claude args", "no tmux", "planted symlink"} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t, copyRoot(t))
			fakeLeaSh(w, leaShLog)
			os.Remove(w.data + "/lea/state/claude.json")
			w.env["FAKE_DOCKER_RC_IMAGE_INSPECT"] = "1"
			args, want := []string(nil), ""
			switch name {
			case "claude args":
				args, want = []string{"--resume"}, "lea new -- --resume"
			case "no tmux":
				w.env["LEA_TMUX_BIN"] = w.dir + "/none"
				want = "is not an executable file"
			default:
				target := w.dir + "/target"
				os.MkdirAll(target, 0o755)
				p := w.data + "/lea/state/go"
				os.RemoveAll(p)
				os.Symlink(target, p)
				args, want = []string{"shell"}, p+" is a symbolic link"
			}
			if got := w.launcher().Main(args); got != 1 || !strings.Contains(w.errb.String(), want) {
				t.Fatalf("status %d: %q", got, w.errb.String())
			}
			if w.hasCall("docker") || w.hasCall("lea-sh") {
				t.Fatalf("calls: %v", w.calls())
			}
		})
	}
}

// A pull that fails stops lea with the command to retry: lea never builds the image instead, and
// never records lea:dev (unverified, and skipped by every later check) in the user config.
func TestFirstRunFailedPullStops(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_IMAGE=reg/a:1\nLEA_PROXY_IMAGE=reg/b:1\n")
	w.env["FAKE_DOCKER_RC_IMAGE_INSPECT"] = "1"
	w.env["FAKE_DOCKER_RC_PULL_REG_A_1"] = "1"
	err := w.launcher().firstRun()
	if err == nil {
		t.Fatal("firstRun succeeded")
	}
	if e := w.errb.String(); !strings.Contains(e, "cannot pull reg/a:1: check your network or registry (docker pull reg/a:1), then run lea again") {
		t.Errorf("stderr: %s", e)
	}
	if w.hasCall("docker [build]") || w.hasCall("docker [pull] [reg/b:1]") {
		t.Errorf("calls: %v", w.calls())
	}
	b, _ := os.ReadFile(w.env["XDG_CONFIG_HOME"] + "/lea/lea.conf")
	if strings.Contains(string(b), ":dev") {
		t.Errorf("user config: %s", b)
	}
}

// docker pull's progress is one line per step on success (lea's "pulling" line), and shown in
// full when the pull fails.
func TestFirstRunPullOutputOnlyOnFailure(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_IMAGE=reg/a:1\nLEA_PROXY_IMAGE=reg/b:1\n")
	w.env["FAKE_DOCKER_RC_IMAGE_INSPECT"] = "1"
	w.env["FAKE_DOCKER_OUT_PULL_REG_A_1"] = "layer progress of a"
	w.env["FAKE_DOCKER_OUT_PULL_REG_B_1"] = "layer progress of b"
	if err := w.launcher().firstRun(); err != nil {
		t.Fatalf("firstRun: %v\n%s", err, w.errb.String())
	}
	if e := w.errb.String(); strings.Contains(e, "layer progress") || !strings.Contains(e, "pulling reg/a:1") {
		t.Errorf("stderr on success: %s", e)
	}
	w = newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_IMAGE=reg/a:1\nLEA_PROXY_IMAGE=reg/b:1\n")
	w.env["FAKE_DOCKER_RC_IMAGE_INSPECT"] = "1"
	w.env["FAKE_DOCKER_OUT_PULL_REG_A_1"] = "registry unreachable"
	w.env["FAKE_DOCKER_RC_PULL_REG_A_1"] = "1"
	if err := w.launcher().firstRun(); err == nil {
		t.Fatal("firstRun succeeded")
	}
	if e := w.errb.String(); !strings.Contains(e, "registry unreachable") {
		t.Errorf("stderr on failure: %s", e)
	}
}

// loxUpWorld is a projects directory with a Lock Family (lockfamily.yaml) and LEA_LOX_UP=conf.
func loxUpWorld(t *testing.T, conf string) *world {
	t.Helper()
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, conf)
	if err := os.MkdirAll(w.proj+"/.lockfamily", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(w.proj+"/.lockfamily/lockfamily.yaml", []byte("locks: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pr := lockUpComposeProject(w.proj)
	up := `{"version": 1, "project": "` + pr + `", "workspace": "` + w.proj + `",
		"locks": {"crm": {"type": "locksql", "socket_volume": "` + pr + `_crm-run"},
		"bus": {"type": "lockmq", "socket_volume": "` + pr + `_bus-run"}}}`
	if err := os.WriteFile(w.proj+"/.lockfamily/up.json", []byte(up), 0o600); err != nil {
		t.Fatal(err)
	}
	return w
}

// loxJournal writes lox run's session journal of w's workspace in its state home, events in order.
func loxJournal(t *testing.T, w *world, events ...string) {
	t.Helper()
	dir := filepath.Join(w.dir, "state", "lockfamily", lockUpComposeProject(w.proj))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range events {
		b.WriteString(`{"at":"2026-11-06T10:00:00Z","event":"` + e + `","user":"me","workspace":"~/p"}` + "\n")
	}
	if err := os.WriteFile(filepath.Join(dir, loxRunsFile), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	w.env["XDG_STATE_HOME"] = filepath.Join(w.dir, "state")
}

// stubLoxRunProcs fakes the process table: running, and whether lea can read it.
func stubLoxRunProcs(t *testing.T, running, ok bool) {
	old := loxRunProcs
	loxRunProcs = func() (bool, bool) { return running, ok }
	t.Cleanup(func() { loxRunProcs = old })
}

// No lox command runs at start (#300): no lox init, lox run, lox attach, not even lox version.
func TestEnsureHubRunsNoLox(t *testing.T) {
	for _, conf := range []string{"", "LEA_HUB=0\n"} {
		w := loxUpWorld(t, conf)
		stubLoxRunProcs(t, false, true)
		if err := w.launcher().ensureHub(w.proj); err != nil {
			t.Fatal(err)
		}
		if w.hasCall("lox") || w.hasCall("docker") {
			t.Fatalf("conf %q: calls %v", conf, w.calls())
		}
	}
	w := newWorld(t, repoRoot(t)) // no .lockfamily at all: no lox init
	if err := w.launcher().ensureHub(w.proj); err != nil {
		t.Fatal(err)
	}
	if w.hasCall("lox") || w.errb.String() != "" {
		t.Fatalf("calls %v, stderr %q", w.calls(), w.errb.String())
	}
}

// Locks and no lox run: lea says to open another terminal and run lox run, and starts anyway.
func TestEnsureHubSaysRunLoxRun(t *testing.T) {
	w := loxUpWorld(t, "")
	stubLoxRunProcs(t, false, true)
	if err := w.launcher().ensureHub(w.proj); err != nil {
		t.Fatalf("ensureHub() = %v, want lea to start anyway", err)
	}
	want := "lea: Open another terminal (outside lea) and run `lox run` to approve the agents' connections and writes.\n"
	if got := w.errb.String(); got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

// A lockfamily.yaml that lox run never applied (no up.json) has locks too: lea says it.
func TestEnsureHubLockfamilyWithoutUpJSON(t *testing.T) {
	w := loxUpWorld(t, "")
	if err := os.Remove(w.proj + "/.lockfamily/up.json"); err != nil {
		t.Fatal(err)
	}
	stubLoxRunProcs(t, false, true)
	if err := w.launcher().ensureHub(w.proj); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.errb.String(), "run `lox run`") {
		t.Fatalf("stderr = %q", w.errb.String())
	}
}

// lox run serves (its journal's last event is a start or a mode switch, and its process runs):
// lea says nothing. A stop, or a start whose process is gone (killed), is no lox run.
func TestEnsureHubLoxRunServing(t *testing.T) {
	for _, c := range []struct {
		events      []string
		running, ok bool
		quiet       bool
	}{
		{[]string{"start"}, true, true, true},
		{[]string{"start", "mode"}, true, true, true},
		{[]string{"start"}, false, false, true}, // no /proc (macOS): the journal says
		{[]string{"start", "stop"}, true, true, false},
		{[]string{"start"}, false, true, false}, // killed before its stop
		{nil, true, true, false},
	} {
		w := loxUpWorld(t, "")
		loxJournal(t, w, c.events...)
		stubLoxRunProcs(t, c.running, c.ok)
		if err := w.launcher().ensureHub(w.proj); err != nil {
			t.Fatal(err)
		}
		if quiet := w.errb.String() == ""; quiet != c.quiet {
			t.Errorf("%+v: stderr %q", c, w.errb.String())
		}
	}
}

// LEA_LOX_UP is obsolete: set, lea warns and ignores it (it runs no lox up).
func TestEnsureHubWarnsObsoleteLoxUp(t *testing.T) {
	w := loxUpWorld(t, "LEA_LOX_UP=1\n")
	loxJournal(t, w, "start")
	stubLoxRunProcs(t, true, true)
	if err := w.launcher().ensureHub(w.proj); err != nil {
		t.Fatal(err)
	}
	if got := w.errb.String(); !strings.Contains(got, "warning: LEA_LOX_UP=1 is obsolete and ignored: lea no longer runs lox up") {
		t.Fatalf("stderr = %q", got)
	}
	if w.hasCall("lox") {
		t.Fatalf("calls %v", w.calls())
	}
}

// lea-ctl's container sees neither the journal nor the host's processes: lea does not tell.
func TestLoxRunStateUnknownInCtl(t *testing.T) {
	get := func(k string) string { return map[string]string{"LEA_CTL_INSIDE": "1", "HOME": "/h"}[k] }
	if st := LoxRunState(get, "/p"); st != LoxRunUnknown {
		t.Fatalf("LoxRunState = %q", st)
	}
	if st := LoxRunState(func(string) string { return "" }, "/p"); st != LoxRunUnknown {
		t.Fatalf("no HOME: LoxRunState = %q", st)
	}
}

// The journal's last event that reads counts; a torn last line is skipped.
func TestLastRunEvent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "runs.jsonl")
	for _, c := range []struct{ body, want string }{
		{`{"event":"start"}` + "\n", "start"},
		{`{"event":"start"}` + "\n" + `{"event":"stop"}` + "\n", "stop"},
		{`{"event":"stop"}` + "\n" + `{"event":"start"}` + "\n" + `{"ev`, "start"},
		{`{"event":"other"}` + "\n", ""},
		{"", ""},
	} {
		if err := os.WriteFile(p, []byte(c.body), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := lastRunEvent(p); got != c.want {
			t.Errorf("lastRunEvent(%q) = %q, want %q", c.body, got, c.want)
		}
	}
	if got := lastRunEvent(filepath.Join(t.TempDir(), "missing")); got != "" {
		t.Errorf("missing journal: %q", got)
	}
}

// isLoxRun knows lox run by its argv: lox (any path), then run.
func TestIsLoxRun(t *testing.T) {
	for argv, want := range map[string]bool{
		"lox\x00run": true, "/opt/homebrew/bin/lox\x00run\x00--manual": true,
		"lox\x00approve": false, "lox": false, "xlox\x00run": false, "sh\x00-c\x00lox run": false,
	} {
		if got := isLoxRun(strings.Split(argv, "\x00")); got != want {
			t.Errorf("isLoxRun(%q) = %v, want %v", argv, got, want)
		}
	}
}

// procLoxRun reads this host's process table where there is one.
func TestProcLoxRunReadsProc(t *testing.T) {
	if _, err := os.Stat("/proc/self/cmdline"); err != nil {
		t.Skip("no /proc")
	}
	if _, ok := procLoxRun(); !ok {
		t.Fatal("procLoxRun() could not read /proc")
	}
	if !procOwnedBy("/proc/self/status", strconv.Itoa(os.Getuid())) {
		t.Fatal("procOwnedBy(/proc/self/status) = false")
	}
}
