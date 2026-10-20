package tmux

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// lockPanes starts a session with an agent's pane and a lock console's pane, both running cmd,
// tagged as the daemon tags them, and returns their ids.
func lockPanes(t *testing.T, tm func(args ...string) string, session string, cmd ...string) (agent, lock string) {
	t.Helper()
	agent = tm(append([]string{"new-session", "-d", "-s", session, "-x", "120", "-y", "40", "-P", "-F", "#{pane_id}"}, cmd...)...)
	lock = tm(append([]string{"split-window", "-d", "-v", "-t", agent, "-P", "-F", "#{pane_id}"}, cmd...)...)
	tm("set-option", "-p", "-t", agent, "@lea_agent", "dev-1")
	tm("set-option", "-p", "-t", lock, "@lea_role", "lock")
	tm("set-option", "-p", "-t", lock, "@lea_lock", "locksql/prod")
	return agent, lock
}

// A lock console's pane never takes a paste of lea's (ctrl+v: agents can write the host
// clipboard) and never enters copy mode (a copy goes to the clipboard FIFO, which the agents'
// containers open too); its border says LOCK and its id. The bindings' commands and formats run
// in a real tmux, against each pane.
func TestRealLockPaneTakesNoPasteAndNoCopy(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "pbpaste"), []byte("#!/bin/sh\nprintf 'y'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", "")
	s := realServer(t)
	tm := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-L", "lea-test", "-f", s.Conf}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	agent, lock := lockPanes(t, tm, "lea-l", "sleep", "600")

	// ctrl+v's first step writes the clipboard to the paste file: nothing for the lock pane.
	line := ""
	for _, l := range strings.Split(Conf(), "\n") {
		if strings.HasPrefix(l, "bind-key -n C-v ") {
			line = l
		}
	}
	read, _, ok := strings.Cut(strings.TrimPrefix(line, "bind-key -n C-v run-shell '"), `' \; load-buffer`)
	if !ok {
		t.Fatalf("unexpected ctrl+v binding %q", line)
	}
	paste := SocketPath(os.Getenv, os.Getuid(), "lea-test") + ".paste"
	for _, c := range []struct{ pane, want string }{{agent, "y"}, {lock, ""}} {
		os.Remove(paste)
		tm("run-shell", "-t", c.pane, read)
		b, err := os.ReadFile(paste)
		if err != nil || string(b) != c.want {
			t.Errorf("ctrl+v over %s reads %q (%v), want %q", c.pane, b, err, c.want)
		}
	}

	// The two ways into copy mode: the wheel's and the drag's conditions, per pane.
	for _, bind := range []string{"WheelUpPane", "MouseDrag1Pane"} {
		line := ""
		for _, l := range strings.Split(Conf(), "\n") {
			if strings.HasPrefix(l, "bind-key -n "+bind+" ") {
				line = l
			}
		}
		_, inner, ok := strings.Cut(line, `"if-shell -F '`)
		cond, _, ok2 := strings.Cut(inner, "'")
		if !ok || !ok2 || !strings.Contains(inner, "copy-mode -H") {
			t.Fatalf("%s: no guarded copy-mode in %q", bind, line)
		}
		if got := tm("display-message", "-p", "-t", agent, cond); got != "1" {
			t.Errorf("%s over an agent: %q, want copy mode", bind, got)
		}
		if got := tm("display-message", "-p", "-t", lock, cond); got != "0" {
			t.Errorf("%s over the lock pane: %q, want no copy mode", bind, got)
		}
	}

	border := tm("display-message", "-p", "-t", lock, "#{T:pane-border-format}")
	if !strings.Contains(border, " LOCK locksql/prod ") || !strings.Contains(border, "bg=#f38ba8") {
		t.Errorf("the lock pane's border: %q", border)
	}
	if b := tm("display-message", "-p", "-t", agent, "#{T:pane-border-format}"); strings.Contains(b, "LOCK") || !strings.Contains(b, "dev-1") {
		t.Errorf("an agent's border: %q", b)
	}
}

// ctrl+space from the deck goes to the agent, never into the lock console under it, even when the
// console was the pane last used on the right (tmux's select-pane -R picks that one); from the
// console it goes back to the deck. The binding's command runs as the key would run it.
func TestRealCtrlSpaceNeverLandsInALockPane(t *testing.T) {
	s := realServer(t)
	tm := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-L", "lea-test", "-f", s.Conf}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	cmd := ""
	for _, l := range strings.Split(Conf(), "\n") {
		if c, ok := strings.CutPrefix(l, "bind-key -n C-Space "); ok {
			cmd = c
		}
	}
	if cmd == "" {
		t.Fatal("no ctrl+space binding")
	}
	key := filepath.Join(os.Getenv("TMUX_TMPDIR"), "ctrl-space.conf")
	if err := os.WriteFile(key, []byte(cmd+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	deck := tm("new-session", "-d", "-s", "lea-s", "-x", "120", "-y", "40", "-P", "-F", "#{pane_id}", "sleep", "600")
	slot := tm("split-window", "-d", "-h", "-t", deck, "-P", "-F", "#{pane_id}", "sleep", "600")
	lock := tm("split-window", "-d", "-v", "-t", slot, "-P", "-F", "#{pane_id}", "sleep", "600")
	tm("set-option", "-p", "-t", deck, "@lea_role", "deck")
	tm("set-option", "-p", "-t", slot, "@lea_agent", "dev-1")
	tm("set-option", "-p", "-t", lock, "@lea_role", "lock")
	press := func() string {
		t.Helper()
		tm("source-file", key)
		return tm("display-message", "-p", "-t", "=lea-s:", "#{pane_id}")
	}
	// The user answered in the console, then came back to the deck.
	tm("select-pane", "-t", lock)
	tm("select-pane", "-t", deck)
	if got := press(); got != slot {
		t.Fatalf("ctrl+space from the deck went to %s, want the agent %s (lock %s)", got, slot, lock)
	}
	if got := press(); got != deck {
		t.Fatalf("ctrl+space from the agent went to %s, want the deck %s", got, deck)
	}
	tm("select-pane", "-t", lock)
	if got := press(); got != deck {
		t.Fatalf("ctrl+space from the console went to %s, want the deck %s", got, deck)
	}
}
