package deck

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/tmux"
)

// The deck end to end: the test binary itself draws the deck (deckHelperEnv) in a pane of a real
// tmux server with lea's configuration, a client is attached through a pseudo-terminal (script),
// and mouse events are typed into that terminal as a real one sends them. make go-test-tmux runs
// it; it is skipped without tmux or script.

const deckHelperEnv = "LEA_DECK_E2E_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(deckHelperEnv) == "1" {
		if err := Run(e2eAPI{&fakeAPI{snap: Snapshot{Version: 1, Shown: "lea-a/a",
			Agents: []Agent{ag("a", "idle", 1), ag("b", "idle", 1), ag("c", "idle", 1)}}}}, "lea-a", false, nil); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// e2eAPI answers the first watch at once, then holds each one as the daemon does.
type e2eAPI struct{ *fakeAPI }

func (a e2eAPI) Watch(ctx context.Context, c string, since int64) (Snapshot, error) {
	if since > 0 {
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
		}
	}
	return a.fakeAPI.Watch(ctx, c, since)
}

// Every right click in the deck opens a menu (#44): over a row its menu, anywhere else in the
// column (the frame, the empty space below the rows) the deck's own, also while another pane has
// the keyboard.
func TestRealEveryRightClickOpensAMenu(t *testing.T) {
	for _, bin := range []string{"tmux", "script"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not installed: make go-test-tmux runs this test", bin)
		}
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Setenv("TMUX_TMPDIR", dir)
	t.Setenv("TERM", "xterm-256color")
	conf := filepath.Join(dir, "tmux.conf")
	if err := tmux.WriteConf(conf); err != nil {
		t.Fatal(err)
	}
	tm := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-L", "lea-deck-e2e", "-f", conf}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	t.Cleanup(func() { exec.Command("tmux", "-L", "lea-deck-e2e", "kill-server").Run() })
	tm("new-session", "-d", "-s", "e2e", "-x", "80", "-y", "24", "env", deckHelperEnv+"=1", self)
	deck := tm("display-message", "-p", "-t", "e2e:0.0", "#{pane_id}")
	tm("set-option", "-p", "-t", deck, "@lea_role", "deck")
	tm("split-window", "-h", "-t", deck, "sleep", "600")
	tm("resize-pane", "-t", deck, "-x", strconv.Itoa(Width))

	client := exec.Command("script", "-qfc", "tmux -L lea-deck-e2e attach -t e2e", "/dev/null")
	in, err := client.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	client.Stdout, client.Stderr = io.Discard, io.Discard
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { in.Close(); client.Process.Kill(); client.Wait() })

	screen := func() string { return tm("capture-pane", "-p", "-t", deck) }
	waitFor := func(what string, ok func(string) bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			s := screen()
			if ok(s) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s:\n%s", what, s)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	waitFor("the deck", func(s string) bool { return strings.Contains(s, "3 agents") })
	waitFor("the client", func(string) bool { return tm("list-clients", "-F", "#{client_name}") != "" })

	// rightClick presses and releases the right button over line y of the deck's pane, as the
	// terminal sends it to tmux (SGR, 1-based cells of the client's screen).
	top, _ := strconv.Atoi(tm("display-message", "-p", "-t", deck, "#{pane_top}"))
	rightClick := func(y int) {
		t.Helper()
		row := top + y + 1
		if _, err := fmt.Fprintf(in, "\x1b[<2;6;%dM", row); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
		if _, err := fmt.Fprintf(in, "\x1b[<2;6;%dm", row); err != nil {
			t.Fatal(err)
		}
	}
	closed := func(s string) bool { return !strings.Contains(s, "esc close") }
	for _, c := range []struct {
		y    int
		want string
	}{
		{3, "Rename"},     // b's first line: its menu
		{2, "Rename"},     // a's second line
		{12, "New agent"}, // the empty column below the rows
		{0, "New agent"},  // the deck's top frame
		{20, "New agent"}, // near the bottom
	} {
		// Past tmux's double-click-time (300 ms): a second press sooner is a SecondClick3Pane,
		// which tmux hands to the pane without the binding's select-pane.
		time.Sleep(400 * time.Millisecond)
		tm("select-pane", "-t", "e2e:0.1") // the agent's pane has the keyboard
		rightClick(c.y)
		// esc then goes to the deck: the right click gave it the keyboard.
		waitFor(fmt.Sprintf("a menu with %q after a right click on line %d", c.want, c.y), func(s string) bool {
			return strings.Contains(s, c.want) && strings.Contains(s, "esc close")
		})
		if _, err := io.WriteString(in, "\x1b"); err != nil { // esc, typed as the user does
			t.Fatal(err)
		}
		waitFor(fmt.Sprintf("the menu to close after a right click on line %d", c.y), closed)
	}
}
