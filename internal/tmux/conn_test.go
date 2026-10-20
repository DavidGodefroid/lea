package tmux

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeTmux struct {
	in  *bufio.Scanner
	mu  sync.Mutex
	out io.WriteCloser
	n   int
}

func pipeConn(t *testing.T) (*Conn, *fakeTmux) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := NewConn(inW, outR)
	t.Cleanup(func() { outW.Close(); c.Close() })
	return c, &fakeTmux{in: bufio.NewScanner(inR), out: outW}
}

func (f *fakeTmux) line(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fmt.Fprintln(f.out, s)
}

func (f *fakeTmux) block(flags string, lines []string, isErr bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	fmt.Fprintf(f.out, "%%begin 1700000000 %d %s\n", f.n, flags)
	for _, l := range lines {
		fmt.Fprintln(f.out, l)
	}
	end := "%end"
	if isErr {
		end = "%error"
	}
	fmt.Fprintf(f.out, "%s 1700000000 %d %s\n", end, f.n, flags)
}

// serve answers every command line it reads, in order, with a block of flags 1.
func (f *fakeTmux) serve(answer func(cmd string) ([]string, bool)) {
	go func() {
		for f.in.Scan() {
			lines, isErr := answer(f.in.Text())
			f.block("1", lines, isErr)
		}
	}()
}

func lastWord(cmd string) string {
	i := strings.LastIndex(cmd, " '")
	return strings.Trim(cmd[i+1:], "'")
}

func TestCommandQuotesEveryArgument(t *testing.T) {
	got, err := Command("set-option", "-t", "=lea-a:", "@lea_workdir", `/p/it's; $HOME #{pane_id} ~`, "")
	if err != nil {
		t.Fatal(err)
	}
	want := `set-option '-t' '=lea-a:' '@lea_workdir' '/p/it'\''s; $HOME #{pane_id} ~' ''`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestCommandRefusesControlCharactersAndBadNames(t *testing.T) {
	for _, a := range []string{"a\nkill-server", "tab\there", "esc\x1b", "del\x7f"} {
		if _, err := Command("display-message", a); err == nil {
			t.Fatalf("argument %q accepted", a)
		}
	}
	for _, n := range []string{"", "kill-server;", "Send", "x y"} {
		if _, err := Command(n); err == nil {
			t.Fatalf("command name %q accepted", n)
		}
	}
}

func TestRunSkipsBlocksThatAreNotOurs(t *testing.T) {
	c, f := pipeConn(t)
	f.block("0", nil, false) // the attach command's own block
	f.serve(func(cmd string) ([]string, bool) { return []string{"answer to " + cmd}, false })
	out, err := c.Run(context.Background(), "display-message", "-p", "x")
	if err != nil || len(out) != 1 || out[0] != "answer to display-message '-p' 'x'" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestRunReturnsTmuxErrors(t *testing.T) {
	c, f := pipeConn(t)
	f.serve(func(string) ([]string, bool) { return []string{"can't find pane: %9"}, true })
	_, err := c.Run(context.Background(), "kill-pane", "-t", "%9")
	if err == nil || !strings.Contains(err.Error(), "can't find pane: %9") {
		t.Fatalf("err=%v", err)
	}
}

func TestEventsComeOnlyFromOutsideBlocks(t *testing.T) {
	c, f := pipeConn(t)
	f.line("%window-close @7")
	f.serve(func(string) ([]string, bool) { return []string{"%3|@1|0|deck|"}, false })
	out, err := c.Run(context.Background(), "list-panes")
	if err != nil || len(out) != 1 || out[0] != "%3|@1|0|deck|" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	select {
	case ev := <-c.Events():
		if ev.Name != "%window-close" || len(ev.Args) != 1 || ev.Args[0] != "@7" {
			t.Fatalf("event=%+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event")
	}
	select {
	case ev := <-c.Events():
		t.Fatalf("output line taken for an event: %+v", ev)
	default:
	}
}

func TestConcurrentRunsGetTheirOwnAnswers(t *testing.T) {
	c, f := pipeConn(t)
	f.serve(func(cmd string) ([]string, bool) { return []string{lastWord(cmd)}, false })
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			want := fmt.Sprint(i)
			out, err := c.Run(context.Background(), "display-message", "-p", want)
			if err != nil || len(out) != 1 || out[0] != want {
				t.Errorf("run %d: out=%q err=%v", i, out, err)
			}
		}(i)
	}
	wg.Wait()
}

func TestEndOfStreamFailsPendingRunsAndClosesEvents(t *testing.T) {
	c, f := pipeConn(t)
	go func() { f.in.Scan(); f.out.Close() }() // read the command, never answer, then exit
	_, err := c.Run(context.Background(), "list-panes")
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("pending run: err=%v", err)
	}
	if _, ok := <-c.Events(); ok {
		t.Fatal("events not closed")
	}
	if _, err := c.Run(context.Background(), "list-panes"); !errors.Is(err, ErrClosed) {
		t.Fatalf("run after close: err=%v", err)
	}
}

func TestRunHonoursItsContext(t *testing.T) {
	c, f := pipeConn(t)
	go func() {
		for f.in.Scan() {
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.Run(ctx, "list-panes"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
}

func TestSocketPathAndItsLimit(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if got := SocketPath(env(nil), 1000, "lea"); got != "/tmp/tmux-1000/lea" {
		t.Fatal(got)
	}
	if got := SocketPath(env(map[string]string{"TMUX_TMPDIR": "/run/x"}), 1000, "lea"); got != "/run/x/tmux-1000/lea" {
		t.Fatal(got)
	}
	t.Setenv("TMUX_TMPDIR", "/"+strings.Repeat("d", 120))
	_, err := Server{Socket: "lea"}.Sessions(context.Background())
	if err == nil || !strings.Contains(err.Error(), "limit is 104") {
		t.Fatalf("err=%v", err)
	}
}

func TestWriteConf(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tmux.conf")
	if err := WriteConf(p); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil || string(b) != Conf() {
		t.Fatalf("content mismatch: %v", err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	for _, want := range []string{"set -g prefix None", "set -g remain-on-exit on", "set -g set-clipboard off",
		"bind-key -n C-Space", "if-shell -F '#{@lea_deck}' 'resize-pane -t :.0 -x " + strconv.Itoa(DeckWidth) + "'",
		"set -gqu remain-on-exit-format", "'#{!=:#{@lea_role},deck}' 'copy-mode -H -e'"} {
		if !strings.Contains(Conf(), want) {
			t.Fatalf("config lacks %q", want)
		}
	}
	// Every way into the history hides tmux's own position (it counted up from the bottom): the
	// slot's bar shows it instead.
	if n := strings.Count(Conf(), "'copy-mode "); n == 0 || n != strings.Count(Conf(), "'copy-mode -H ") {
		t.Fatalf("%d copy-mode commands, not all with -H", n)
	}
	if strings.Contains(Conf(), "@DECK") {
		t.Fatal("an unexpanded placeholder is left in the config")
	}
	// The deck window is found by its @lea_deck mark, never by its name: an agent's window may
	// carry any name.
	if strings.Contains(Conf(), ":deck") {
		t.Fatal("the config targets the deck window by name")
	}
	// The dead-pane line is the agents' own (set on their panes): no global copy of it.
	if strings.Contains(Conf(), "x in the deck removes it") {
		t.Fatal("the config sets the agents' dead-pane line for every pane")
	}
	link := filepath.Join(dir, "link.conf")
	os.Symlink(filepath.Join(dir, "elsewhere"), link)
	if err := WriteConf(link); err == nil {
		t.Fatal("wrote through a symlink")
	}
}

func TestCommandAllowListRefusesTypingAndUnknownCommands(t *testing.T) {
	for _, n := range []string{"send-keys", "send", "send-k", "send-prefix", "paste-buffer", "pasteb", "paste-b", "bogus-command"} {
		if _, err := Command(n, "-t", "%1", "x"); err == nil {
			t.Fatalf("%s accepted", n)
		}
	}
	c, _ := pipeConn(t)
	if _, err := c.Run(context.Background(), "send", "-t", "%1", "x"); err == nil {
		t.Fatal("Run accepted send")
	}
	for n := range allowed {
		if _, err := Command(n, "-t", "%1"); err != nil {
			t.Fatalf("%s refused: %v", n, err)
		}
	}
}

// The layout repair needs these; none of them types into a pane.
func TestCommandAllowListHasTheLayoutRepairCommands(t *testing.T) {
	for _, n := range []string{"break-pane", "select-layout"} {
		if _, err := Command(n, "-t", "%1"); err != nil {
			t.Fatalf("%s refused: %v", n, err)
		}
	}
}

// source-file is run by Open only, in a one-shot client, on lea's own configuration: the control
// connection never runs it, so no argument can make lea source a file it did not write.
func TestCommandAllowListRefusesSourceFile(t *testing.T) {
	for _, n := range []string{"source-file", "source"} {
		if _, err := Command(n, "/tmp/x.conf"); err == nil {
			t.Fatalf("%s accepted", n)
		}
	}
}

// A conf line an older tmux does not know fails source-file on a running server: Open warns and
// carries on, as tmux itself does when it reads the file at start-up.
func TestOpenCarriesOnWhenTheConfFailsOnARunningServer(t *testing.T) {
	t.Setenv("TMUX_TMPDIR", t.TempDir())
	bin, log := fakeBin(t, `case "$*" in
  *list-sessions*) echo old ;;
  *source-file*) echo '/c/tmux.conf:40: invalid option: extended-keys-format' >&2; exit 1 ;;
esac
exit 0`)
	c, err := Server{Bin: bin, Socket: "lea", Conf: "/c/tmux.conf"}.Open(context.Background(), "s", []string{"sleep", "1"})
	if err != nil {
		t.Fatalf("Open failed on a conf error: %v", err)
	}
	c.Close()
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		t.Fatal("fake tmux did not exit")
	}
	if b, _ := os.ReadFile(log); !strings.Contains(string(b), "source-file /c/tmux.conf") || !strings.Contains(string(b), "attach-session") {
		t.Fatalf("log:\n%s", b)
	}
}

func fakeBin(t *testing.T, script string) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	bin, log = filepath.Join(dir, "tmux"), filepath.Join(dir, "log")
	body := "#!/bin/sh\necho \"$@\" >> '" + log + "'\n" + script + "\n"
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func TestSessionsDistinguishesNoServerFromRealErrors(t *testing.T) {
	t.Setenv("TMUX_TMPDIR", t.TempDir())
	ctx := context.Background()
	for _, msg := range []string{"no server running on /tmp/tmux-1/lea",
		"error connecting to /tmp/tmux-1/lea (No such file or directory)"} {
		bin, _ := fakeBin(t, "echo '"+msg+"' >&2; exit 1")
		got, err := Server{Bin: bin, Socket: "lea"}.Sessions(ctx)
		if err != nil || got != nil {
			t.Fatalf("%q: got=%v err=%v", msg, got, err)
		}
	}
	bin, _ := fakeBin(t, "echo 'directory /tmp/tmux-1 has unsafe permissions' >&2; exit 1")
	_, err := Server{Bin: bin, Socket: "lea"}.Sessions(ctx)
	if err == nil || !strings.Contains(err.Error(), "unsafe permissions") {
		t.Fatalf("err=%v", err)
	}
	bin, _ = fakeBin(t, "echo a; echo b")
	if got, err := (Server{Bin: bin, Socket: "lea"}).Sessions(ctx); err != nil || len(got) != 2 {
		t.Fatalf("got=%v err=%v", got, err)
	}
}

func TestOpenRequiresAConfigFile(t *testing.T) {
	t.Setenv("TMUX_TMPDIR", t.TempDir())
	bin, log := fakeBin(t, "exit 0")
	if _, err := (Server{Bin: bin, Socket: "lea"}).Open(context.Background(), "s", []string{"sleep", "1"}); err == nil {
		t.Fatal("Open accepted an empty Conf")
	}
	if b, _ := os.ReadFile(log); len(b) != 0 {
		t.Fatalf("tmux was run:\n%s", b)
	}
	bin, log = fakeBin(t, `case "$*" in *has-session*) exit 1;; esac; exit 0`)
	c, err := Server{Bin: bin, Socket: "lea", Conf: "/c/tmux.conf"}.Open(context.Background(), "s", []string{"sleep", "1"})
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	// The fake process must be gone before TempDir cleanup removes its directory.
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		t.Fatal("fake tmux did not exit")
	}
	if b, _ := os.ReadFile(log); !strings.Contains(string(b), "-f /c/tmux.conf new-session") {
		t.Fatalf("log:\n%s", b)
	}
}

func TestShellLine(t *testing.T) {
	if got := ShellLine([]string{"/opt/my lea/lea", "deck", "--thread", "it's"}); got != `'/opt/my lea/lea' 'deck' '--thread' 'it'\''s'` {
		t.Fatalf("%s", got)
	}
}

func TestPopupRunsAOneShotClient(t *testing.T) {
	bin, log := fakeBin(t, "exit 0")
	s := Server{Bin: bin, Socket: "lea-test", Conf: "/unused"}
	if err := s.Popup(context.Background(), "/dev/pts/3", []string{"/opt/lea", "deck", "--thread", "lea-a/claude-1"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	want := `-L lea-test -f /unused display-popup -E -c /dev/pts/3 -w 90% -h 80% '/opt/lea' 'deck' '--thread' 'lea-a/claude-1'`
	if strings.TrimSpace(string(b)) != want {
		t.Fatalf("args=%q\nwant %q", strings.TrimSpace(string(b)), want)
	}
}

func TestPopupRequiresAConfigFile(t *testing.T) {
	bin, log := fakeBin(t, "exit 0")
	if err := (Server{Bin: bin, Socket: "lea-test"}).Popup(context.Background(), "/dev/pts/3", []string{"true"}); err == nil {
		t.Fatal("Popup accepted an empty Conf")
	}
	if b, _ := os.ReadFile(log); len(b) != 0 {
		t.Fatalf("tmux was run:\n%s", b)
	}
}

func TestPopupReportsAnImmediateFailureAndLeavesASlowOneRunning(t *testing.T) {
	bin, _ := fakeBin(t, "echo 'no client /dev/pts/9' >&2; exit 1")
	err := Server{Bin: bin, Socket: "lea-test", Conf: "/unused"}.Popup(context.Background(), "/dev/pts/9", []string{"true"})
	if err == nil || !strings.Contains(err.Error(), "no client") {
		t.Fatalf("err=%v", err)
	}
	slow, _ := fakeBin(t, "sleep 5")
	began := time.Now()
	if err := (Server{Bin: slow, Socket: "lea-test", Conf: "/unused"}).Popup(context.Background(), "/dev/pts/3", []string{"true"}); err != nil {
		t.Fatal(err)
	}
	if time.Since(began) > 2*time.Second {
		t.Fatal("Popup waited for the popup to close")
	}
}

func TestPopupRefusesControlCharactersAndFormats(t *testing.T) {
	bin, _ := fakeBin(t, "exit 0")
	s := Server{Bin: bin, Socket: "lea-test", Conf: "/unused"}
	for _, argv := range [][]string{{"lea", "a\nb"}, {"lea", "#{pane_id}"}} {
		if err := s.Popup(context.Background(), "/dev/pts/3", argv); err == nil {
			t.Fatalf("%q accepted", argv)
		}
	}
}

// Server.Client lives in server.go; its refusal test is here, with the other tests that name
// the typing commands (the only file TestNothingTypesIntoATerminal lets name them).
func TestClientRefusesCommandsOffTheList(t *testing.T) {
	s := Server{Bin: "/bin/false", Socket: "lea", Conf: "/h/tmux.conf"}
	for _, name := range []string{"send-keys", "send", "paste-buffer", "pasteb", "run-shell", "source-file"} {
		if err := s.Client(context.Background(), name, "-t", "x"); err == nil || !strings.Contains(err.Error(), "not one lea runs") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if err := (Server{Bin: "/bin/true", Socket: "lea"}).Client(context.Background(), "kill-server"); err == nil {
		t.Error("no configuration file: refused")
	}
}
