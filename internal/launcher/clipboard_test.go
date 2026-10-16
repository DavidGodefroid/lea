package launcher

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// clipboardWorld enables the bridge with a fake wl-copy and the FIFO in place.
func clipboardWorld(t *testing.T, root string) *world {
	t.Helper()
	w := newWorld(t, root)
	w.env["LEA_CLIPBOARD"] = "1"
	w.env["WAYLAND_DISPLAY"] = "wayland-test"
	if err := os.WriteFile(w.dir+"/bin/wl-copy", []byte("#!/bin/sh\ncat >/dev/null\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := w.launcher().clipboardArgs(); err != nil {
		t.Fatal(err)
	}
	return w
}

// startGoLoop starts the loop with the Go launcher and returns its pid.
func startGoLoop(t *testing.T, w *world) int {
	t.Helper()
	l := w.launcher()
	if err := l.clipboardStart(); err != nil {
		t.Fatal(err)
	}
	// The loop is known by its command line, which shows once the child has exec'ed sh.
	pid, ok := l.clipboardLoopPid()
	for i := 0; !ok && i < 100; i++ {
		time.Sleep(10 * time.Millisecond)
		pid, ok = l.clipboardLoopPid()
	}
	if !ok {
		t.Fatal("no loop started")
	}
	t.Cleanup(func() { syscall.Kill(-pid, syscall.SIGKILL) })
	return pid
}

func waitGone(pid int) bool {
	for i := 0; i < 100 && liveProcess(pid); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	return !liveProcess(pid)
}

func TestClipboardStopKeepsTheLoopWhileADeckRuns(t *testing.T) {
	w := clipboardWorld(t, repoRoot(t))
	pid := startGoLoop(t, w)
	if err := w.launcher().clipboardStop(false); err != nil { // fake tmux: list-sessions succeeds
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if !liveProcess(pid) || !exists(w.hostDir()+"/clipboard.pid") {
		t.Fatal("the loop stopped while a deck session runs")
	}
	w.env["FAKE_TMUX_RC"] = "1" // no deck session, no lea session
	if err := w.launcher().clipboardStop(false); err != nil {
		t.Fatal(err)
	}
	if !waitGone(pid) || exists(w.hostDir()+"/clipboard.pid") {
		t.Fatal("the loop survived the last session")
	}
}

func TestClipboardLoopPidChecksItsCommandLine(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	l := w.launcher()
	// A live pid (this test) whose command line is not the loop's.
	os.WriteFile(w.hostDir()+"/clipboard.pid", []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600)
	if _, ok := l.clipboardLoopPid(); ok {
		t.Fatal("a pid that is not the loop is not the loop")
	}
}

// A symlink planted on the clipboard paths stops the launcher with lea_safe_path's message, as
// the shell does (lea_clipboard_start and lea_clipboard_stop die there).
func TestClipboardRefusesSymlinkedPaths(t *testing.T) {
	w := clipboardWorld(t, repoRoot(t))
	w.env["FAKE_TMUX_RC"] = "1" // no deck session: the sessions are counted
	os.Symlink(w.dir, w.hostDir()+"/sessions")
	err := w.launcher().clipboardStop(false)
	if err == nil || !strings.Contains(err.Error(), "is a symbolic link") {
		t.Fatalf("stop: %v", err)
	}
	os.Remove(w.hostDir() + "/sessions")
	os.Symlink(w.dir+"/elsewhere", w.hostDir()+"/clipboard.pid")
	err = w.launcher().clipboardStart()
	if err == nil || !strings.Contains(err.Error(), "is a symbolic link") {
		t.Fatalf("start: %v", err)
	}
	if exists(w.dir + "/elsewhere") {
		t.Fatal("written through the symlink")
	}
}

// The host loop copies what lea writes into the FIFO to the host clipboard, skips an empty write
// (it would clear the clipboard), is reused by a second launch, outlives a session while another
// is open and stops with the last one (clipboard tests of the retired container.bats).
func TestClipboardLoopCopiesWhatLeaWrites(t *testing.T) {
	w := clipboardWorld(t, repoRoot(t))
	w.env["FAKE_TMUX_RC"] = "1" // no deck session
	clip := w.dir + "/clip"
	// The fake host wl-copy ends each copy with '|', so that an empty copy would show.
	os.WriteFile(w.dir+"/bin/wl-copy", []byte("#!/bin/sh\n{ cat; printf '|'; } >>'"+clip+"'\n"), 0o755)
	pid := startGoLoop(t, w)
	fifo := w.data + "/lea/state/clipboard/copy"
	write := func(s string) {
		f, err := os.OpenFile(fifo, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(s)
		f.Close()
	}
	wait := func(want string) {
		t.Helper()
		for i := 0; i < 100 && readTrim(clip) != want; i++ {
			time.Sleep(20 * time.Millisecond)
		}
		if got := readTrim(clip); got != want {
			t.Fatalf("clipboard %q, want %q", got, want)
		}
	}
	write("")
	time.Sleep(200 * time.Millisecond)
	write("hello")
	wait("hello|")
	l := w.launcher()
	if err := l.clipboardStart(); err != nil {
		t.Fatal(err)
	}
	if again, _ := l.clipboardLoopPid(); again != pid || readTrim(w.hostDir()+"/clipboard.pid") != strconv.Itoa(pid) {
		t.Fatalf("a second launch started another loop: %d then %d", pid, again)
	}
	other := os.Getpid() // another open session, live
	if err := l.sessionBegin("other", other); err != nil {
		t.Fatal(err)
	}
	if err := l.clipboardStop(false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if !liveProcess(pid) {
		t.Fatal("the loop stopped while another session is open")
	}
	l.sessionEnd("other", other)
	if err := l.clipboardStop(false); err != nil {
		t.Fatal(err)
	}
	if !waitGone(pid) || exists(w.hostDir()+"/clipboard.pid") {
		t.Fatal("the loop survived the last session")
	}
}

// A stale pid file never makes stop --all kill a foreign process.
func TestClipboardStopSparesAForeignProcess(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	foreign := exec.Command("sleep", "30")
	if err := foreign.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { foreign.Process.Kill(); foreign.Wait() })
	os.WriteFile(w.hostDir()+"/clipboard.pid", []byte(strconv.Itoa(foreign.Process.Pid)+"\n"), 0o600)
	if err := w.launcher().clipboardStop(true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if !liveProcess(foreign.Process.Pid) {
		t.Fatal("a foreign process was killed")
	}
}
