//go:build linux

package launcher

import (
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The test binary doubles as a fake daemon: copied to any path and run as "<path> daemon" with
// LEA_TEST_FAKE_DAEMON set to a socket path, it listens there (so its pid is the socket's peer)
// and, with LEA_TEST_FAKE_DAEMON_TERM=ignore, ignores SIGTERM.
func init() {
	sock := os.Getenv("LEA_TEST_FAKE_DAEMON")
	if sock == "" || len(os.Args) < 2 || os.Args[1] != "daemon" {
		return
	}
	if os.Getenv("LEA_TEST_FAKE_DAEMON_TERM") == "ignore" {
		signal.Ignore(syscall.SIGTERM)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		os.Exit(3)
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			os.Exit(4)
		}
		c.Close()
	}
}

// fakeDaemon runs a copy of the test binary at core as "<core> daemon", listening on sock. Its
// exit is reaped at once (done closes), so a stopped daemon leaves no zombie behind.
func fakeDaemon(t *testing.T, core, sock string, ignoreTerm bool) (pid int, done chan struct{}) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Dir(core), 0o755)
	in, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	out, err := os.OpenFile(core, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(out, in)
	in.Close()
	out.Close()
	cmd := exec.Command(core, "daemon")
	cmd.Env = append(os.Environ(), "LEA_TEST_FAKE_DAEMON="+sock)
	if ignoreTerm {
		cmd.Env = append(cmd.Env, "LEA_TEST_FAKE_DAEMON_TERM=ignore")
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done = make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	t.Cleanup(func() { cmd.Process.Kill(); <-done })
	for i := 0; ; i++ {
		if c, err := net.Dial("unix", sock); err == nil {
			c.Close()
			break
		}
		if i >= 200 {
			t.Fatal("the fake daemon does not listen")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cmd.Process.Pid, done
}

func waitDone(t *testing.T, done chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal(what)
	}
}

// brewWorld is a world whose lea is a Homebrew install of 0.7.1 (<Cellar>/lea/0.7.1/libexec)
// and whose daemon socket path is short enough to bind.
func brewWorld(t *testing.T) (w *world, cellar, sock string) {
	w = newWorld(t, repoRoot(t))
	cellar = w.dir + "/Cellar/lea"
	sd, err := os.MkdirTemp("", "lea283")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sd) })
	sock = sd + "/d.sock"
	w.env["LEA_DAEMON_SOCK"] = sock
	return w, cellar, sock
}

// #283: after `brew upgrade`, the daemon is the previous version's
// (<Cellar>/lea/0.7.0/libexec/build/lea daemon, its directory removed by Homebrew), a layout
// the install-dir rule of #34 does not know. It listens on lea's daemon socket: the socket's
// peer is lea's daemon, so `lea stop --all` stops it, with or without the pid file.
func TestStopDaemonStopsTheDaemonOfAPreviousHomebrewVersion(t *testing.T) {
	for _, pidfile := range []bool{true, false} {
		t.Run("pidfile="+strconv.FormatBool(pidfile), func(t *testing.T) {
			w, cellar, sock := brewWorld(t)
			pid, done := fakeDaemon(t, cellar+"/0.7.0/libexec/build/lea", sock, false)
			os.RemoveAll(cellar + "/0.7.0") // brew cleanup: the running binary is gone
			l := w.launcher()
			l.Root, l.Core = cellar+"/0.7.1/libexec", cellar+"/0.7.1/libexec/build/lea"
			l.Sleep = time.Sleep
			pf := w.hostDir() + "/daemon.pid"
			if pidfile {
				os.WriteFile(pf, []byte(strconv.Itoa(pid)+"\n"), 0o600)
			}
			if got := l.Main([]string{"stop", "--all"}); got != 0 {
				t.Fatalf("lea stop --all: %d\n%s", got, w.errb.String())
			}
			waitDone(t, done, "the daemon of the previous Homebrew version was not stopped")
			if exists(pf) {
				t.Fatal("the pid file of a stopped daemon stays")
			}
			if strings.Contains(w.errb.String(), "killed") {
				t.Fatalf("a daemon that stops on SIGTERM is not reported as killed:\n%s", w.errb.String())
			}
		})
	}
}

// #283: `lea` restarts the daemon of the previous Homebrew version instead of failing with
// "did not stop".
func TestEnsureDaemonRestartsTheDaemonOfAPreviousHomebrewVersion(t *testing.T) {
	w, cellar, sock := brewWorld(t)
	pid, done := fakeDaemon(t, cellar+"/0.7.0/libexec/build/lea", sock, false)
	os.RemoveAll(cellar + "/0.7.0")
	os.WriteFile(w.hostDir()+"/daemon.version", []byte("v0.7.0\n"), 0o600)
	os.WriteFile(w.hostDir()+"/daemon.pid", []byte(strconv.Itoa(pid)+"\n"), 0o600)
	// The core answers `agents` while the old daemon runs, and once the new one started.
	os.WriteFile(w.core, []byte(`#!/bin/sh
case $1 in
  version) echo v0.7.1 ;;
  agents) [ -f "$FAKE_LOG.up" ] || [ -d /proc/`+strconv.Itoa(pid)+` ] ;;
  daemon) touch "$FAKE_LOG.up" ;;
esac
`), 0o755)
	l := w.launcher()
	l.Root = cellar + "/0.7.1/libexec"
	l.Sleep = time.Sleep
	if err := l.ensureDaemon(); err != nil {
		t.Fatalf("%v\n%s", err, w.errb.String())
	}
	waitDone(t, done, "the daemon of the previous Homebrew version was not stopped")
	daemonDone(t, w)
}

// #283: lea's own daemon that ignores SIGTERM is killed (SIGKILL after the wait), with one line
// saying so, and its pid file goes.
func TestStopDaemonKillsItsOwnDaemonThatIgnoresTerm(t *testing.T) {
	w, cellar, sock := brewWorld(t)
	pid, done := fakeDaemon(t, cellar+"/0.7.0/libexec/build/lea", sock, true)
	l := w.launcher()
	l.Root, l.Core = cellar+"/0.7.1/libexec", cellar+"/0.7.1/libexec/build/lea"
	pf := w.hostDir() + "/daemon.pid"
	os.WriteFile(pf, []byte(strconv.Itoa(pid)+"\n"), 0o600)
	l.Sleep = func(time.Duration) { time.Sleep(5 * time.Millisecond) }
	l.stopDaemon()
	waitDone(t, done, "the daemon ignoring SIGTERM was not killed")
	want := "lea: the lea daemon (pid " + strconv.Itoa(pid) + ") did not stop on request: killed it\n"
	if got := w.errb.String(); got != want {
		t.Fatalf("stderr %q, want %q", got, want)
	}
	if exists(pf) {
		t.Fatal("the pid file of a killed daemon stays")
	}
}

// A "<path>/lea daemon" that is not the peer of lea's daemon socket, named by no pid file of
// lea's own, is never signalled.
func TestStopDaemonNeverKillsALeaDaemonItCannotProveIsItsOwn(t *testing.T) {
	w, cellar, sock := brewWorld(t)
	// another lea's daemon, on another socket
	pid, done := fakeDaemon(t, w.dir+"/other/build/lea", sock+".other", false)
	l := w.launcher()
	l.Root, l.Core = cellar+"/0.7.1/libexec", cellar+"/0.7.1/libexec/build/lea"
	pf := w.hostDir() + "/daemon.pid"
	os.WriteFile(pf, []byte(strconv.Itoa(pid)+"\n"), 0o600)
	l.Sleep = func(time.Duration) { time.Sleep(5 * time.Millisecond) }
	l.stopDaemon()
	select {
	case <-done:
		t.Fatal("a daemon lea cannot prove is its own was signalled")
	case <-time.After(200 * time.Millisecond):
	}
	if w.errb.Len() != 0 {
		t.Fatalf("stderr: %s", w.errb.String())
	}
}
