package launcher

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func running(w *world) {
	w.env["FAKE_DOCKER_OUT_NETWORK_INSPECT"] = "true"
	w.env["FAKE_DOCKER_OUT_CONTAINER_INSPECT"] = "172.18.0.5"
	w.env["FAKE_DOCKER_OUT_INSPECT__F"] = "true"
	// The running proxy was started from this lea's rules.
	if r := (&Launcher{Root: w.root}).proxyRules(); r != "" {
		w.env["FAKE_DOCKER_OUT_INSPECT___FORMAT"] = "@" + r
	}
}

func TestDeckOpenAttachesWithLeaConfig(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	running(w)
	l := w.launcher()
	if got := l.Main(nil); got != 0 {
		t.Fatalf("status %d: %s", got, w.errb.String())
	}
	if !w.hasCall("core [deck-open] [--container] [" + w.name() + "] [--workdir] [" + w.proj + "]") {
		t.Fatalf("calls:\n%s", strings.Join(w.calls(), "\n"))
	}
	want := []string{w.tmux, "-L", "lea", "-f", w.hostDir() + "/tmux.conf", "attach-session", "-t", "=" + w.name()}
	if len(w.execs) != 1 || strings.Join(w.execs[0], " ") != strings.Join(want, " ") {
		t.Fatalf("exec %q", w.execs)
	}
	if exists(w.hostDir() + "/locks/" + w.id()) {
		t.Fatal("the projects lock is released before the attach")
	}
	if entries, _ := os.ReadDir(w.hostDir() + "/sessions/" + w.id()); len(entries) != 0 {
		t.Fatal("the deck records no session")
	}
	if w.hasCall("[lea-session]") {
		t.Fatal("no lea-session with the deck")
	}
}

func TestDeckRefusesClaudeArgumentsBeforeDocker(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	l := w.launcher()
	if got := l.Main([]string{"--resume"}); got != 1 || !strings.Contains(w.errb.String(), "lea new -- --resume") {
		t.Fatalf("%d %q", got, w.errb.String())
	}
	if w.hasCall("docker") {
		t.Fatal("docker was called")
	}
}

func TestFailingDeckOpenReleasesTheLock(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	running(w)
	w.env["FAKE_CORE_RC_deck_open"] = "1"
	if got := w.launcher().Main(nil); got != 1 {
		t.Fatalf("status %d", got)
	}
	if !w.hasCall("core [deck-open]") {
		t.Fatalf("deck-open never ran:\n%s", strings.Join(w.calls(), "\n"))
	}
	if len(w.execs) != 0 || exists(w.hostDir()+"/locks/"+w.id()) {
		t.Fatalf("exec %q, lock left: %v", w.execs, exists(w.hostDir()+"/locks/"+w.id()))
	}
}

func TestFailedStartReleasesTheLocks(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	running(w)
	w.env["FAKE_DOCKER_OUT_INSPECT__F_AT_"+fakeKey(w.name())] = "false" // the proxy runs
	w.env["FAKE_DOCKER_RC_RUN__D_AT_LEA_DEV"] = "1"                     // docker run -d … lea:dev fails
	if got := w.launcher().Main(nil); got == 0 {
		t.Fatal("a failed start is an error")
	}
	if !w.hasCall("docker [run] [-d] [--init]") {
		t.Fatalf("the container's docker run never ran:\n%s", strings.Join(w.calls(), "\n"))
	}
	for _, n := range []string{w.id(), "global"} {
		if exists(w.hostDir() + "/locks/" + n) {
			t.Errorf("lock %s left behind", n)
		}
	}
}

func TestFreshOnlyWhenTheLauncherStartedTheContainer(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	running(w)
	w.env["FAKE_DOCKER_OUT_INSPECT__F"] = "false"
	w.launcher().Main([]string{"new", "rev"})
	if !w.hasCall("core [deck-open] [--container] [" + w.name() + "] [--workdir] [" + w.proj + "] [--no-default] [--fresh]") {
		t.Fatalf("calls:\n%s", strings.Join(w.calls(), "\n"))
	}
	if !w.hasCall("core [new] [--container] [" + w.name() + "] [--workdir] [" + w.proj + "] [rev]") {
		t.Fatalf("calls:\n%s", strings.Join(w.calls(), "\n"))
	}
}

func TestShellSessionStopsTheContainerWhenLastAndAgentless(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	running(w)
	if got := w.launcher().Main([]string{"shell", "--", "true"}); got != 0 {
		t.Fatalf("status %d: %s", got, w.errb.String())
	}
	for _, c := range []string{"[" + w.name() + "] [lea-session] [--shell] [true]", "docker [stop] [" + w.name() + "]",
		"docker [network] [rm] [lea-internal-" + w.id() + "]"} {
		if !w.hasCall(c) {
			t.Errorf("missing %s", c)
		}
	}
	if entries, _ := os.ReadDir(w.hostDir() + "/sessions/" + w.id()); len(entries) != 0 {
		t.Fatal("session file left")
	}
}

func TestShellSessionKeepsTheContainerWhileAgentsRun(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	running(w)
	w.env["FAKE_DOCKER_OUT_EXEC__U"] = "claude-1"
	w.launcher().Main([]string{"shell", "--", "true"})
	if w.hasCall("docker [stop]") {
		t.Fatal("stopped a container with live agents")
	}
}

func TestInterruptDuringASessionEndsIt(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	running(w)
	l := w.launcher()
	u, err := l.up(w.proj)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.sessionBegin(u.sid, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	l.atExit(func(x *Launcher) { x.endSession(w.proj); x.clipboardStop(false) })
	l.unlock(u.sid)
	l.Interrupt()
	if !w.hasCall("docker [stop] [" + w.name() + "]") {
		t.Fatal("the last session's end stops the container")
	}
	if entries, _ := os.ReadDir(w.hostDir() + "/sessions/" + w.id()); len(entries) != 0 {
		t.Fatal("session file left")
	}
}

func TestStopAndStopAll(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	running(w)
	w.launcher().Main([]string{"stop"})
	for _, c := range []string{"tmux [-L] [lea] [-f] [" + w.hostDir() + "/tmux.conf] [kill-session] [-t] [=" + w.name() + "]",
		"docker [stop] [" + w.name() + "]", "docker [network] [rm] [lea-internal-" + w.id() + "]"} {
		if !w.hasCall(c) {
			t.Errorf("stop: missing %s", c)
		}
	}
	os.WriteFile(w.log, nil, 0o644)
	w.env["FAKE_DOCKER_OUT_PS___FILTER"] = "c1\t" + w.proj + "\nc2\t"
	w.env["FAKE_DOCKER_OUT_NETWORK_LS"] = "lea-internal-abc\nlea-egress\nother-internal-x"
	w.launcher().Main([]string{"stop", "--all"})
	for _, c := range []string{"docker [stop] [c1]", "docker [stop] [c2]", "docker [network] [rm] [lea-internal-abc]",
		"tmux [-L] [lea] [-f] [" + w.hostDir() + "/tmux.conf] [kill-server]"} {
		if !w.hasCall(c) {
			t.Errorf("stop --all: missing %s", c)
		}
	}
	if w.hasCall("[other-internal-x]") || w.hasCall("docker [network] [rm] [lea-egress]") {
		t.Error("stop --all removes only the prefix's internal networks")
	}
}

func TestEveryLauncherTmuxCallCarriesTheConf(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	running(w)
	w.launcher().Main(nil)
	w.launcher().Main([]string{"stop"})
	w.launcher().Main([]string{"stop", "--all"})
	seen := 0
	for _, c := range w.calls() {
		if strings.HasPrefix(c, "tmux [-L] [lea] [-f] ["+w.hostDir()+"/tmux.conf] ") {
			seen++
		}
		if strings.HasPrefix(c, "tmux ") && !strings.HasPrefix(c, "tmux [-V]") &&
			!strings.HasPrefix(c, "tmux [-L] [lea] [-f] ["+w.hostDir()+"/tmux.conf] ") {
			t.Errorf("tmux call without lea's conf: %s", c)
		}
	}
	// The attach (recorded exec), the kill-session of stop and the kill-server of stop --all.
	if seen < 3 {
		t.Fatalf("only %d tmux calls with lea's conf seen:\n%s", seen, strings.Join(w.calls(), "\n"))
	}
}

func TestOldFormIsRefused(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	if got := w.launcher().Main([]string{w.proj}); got != 2 || !strings.Contains(w.errb.String(), "lea no longer takes a directory") {
		t.Fatalf("%d %q", got, w.errb.String())
	}
}

// lea stop runs the exit scan through lea-sh with errexit off, as the shell's
// `lea_scan_finish "$_tp" || true`: a failing scan neither hides the open-session warning nor
// aborts the stop.
func TestStopGoesOnWhenTheExitScanFails(t *testing.T) {
	root := copyRoot(t)
	w := newWorld(t, root)
	running(w)
	if err := os.WriteFile(root+"/libexec/lea-sh", []byte("#!/bin/sh\necho \"lea-sh $*\" >>\"$FAKE_LOG\"\necho 'lea: WARNING: scan' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	l := w.launcher()
	if err := l.sessionBegin(w.id(), os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if got := l.Main([]string{"stop"}); got != 0 {
		t.Fatalf("status %d: %s", got, w.errb.String())
	}
	if !strings.Contains(w.errb.String(), "warning: 1 lea session(s) still open on "+w.proj) ||
		!strings.Contains(w.errb.String(), "lea: WARNING: scan") {
		t.Fatalf("stderr %q", w.errb.String())
	}
	for _, c := range []string{"lea-sh scan-finish " + w.proj, "docker [stop] [" + w.name() + "]",
		"docker [network] [rm] [lea-internal-" + w.id() + "]"} {
		if !w.hasCall(c) {
			t.Errorf("missing %s", c)
		}
	}
	if exists(w.hostDir() + "/locks/global") {
		t.Fatal("global lock left behind")
	}
}

// A signal while a session ends on the normal path does not cut the end short: as the shell's
// lea__session_end starts with trap ” INT TERM, the end runs to the container's stop.
func TestInterruptDuringTheNormalSessionEndLetsItFinish(t *testing.T) {
	root := copyRoot(t)
	w := newWorld(t, root)
	running(w)
	// The exit scan blocks until the test lets it go.
	script := "#!/bin/sh\necho \"lea-sh $*\" >>\"$FAKE_LOG\"\ntouch '" + w.dir + "/scanning'\nwhile [ ! -e '" + w.dir + "/go' ]; do sleep 0.01; done\n"
	if err := os.WriteFile(root+"/libexec/lea-sh", []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	l := w.launcher()
	if err := l.sessionBegin(w.id(), os.Getpid()); err != nil {
		t.Fatal(err)
	}
	finish := l.atExit(func(x *Launcher) { x.endSession(w.proj); x.clipboardStop(false) })
	ended, interrupted := make(chan struct{}), make(chan struct{})
	go func() { finish(); close(ended) }()
	for i := 0; !exists(w.dir + "/scanning"); i++ {
		if i > 500 {
			t.Fatal("the exit scan never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	go func() { l.Interrupt(); close(interrupted) }()
	for !l.isInterrupted() {
		time.Sleep(time.Millisecond)
	}
	os.WriteFile(w.dir+"/go", nil, 0o644)
	<-ended
	<-interrupted
	for _, c := range []string{"docker [stop] [" + w.name() + "]", "docker [network] [rm] [lea-internal-" + w.id() + "]"} {
		if !w.hasCall(c) {
			t.Errorf("missing %s:\n%s", c, strings.Join(w.calls(), "\n"))
		}
	}
	for _, n := range []string{w.id(), "global"} {
		if exists(w.hostDir() + "/locks/" + n) {
			t.Errorf("lock %s left behind", n)
		}
	}
}

// The bats suites of the retired shell launcher (open.bats, deck.bats) asserted the behaviours
// below on lea_shell, lea_new and lea_stop; they are asserted here on the Go launcher.

// A lea shell session leaves no lock behind, whether the session succeeds, its command fails, the
// container does not start or the proxy has no address on the container's network.
func TestShellSessionReleasesTheLocks(t *testing.T) {
	cases := map[string]struct {
		env  map[string]string
		args []string
		ok   bool
	}{
		"success":            {nil, []string{"shell"}, true},
		"failing command":    {map[string]string{"FAKE_DOCKER_RC_EXEC__I": "7"}, []string{"shell", "--", "false"}, false},
		"container not run":  {map[string]string{"FAKE_DOCKER_OUT_INSPECT__F_AT_$NAME": "false", "FAKE_DOCKER_RC_RUN__D_AT_LEA_DEV": "1"}, []string{"shell"}, false},
		"proxy without ip":   {map[string]string{"FAKE_DOCKER_OUT_CONTAINER_INSPECT": ""}, []string{"shell"}, false},
		"lea new":            {nil, []string{"new", "-p", "claude", "rev", "--", "--resume"}, true},
		"lea new, new fails": {map[string]string{"FAKE_CORE_RC_new": "4"}, []string{"new"}, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t, repoRoot(t))
			running(w)
			for k, v := range c.env {
				w.env[strings.ReplaceAll(k, "$NAME", fakeKey(w.name()))] = v
			}
			got := w.launcher().Main(c.args)
			if (got == 0) != c.ok {
				t.Fatalf("status %d: %s", got, w.errb.String())
			}
			if name == "failing command" && got != 7 {
				t.Fatalf("the session's status is returned: %d", got)
			}
			if name == "container not run" && !w.hasCall("docker [run] [-d] [--init]") {
				t.Fatalf("the container's docker run never ran:\n%s", strings.Join(w.calls(), "\n"))
			}
			for _, n := range []string{w.id(), "global"} {
				if exists(w.hostDir() + "/locks/" + n) {
					t.Errorf("lock %s left behind", n)
				}
			}
			if len(w.execs) != 0 {
				t.Errorf("attached: %q", w.execs)
			}
		})
	}
}

// lea new opens the deck without a default agent, then starts the agent with every argument
// passed on (role, repo, branch, brief, name, claude arguments), and never attaches.
func TestNewPassesItsArgumentsAndNeverAttaches(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	running(w)
	args := []string{"new", "-r", "lead", "--repo", "shop", "--branch", "feat/x", "--brief", "b.md", "rev", "--", "--resume"}
	if got := w.launcher().Main(args); got != 0 {
		t.Fatalf("status %d: %s", got, w.errb.String())
	}
	pre := "core [deck-open] [--container] [" + w.name() + "] [--workdir] [" + w.proj + "] [--no-default]"
	next := "core [new] [--container] [" + w.name() + "] [--workdir] [" + w.proj + "] [-r] [lead] [--repo] [shop] [--branch] [feat/x] [--brief] [b.md] [rev] [--] [--resume]"
	calls := strings.Join(w.calls(), "\n")
	if i, j := strings.Index(calls, pre), strings.Index(calls, next); i < 0 || j < i {
		t.Fatalf("calls:\n%s", calls)
	}
	if w.hasCall("attach-session") || len(w.execs) != 0 {
		t.Fatal("lea new attached")
	}
}

// A shell session started from inside the projects directory keeps its working directory;
// started from elsewhere, it starts at the projects directory.
func TestShellSessionWorkingDirectory(t *testing.T) {
	for cwd, want := range map[string]string{"sub": "/sub", "": "", "..": ""} {
		w := newWorld(t, repoRoot(t))
		running(w)
		l := w.launcher()
		l.Cwd = filepath.Clean(w.proj + "/" + cwd)
		os.MkdirAll(l.Cwd, 0o755)
		if got := l.Main([]string{"shell"}); got != 0 {
			t.Fatalf("%q: status %d: %s", cwd, got, w.errb.String())
		}
		if !w.hasCall("[-w] [" + w.proj + want + "] [" + w.name() + "] [lea-session] [--shell]") {
			t.Errorf("%q: calls:\n%s", cwd, strings.Join(w.calls(), "\n"))
		}
		if w.hasCall("docker [run]") {
			t.Errorf("%q: a running container is reused", cwd)
		}
	}
}

// The container stays while another lea session on the same projects directory is live.
func TestShellSessionKeepsTheContainerWhileAnotherSessionIsOpen(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	running(w)
	other := exec.Command("sleep", "30")
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { other.Process.Kill(); other.Wait() })
	l := w.launcher()
	if err := l.sessionBegin(w.id(), other.Process.Pid); err != nil {
		t.Fatal(err)
	}
	if got := l.Main([]string{"shell"}); got != 0 {
		t.Fatalf("status %d: %s", got, w.errb.String())
	}
	if w.hasCall("docker [stop]") || w.hasCall("[network] [rm]") {
		t.Fatalf("the container was stopped under a live session:\n%s", strings.Join(w.calls(), "\n"))
	}
}

// plantAfterSnapshot writes a start snapshot of the projects directory (through lea-sh, as the
// launcher does), then plants a hook in one of its repositories.
func plantAfterSnapshot(t *testing.T, w *world, l *Launcher) string {
	t.Helper()
	if err := os.MkdirAll(w.proj+"/r/.git/hooks", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := l.sh("scan-begin", w.proj); err != nil {
		t.Fatal(err)
	}
	hook := w.proj + "/r/.git/hooks/pre-push"
	if err := os.WriteFile(hook, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return hook
}

// The start snapshot lives in the host-only directory and the last session's exit scan consumes
// it; nothing of the launcher's bookkeeping lands in the state directory mounted into lea.
func TestShellSessionConsumesItsStartSnapshot(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	running(w)
	w.env["FAKE_DOCKER_OUT_INSPECT__F_AT_"+fakeKey(w.name())] = "false"
	if got := w.launcher().Main([]string{"shell"}); got != 0 {
		t.Fatalf("status %d: %s", got, w.errb.String())
	}
	if !w.hasCall("docker [run] [-d] [--init]") || strings.Contains(w.errb.String(), "no start snapshot") {
		t.Fatalf("calls:\n%s\nstderr %q", strings.Join(w.calls(), "\n"), w.errb.String())
	}
	if exists(w.hostDir() + "/scan/" + w.id() + ".start") {
		t.Fatal("the exit scan left the start snapshot")
	}
	for _, d := range []string{"scan", "sessions", "locks"} {
		if exists(w.data + "/lea/state/" + d) {
			t.Errorf("state/%s exists: the launcher's bookkeeping is host-only", d)
		}
	}
}

// A start snapshot left by a run that missed its exit scan is reported at the next start, and the
// new snapshot replaces it.
func TestAMissedExitScanIsReportedAtTheNextStart(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	running(w)
	w.env["FAKE_DOCKER_OUT_INSPECT__F_AT_"+fakeKey(w.name())] = "false"
	l := w.launcher()
	hook := plantAfterSnapshot(t, w, l)
	other := exec.Command("sleep", "30") // keeps the container, and so the new snapshot
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { other.Process.Kill(); other.Wait() })
	if err := l.sessionBegin(w.id(), other.Process.Pid); err != nil {
		t.Fatal(err)
	}
	if got := w.launcher().Main([]string{"shell"}); got != 0 {
		t.Fatalf("status %d: %s", got, w.errb.String())
	}
	if !strings.Contains(w.errb.String(), "ended without its exit scan") || !strings.Contains(w.errb.String(), "+hook "+hook+" ") {
		t.Fatalf("stderr %q", w.errb.String())
	}
	if !w.hasCall("docker [run] [-d] [--init]") {
		t.Fatal("the container was not started after the report")
	}
	if b, _ := os.ReadFile(w.hostDir() + "/scan/" + w.id() + ".start"); !strings.Contains(string(b), "hook "+hook+" ") {
		t.Fatalf("the new snapshot does not hold the hook:\n%s", b)
	}
}

// lea stop and lea stop --all run the exit scan of a running container (an unreadable hook the
// agent planted does not silence the warning) and consume its snapshot; stop --all skips a
// container without a projects label.
func TestStopRunsTheExitScan(t *testing.T) {
	for _, args := range [][]string{{"stop"}, {"stop", "--all"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			w := newWorld(t, repoRoot(t))
			running(w)
			w.env["FAKE_DOCKER_OUT_PS___FILTER"] = "c1\t" + w.proj + "\nproxy\t"
			l := w.launcher()
			hook := plantAfterSnapshot(t, w, l)
			if os.Getuid() != 0 {
				unreadable := w.proj + "/r/.git/hooks/b-hook"
				os.WriteFile(unreadable, []byte("b\n"), 0o000)
			}
			if got := l.Main(args); got != 0 {
				t.Fatalf("status %d: %s", got, w.errb.String())
			}
			if !strings.Contains(w.errb.String(), "WARNING: files that the host executes changed") ||
				!strings.Contains(w.errb.String(), "+hook "+hook+" ") || strings.Count(w.errb.String(), "WARNING") != 1 {
				t.Fatalf("stderr %q", w.errb.String())
			}
			if exists(w.hostDir() + "/scan/" + w.id() + ".start") {
				t.Fatal("the snapshot is left")
			}
			if len(args) == 2 && !w.hasCall("docker [stop] [c1]") {
				t.Fatalf("calls:\n%s", strings.Join(w.calls(), "\n"))
			}
		})
	}
}

// stop --all goes on past a failing docker stop, and stops the daemon; a plain stop does not.
func TestStopAllStopsTheDaemonAndGoesOn(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	running(w)
	pf := w.hostDir() + "/daemon.pid"
	os.WriteFile(pf, []byte("999999999\n"), 0o600) // no such process: only the file goes
	if got := w.launcher().Main([]string{"stop"}); got != 0 || !exists(pf) {
		t.Fatalf("stop: status %d, daemon pid file kept: %v", got, exists(pf))
	}
	w.env["FAKE_DOCKER_OUT_PS___FILTER"] = "c1\t\nc2\t"
	w.env["FAKE_DOCKER_RC_STOP_C1"] = "1"
	if got := w.launcher().Main([]string{"stop", "--all"}); got != 0 {
		t.Fatalf("stop --all: status %d: %s", got, w.errb.String())
	}
	if !w.hasCall("docker [stop] [c2]") || exists(pf) {
		t.Fatalf("pid file left: %v; calls:\n%s", exists(pf), strings.Join(w.calls(), "\n"))
	}
}

// Without a usable tmux the deck is refused with the command that installs one (or builds lea's
// own), before any docker call and without installing anything; an old tmux is named. lea stop
// works without tmux.
func TestDeckNeedsAUsableTmux(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	running(w)
	osr := w.dir + "/os-release"
	w.env["LEA_OS_RELEASE"] = osr
	os.WriteFile(osr, []byte("ID=fedora\n"), 0o644)
	w.env["FAKE_TMUX_VERSION"] = "tmux 3.1c"
	if got := w.launcher().Main(nil); got != 1 ||
		!strings.Contains(w.errb.String(), "found 'tmux 3.1c'. Upgrade it with: sudo dnf install tmux, or build a static one with: make -C") {
		t.Fatalf("old tmux: %d %q", got, w.errb.String())
	}
	if w.hasCall("docker") {
		t.Fatal("docker was called")
	}
	w.errb.Reset()
	w.env["LEA_TMUX_BIN"] = w.dir + "/none"
	if got := w.launcher().Main(nil); got != 1 || !strings.Contains(w.errb.String(), "LEA_TMUX_BIN="+w.dir+"/none is not an executable file") {
		t.Fatalf("no tmux: %d %q", got, w.errb.String())
	}
	if got := w.launcher().Main([]string{"stop"}); got != 0 || !w.hasCall("docker [stop] ["+w.name()+"]") {
		t.Fatalf("stop without tmux: %d %q", got, w.errb.String())
	}
	for _, c := range w.calls() {
		if strings.Contains(c, "sudo") {
			t.Fatalf("lea ran %s", c)
		}
	}
	// The message without LEA_TMUX_BIN, with no tmux anywhere: the install command and the build.
	delete(w.env, "LEA_TMUX_BIN")
	l := w.launcher()
	os.WriteFile(osr, []byte("ID=ubuntu\n"), 0o644)
	if msg := l.tmuxMissing(); msg != `tmux is not installed. Install it with: sudo apt install tmux, or build a static one with: make -C "`+l.Root+`" tmux-build` {
		t.Fatalf("%q", msg)
	}
}

// Networks and the proxy are set up under the global lock, which is released afterwards.
func TestNetworksAndProxyAreSetUpUnderTheGlobalLock(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	running(w)
	w.env["FAKE_DOCKER_RC_NETWORK_INSPECT"] = "1"
	w.env["FAKE_DOCKER_OUT_INSPECT__F_AT_LEA_PROXY"] = "false"
	wrap := w.dir + "/wrap"
	os.MkdirAll(wrap, 0o755)
	os.WriteFile(wrap+"/docker", []byte("#!/bin/sh\ncase \"$1 $2\" in 'network create'|'run -d'|'network connect')\n"+
		"  if [ -d '"+w.hostDir()+"/locks/global' ]; then echo \"locked $1 $2\" >>\"$FAKE_LOG\"; else echo \"UNLOCKED $1 $2\" >>\"$FAKE_LOG\"; fi ;;\nesac\n"+
		"exec '"+repoRoot(t)+"/tests/fakes/docker' \"$@\"\n"), 0o755)
	w.env["PATH"] = wrap + ":" + w.env["PATH"]
	w.launcher().Main([]string{"shell"})
	calls := strings.Join(w.calls(), "\n")
	for _, want := range []string{"locked network create", "locked run -d"} {
		if !strings.Contains(calls, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(calls, "UNLOCKED") {
		t.Fatalf("calls:\n%s", calls)
	}
	if exists(w.hostDir() + "/locks/global") {
		t.Fatal("global lock left")
	}
}
