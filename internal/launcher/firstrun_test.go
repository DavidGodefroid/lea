package launcher

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestLoxCheckMissing(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["PATH"] = w.dir + "/bin:/usr/bin:/bin"
	err := w.launcher().loxCheck()
	want := "lox not found: lea starts the Lock Family hub with it: brew install davidgodefroid/tap/lox, then run lea again (LEA_HUB=0 lea runs without the hub)"
	if err == nil || err.Error() != want {
		t.Fatalf("loxCheck() = %v, want %q", err, want)
	}
}

func TestLoxCheckTooOld(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["FAKE_LOX_VERSION"] = "0.4.0"
	err := w.launcher().loxCheck()
	want := "lox 0.4.0 is older than 0.5.0, which lea needs to start the hub: brew upgrade lox, then run lea again (LEA_HUB=0 lea runs without the hub)"
	if err == nil || err.Error() != want {
		t.Fatalf("loxCheck() = %v, want %q", err, want)
	}
}

func TestLoxCheckOK(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	if err := w.launcher().loxCheck(); err != nil {
		t.Fatal(err)
	}
	if !w.hasCall("lox [version]") {
		t.Fatalf("calls: %v", w.calls())
	}
}

func TestLoxCheckDevWarns(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["FAKE_LOX_VERSION"] = "dev"
	if err := w.launcher().loxCheck(); err != nil {
		t.Fatal(err)
	}
	if got := w.errb.String(); got != "lea: warning: lox dev: its version is not checked\n" {
		t.Fatalf("stderr = %q", got)
	}
}

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

func TestLoxCheckVersionFails(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["FAKE_LOX_RC"] = "3"
	err := w.launcher().loxCheck()
	want := "lox version failed (above): reinstall lox (brew reinstall davidgodefroid/tap/lox), then run lea again (LEA_HUB=0 lea runs without the hub)"
	if err == nil || err.Error() != want {
		t.Fatalf("loxCheck() = %v, want %q", err, want)
	}
}

func TestLoxCheckPrintsNoVersion(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["FAKE_LOX_VERSION"] = ""
	err := w.launcher().loxCheck()
	want := "lox version printed no version: reinstall lox (brew reinstall davidgodefroid/tap/lox), then run lea again (LEA_HUB=0 lea runs without the hub)"
	if err == nil || err.Error() != want {
		t.Fatalf("loxCheck() = %v, want %q", err, want)
	}
}

func upJSON(t *testing.T, w *world) {
	t.Helper()
	if err := os.MkdirAll(w.proj+"/.lockfamily", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(w.proj+"/.lockfamily/up.json", []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureHubRunsLoxInit(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	if err := w.launcher().ensureHub(w.proj); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(w.calls(), "\n"), "lox [version]\nlox [init]"; got != want {
		t.Fatalf("calls: %q, want %q", got, want)
	}
	if got := w.errb.String(); got != "lea: first run: starting the Lock Family hub (lox init)\n" {
		t.Fatalf("stderr = %q", got)
	}
}

func TestEnsureHubSkippedWithUpJSON(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	upJSON(t, w)
	if err := w.launcher().ensureHub(w.proj); err != nil {
		t.Fatal(err)
	}
	if w.hasCall("lox") {
		t.Fatalf("calls: %v", w.calls())
	}
}

func TestEnsureHubSkippedWithoutTheHub(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_HUB=0\n")
	if err := w.launcher().ensureHub(w.proj); err != nil {
		t.Fatal(err)
	}
	if w.hasCall("lox") {
		t.Fatalf("calls: %v", w.calls())
	}
}

func TestEnsureHubLoxMissing(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["PATH"] = w.dir + "/bin:/usr/bin:/bin"
	err := w.launcher().ensureHub(w.proj)
	if err == nil || !strings.HasPrefix(err.Error(), "lox not found:") {
		t.Fatalf("ensureHub() = %v", err)
	}
	if w.hasCall("docker [run]") || w.hasCall("lox") {
		t.Fatalf("calls: %v", w.calls())
	}
}

func TestEnsureHubLoxTooOld(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["FAKE_LOX_VERSION"] = "0.4.0"
	err := w.launcher().ensureHub(w.proj)
	if err == nil || !strings.Contains(err.Error(), "lox 0.4.0 is older than 0.5.0") {
		t.Fatalf("ensureHub() = %v", err)
	}
	if w.hasCall("lox [init]") {
		t.Fatalf("calls: %v", w.calls())
	}
}

func TestEnsureHubLoxInitFails(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["FAKE_LOX_RC_INIT"] = "1"
	err := w.launcher().ensureHub(w.proj)
	want := "lox init failed (above): fix it, then run lea again (LEA_HUB=0 lea runs without the hub)"
	if err == nil || err.Error() != want {
		t.Fatalf("ensureHub() = %v, want %q", err, want)
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

// Lines of a console's /proc/net/unix: its socket listening, or connected only (no listener).
const (
	unixListen = "0000000000000000: 00000002 00000000 00010000 0001 01 4242 /run/locksql/user-p.sock"
	unixNone   = "0000000000000000: 00000003 00000000 00000000 0001 03 4243 /run/locksql/user-p.sock"
	busListen  = "0000000000000000: 00000002 00000000 00010000 0001 01 4244 /run/lockmq/user-p.sock"
)

// psCall is the docker ps of lox up's containers of w's projects directory.
func psCall(w *world) string {
	return "docker [ps] [--filter] [label=com.docker.compose.project=" + lockUpComposeProject(w.proj) + `] [--format] [{{.Label "com.docker.compose.service"}}={{.ID}}]`
}

// serving makes every console of loxUpWorld serve: crm on c1, bus on b1.
func serving(w *world) {
	w.env["FAKE_DOCKER_OUT_EXEC_C1"] = unixListen
	w.env["FAKE_DOCKER_OUT_EXEC_B1"] = busListen
}

// The hub is down: lea runs lox up once, then checks each console lox up runs; crm waits for its
// start question.
func TestLoxUpHubDown(t *testing.T) {
	w := loxUpWorld(t, "LEA_LOX_UP=1\n")
	w.env["FAKE_DOCKER_OUTSEQ_PS___FILTER"] = "lock-bus=b1|lockhub=h1 lock-crm=c1 lock-bus=b1"
	w.env["FAKE_DOCKER_OUT_EXEC_C1"] = unixNone
	w.env["FAKE_DOCKER_OUT_EXEC_B1"] = busListen
	if err := w.launcher().ensureHub(w.proj); err != nil {
		t.Fatal(err)
	}
	ps := psCall(w)
	want := []string{ps, "lox [up]", ps, "docker [exec] [b1] [cat] [/proc/net/unix]", "docker [exec] [c1] [cat] [/proc/net/unix]"}
	if got := strings.Join(w.calls(), "\n"); got != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
	wantErr := "lea: starting the Lock Family hub and its locks (lox up, LEA_LOX_UP=1)\n" +
		"lea: locksql crm waits for its start question (its console runs but does not serve yet): lox attach crm\n"
	if got := w.errb.String(); got != wantErr {
		t.Fatalf("stderr = %q, want %q", got, wantErr)
	}
}

// Hub and consoles run and serve: no lox up, nothing said.
func TestLoxUpHubAlreadyUp(t *testing.T) {
	w := loxUpWorld(t, "LEA_LOX_UP=1\n")
	w.env["FAKE_DOCKER_OUT_PS___FILTER"] = "lockhub=h1\nlock-crm=c1\nlock-bus=b1"
	serving(w)
	if err := w.launcher().ensureHub(w.proj); err != nil {
		t.Fatal(err)
	}
	if w.hasCall("lox") || w.errb.String() != "" {
		t.Fatalf("calls: %v, stderr %q", w.calls(), w.errb.String())
	}
}

// The hub runs but a lock's console does not (lea stop, a console that exited): LEA_LOX_UP=1 runs
// lox up for it too, not only for a hub that is down.
func TestLoxUpLockDown(t *testing.T) {
	w := loxUpWorld(t, "LEA_LOX_UP=1\n")
	w.env["FAKE_DOCKER_OUTSEQ_PS___FILTER"] = "lockhub=h1 lock-bus=b1|lockhub=h1 lock-crm=c1 lock-bus=b1"
	serving(w)
	if err := w.launcher().ensureHub(w.proj); err != nil {
		t.Fatal(err)
	}
	if !w.hasCall("lox [up]") {
		t.Fatalf("no lox up: calls: %v", w.calls())
	}
	if want := "lea: starting the Lock Family hub and its locks (lox up, LEA_LOX_UP=1)\n"; w.errb.String() != want {
		t.Fatalf("stderr = %q, want %q", w.errb.String(), want)
	}
}

// Off (unset or 0): no lox up, but every declared lock is still checked and the one that does not
// serve is named, with what it waits for. Without a Lock Family: no docker, no lox.
func TestLoxUpOff(t *testing.T) {
	for name, conf := range map[string]string{"unset": "", "zero": "LEA_LOX_UP=0\n"} {
		t.Run(name, func(t *testing.T) {
			w := loxUpWorld(t, conf)
			w.env["FAKE_DOCKER_OUT_PS___FILTER"] = "lockhub=h1\nlock-crm=c1"
			w.env["FAKE_DOCKER_OUT_EXEC_C1"] = unixListen
			if err := w.launcher().ensureHub(w.proj); err != nil {
				t.Fatal(err)
			}
			if w.hasCall("lox") {
				t.Fatalf("calls: %v", w.calls())
			}
			if want := "lea: lockmq bus: its console is not running: run lox up\n"; w.errb.String() != want {
				t.Fatalf("stderr = %q, want %q", w.errb.String(), want)
			}
		})
	}
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_LOX_UP=1\nLEA_HUB=0\n")
	if err := w.launcher().ensureHub(w.proj); err != nil {
		t.Fatal(err)
	}
	if w.hasCall("docker") || w.hasCall("lox") {
		t.Fatalf("no lockfamily.yaml: calls: %v", w.calls())
	}
}

func TestLoxUpFailureWarns(t *testing.T) {
	w := loxUpWorld(t, "LEA_LOX_UP=1\n")
	w.env["FAKE_LOX_RC"] = "1"
	if err := w.launcher().ensureHub(w.proj); err != nil {
		t.Fatalf("ensureHub() = %v, want lea to start anyway", err)
	}
	want := "lea: starting the Lock Family hub and its locks (lox up, LEA_LOX_UP=1)\n" +
		"lea: warning: lox up failed (above): lea starts without it; fix it, then run lox up\n" +
		"lea: lockmq bus: its console is not running: run lox up\n" +
		"lea: locksql crm: its console is not running: run lox up\n"
	if got := w.errb.String(); got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func TestLoxUpDockerFailsWarns(t *testing.T) {
	w := loxUpWorld(t, "LEA_LOX_UP=1\n")
	w.env["FAKE_DOCKER_RC_PS___FILTER"] = "1"
	if err := w.launcher().ensureHub(w.proj); err != nil {
		t.Fatal(err)
	}
	if w.hasCall("lox") || !strings.Contains(w.errb.String(), "warning: LEA_LOX_UP: cannot list") {
		t.Fatalf("calls: %v, stderr %q", w.calls(), w.errb.String())
	}
}

// After lea stop, then lea (#259): the hub and crm's console run, but crm's console restarted and
// waits for its start question. At a terminal lea offers to answer it there (lox attach, in the
// projects directory), then checks again; enter takes the default, yes.
func TestConsoleWaitsAttach(t *testing.T) {
	w := loxUpWorld(t, "")
	w.env["FAKE_DOCKER_OUT_PS___FILTER"] = "lockhub=h1\nlock-crm=c1\nlock-bus=b1"
	w.env["FAKE_DOCKER_OUTSEQ_EXEC_C1"] = unixNone + "|" + unixListen
	w.env["FAKE_DOCKER_OUT_EXEC_B1"] = busListen
	l := w.launcher()
	l.Stdin = strings.NewReader("\n")
	defer stubAtTerminal(true)()
	if err := l.ensureHub(w.proj); err != nil {
		t.Fatal(err)
	}
	if !w.hasCall("lox [attach] [crm]") || w.hasCall("lox [attach] [bus]") {
		t.Fatalf("calls: %v", w.calls())
	}
	e := w.errb.String()
	for _, s := range []string{
		"lea: locksql crm waits for its start question (its console runs but does not serve yet): lox attach crm\n",
		"answer it now with lox attach crm?",
		"Ctrl-P Ctrl-Q",
	} {
		if !strings.Contains(e, s) {
			t.Fatalf("stderr %q lacks %q", e, s)
		}
	}
	if strings.Contains(e, "still waits") {
		t.Fatalf("stderr %q: crm serves after the attach", e)
	}
}

// The user declines, or the console still waits after the attach: lea says so and starts anyway.
func TestConsoleWaitsDeclinedOrStill(t *testing.T) {
	for name, tc := range map[string]struct{ in, attach string }{"no": {"n\n", ""}, "still": {"y\n", "lox [attach] [crm]"}} {
		t.Run(name, func(t *testing.T) {
			w := loxUpWorld(t, "")
			w.env["FAKE_DOCKER_OUT_PS___FILTER"] = "lockhub=h1\nlock-crm=c1\nlock-bus=b1"
			w.env["FAKE_DOCKER_OUT_EXEC_C1"] = unixNone
			w.env["FAKE_DOCKER_OUT_EXEC_B1"] = busListen
			l := w.launcher()
			l.Stdin = strings.NewReader(tc.in)
			defer stubAtTerminal(true)()
			if err := l.ensureHub(w.proj); err != nil {
				t.Fatal(err)
			}
			if tc.attach == "" && w.hasCall("lox [attach]") || tc.attach != "" && !w.hasCall(tc.attach) {
				t.Fatalf("calls: %v", w.calls())
			}
			if want := "lea: locksql crm still waits for its start question: lea starts without it; lox attach crm to answer it\n"; !strings.HasSuffix(w.errb.String(), want) {
				t.Fatalf("stderr = %q, want suffix %q", w.errb.String(), want)
			}
		})
	}
}

// Not at a terminal (lea shell -- CMD from a script): the waiting console is named, nothing asked.
func TestConsoleWaitsNoTerminal(t *testing.T) {
	w := loxUpWorld(t, "")
	w.env["FAKE_DOCKER_OUT_PS___FILTER"] = "lockhub=h1\nlock-crm=c1\nlock-bus=b1"
	w.env["FAKE_DOCKER_OUT_EXEC_C1"] = unixNone
	w.env["FAKE_DOCKER_OUT_EXEC_B1"] = busListen
	if err := w.launcher().ensureHub(w.proj); err != nil {
		t.Fatal(err)
	}
	if w.hasCall("lox") {
		t.Fatalf("calls: %v", w.calls())
	}
	if want := "lea: locksql crm waits for its start question (its console runs but does not serve yet): lox attach crm\n"; w.errb.String() != want {
		t.Fatalf("stderr = %q, want %q", w.errb.String(), want)
	}
}

// A console still starting is not waiting: lea checks again until consoleGrace ends.
func TestConsoleStartingIsNotWaiting(t *testing.T) {
	w := loxUpWorld(t, "")
	w.env["FAKE_DOCKER_OUT_PS___FILTER"] = "lockhub=h1\nlock-crm=c1\nlock-bus=b1"
	w.env["FAKE_DOCKER_OUTSEQ_EXEC_C1"] = "|" + unixListen
	w.env["FAKE_DOCKER_OUT_EXEC_B1"] = busListen
	defer stubConsoleGrace(time.Second, time.Millisecond)()
	if err := w.launcher().ensureHub(w.proj); err != nil {
		t.Fatal(err)
	}
	if w.errb.String() != "" {
		t.Fatalf("stderr = %q", w.errb.String())
	}
}

func TestUnixListening(t *testing.T) {
	head := "Num       RefCount Protocol Flags    Type St Inode Path\n"
	for in, want := range map[string]bool{
		head + unixListen: true,
		head + unixNone:   false,
		head + busListen:  false, // another tool's directory
		head + "0000000000000000: 00000002 00000000 00010000 0001 01 4242 /run/locksql-other/x.sock": false,
		head + "0000000000000000: 00000002 00000000 00010000 0001 01 4242":                           false,
		"": false,
	} {
		if got := unixListening(in, "locksql"); got != want {
			t.Errorf("unixListening(%q) = %v, want %v", in, got, want)
		}
	}
}

func stubAtTerminal(v bool) func() {
	old := atTerminal
	atTerminal = func(*Launcher) bool { return v }
	return func() { atTerminal = old }
}

func stubConsoleGrace(grace, step time.Duration) func() {
	og, ostep := consoleGrace, consoleStep
	consoleGrace, consoleStep = grace, step
	return func() { consoleGrace, consoleStep = og, ostep }
}

// A waiting console is said at once in tests: no grace to start.
func init() { consoleGrace = 0 }

// Several consoles wait (#282): lea goes through them one after the other and names which one it
// is on.
func TestConsolesWaitNOfM(t *testing.T) {
	w := loxUpWorld(t, "")
	w.env["FAKE_DOCKER_OUT_PS___FILTER"] = "lockhub=h1\nlock-crm=c1\nlock-bus=b1"
	w.env["FAKE_DOCKER_OUT_EXEC_C1"] = unixNone
	w.env["FAKE_DOCKER_OUT_EXEC_B1"] = unixNone
	if err := w.launcher().ensureHub(w.proj); err != nil {
		t.Fatal(err)
	}
	e := w.errb.String()
	for _, s := range []string{
		"lea: lockmq bus (1 of 2) waits for its start question (its console runs but does not serve yet): lox attach bus\n",
		"lea: locksql crm (2 of 2) waits for its start question (its console runs but does not serve yet): lox attach crm\n",
	} {
		if !strings.Contains(e, s) {
			t.Fatalf("stderr %q lacks %q", e, s)
		}
	}
}
