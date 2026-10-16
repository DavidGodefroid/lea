package tmux

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func realServer(t *testing.T) Server {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed: make go-test-tmux runs this test")
	}
	dir, err := os.MkdirTemp("", "lt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("TMUX_TMPDIR", dir)
	conf := filepath.Join(dir, "tmux.conf")
	if err := WriteConf(conf); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { exec.Command("tmux", "-L", "lea-test", "kill-server").Run() })
	return Server{Socket: "lea-test", Conf: conf}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRealControlMode(t *testing.T) {
	s := realServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := s.Open(ctx, "lea-t", []string{"sleep", "600"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	run := func(args ...string) []string {
		t.Helper()
		out, err := c.Run(ctx, args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return out
	}

	out := run("list-panes", "-s", "-t", "=lea-t", "-F", "#{pane_id}|#{window_name}|#{pane_dead}")
	if len(out) != 1 || !strings.HasSuffix(out[0], "|deck|0") {
		t.Fatalf("panes=%q", out)
	}
	deck := strings.SplitN(out[0], "|", 2)[0]

	run("set-option", "-p", "-t", deck, "@lea_role", "deck")
	if got := run("display-message", "-p", "-t", deck, "#{@lea_role}"); !slices.Equal(got, []string{"deck"}) {
		t.Fatalf("pane option=%q", got)
	}

	// Quoting: a value with a quote, separators, a variable, a format and a tilde stays literal.
	v := `/tmp/it's here; $HOME #{pane_id} ~`
	run("set-option", "-t", "=lea-t:", "@lea_workdir", v)
	if got := run("display-message", "-p", "-t", "=lea-t:", "#{@lea_workdir}"); !slices.Equal(got, []string{v}) {
		t.Fatalf("session option=%q", got)
	}
	file := filepath.Join(os.Getenv("TMUX_TMPDIR"), "argv")
	run("new-window", "-d", "-t", "=lea-t:", "--", "sh", "-c", `printf %s "$1" > "$2"`, "sh", v, file)
	waitFor(t, "the argv file", func() bool { b, _ := os.ReadFile(file); return len(b) > 0 })
	if b, _ := os.ReadFile(file); string(b) != v {
		t.Fatalf("argv reached the command as %q", b)
	}

	// The control client is flagged and does not size the windows.
	cl := run("list-clients", "-t", "=lea-t", "-F", "#{client_control_mode}|#{client_flags}")
	if len(cl) != 1 || !strings.HasPrefix(cl[0], "1|") || !strings.Contains(cl[0], "ignore-size") {
		t.Fatalf("clients=%q", cl)
	}

	// A pane that exits stays (remain-on-exit) and its death arrives as a subscription change.
	run("refresh-client", "-B", "lea-dead:%*:#{pane_dead}")
	ids := run("split-window", "-h", "-d", "-t", deck, "-P", "-F", "#{pane_id}", "--", "sh", "-c", "exit 0")
	if len(ids) != 1 {
		t.Fatalf("split=%q", ids)
	}
	waitFor(t, "a subscription change for the dead pane", func() bool {
		for {
			select {
			case ev := <-c.Events():
				if ev.Name == "%subscription-changed" && len(ev.Args) > 0 && ev.Args[0] == "lea-dead" &&
					slices.Contains(ev.Args, ids[0]) && ev.Args[len(ev.Args)-1] == "1" {
					return true
				}
			default:
				return false
			}
		}
	})

	// swap-pane moves a parked pane into the deck window.
	park := run("new-window", "-d", "-t", "=lea-t:", "-P", "-F", "#{pane_id}", "--", "sleep", "600")
	run("swap-pane", "-d", "-s", park[0], "-t", ids[0])
	if w := run("display-message", "-p", "-t", park[0], "#{window_name}"); !slices.Equal(w, []string{"deck"}) {
		t.Fatalf("parked pane is in window %q after the swap", w)
	}

	// Killing the session ends the control client: Events closes.
	exec.Command("tmux", "-L", "lea-test", "kill-session", "-t", "=lea-t").Run()
	waitFor(t, "the end of the control client", func() bool {
		for {
			select {
			case _, ok := <-c.Events():
				if !ok {
					return true
				}
			default:
				return false
			}
		}
	})
}

// Claude Code never asks tmux for extended keys, so with `extended-keys on` shift+enter reaches
// it as a plain Enter (the prompt is sent). lea's server forces them, in the CSI u form Claude
// reads, on every pane.
func TestRealPanesGetExtendedKeysWithoutAsking(t *testing.T) {
	s := realServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := s.Open(ctx, "lea-k", []string{"sleep", "600"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	tm := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-L", "lea-test"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if v := tm("show-options", "-s", "-v", "extended-keys"); v != "always" {
		t.Errorf("extended-keys = %q, want always", v)
	}
	if v := tm("show-options", "-s", "-v", "extended-keys-format"); v != "csi-u" {
		t.Errorf("extended-keys-format = %q, want csi-u", v)
	}
	if v := tm("display-message", "-p", "-t", "=lea-k:", "#{pane_key_mode}"); v == "VT10x" {
		t.Errorf("pane key mode = %q: a pane that never asked gets no extended keys", v)
	}
}

// A terminal resize resizes the client first (client-resized) and the window after it, which
// spreads the new width over both panes: the deck is put back to 34 columns once the window
// has its new size.
func TestRealDeckKeepsItsWidthWhenTheWindowIsResized(t *testing.T) {
	s := realServer(t)
	tm := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-L", "lea-test", "-f", s.Conf}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	// The deck window is the one marked @lea_deck, whatever its name; another window, even one
	// named deck, is left alone.
	tm("new-session", "-d", "-s", "lea-r", "-n", "main", "-x", "200", "-y", "50", "sleep", "600")
	tm("split-window", "-h", "-t", "=lea-r:main", "sleep", "600")
	tm("set-option", "-w", "-t", "=lea-r:main", "@lea_deck", "1")
	tm("resize-pane", "-t", "=lea-r:main.0", "-x", "34")
	tm("new-window", "-d", "-t", "=lea-r:", "-n", "deck", "sleep", "600")
	tm("split-window", "-h", "-t", "=lea-r:deck", "sleep", "600")
	tm("resize-pane", "-t", "=lea-r:deck.0", "-x", "34")
	for _, w := range []string{"120", "180"} {
		tm("resize-window", "-t", "=lea-r:main", "-x", w, "-y", "40")
		tm("resize-window", "-t", "=lea-r:deck", "-x", w, "-y", "40")
		waitFor(t, "deck width 34 at "+w, func() bool {
			return tm("display-message", "-p", "-t", "=lea-r:main.0", "#{pane_width}") == "34"
		})
		if got := tm("display-message", "-p", "-t", "=lea-r:deck.0", "#{pane_width}"); got == "34" {
			t.Errorf("at %s an unmarked window's left pane was resized to the deck's width", w)
		}
	}
}

// An agent reattached at the size it already had gets no SIGWINCH and does not repaint. The
// daemon makes the deck one column wider, then puts its width back: the program in the slot
// sees a size change, without anything typed into it.
func TestRealResizingTheDeckSignalsTheSlot(t *testing.T) {
	s := realServer(t)
	tm := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-L", "lea-test", "-f", s.Conf}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	file := filepath.Join(os.Getenv("TMUX_TMPDIR"), "winch")
	tm("new-session", "-d", "-s", "lea-w", "-n", "deck", "-x", "200", "-y", "50", "sleep", "600")
	tm("split-window", "-h", "-t", "=lea-w:deck", "sh", "-c",
		`trap 'echo w >> "$1"' WINCH; : > "$1.ready"; while :; do sleep 0.05; done`, "sh", file)
	tm("resize-pane", "-t", "=lea-w:deck.0", "-x", "34")
	waitFor(t, "the trapping shell", func() bool { _, err := os.Stat(file + ".ready"); return err == nil })
	os.Remove(file)
	tm("resize-pane", "-t", "=lea-w:deck.0", "-x", "35")
	waitFor(t, "SIGWINCH in the slot", func() bool { b, _ := os.ReadFile(file); return len(b) > 0 })
	tm("resize-pane", "-t", "=lea-w:deck.0", "-x", "34")
	waitFor(t, "a second SIGWINCH in the slot", func() bool { b, _ := os.ReadFile(file); return len(b) > 2 })
	if w := tm("display-message", "-p", "-t", "=lea-w:deck.0", "#{pane_width}"); w != "34" {
		t.Fatalf("deck width %s after the redraw", w)
	}
}

// A popup opened by Popup shows on a user's terminal and runs its command, while the daemon's
// control connection keeps answering (display-popup holds only the one-shot client).
func TestRealPopupRunsOnTheUsersTerminal(t *testing.T) {
	s := realServer(t)
	if _, err := exec.LookPath("script"); err != nil {
		t.Skip("script (util-linux) is needed to attach a terminal")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := s.Open(ctx, "lea-p", []string{"sleep", "600"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	in, w, err := os.Pipe() // keeps the terminal's input open
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	att := exec.Command("script", "-qfc", "stty cols 120 rows 40; exec tmux -L lea-test -f "+Quote(s.Conf)+" attach-session -t =lea-p", "/dev/null")
	att.Stdin = in
	// script runs the command with $SHELL: zsh would read =lea-p as an equals expansion.
	att.Env = append(os.Environ(), "TERM=xterm-256color", "SHELL=/bin/sh")
	if err := att.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { att.Process.Kill(); att.Wait() })
	var client string
	waitFor(t, "the user's terminal to attach", func() bool {
		out, err := c.Run(ctx, "list-clients", "-t", "=lea-p", "-F", "#{client_name}|#{client_control_mode}")
		if err != nil {
			return false
		}
		for _, l := range out {
			if name, ctl, ok := strings.Cut(l, "|"); ok && ctl == "0" {
				client = name
				return true
			}
		}
		return false
	})
	file := filepath.Join(os.Getenv("TMUX_TMPDIR"), "popup")
	if err := s.Popup(ctx, client, []string{"sh", "-c", `printf ok > "$1"; sleep 3`, "sh", file}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Run(ctx, "list-panes", "-s", "-t", "=lea-p"); err != nil {
		t.Fatalf("the control connection is held by the popup: %v", err)
	}
	waitFor(t, "the popup's command", func() bool { b, _ := os.ReadFile(file); return string(b) == "ok" })
}

// keyNames lists the keys bound in a key table; none when the table is empty or gone.
func keyNames(t *testing.T, tm func(...string) (string, error), table string) []string {
	t.Helper()
	out, err := tm("list-keys", "-T", table)
	if err != nil {
		if strings.Contains(out, "doesn't exist") {
			return nil
		}
		t.Fatalf("list-keys -T %s: %v: %s", table, err, out)
	}
	var keys []string
	for _, l := range strings.Split(out, "\n") {
		if f := strings.Fields(l); len(f) >= 4 {
			keys = append(keys, f[3])
		}
	}
	slices.Sort(keys)
	return keys
}

// The user cannot change the layout: tmux's default mouse bindings (right-click menus with
// split, swap, kill and zoom, border drags, status clicks) are gone, and only lea's are left.
// Sourcing the file again changes nothing.
func TestRealConfLocksTheLayoutAndIsIdempotent(t *testing.T) {
	s := realServer(t)
	tm := func(args ...string) (string, error) {
		out, err := exec.Command("tmux", append([]string{"-L", "lea-test", "-f", s.Conf}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	if out, err := tm("new-session", "-d", "-s", "lea-l", "-n", "deck", "sleep", "600"); err != nil {
		t.Fatalf("new-session: %v: %s", err, out)
	}
	if got, want := keyNames(t, tm, "root"), []string{"C-Space", "MouseDown1Pane", "MouseDown3Pane", "MouseDrag1Pane", "WheelDownPane", "WheelUpPane"}; !slices.Equal(got, want) {
		t.Fatalf("root table %q, want %q", got, want)
	}
	for _, k := range []string{"MouseDown3Status", "MouseDrag1Border", "M-MouseDown3Pane"} {
		if slices.Contains(keyNames(t, tm, "root"), k) {
			t.Errorf("%s is still bound", k)
		}
	}
	if got := keyNames(t, tm, "prefix"); len(got) != 0 {
		t.Errorf("prefix table %q, want none", got)
	}
	for _, table := range []string{"copy-mode", "copy-mode-vi"} {
		if got, want := keyNames(t, tm, table), []string{"C-c", "Escape", "MouseDrag1Pane", "MouseDragEnd1Pane", "WheelDownPane", "WheelUpPane", "q"}; !slices.Equal(got, want) {
			t.Errorf("%s table %q, want %q", table, got, want)
		}
	}
	// The agents' dead-pane line is set on their panes only: a dead deck or placeholder keeps
	// tmux's own line.
	if v, _ := tm("show-options", "-g", "-v", "remain-on-exit-format"); strings.Contains(v, "x in the deck removes it") {
		t.Errorf("remain-on-exit-format = %q, set for every pane", v)
	}
	// The wheel over the deck (which never asks for the mouse) does not enter copy mode.
	if out, _ := tm("list-keys", "-T", "root", "WheelUpPane"); !strings.Contains(out, "#{!=:#{@lea_role},deck}") {
		t.Errorf("WheelUpPane = %q", out)
	}
	state := func() string {
		var b strings.Builder
		for _, args := range [][]string{{"list-keys"}, {"show-options", "-g"}, {"show-options", "-s"},
			{"show-hooks", "-g"}, {"show-environment", "-g"}} {
			out, err := tm(args...)
			if err != nil {
				t.Fatalf("%v: %v: %s", args, err, out)
			}
			b.WriteString(out + "\n")
		}
		return b.String()
	}
	before := state()
	for range 2 {
		if out, err := tm("source-file", s.Conf); err != nil {
			t.Fatalf("source-file: %v: %s", err, out)
		}
	}
	if after := state(); after != before {
		t.Fatalf("sourcing the file again changed the server:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// The configuration reaches only a server started with it: Open applies it again to a server
// it did not start (here one started with tmux's defaults), so an upgrade needs no kill-server.
func TestRealOpenAppliesTheConfToARunningServer(t *testing.T) {
	s := realServer(t)
	tm := func(args ...string) (string, error) {
		out, err := exec.Command("tmux", append([]string{"-L", "lea-test"}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	if out, err := tm("-f", "/dev/null", "new-session", "-d", "-s", "old", "sleep", "600"); err != nil {
		t.Fatalf("new-session: %v: %s", err, out)
	}
	if !slices.Contains(keyNames(t, tm, "root"), "DoubleClick1Pane") {
		t.Fatal("the default bindings are missing before Open: the test proves nothing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := s.Open(ctx, "lea-o", []string{"sleep", "600"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !c.Created() {
		t.Error("Open created the session but does not say so")
	}
	if got := keyNames(t, tm, "root"); slices.Contains(got, "DoubleClick1Pane") || !slices.Contains(got, "C-Space") {
		t.Fatalf("root table after Open: %q", got)
	}
	c2, err := s.Open(ctx, "lea-o", []string{"sleep", "600"})
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if c2.Created() {
		t.Error("a second Open says it created the session")
	}
}

// Applying the conf to a running server must not leave answers behind on the control client:
// each line the conf holds would otherwise answer, in turn, the commands that follow Open.
func TestRealCommandsAfterOpenOnARunningServerGetTheirOwnAnswers(t *testing.T) {
	s := realServer(t)
	tm := func(args ...string) (string, error) {
		out, err := exec.Command("tmux", append([]string{"-L", "lea-test"}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	if out, err := tm("-f", "/dev/null", "new-session", "-d", "-s", "old", "sleep", "600"); err != nil {
		t.Fatalf("new-session: %v: %s", err, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, name := range []string{"lea-n", "old"} { // a new session, then an existing one
		c, err := s.Open(ctx, name, []string{"sleep", "600"})
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 20; i++ {
			want := fmt.Sprintf("answer-%d", i)
			got, err := c.Run(ctx, "display-message", "-p", "-t", "="+name+":", want)
			if err != nil || len(got) != 1 || got[0] != want {
				c.Close()
				t.Fatalf("%s: command %d after Open got %q, %v; want %q", name, i, got, err, want)
			}
		}
		c.Close()
	}
}

// A conf line the running tmux refuses (an option of a newer tmux) does not cost the user the
// deck: Open warns and attaches all the same.
func TestRealOpenCarriesOnWhenTheConfFailsOnARunningServer(t *testing.T) {
	s := realServer(t)
	bad := filepath.Join(os.Getenv("TMUX_TMPDIR"), "bad.conf")
	if err := os.WriteFile(bad, []byte(Conf()+"set -g lea-no-such-option on\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.Conf = bad
	if out, err := exec.Command("tmux", "-L", "lea-test", "-f", "/dev/null", "new-session", "-d", "-s", "old", "sleep", "600").CombinedOutput(); err != nil {
		t.Fatalf("new-session: %v: %s", err, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := s.Open(ctx, "lea-b", []string{"sleep", "600"})
	if err != nil {
		t.Fatalf("Open failed on a conf error: %v", err)
	}
	defer c.Close()
	if out, err := c.Run(ctx, "display-message", "-p", "-t", "=lea-b:", "ok"); err != nil || len(out) != 1 || out[0] != "ok" {
		t.Fatalf("after Open: %q, %v", out, err)
	}
}

// Releasing a mouse drag copies the selection into the host clipboard bridge's FIFO, found under
// XDG_DATA_HOME as the launcher finds it. Neither the drag nor a selection can be made without
// typing into the pane: tmux's server runs the copy command of the binding on the text instead,
// in the environment copy-pipe gives it.
func TestRealDragEndCopiesIntoTheClipboardFifo(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	fifo := filepath.Join(data, "lea", "state", "clipboard", "copy")
	if err := os.MkdirAll(filepath.Dir(fifo), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	s := realServer(t)
	tm := func(args ...string) (string, error) {
		out, err := exec.Command("tmux", append([]string{"-L", "lea-test", "-f", s.Conf}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	// The copy command of each copy table's MouseDragEnd1Pane binding: the same in both.
	pipes := map[string]string{}
	for _, l := range strings.Split(Conf(), "\n") {
		f := strings.Fields(l)
		if len(f) > 3 && f[0] == "bind-key" && f[3] == "MouseDragEnd1Pane" {
			if _, p, ok := strings.Cut(l, " copy-pipe-and-cancel '"); ok {
				pipes[f[2]], _, _ = strings.Cut(p, "'")
				if !strings.Contains(l, "display-popup -B -x 0 -y 0 -w 100% -h 3") {
					t.Errorf("%s does not say Copied", f[2])
				}
			}
		}
	}
	pipe := pipes["copy-mode"]
	if pipe == "" || pipes["copy-mode-vi"] != pipe {
		t.Fatalf("copy-mode and copy-mode-vi do not both copy on MouseDragEnd1Pane: %q", pipes)
	}
	got := make(chan string, 1)
	go func() {
		b, _ := os.ReadFile(fifo)
		got <- string(b)
	}()
	if out, err := tm("new-session", "-d", "-s", "lea-c", "sleep", "600"); err != nil {
		t.Fatalf("new-session: %v: %s", err, out)
	}
	if out, err := tm("run-shell", "-t", "lea-c", "printf 'copied by lea' | { "+pipe+"; }"); err != nil {
		t.Fatalf("run-shell: %v: %s", err, out)
	}
	select {
	case s := <-got:
		if s != "copied by lea" {
			t.Fatalf("the FIFO got %q", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing reached the FIFO")
	}
}
