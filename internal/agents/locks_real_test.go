package agents

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/store"
	"github.com/DavidGodefroid/lea/internal/tmux"
)

// TestRealLockConsolePane opens a lock console pane against a real tmux server, with a stand-in
// for docker attach: the pane sits under the slot in the deck window, red-bordered, with a short
// history and remain-on-exit off, the keyboard left on the deck; it survives the layout repair;
// when the attach ends its pane closes, leaving no dead pane (nor its screen) behind; lea lock
// close closes the pane and touches no console; an attach that ends at once is a failure.
func TestRealLockConsolePane(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed: make go-test-tmux runs this test")
	}
	old := debounce
	debounce = 20 * time.Millisecond
	t.Cleanup(func() { debounce = old })
	dir, err := os.MkdirTemp("", "lk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("TMUX_TMPDIR", dir)
	conf := filepath.Join(dir, "tmux.conf")
	if err := tmux.WriteConf(conf); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "lea")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 600\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// docker attach …: records its argv, one per line, then stays attached; with the file fail
	// present it exits at once, as an attach to a console that stopped.
	docker := filepath.Join(dir, "docker")
	argv := filepath.Join(dir, "argv")
	fail := filepath.Join(dir, "fail")
	if err := os.WriteFile(docker, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > "+argv+"\n[ -e "+fail+" ] && { echo 'You cannot attach to a stopped container'; exit 1; }\nexec sleep 600\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	const sock = "lea-test-locks"
	t.Cleanup(func() { exec.Command("tmux", "-L", sock, "kill-server").Run() })
	tm := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-L", sock, "-f", conf}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	st, err := store.Open(filepath.Join(dir, "lea.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fc := &fakeConsoles{}
	fl := &fakeLox{c: LockConsole{Instance: "crm", Container: crmID}}
	spec := sqlSpec
	spec.Console = fl.console
	m := NewManager(Config{Store: st, Term: Tmux(tmux.Server{Socket: sock, Conf: conf}),
		Containers: &fakeContainers{live: map[string][]string{}, projects: map[string]string{C: "/home/u/p"}}, LeaBin: bin,
		Docker: docker, UID: 1000, GID: 1000, Now: func() int64 { return 1000 }, Consoles: fc,
		LockSpec: func(string) (LockSpec, error) { return spec, nil }})
	if err := m.Ensure(bg, C, "/"); err != nil {
		t.Fatal(err)
	}
	if st, err := m.LockOpen(bg, C, "locksql", "prod"); err != nil || st != "opened" {
		t.Fatalf("LockOpen: %q %v", st, err)
	}
	panes := func() string {
		return tm("list-panes", "-s", "-t", "="+C, "-F",
			"#{@lea_role}|#{@lea_lock}|#{@lea_deck}|#{pane_at_top}|#{pane_at_left}|#{pane_active}|#{history_limit}|#{remain-on-exit}|#{pane_dead}")
	}
	check := func(what string) {
		t.Helper()
		got := panes()
		for _, want := range []string{
			roleDeck + "||1|1|1|1|10000|", // the deck keeps the keyboard
			rolePlace + "||1|1|0|0|10000|",
			roleLock + "|locksql/prod|1|0|0|0|" + lockHistory + "|off|0", // under the slot
		} {
			if !strings.Contains(got, want) {
				t.Fatalf("%s: no pane %q in\n%s", what, want, got)
			}
		}
	}
	check("opened")
	waitArgv := func() string {
		deadline := time.Now().Add(5 * time.Second)
		for {
			if b, err := os.ReadFile(argv); err == nil && len(b) > 0 {
				return string(b)
			}
			if time.Now().After(deadline) {
				t.Fatal("the console never started")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if a := waitArgv(); a != "attach\n--sig-proxy=false\n--detach-keys\nctrl-p,ctrl-q\n"+crmID+"\n" {
		t.Fatalf("attach argv:\n%s", a)
	}
	lock := tm("list-panes", "-s", "-t", "="+C, "-f", "#{==:#{@lea_role},lock}", "-F", "#{pane_id}")
	if b := tm("display-message", "-p", "-t", lock, "#{T:pane-border-format}"); !strings.Contains(b, " LOCK locksql/prod ") {
		t.Fatalf("border %q", b)
	}
	if err := m.Ensure(bg, C, ""); err != nil {
		t.Fatal(err)
	}
	check("after a repair")

	// The attach ends: its pane closes, no dead pane is left.
	time.Sleep(1500 * time.Millisecond) // let the new pane's notifications pass
	pid := tm("display-message", "-p", "-t", lock, "#{pane_pid}")
	if err := exec.Command("kill", pid).Run(); err != nil {
		t.Fatal(err)
	}
	waitPanes := func(what string, ok func(string) bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !ok(panes()) {
			if time.Now().After(deadline) {
				t.Fatalf("%s: %s", what, panes())
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	waitPanes("the console's pane after it exited", func(p string) bool { return !strings.Contains(p, roleLock) })

	// Opened again, then closed by the user: the pane closes, no console is touched.
	os.Remove(argv)
	if st, err := m.LockOpen(bg, C, "locksql", "prod"); err != nil || st != "opened" {
		t.Fatalf("reopen: %q %v", st, err)
	}
	waitArgv()
	if st, err := m.LockClose(bg, C, "locksql", "prod"); err != nil || st != "closed" {
		t.Fatalf("close: %q %v", st, err)
	}
	waitPanes("after lea lock close", func(p string) bool { return !strings.Contains(p, roleLock) })
	if len(fc.calls) != 0 {
		t.Fatalf("consoles: %q", fc.calls)
	}

	// An attach that ends at once: LockOpen says so (never "opened"), and its pane closes with
	// it, never left dead with its screen.
	os.Remove(argv)
	if err := os.WriteFile(fail, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if st, err := m.LockOpen(bg, C, "locksql", "prod"); err == nil || !strings.Contains(err.Error(), "did not attach") {
		t.Fatalf("open of a failing attach: %q %v", st, err)
	}
	waitArgv()
	waitPanes("after a console that exited at once", func(p string) bool { return !strings.Contains(p, roleLock) })
	if p := panes(); strings.Contains(p, "|1\n") || strings.HasSuffix(p, "|1") {
		t.Fatalf("a dead pane is left: %s", p)
	}
}
