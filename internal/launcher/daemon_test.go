package launcher

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestEnsureDaemonLeavesAMatchingDaemon(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	if err := w.launcher().ensureDaemon(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(w.calls(), "\n"); got != "core [agents]\ncore [version]" {
		t.Fatalf("calls:\n%s", got)
	}
}

// A daemon that recorded other approval settings is restarted: it stops (here: stops answering
// after one call), a new one is started and answers.
func TestEnsureDaemonRestartsOnDifferentRecords(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	os.WriteFile(w.hostDir()+"/daemon.approvals", []byte("pane 60"), 0o600)
	os.WriteFile(w.core, []byte(`#!/bin/sh
l=core; for a in "$@"; do l="$l [$a]"; done; printf '%s\n' "$l" >>"$FAKE_LOG" # one write: the daemon logs concurrently
n=$(cat "$FAKE_LOG.n" 2>/dev/null || echo 0); n=$((n + 1)); echo "$n" >"$FAKE_LOG.n"
case $1 in
  agents) [ "$n" -eq 1 ] || [ -f "$FAKE_LOG.up" ] ;;
  daemon) touch "$FAKE_LOG.up" ;;
esac
`), 0o755)
	if err := w.launcher().ensureDaemon(); err != nil {
		t.Fatal(err)
	}
	daemonDone(t, w)
	got := strings.Join(w.calls(), "\n")
	// the started daemon logs asynchronously: wait for its line
	for i := 0; i < 50 && !strings.Contains(got, "[daemon]"); i++ {
		time.Sleep(20 * time.Millisecond)
		got = strings.Join(w.calls(), "\n")
	}
	// answers, its records differ, stops answering; then one daemon is started and polled until
	// it answers (the polls may precede its own log line, and more than one may be needed).
	rest, ok := strings.CutPrefix(got, "core [agents]\ncore [version]\ncore [agents]\n")
	daemons, polls := 0, 0
	for _, c := range strings.Split(rest, "\n") {
		switch c {
		case "core [daemon]":
			daemons++
		case "core [agents]":
			polls++
		default:
			ok = false
		}
	}
	if !ok || daemons != 1 || polls == 0 {
		t.Fatalf("calls:\n%s", got)
	}
	if b, err := os.ReadFile(w.hostDir() + "/daemon.pid"); err != nil || strings.TrimSpace(string(b)) == "" {
		t.Fatalf("pid file: %q %v", b, err)
	}
}

// daemonDone waits for the fake daemon started in the background to make its last write ($FAKE_LOG.up):
// the polls share a counter with it, so the launcher may see an answer before it ran, and a write
// after the test would race the removal of its directory.
func daemonDone(t *testing.T, w *world) {
	t.Helper()
	for i := 0; i < 100 && !exists(w.log+".up"); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
}

// coreDaemon starts a process whose command line is "<core> daemon", as lea_daemon_stop checks:
// a copy of sh named core reading the script file "daemon" from its working directory.
func coreDaemon(t *testing.T, dir string) (core string, cmd *exec.Cmd) {
	t.Helper()
	core = dir + "/core"
	sh, _ := exec.LookPath("sh")
	b, _ := os.ReadFile(sh)
	os.WriteFile(core, b, 0o755)
	os.WriteFile(dir+"/daemon", []byte("sleep 30\n"), 0o644)
	cmd = exec.Command(core, "daemon")
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	return core, cmd
}

func TestStopDaemonKillsOnlyTheCoreDaemon(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	core, d := coreDaemon(t, w.dir)
	other := exec.Command("sleep", "30")
	other.Start()
	t.Cleanup(func() { other.Process.Kill(); other.Wait() })
	l := w.launcher()
	l.Core = core
	pf := w.hostDir() + "/daemon.pid"
	os.WriteFile(pf, []byte(strconv.Itoa(other.Process.Pid)+"\n"), 0o600)
	l.stopDaemon()
	if other.Process.Signal(syscall.Signal(0)) != nil || exists(pf) {
		t.Fatal("an unrelated process behind a stale pid file is never killed; the file goes")
	}
	os.WriteFile(pf, []byte(strconv.Itoa(d.Process.Pid)+"\n"), 0o600)
	l.stopDaemon()
	done := make(chan error, 1)
	go func() { done <- d.Wait() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the core daemon was not stopped")
	}
}

// The shell's lea_daemon_stop must recognise a daemon the Go launcher started: argv[0] is the
// full core path and argv[1] "daemon"; and the daemon runs in a session of its own.
func TestStartedDaemonHasTheCommandLineTheShellChecks(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	os.Remove(w.hostDir() + "/daemon.approvals") // records differ: a daemon is started
	os.WriteFile(w.core, []byte(`#!/bin/sh
case $1 in
  agents) [ -f "$FAKE_LOG.up" ] ;;
  daemon) printf '%s %s' "$0" "$1" >"$FAKE_LOG.argv"; touch "$FAKE_LOG.up"; exec sleep 30 ;;
esac
`), 0o755)
	if err := w.launcher().ensureDaemon(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(w.hostDir() + "/daemon.pid")
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	t.Cleanup(func() { syscall.Kill(pid, syscall.SIGKILL) })
	if argv := readTrim(w.log + ".argv"); argv != w.core+" daemon" {
		t.Fatalf("argv %q, want %q", argv, w.core+" daemon")
	}
	if pgid, _ := syscall.Getpgid(pid); pgid != pid {
		t.Fatalf("the daemon leads its own session (setsid): pgid %d, pid %d", pgid, pid)
	}
}

// HEAD's lea_daemon_ensure also restarts a daemon of another build: the daemon records
// `<core> version` in daemon.version at start; the launcher never writes that file.
func TestEnsureDaemonRestartsAnotherBuild(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	os.WriteFile(w.hostDir()+"/daemon.version", []byte("v1\n"), 0o600)
	os.WriteFile(w.core, []byte(`#!/bin/sh
l=core; for a in "$@"; do l="$l [$a]"; done; printf '%s\n' "$l" >>"$FAKE_LOG" # one write: the daemon logs concurrently
n=$(cat "$FAKE_LOG.n" 2>/dev/null || echo 0); n=$((n + 1)); echo "$n" >"$FAKE_LOG.n"
case $1 in
  version) echo v2 ;;
  agents) [ "$n" -eq 1 ] || [ -f "$FAKE_LOG.up" ] ;;
  daemon) touch "$FAKE_LOG.up" ;;
esac
`), 0o755)
	if err := w.launcher().ensureDaemon(); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(w.calls(), "\n")
	for i := 0; i < 50 && !strings.Contains(got, "[daemon]"); i++ {
		time.Sleep(20 * time.Millisecond)
		got = strings.Join(w.calls(), "\n")
	}
	if !strings.Contains(got, "core [version]") || !strings.Contains(got, "core [daemon]") {
		t.Fatalf("a daemon of another build is restarted; calls:\n%s", got)
	}
	if readTrim(w.hostDir()+"/daemon.version") != "v1" {
		t.Fatal("daemon.version is the daemon's to write")
	}

	// the same build: left running
	w2 := newWorld(t, repoRoot(t))
	os.WriteFile(w2.hostDir()+"/daemon.version", []byte("v2\n"), 0o600)
	os.WriteFile(w2.core, []byte("#!/bin/sh\n{ printf 'core'; for a in \"$@\"; do printf ' [%s]' \"$a\"; done; printf '\\n'; } >>\"$FAKE_LOG\"\n[ \"$1\" = version ] && echo v2\nexit 0\n"), 0o755)
	if err := w2.launcher().ensureDaemon(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(w2.calls(), "\n"); got != "core [agents]\ncore [version]" {
		t.Fatalf("calls:\n%s", got)
	}
}

// The daemon drives the tmux the launcher resolved: a daemon that recorded another tmux, or
// other approval settings, is restarted, and the new one gets LEA_TMUX_BIN (daemon tests of the
// retired deck.bats and core.bats).
func TestEnsureDaemonPinsTheResolvedTmuxAndApprovals(t *testing.T) {
	for name, record := range map[string][2]string{"tmux": {"daemon.tmux", "/old/tmux"}, "approvals": {"daemon.approvals", "pane 5"}, "resume": {"daemon.resume", "off 60"}, "lockhttp": {"daemon.lockhttp", "on"}, "stacks": {"daemon.stacks", "30"}, "admit": {"daemon.admit", "50 85 10 40 2"}} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t, repoRoot(t))
			delete(w.env, "LEA_TMUX_BIN") // resolved from PATH: the world's tmux
			os.WriteFile(w.hostDir()+"/"+record[0], []byte(record[1]), 0o600)
			os.WriteFile(w.core, []byte(`#!/bin/sh
printf 'core %s tmux=%s\n' "$1" "${LEA_TMUX_BIN:-}" >>"$FAKE_LOG"
n=$(cat "$FAKE_LOG.n" 2>/dev/null || echo 0); n=$((n + 1)); echo "$n" >"$FAKE_LOG.n"
case $1 in
  agents) [ "$n" -eq 1 ] || [ -f "$FAKE_LOG.up" ] ;;
  daemon) touch "$FAKE_LOG.up" ;;
esac
`), 0o755)
			if err := w.launcher().ensureDaemon(); err != nil {
				t.Fatal(err)
			}
			daemonDone(t, w)
			want := "core daemon tmux=" + w.tmux
			got := strings.Join(w.calls(), "\n")
			for i := 0; i < 50 && !strings.Contains(got, want); i++ {
				time.Sleep(20 * time.Millisecond)
				got = strings.Join(w.calls(), "\n")
			}
			if !strings.Contains(got, want) {
				t.Fatalf("calls:\n%s", got)
			}
		})
	}
	// The same records: the daemon is left, and the launcher still pins the resolved tmux.
	w := newWorld(t, repoRoot(t))
	delete(w.env, "LEA_TMUX_BIN")
	l := w.launcher()
	if err := l.ensureDaemon(); err != nil || exists(w.log+".daemon") || l.Cfg.Get("LEA_TMUX_BIN") != w.tmux {
		t.Fatalf("%v, LEA_TMUX_BIN=%q\n%s", err, l.Cfg.Get("LEA_TMUX_BIN"), strings.Join(w.calls(), "\n"))
	}
}

func TestStopDaemonWithoutAPidFile(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.launcher().stopDaemon()
	if exists(w.hostDir() + "/daemon.pid") {
		t.Fatal("a pid file appeared")
	}
}

// daemonAt starts a process whose command line is "<core> daemon" for a core at any path: a copy
// of sh named as the core, reading the script file "daemon" from a working directory of its own.
func daemonAt(t *testing.T, core, script string) *exec.Cmd {
	t.Helper()
	os.MkdirAll(filepath.Dir(core), 0o755)
	sh, _ := exec.LookPath("sh")
	b, _ := os.ReadFile(sh)
	os.WriteFile(core, b, 0o755)
	wd := t.TempDir()
	os.WriteFile(wd+"/daemon", []byte(script), 0o644)
	cmd := exec.Command(core, "daemon")
	cmd.Dir = wd
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	return cmd
}

func waitExit(t *testing.T, d *exec.Cmd, what string) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- d.Wait() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal(what)
	}
}

// #34: after an update the running daemon is the previous install's
// (<install dir>/<old version>/build/lea daemon), never the current core: it is still stopped.
func TestStopDaemonStopsTheDaemonOfAPreviousInstall(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	inst := w.dir + "/lea-install"
	d := daemonAt(t, inst+"/v0.1.2/build/lea", "sleep 30\n")
	l := w.launcher()
	l.Root, l.Core = inst+"/v0.1.3", inst+"/v0.1.3/build/lea"
	pf := w.hostDir() + "/daemon.pid"
	os.WriteFile(pf, []byte(strconv.Itoa(d.Process.Pid)+"\n"), 0o600)
	l.stopDaemon()
	waitExit(t, d, "the daemon of the previous install was not stopped")
}

// A "<path>/lea daemon" outside the lea install directory is not lea's: never signalled.
func TestStopDaemonLeavesALeaDaemonOutsideTheInstallDir(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	d := daemonAt(t, w.dir+"/elsewhere/v1/build/lea", "sleep 30\n")
	l := w.launcher()
	l.Root, l.Core = w.dir+"/lea-install/v2", w.dir+"/lea-install/v2/build/lea"
	pf := w.hostDir() + "/daemon.pid"
	os.WriteFile(pf, []byte(strconv.Itoa(d.Process.Pid)+"\n"), 0o600)
	l.stopDaemon()
	time.Sleep(100 * time.Millisecond)
	if d.Process.Signal(syscall.Signal(0)) != nil {
		t.Fatal("a process outside the install dir was signalled")
	}
	if exists(pf) {
		t.Fatal("a pid file naming no lea daemon is stale: it goes")
	}
}

// The pid file stays until the daemon is gone: a daemon that outlives the stop can be stopped
// on the next attempt (#34: the pid was lost and every retry failed).
func TestStopDaemonKeepsThePidFileWhileTheDaemonRuns(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	core := w.dir + "/lea-install/v1/build/lea"
	d := daemonAt(t, core, "trap '' TERM\nsleep 30\n")
	l := w.launcher()
	l.Root, l.Core = w.dir+"/lea-install/v1", core
	pf := w.hostDir() + "/daemon.pid"
	os.WriteFile(pf, []byte(strconv.Itoa(d.Process.Pid)+"\n"), 0o600)
	time.Sleep(100 * time.Millisecond) // the trap is set
	l.stopDaemon()
	if d.Process.Signal(syscall.Signal(0)) != nil {
		t.Fatal("the daemon ignoring TERM is still running")
	}
	if !exists(pf) {
		t.Fatal("the pid file of a running daemon was removed")
	}
	// it is gone: the next stop drops the file
	d.Process.Kill()
	waitExit(t, d, "kill")
	l.stopDaemon()
	if exists(pf) {
		t.Fatal("the pid file of a gone daemon stays")
	}
}

// LEA_STACK_IDLE: minutes an agent stays idle before lea stops its stack (#132): 10 by default
// or when invalid, 0 (off) to 1440.
func TestStackIdleSetting(t *testing.T) {
	for v, want := range map[string]time.Duration{"": 10 * time.Minute, "0": 0, "3": 3 * time.Minute, "1440": 24 * time.Hour,
		"1441": 10 * time.Minute, "-1": 10 * time.Minute, "ten": 10 * time.Minute} {
		get := func(k string) string {
			if k == "LEA_STACK_IDLE" {
				return v
			}
			return ""
		}
		if got := StackIdleSetting(get); got != want {
			t.Errorf("LEA_STACK_IDLE=%q: %v, want %v", v, got, want)
		}
		if got, wantStamp := DaemonStacksStamp(get), strconv.Itoa(int(want/time.Minute))+" 0 4"; got != wantStamp {
			t.Errorf("LEA_STACK_IDLE=%q: stamp %q, want %q", v, got, wantStamp)
		}
	}
}

// LEA_HEAVY_SLOTS (0: from the CPUs) and LEA_HEAVY_WORKERS (4) of the heavy jobs (#132).
func TestHeavySettings(t *testing.T) {
	for _, c := range []struct {
		slots, workers string
		ws, ww         int
	}{{"", "", 0, 4}, {"3", "2", 3, 2}, {"0", "8", 0, 8}, {"-1", "0", 0, 4}, {"257", "300", 0, 4}, {"x", "y", 0, 4}} {
		get := func(k string) string {
			switch k {
			case "LEA_HEAVY_SLOTS":
				return c.slots
			case "LEA_HEAVY_WORKERS":
				return c.workers
			}
			return ""
		}
		if s, w := HeavySettings(get); s != c.ws || w != c.ww {
			t.Errorf("%+v: %d %d", c, s, w)
		}
	}
}

// LEA_ADMIT_* (#132, P2): each threshold 0 (off) to its maximum, the default otherwise.
func TestAdmitSettings(t *testing.T) {
	for _, c := range []struct {
		env  map[string]string
		want string
	}{
		{nil, "40 85 10 40 2"},
		{map[string]string{"LEA_ADMIT_CPU_PSI": "0", "LEA_ADMIT_MEM_PCT": "90", "LEA_ADMIT_MEM_PSI": "2.5", "LEA_ADMIT_IO_PSI": "0", "LEA_ADMIT_LOAD": "1.5"}, "0 90 2.5 0 1.5"},
		{map[string]string{"LEA_ADMIT_CPU_PSI": "101", "LEA_ADMIT_MEM_PCT": "-1", "LEA_ADMIT_MEM_PSI": "x", "LEA_ADMIT_LOAD": "65"}, "40 85 10 40 2"},
	} {
		get := func(k string) string { return c.env[k] }
		if got := DaemonAdmitStamp(get); got != c.want {
			t.Errorf("%v: %q, want %q", c.env, got, c.want)
		}
	}
}
