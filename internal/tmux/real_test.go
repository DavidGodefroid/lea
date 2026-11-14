package tmux

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
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
	if err := WriteConf(conf, ""); err != nil {
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
	// ClientTTYs' format names it a control client (1), which gets no bell (#165).
	if tt := run("list-clients", "-t", "=lea-t", "-F", clientTTYsFormat); len(tt) != 1 || !strings.HasPrefix(tt[0], "1 ") || len(parseClientTTYs(tt[0])) != 0 {
		t.Fatalf("client ttys=%q", tt)
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
// spreads the new width over both panes: the deck is put back to the @lea_deck_width option's
// width (the default while nothing set it) once the window has its new size.
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
	if got := tm("show-options", "-s", "-v", "@lea_deck_width"); got != "34" {
		t.Fatalf("the conf's default width is %q, want 34", got)
	}
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
	// The user's width, in the option, is the one kept.
	tm("set-option", "-s", "@lea_deck_width", "50")
	tm("resize-window", "-t", "=lea-r:main", "-x", "160", "-y", "40")
	waitFor(t, "deck width 50", func() bool {
		return tm("display-message", "-p", "-t", "=lea-r:main.0", "#{pane_width}") == "50"
	})
}

// Dragging the border between the deck and the agent pane resizes the deck, and releasing the
// drag stores the width, clamped to its bounds, in the @lea_deck_width option the resize hook
// reads. The mouse events are written to an attached terminal as a real one sends them.
func TestRealDraggingTheBorderResizesAndStoresTheDeckWidth(t *testing.T) {
	s := realServer(t)
	tm := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-L", "lea-test", "-f", s.Conf}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	tm("new-session", "-d", "-s", "lea-d", "-n", "main", "-x", "200", "-y", "50", "sleep", "600")
	tm("split-window", "-h", "-t", "=lea-d:main", "sleep", "600")
	tm("set-option", "-w", "-t", "=lea-d:main", "@lea_deck", "1")
	tm("resize-pane", "-t", "=lea-d:main.0", "-x", "34")
	in, w, err := os.Pipe() // keeps the terminal's input open
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	att := exec.Command("script", "-qfc", "stty cols 200 rows 50; exec tmux -L lea-test -f "+Quote(s.Conf)+" attach-session -t =lea-d", "/dev/null")
	att.Stdin = in
	att.Stdout, att.Stderr = io.Discard, io.Discard
	att.Env = append(os.Environ(), "TERM=xterm-256color", "SHELL=/bin/sh")
	if err := att.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { att.Process.Kill(); att.Wait() })
	waitFor(t, "the user's terminal to attach", func() bool {
		out, err := exec.Command("tmux", "-L", "lea-test", "list-clients", "-t", "=lea-d", "-F", "#{client_name}|#{client_control_mode}").Output()
		if err != nil {
			return false
		}
		for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if _, ctl, ok := strings.Cut(l, "|"); ok && ctl == "0" {
				return true
			}
		}
		return false
	})
	width := func() string { return tm("display-message", "-p", "-t", "=lea-d:main.0", "#{pane_width}") }
	stored := func() string { return tm("show-options", "-s", "-v", "@lea_deck_width") }
	// A left-button press (code 0) on the border at column 35, a drag (code 32), the release.
	drag := func(x0, x1 int) {
		t.Helper()
		fmt.Fprintf(w, "\x1b[<0;%d;10M", x0)
		time.Sleep(200 * time.Millisecond)
		fmt.Fprintf(w, "\x1b[<32;%d;10M", x1)
		time.Sleep(200 * time.Millisecond)
		fmt.Fprintf(w, "\x1b[<0;%d;10m", x1)
	}
	drag(35, 50)
	waitFor(t, "the dragged width", func() bool { return width() == "49" })
	waitFor(t, "the stored width", func() bool { return stored() == "49" })
	// Past the bounds the stored width and the pane snap to them: 24 at least, 60 at most.
	drag(50, 10)
	waitFor(t, "the width clamped to 24", func() bool { return width() == "24" && stored() == "24" })
	drag(25, 150)
	waitFor(t, "the width clamped to 60", func() bool { return width() == "60" && stored() == "60" })
}

// The drag's release and the window-resized hook run lea's tmux by its absolute path (#275): with
// lea's tmux outside the server's PATH (a host without tmux, or with one too old, runs lea's own
// static build through LEA_TMUX_BIN), releasing a drag still stores the width and a terminal
// resize still keeps it. lea's tmux here is the test's, reached through a directory whose name
// holds a space; the server runs with a PATH that holds no tmux.
func TestRealConfRunsLeasTmuxWithoutTmuxInPATH(t *testing.T) {
	sys, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed: make go-test-tmux runs this test")
	}
	if _, err := exec.LookPath("script"); err != nil {
		t.Skip("script is not installed")
	}
	dir, err := os.MkdirTemp("", "lp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	bin := filepath.Join(dir, "lea tmux", "tmux")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sys, bin); err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(dir, "tmux.conf")
	if err := WriteConf(conf, bin); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	// The server's environment, and so its run-shell's: no tmux in PATH.
	env := []string{"PATH=" + empty, "TMUX_TMPDIR=" + dir, "HOME=" + dir, "TERM=xterm-256color"}
	tm := func(args ...string) string {
		t.Helper()
		c := exec.Command(bin, append([]string{"-L", "lea-nopath", "-f", conf}, args...)...)
		c.Env = env
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	t.Cleanup(func() {
		c := exec.Command(bin, "-L", "lea-nopath", "kill-server")
		c.Env = env
		c.Run()
	})
	tm("new-session", "-d", "-s", "lea-p", "-n", "main", "-x", "200", "-y", "50", "/bin/sleep", "600")
	tm("split-window", "-h", "-t", "=lea-p:main", "/bin/sleep", "600")
	tm("set-option", "-w", "-t", "=lea-p:main", "@lea_deck", "1")
	tm("resize-pane", "-t", "=lea-p:main.0", "-x", "34")
	if got := tm("show-environment", "-g", "PATH"); got != "PATH="+empty {
		t.Fatalf("the server's PATH is %q, want %q", got, "PATH="+empty)
	}
	in, w, err := os.Pipe() // keeps the terminal's input open
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	att := exec.Command("script", "-qfc", "stty cols 200 rows 50; exec "+Quote(bin)+" -L lea-nopath -f "+Quote(conf)+" attach-session -t =lea-p", "/dev/null")
	att.Stdin = in
	att.Stdout, att.Stderr = io.Discard, io.Discard
	att.Env = append(os.Environ(), "TERM=xterm-256color", "SHELL=/bin/sh", "TMUX_TMPDIR="+dir)
	if err := att.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { att.Process.Kill(); att.Wait() })
	waitFor(t, "the user's terminal to attach", func() bool {
		c := exec.Command(bin, "-L", "lea-nopath", "list-clients", "-t", "=lea-p", "-F", "#{client_control_mode}")
		c.Env = env
		out, err := c.Output()
		return err == nil && strings.Contains(string(out), "0")
	})
	width := func() string { return tm("display-message", "-p", "-t", "=lea-p:main.0", "#{pane_width}") }
	stored := func() string { return tm("show-options", "-s", "-v", "@lea_deck_width") }
	// A left-button press on the border at column 35, a drag to 45, the release.
	fmt.Fprintf(w, "\x1b[<0;35;10M")
	time.Sleep(200 * time.Millisecond)
	fmt.Fprintf(w, "\x1b[<32;45;10M")
	time.Sleep(200 * time.Millisecond)
	fmt.Fprintf(w, "\x1b[<0;45;10m")
	waitFor(t, "the dragged width", func() bool { return width() == "44" })
	waitFor(t, "the released width stored by lea's tmux", func() bool { return stored() == "44" })
	// A terminal resize spreads the window over both panes; the hook, run by lea's tmux, puts the
	// deck back to the stored width.
	tm("resize-window", "-t", "=lea-p:main", "-x", "160", "-y", "50")
	waitFor(t, "the deck kept at 44 after a resize", func() bool { return width() == "44" })
	tm("resize-window", "-t", "=lea-p:main", "-x", "220", "-y", "50")
	waitFor(t, "the deck kept at 44 after another resize", func() bool { return width() == "44" })
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
	if got, want := keyNames(t, tm, "root"), []string{"C-MouseDown3Pane", "C-Space", "C-v", "MouseDown1Border", "MouseDown1Pane", "MouseDown3Pane", "MouseDrag1Border", "MouseDrag1Pane", "MouseDragEnd1Border", "MouseDragEnd1Pane", "WheelDownPane", "WheelUpPane"}; !slices.Equal(got, want) {
		t.Fatalf("root table %q, want %q", got, want)
	}
	for _, k := range []string{"MouseDown3Status", "M-MouseDown3Pane", "DoubleClick1Pane", "TripleClick1Pane"} {
		if slices.Contains(keyNames(t, tm, "root"), k) {
			t.Errorf("%s is still bound", k)
		}
	}
	if got := keyNames(t, tm, "prefix"); len(got) != 0 {
		t.Errorf("prefix table %q, want none", got)
	}
	// The copy tables: lea's own keys (the wheel and the exits), and the 97 typed keys that leave the history (93 printable
	// ASCII but q, and Space, Enter, BSpace, Tab); never Any, which mouse events would reach.
	for _, table := range []string{"copy-mode", "copy-mode-vi"} {
		got := keyNames(t, tm, table)
		for _, k := range []string{"C-c", "Escape", "WheelDownPane", "WheelUpPane", "q", "Space", "Enter", "BSpace", "Tab", "a"} {
			if !slices.Contains(got, k) {
				t.Errorf("%s table %q lacks %s", table, got, k)
			}
		}
		// No selection of lea's (#419): a drag in the history selects and copies nothing.
		for _, k := range []string{"MouseDrag1Pane", "MouseDragEnd1Pane"} {
			if slices.Contains(got, k) {
				t.Errorf("%s table binds %s", table, k)
			}
		}
		if len(got) != 5+97 || slices.Contains(got, "Any") {
			t.Errorf("%s table has %d keys %q, want %d and no Any", table, len(got), got, 5+97)
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
	// What an older lea's selection (#419) left on its server: the edge-scroll alias and its
	// drag counters.
	for _, args := range [][]string{{"set", "-s", "command-alias[100]", "lea-drag-scroll=display-message old"},
		{"set", "-s", "@lea_drag", "3"}, {"set", "-s", "@lea_drag_n", "3"}} {
		if out, err := tm(args...); err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
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
	for _, o := range []string{"command-alias[100]", "@lea_drag", "@lea_drag_n"} {
		if v, _ := tm("show-options", "-s", "-v", "-q", o); v != "" {
			t.Errorf("%s = %q after Open, want it dropped", o, v)
		}
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

// attachTerminal attaches a user's terminal (script, 120x40) to session and returns its client;
// everything tmux draws on it is written to the file log.
func attachTerminal(t *testing.T, ctx context.Context, s Server, c *Conn, session, log string) string {
	t.Helper()
	if _, err := exec.LookPath("script"); err != nil {
		t.Skip("script (util-linux) is needed to attach a terminal")
	}
	in, w, err := os.Pipe() // keeps the terminal's input open
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	att := exec.Command("script", "-qfc", "stty cols 120 rows 40; exec tmux -L lea-test -f "+Quote(s.Conf)+" attach-session -t ="+session, log)
	att.Stdin = in
	att.Env = append(os.Environ(), "TERM=xterm-256color", "SHELL=/bin/sh")
	if err := att.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { att.Process.Kill(); att.Wait() })
	var client string
	waitFor(t, "the user's terminal to attach", func() bool {
		out, err := c.Run(ctx, "list-clients", "-t", "="+session, "-F", "#{client_name}|#{client_control_mode}")
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
	return client
}

// A hyperlink (OSC 8) longer than the pane is wide reaches the user's terminal whole, on every
// row it wraps over: the terminal then opens the full link wherever it is clicked.
func TestRealLongHyperlinkReachesTheTerminalWhole(t *testing.T) {
	s := realServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	url := "https://claude.ai/oauth/authorize?code=true&client_id=" + strings.Repeat("0123456789", 25)
	c, err := s.Open(ctx, "lea-h", []string{"sh", "-c", `printf '\033]8;;%s\033\\%s\033]8;;\033\\\n' "$1" "$1"; exec sleep 600`, "sh", url})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	log := filepath.Join(os.Getenv("TMUX_TMPDIR"), "terminal")
	attachTerminal(t, ctx, s, c, "lea-h", log)
	var drawn string
	waitFor(t, "the link on the terminal", func() bool {
		b, _ := os.ReadFile(log)
		drawn = string(b)
		return strings.Contains(drawn, "0123456789\x1b]8;")
	})
	// tmux opens the link on each row it draws (OSC 8 with an id, then the URL): at least two
	// rows of 120 columns hold the 300-character link, each carrying it whole.
	if n := strings.Count(drawn, ";"+url+"\x1b\\"); n < 2 {
		t.Errorf("the link reaches the terminal whole on %d rows, want 2 or more: %q", n, drawn)
	}
}

// The terminal beside the deck says on its top border whether it has the keyboard: "typing here"
// while it has it, the keys that give it the keyboard while the deck has it. The deck's border
// and the empty slot's say nothing.
func TestRealTheSlotSaysWhetherItHasTheKeyboard(t *testing.T) {
	s := realServer(t)
	tm := func(args ...string) string {
		t.Helper()
		// -u: the client is UTF-8, as the user's terminal is (the bar's · and ▶).
		out, err := exec.Command("tmux", append([]string{"-u", "-L", "lea-test", "-f", s.Conf}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	tm("new-session", "-d", "-s", "lea-k", "-x", "160", "-y", "40", "sleep", "600")
	tm("split-window", "-h", "-t", "=lea-k:", "sleep", "600")
	tm("set-option", "-p", "-t", "=lea-k:.0", "@lea_role", "deck")
	tm("set-option", "-p", "-t", "=lea-k:.1", "@lea_agent", "fix-login")
	border := func(pane string) string { return tm("display-message", "-p", "-t", pane, "#{T:pane-border-format}") }
	if v := tm("show-options", "-g", "-v", "pane-border-status"); v != "top" {
		t.Fatalf("pane-border-status = %q, want top", v)
	}
	tm("select-pane", "-t", "=lea-k:.1")
	if b := border("=lea-k:.1"); !strings.Contains(b, "fix-login · typing here") || !strings.Contains(b, "bg=#89b4fa") {
		t.Errorf("the focused agent's border: %q", b)
	}
	if b := border("=lea-k:.0"); b != "" {
		t.Errorf("the deck's border shows %q", b)
	}
	// Scrolled back, the bar says the keys go to the history, not "typing here".
	tm("copy-mode", "-t", "=lea-k:.1")
	if b := border("=lea-k:.1"); !strings.Contains(b, "fix-login · history 0/0: type or q to go back") || strings.Contains(b, "typing here") {
		t.Errorf("the focused agent's border in the history: %q", b)
	}
	tm("send-keys", "-X", "-t", "=lea-k:.1", "cancel")
	tm("select-pane", "-t", "=lea-k:.0")
	if b := border("=lea-k:.1"); !strings.Contains(b, "fix-login · enter or ctrl+space: type here") || strings.Contains(b, "typing here") {
		t.Errorf("the agent's border while the deck has the keyboard: %q", b)
	}
	tm("set-option", "-p", "-u", "-t", "=lea-k:.1", "@lea_agent")
	tm("set-option", "-p", "-t", "=lea-k:.1", "@lea_shell", "sh-1")
	tm("select-pane", "-t", "=lea-k:.1")
	if b := border("=lea-k:.1"); !strings.Contains(b, "sh-1 · typing here") {
		t.Errorf("a shell's border: %q", b)
	}
	tm("set-option", "-p", "-u", "-t", "=lea-k:.1", "@lea_shell")
	tm("set-option", "-p", "-t", "=lea-k:.1", "@lea_role", "placeholder")
	if b := border("=lea-k:.1"); b != "" {
		t.Errorf("the empty slot's border shows %q", b)
	}
}

// ctrl+v pastes the host clipboard's text into the pane that has the keyboard (here pbpaste, the
// first reader found with no display set), and leaves no buffer and no file behind.
func TestRealCtrlVPastesTheHostClipboard(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "pbpaste"), []byte("#!/bin/sh\nprintf 'from the clipboard: h\\303\\251llo'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", "")
	s := realServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	got := filepath.Join(os.Getenv("TMUX_TMPDIR"), "pasted")
	c, err := s.Open(ctx, "lea-v", []string{"sh", "-c", `stty raw -echo; exec cat > "$1"`, "sh", got})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	client := attachTerminal(t, ctx, s, c, "lea-v", filepath.Join(os.Getenv("TMUX_TMPDIR"), "terminal"))
	// send-keys -K: the key as the user's terminal sends it, through the key bindings.
	if out, err := exec.Command("tmux", "-L", "lea-test", "send-keys", "-K", "-c", client, "C-v").CombinedOutput(); err != nil {
		t.Fatalf("send-keys -K: %v: %s", err, out)
	}
	waitFor(t, "the paste in the pane", func() bool { b, _ := os.ReadFile(got); return string(b) == "from the clipboard: héllo" })
	if out, _ := exec.Command("tmux", "-L", "lea-test", "list-buffers", "-F", "#{buffer_name}").Output(); strings.Contains(string(out), "lea-paste") {
		t.Errorf("the paste buffer is left: %q", out)
	}
	sock := SocketPath(os.Getenv, os.Getuid(), "lea-test")
	if _, err := os.Stat(sock + ".paste"); !os.IsNotExist(err) {
		t.Errorf("the paste file is left: %v", err)
	}
}

// A pane keeps its history when its program asks for the alternate screen: abduco, which runs
// every agent and shell, asks for it when it attaches, and the history (Claude Code's
// conversation) would be gone from the wheel's reach.
func TestRealAPaneKeepsItsHistoryUnderAbduco(t *testing.T) {
	s := realServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := s.Open(ctx, "lea-h2", []string{"sh", "-c", `printf '\033[?1049h\033[H'; seq 1 200; exec sleep 600`})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var out []string
	waitFor(t, "the pane's history", func() bool {
		out, err = c.Run(ctx, "display-message", "-p", "-t", "=lea-h2:", "#{alternate_on}|#{history_size}")
		if err != nil || len(out) != 1 {
			return false
		}
		alt, n, _ := strings.Cut(out[0], "|")
		h, _ := strconv.Atoi(n)
		return alt == "0" && h > 100
	})
}

// The wheel puts an agent's pane into copy mode, where tmux drops every key the copy table does
// not bind: the pane still showed Claude Code's prompt and typing did nothing. A key typed in the
// history now leaves it (lea types nothing: that key is dropped) and the next keys reach the
// program; ctrl+space still moves the keyboard to the deck.
func TestRealTypingInTheHistoryLeavesIt(t *testing.T) {
	s := realServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	got := filepath.Join(os.Getenv("TMUX_TMPDIR"), "typed")
	c, err := s.Open(ctx, "lea-y", []string{"sh", "-c", `stty raw -echo; exec cat > "$1"`, "sh", got})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	client := attachTerminal(t, ctx, s, c, "lea-y", filepath.Join(os.Getenv("TMUX_TMPDIR"), "terminal"))
	tm := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-L", "lea-test"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	pane := "=lea-y:"
	inMode := func() bool { return tm("display-message", "-p", "-t", pane, "#{pane_in_mode}") == "1" }
	enter := func() {
		t.Helper()
		tm("copy-mode", "-t", pane)
		waitFor(t, "copy mode", inMode)
	}
	// send-keys -K: each key as the user's terminal sends it, through the key bindings.
	for _, k := range []string{"h", "'", "#", `"`, "Space", "Enter", "BSpace", "q", "Escape"} {
		enter()
		tm("send-keys", "-K", "-c", client, k)
		waitFor(t, k+" to leave copy mode", func() bool { return !inMode() })
	}
	// None of those keys reached the program, which still reads its terminal: keys sent to the
	// pane itself arrive alone (send-keys -K delivered no unbound key to the program in CI).
	tm("send-keys", "-t", pane, "o", "k", "Enter")
	var typed []byte
	for deadline := time.Now().Add(5 * time.Second); string(typed) != "ok\r" && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		typed, _ = os.ReadFile(got)
	}
	if string(typed) != "ok\r" {
		t.Fatalf("the program got %q, want only the keys sent after the history, %q", typed, "ok\r")
	}
	// ctrl+space in the history still moves the keyboard (its root binding is not shadowed).
	tm("split-window", "-h", "-b", "-d", "-t", pane, "sleep", "600")
	enter()
	before := tm("display-message", "-p", "-t", pane, "#{pane_id}")
	tm("send-keys", "-K", "-c", client, "C-Space")
	waitFor(t, "ctrl+space to move the keyboard", func() bool {
		return tm("display-message", "-p", "-t", pane, "#{pane_id}") != before
	})
}

// Scrolled back into the history, the slot's bar says where the view is in the conversation,
// counting down from its last line: the line at the top of the view out of the history's lines
// (tmux's own indicator counted the lines scrolled, 5/281 just under the bottom, and is hidden).
func TestRealTheHistoryPositionCountsDownFromTheBottom(t *testing.T) {
	s := realServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := s.Open(ctx, "lea-p", []string{"sh", "-c", "seq 1 300; exec sleep 600"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	log := filepath.Join(os.Getenv("TMUX_TMPDIR"), "terminal")
	attachTerminal(t, ctx, s, c, "lea-p", log)
	tm := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-u", "-L", "lea-test"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	pane := "=lea-p:"
	tm("set-option", "-p", "-t", pane, "@lea_agent", "fix-login")
	waitFor(t, "the pane's history", func() bool {
		h, _ := strconv.Atoi(tm("display-message", "-p", "-t", pane, "#{history_size}"))
		return h > 200
	})
	// Into the history as the wheel goes there.
	var enter string
	for _, l := range strings.Split(Conf(), "\n") {
		if strings.HasPrefix(l, "bind-key -n WheelUpPane ") {
			if m := regexp.MustCompile(`'(copy-mode[^']*)'`).FindStringSubmatch(l); m != nil {
				enter = m[1]
			}
		}
	}
	if enter == "" {
		t.Fatal("WheelUpPane enters no copy mode")
	}
	tm(append(strings.Fields(enter), "-t", pane)...)
	tm("send-keys", "-X", "-t", pane, "-N", "5", "scroll-up")
	hs, _ := strconv.Atoi(tm("display-message", "-p", "-t", pane, "#{history_size}"))
	if sp := tm("display-message", "-p", "-t", pane, "#{scroll_position}"); sp != "5" {
		t.Fatalf("scroll_position = %s, want 5", sp)
	}
	want := fmt.Sprintf("history %d/%d", hs-5, hs)
	if b := tm("display-message", "-p", "-t", pane, "#{T:pane-border-format}"); !strings.Contains(b, want) {
		t.Errorf("the bar in the history: %q, want %q", b, want)
	}
	var drawn string
	waitFor(t, "the position on the terminal", func() bool {
		b, _ := os.ReadFile(log)
		drawn = string(b)
		return strings.Contains(drawn, want)
	})
	if old := fmt.Sprintf("[5/%d]", hs); strings.Contains(drawn, old) {
		t.Errorf("tmux's own indicator %s is still drawn", old)
	}
	// Back at the bottom, then out of the history: the bar no longer counts.
	tm("send-keys", "-X", "-t", pane, "cancel")
	if b := tm("display-message", "-p", "-t", pane, "#{T:pane-border-format}"); strings.Contains(b, "history") {
		t.Errorf("the bar out of the history: %q", b)
	}
}

// ctrl+v typed on the user's terminal pastes into an agent but not into a lock console.
func TestRealCtrlVPastesNothingIntoALockPane(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "pbpaste"), []byte("#!/bin/sh\nprintf 'yes'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", "")
	s := realServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := os.Getenv("TMUX_TMPDIR")
	c, err := s.Open(ctx, "lea-k", []string{"sleep", "600"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	client := attachTerminal(t, ctx, s, c, "lea-k", filepath.Join(dir, "terminal"))
	tm := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-L", "lea-test"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	sink := func(f string) []string { return []string{"sh", "-c", `stty raw -echo; exec cat > "$1"`, "sh", f} }
	got := map[string]string{"agent": filepath.Join(dir, "agent"), "lock": filepath.Join(dir, "lock")}
	agent := tm(append([]string{"new-window", "-t", "=lea-k:", "-P", "-F", "#{pane_id}"}, sink(got["agent"])...)...)
	lock := tm(append([]string{"split-window", "-v", "-t", agent, "-P", "-F", "#{pane_id}"}, sink(got["lock"])...)...)
	tm("set-option", "-p", "-t", agent, "@lea_agent", "dev-1")
	tm("set-option", "-p", "-t", lock, "@lea_role", "lock")
	tm("set-option", "-p", "-t", lock, "@lea_lock", "locksql")
	waitFor(t, "both sinks", func() bool {
		_, e1 := os.Stat(got["agent"])
		_, e2 := os.Stat(got["lock"])
		return e1 == nil && e2 == nil
	})
	// The lock pane first, then the agent's: once the agent's paste arrived, the lock pane's
	// binding has run too (one client, keys in order).
	for _, p := range []string{lock, agent} {
		tm("select-pane", "-t", p)
		tm("send-keys", "-K", "-c", client, "C-v")
	}
	waitFor(t, "the paste in the agent's pane", func() bool { b, _ := os.ReadFile(got["agent"]); return string(b) == "yes" })
	if b, _ := os.ReadFile(got["lock"]); len(b) != 0 {
		t.Fatalf("the lock pane got a paste: %q", b)
	}
}

// ctrl+space typed on the user's terminal, from the deck, goes to the agent and never into the
// lock console under it, even when the console had the keyboard last.
func TestRealCtrlSpaceKeySkipsTheLockPane(t *testing.T) {
	s := realServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := s.Open(ctx, "lea-cs", []string{"sleep", "600"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	client := attachTerminal(t, ctx, s, c, "lea-cs", filepath.Join(os.Getenv("TMUX_TMPDIR"), "terminal"))
	tm := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-L", "lea-test"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	deck := tm("display-message", "-p", "-t", "=lea-cs:", "#{pane_id}")
	slot := tm("split-window", "-d", "-h", "-t", deck, "-P", "-F", "#{pane_id}", "sleep", "600")
	lock := tm("split-window", "-d", "-v", "-t", slot, "-P", "-F", "#{pane_id}", "sleep", "600")
	tm("set-option", "-p", "-t", lock, "@lea_role", "lock")
	tm("select-pane", "-t", lock)
	tm("select-pane", "-t", deck)
	tm("send-keys", "-K", "-c", client, "C-Space")
	waitFor(t, "the keyboard on the agent", func() bool { return tm("display-message", "-p", "-t", "=lea-cs:", "#{pane_id}") == slot })
}

// dragWindow is lea's deck window on a real server, with a user's terminal (200x50) attached:
// the deck on the left (34 columns), an agent with 5000 lines of history on the right, and with
// split, a lock console under the agent (10 rows). It returns the tmux runner, the terminal's
// input (mouse events are written there as the terminal sends them) and the agent's pane.
func dragWindow(t *testing.T, split bool) (func(args ...string) string, io.Writer, string) {
	t.Helper()
	s := realServer(t)
	if _, err := exec.LookPath("script"); err != nil {
		t.Skip("script (util-linux) is needed to attach a terminal")
	}
	tm := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-L", "lea-test", "-f", s.Conf}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	tm("new-session", "-d", "-s", "lea-g", "-n", "main", "-x", "200", "-y", "50", "sleep", "600")
	tm("split-window", "-h", "-t", "=lea-g:main", "sh", "-c", "seq 1 5000; exec sleep 600")
	tm("set-option", "-w", "-t", "=lea-g:main", "@lea_deck", "1")
	tm("set-option", "-p", "-t", "=lea-g:main.0", "@lea_role", "deck")
	tm("set-option", "-p", "-t", "=lea-g:main.1", "@lea_agent", "fix-login")
	tm("resize-pane", "-t", "=lea-g:main.0", "-x", "34")
	if split {
		tm("split-window", "-v", "-d", "-l", "10", "-t", "=lea-g:main.1", "sleep", "600")
		tm("set-option", "-p", "-t", "=lea-g:main.2", "@lea_role", "lock")
	}
	agent := tm("display-message", "-p", "-t", "=lea-g:main.1", "#{pane_id}")
	waitFor(t, "the agent's history", func() bool {
		h, _ := strconv.Atoi(tm("display-message", "-p", "-t", agent, "#{history_size}"))
		return h > 4000
	})
	in, w, err := os.Pipe() // keeps the terminal's input open
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	att := exec.Command("script", "-qfc", "stty cols 200 rows 50; exec tmux -L lea-test -f "+Quote(s.Conf)+" attach-session -t =lea-g", "/dev/null")
	att.Stdin = in
	att.Stdout, att.Stderr = io.Discard, io.Discard
	att.Env = append(os.Environ(), "TERM=xterm-256color", "SHELL=/bin/sh")
	if err := att.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { att.Process.Kill(); att.Wait() })
	waitFor(t, "the user's terminal to attach", func() bool {
		out, err := exec.Command("tmux", "-L", "lea-test", "list-clients", "-t", "=lea-g", "-F", "#{client_control_mode}").Output()
		return err == nil && strings.Contains(string(out), "0")
	})
	return tm, w, agent
}

// mouse writes an SGR mouse event at the terminal's column x and row y (1-based): a press
// (code 0, M), a drag (code 32, M) or a release (code 0, m).
func mouse(w io.Writer, code, x, y int, release bool) {
	end := "M"
	if release {
		end = "m"
	}
	fmt.Fprintf(w, "\x1b[<%d;%d;%d%s", code, x, y, end)
	time.Sleep(60 * time.Millisecond)
}

// A program that asked for the mouse still gets the whole drag, its release included, now that
// the root table binds MouseDragEnd1Pane; and no edge scroll starts for it.
func TestRealDragInAMouseProgramReachesIt(t *testing.T) {
	fifo := bridgeFifo(t)
	tm, w, agent := dragWindow(t, false)
	got := filepath.Join(os.Getenv("TMUX_TMPDIR"), "mouse")
	tm("respawn-pane", "-k", "-t", agent, "sh", "-c", `printf '\033[?1002h\033[?1006h'; stty raw -echo; exec cat > "$0"`, got)
	waitFor(t, "the program to ask for the mouse", func() bool {
		return tm("display-message", "-p", "-t", agent, "#{mouse_any_flag}") == "1"
	})
	mouse(w, 0, 100, 20, false)
	mouse(w, 32, 100, 30, false)
	mouse(w, 32, 100, 50, false)
	mouse(w, 0, 100, 50, true)
	var b []byte
	waitFor(t, "the release to reach the program", func() bool {
		b, _ = os.ReadFile(got)
		return strings.Contains(string(b), "\x1b[<0;65;49m")
	})
	for _, want := range []string{"\x1b[<0;65;19M", "\x1b[<32;65;29M"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the program got %q, want %q in it", b, want)
		}
	}
	if m := tm("display-message", "-p", "-t", agent, "#{pane_in_mode}"); m != "0" {
		t.Error("the drag put the program's pane into the history")
	}
	if got := fifo(); got != "" {
		t.Errorf("tmux wrote %q into the clipboard bridge's FIFO", got)
	}
}

// bridgeFifo makes the clipboard bridge's FIFO where the launcher makes it, under a fresh
// XDG_DATA_HOME (set before the tmux server starts, which then inherits it), with a reader on it.
// It returns what has reached the FIFO so far.
func bridgeFifo(t *testing.T) func() string {
	t.Helper()
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	fifo := filepath.Join(data, "lea", "state", "clipboard", "copy")
	if err := os.MkdirAll(filepath.Dir(fifo), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 1)
	go func() {
		b, _ := os.ReadFile(fifo)
		got <- string(b)
	}()
	var read string
	done := false
	t.Cleanup(func() {
		// A writer opened and closed ends the reader's ReadFile.
		if f, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			f.Close()
		}
	})
	return func() string {
		time.Sleep(500 * time.Millisecond)
		if !done {
			select {
			case read = <-got:
				done = true
			default:
			}
		}
		return read
	}
}

// lea makes no selection of its own (#419): a drag in a pane whose program did not ask for the
// mouse, across the border and the deck and back, enters no copy mode, selects and copies
// nothing (no tmux buffer, nothing in the clipboard bridge's FIFO) and resizes nothing; a drag in
// the history, which the wheel entered, selects nothing either. Claude Code copies on select, its
// drags forwarded (TestRealDragInAMouseProgramReachesIt).
func TestRealDragInAPlainPaneSelectsNothing(t *testing.T) {
	fifo := bridgeFifo(t)
	tm, w, agent := dragWindow(t, false)
	widths := deckWidths(t, tm)
	// The agent's pane starts at column 36, the border is column 35.
	mouse(w, 0, 40, 20, false)
	mouse(w, 32, 45, 22, false)
	mouse(w, 32, 35, 25, false)
	mouse(w, 32, 30, 26, false)
	mouse(w, 32, 60, 30, false)
	mouse(w, 0, 60, 30, true)
	time.Sleep(300 * time.Millisecond)
	if got := tm("display-message", "-p", "-t", agent, "#{pane_in_mode}"); got != "0" {
		t.Error("the drag put the agent's pane into the history")
	}
	if got := widths(); got != "34 40" {
		t.Errorf("deck width and stored width %q after a drag over the border, want 34 40", got)
	}
	// In the history: the wheel's way in, then a drag.
	tm("copy-mode", "-H", "-t", agent)
	tm("send-keys", "-X", "-t", agent, "-N", "100", "scroll-up")
	mouse(w, 0, 100, 20, false)
	mouse(w, 32, 100, 30, false)
	mouse(w, 32, 100, 40, false)
	mouse(w, 0, 100, 40, true)
	time.Sleep(300 * time.Millisecond)
	if got := tm("display-message", "-p", "-t", agent, "#{pane_in_mode} #{selection_present}"); got != "1 0" {
		t.Errorf("mode and selection %q after a drag in the history, want 1 0 (still there, nothing selected)", got)
	}
	if got := tm("list-buffers"); got != "" {
		t.Errorf("tmux holds buffers %q after the drags", got)
	}
	if got := fifo(); got != "" {
		t.Errorf("tmux wrote %q into the clipboard bridge's FIFO", got)
	}
}

// deckWidths returns the deck's pane width and the stored @lea_deck_width of dragWindow's window,
// and sets the stored width apart from the pane's (40, the pane 34): a release that stored or
// snapped the width would show.
func deckWidths(t *testing.T, tm func(args ...string) string) func() string {
	t.Helper()
	tm("set-option", "-s", DeckWidthOption, "40")
	return func() string {
		return tm("display-message", "-p", "-t", "=lea-g:main.0", "#{pane_width}") + " " + tm("show-options", "-s", "-v", DeckWidthOption)
	}
}

// A drag that starts in the deck and crosses the border leaves the deck's width and the stored
// width alone (#341), released in the deck or on the border: tmux names a drag's first motion by
// the press's cell, every later one by the cell under the pointer, and a drag forwarded to the
// deck (send-keys -M) never takes the drag over. A press on the border then still resizes, and
// its release stores the width.
func TestRealDragFromTheDeckOverTheBorderDoesNotResize(t *testing.T) {
	tm, w, _ := dragWindow(t, false)
	widths := deckWidths(t, tm)
	// The deck spans columns 1 to 34, the border is column 35.
	mouse(w, 0, 20, 20, false)
	mouse(w, 32, 25, 20, false)
	mouse(w, 32, 35, 20, false)
	mouse(w, 32, 28, 20, false)
	mouse(w, 0, 28, 20, true)
	time.Sleep(300 * time.Millisecond)
	if got := widths(); got != "34 40" {
		t.Errorf("deck width and stored width %q after a deck drag over the border, want 34 40", got)
	}
	mouse(w, 0, 20, 20, false)
	mouse(w, 32, 25, 20, false)
	mouse(w, 32, 35, 22, false)
	mouse(w, 0, 35, 22, true)
	time.Sleep(300 * time.Millisecond)
	if got := widths(); got != "34 40" {
		t.Errorf("deck width and stored width %q after a deck drag released on the border, want 34 40", got)
	}
	if v := tm("show-options", "-s", "-v", "-q", BorderPressOption); v != "" {
		t.Errorf("%s = %q after the release", BorderPressOption, v)
	}
	mouse(w, 0, 35, 20, false)
	mouse(w, 32, 50, 20, false)
	mouse(w, 0, 50, 20, true)
	waitFor(t, "the border drag to resize and store the width", func() bool { return widths() == "49 49" })
	if v := tm("show-options", "-s", "-v", "-q", BorderPressOption); v != "" {
		t.Errorf("%s = %q after the border drag's release", BorderPressOption, v)
	}
}

// A program that asked for the mouse keeps its drag when it crosses the border into the deck and
// back: it gets the press, the motions inside its pane and the release, and the deck's width and
// the stored width stay (#341).
func TestRealDragInAMouseProgramOverTheBorderDoesNotResize(t *testing.T) {
	tm, w, agent := dragWindow(t, false)
	widths := deckWidths(t, tm)
	got := filepath.Join(os.Getenv("TMUX_TMPDIR"), "mouse")
	tm("respawn-pane", "-k", "-t", agent, "sh", "-c", `printf '\033[?1002h\033[?1006h'; stty raw -echo; exec cat > "$0"`, got)
	waitFor(t, "the program to ask for the mouse", func() bool {
		return tm("display-message", "-p", "-t", agent, "#{mouse_any_flag}") == "1"
	})
	// The agent's pane starts at column 36: column 40 is its fifth.
	mouse(w, 0, 40, 20, false)
	mouse(w, 32, 42, 20, false)
	mouse(w, 32, 35, 20, false)
	mouse(w, 32, 30, 20, false)
	mouse(w, 32, 45, 20, false)
	mouse(w, 0, 45, 20, true)
	var b []byte
	waitFor(t, "the release to reach the program", func() bool {
		b, _ = os.ReadFile(got)
		return strings.Contains(string(b), "\x1b[<0;10;19m")
	})
	for _, want := range []string{"\x1b[<0;5;19M", "\x1b[<32;7;19M", "\x1b[<32;10;19M"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the program got %q, want %q in it", b, want)
		}
	}
	if got := widths(); got != "34 40" {
		t.Errorf("deck width and stored width %q after a program's drag over the border, want 34 40", got)
	}
}
