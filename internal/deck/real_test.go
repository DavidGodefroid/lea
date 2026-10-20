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
	"unicode/utf8"

	"github.com/DavidGodefroid/lea/internal/tmux"
)

// The deck end to end: the test binary itself draws the deck (deckHelperEnv) in a pane of a real
// tmux server with lea's configuration, a client is attached through a pseudo-terminal (script),
// and mouse events are typed into that terminal as a real one sends them. make go-test-tmux runs
// these tests; they are skipped without tmux or script.

const deckHelperEnv = "LEA_DECK_E2E_HELPER"

// deckLayoutEnv is the file where the helper's deck keeps its folders.
const deckLayoutEnv = "LEA_DECK_E2E_LAYOUT"

// e2eLabel is c's label, longer than a row shows.
const e2eLabel = "refactor-billing-export"

func TestMain(m *testing.M) {
	if mode := os.Getenv(deckHelperEnv); mode != "" {
		c := ag("c", "idle", 1)
		c.Label = e2eLabel
		agents := []Agent{ag("a", "idle", 1), ag("b", "idle", 1), c}
		if mode == "colours" { // issue #72: done, waiting and blocked, all just idle
			now := time.Now().Unix()
			agents = []Agent{ag("green", "idle", now), withTask(ag("wait", "idle", now), true, false),
				withTask(ag("red", "idle", now), true, true)}
		}
		if err := Run(e2eAPI{&fakeAPI{snap: Snapshot{Version: 1, Shown: agents[0].Key,
			Agents: agents}}}, "lea-a", false, nil, nil, "e2e", FileStore{Path: os.Getenv(deckLayoutEnv)}); err != nil {
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

// realDeck is the deck in a real tmux, beside an agent's pane, with a client attached.
type realDeck struct {
	t    *testing.T
	tm   func(args ...string) string
	in   io.Writer // the client's terminal: what the user types and does with the mouse
	pane string    // the deck's pane
	top  int       // the deck pane's first line on the client's screen
	cmd  []string  // the deck's command
	// layout is the file of the deck's folders.
	layout string
}

func startRealDeck(t *testing.T) *realDeck { return startRealDeckMode(t, "1") }

// startRealDeckMode draws the deck the helper's mode names (TestMain).
func startRealDeckMode(t *testing.T, mode string) *realDeck {
	t.Helper()
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
	layout := filepath.Join(dir, "host", "deck", "lea-a.json")
	cmd := []string{"env", deckHelperEnv + "=" + mode, deckLayoutEnv + "=" + layout, self}
	tm(append([]string{"new-session", "-d", "-s", "e2e", "-x", "80", "-y", "24"}, cmd...)...)
	d := &realDeck{t: t, tm: tm, pane: tm("display-message", "-p", "-t", "e2e:0.0", "#{pane_id}"), cmd: cmd, layout: layout}
	tm("set-option", "-p", "-t", d.pane, "@lea_role", "deck")
	tm("split-window", "-h", "-t", d.pane, "sleep", "600")
	tm("resize-pane", "-t", d.pane, "-x", strconv.Itoa(Width))

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
	d.in = in
	d.waitFor("the deck", func(s string) bool { return strings.Contains(s, "3 agents") })
	d.waitFor("the client", func(string) bool { return tm("list-clients", "-F", "#{client_name}") != "" })
	d.top, _ = strconv.Atoi(tm("display-message", "-p", "-t", d.pane, "#{pane_top}"))
	return d
}

func (d *realDeck) screen() string { return d.tm("capture-pane", "-p", "-t", d.pane) }

func (d *realDeck) waitFor(what string, ok func(string) bool) {
	d.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		s := d.screen()
		if ok(s) {
			return
		}
		if time.Now().After(deadline) {
			d.t.Fatalf("timed out waiting for %s:\n%s", what, s)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// typed sends s through the client's terminal.
func (d *realDeck) typed(s string) {
	d.t.Helper()
	if _, err := io.WriteString(d.in, s); err != nil {
		d.t.Fatal(err)
	}
}

// keys types each key on its own, as a user does: keys sent at once reach the deck as one.
func (d *realDeck) keys(ks ...string) {
	d.t.Helper()
	for _, k := range ks {
		d.typed(k)
		time.Sleep(100 * time.Millisecond)
	}
}

// mouse sends an SGR mouse event at line y of the deck's pane (1-based cells of the client's
// screen), as the terminal sends it to tmux.
func (d *realDeck) mouse(code, y int, final byte) {
	d.t.Helper()
	d.typed(fmt.Sprintf("\x1b[<%d;6;%d%c", code, d.top+y+1, final))
}

// Every right click in the deck opens a menu (#44): over a row its menu, anywhere else in the
// column (the frame, the empty space below the rows) the deck's own, also while another pane has
// the keyboard.
func TestRealEveryRightClickOpensAMenu(t *testing.T) {
	d := startRealDeck(t)
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
		d.tm("select-pane", "-t", "e2e:0.1") // the agent's pane has the keyboard
		d.mouse(2, c.y, 'M')
		time.Sleep(20 * time.Millisecond)
		d.mouse(2, c.y, 'm')
		// esc then goes to the deck: the right click gave it the keyboard.
		d.waitFor(fmt.Sprintf("a menu with %q after a right click on line %d", c.want, c.y), func(s string) bool {
			return strings.Contains(s, c.want) && strings.Contains(s, "esc close")
		})
		d.typed("\x1b") // esc, typed as the user does
		d.waitFor(fmt.Sprintf("the menu to close after a right click on line %d", c.y), closed)
	}
}

// The message items explain themselves (#28): in a row's menu, opened by a right click, the
// highlighted Send message and Messages each show what they do under the items.
func TestRealTheMessageItemsExplainThemselves(t *testing.T) {
	d := startRealDeck(t)
	d.tm("select-pane", "-t", "e2e:0.1") // the agent's pane has the keyboard
	d.mouse(2, 3, 'M')                   // b's first line
	time.Sleep(20 * time.Millisecond)
	d.mouse(2, 3, 'm')
	d.waitFor("b's menu", func(s string) bool { return strings.Contains(s, "Send message") && strings.Contains(s, "esc close") })
	if s := d.screen(); strings.Contains(s, "inbox") {
		t.Fatalf("a hint under Show:\n%s", s)
	}
	d.typed("j")
	d.waitFor("Send message's hint", func(s string) bool { return strings.Contains(s, "a note to its inbox") })
	d.typed("j")
	d.waitFor("Messages' hint", func(s string) bool {
		return strings.Contains(s, "questions and") && !strings.Contains(s, "inbox")
	})
	d.typed("\x1b")
	d.waitFor("the menu to close", func(s string) bool { return !strings.Contains(s, "esc close") })
}

// The pointer over a row shows its whole name in the bottom border (#29): tmux passes the moves
// of no button (SGR 35) on to the deck, which asks for all motion.
func TestRealHoverShowsTheFullName(t *testing.T) {
	d := startRealDeck(t)
	if s := d.screen(); strings.Contains(s, e2eLabel) {
		t.Fatalf("the row already shows the whole label:\n%s", s)
	}
	d.tm("select-pane", "-t", "e2e:0.1") // the pointer, not the keyboard
	d.mouse(35, 5, 'M')                  // c's first line
	d.waitFor("the whole label in the bottom border", func(s string) bool { return strings.Contains(s, e2eLabel+" ·") })
	d.mouse(35, 12, 'M') // below the rows
	d.waitFor("the key hints back", func(s string) bool { return strings.Contains(s, "? keys") })
}

// The state marks in a real terminal (#72): an idle agent with nothing pending is a green bullet,
// one whose task has a next step spins, one whose task names a blocker is a red bullet.
func TestRealStateColours(t *testing.T) {
	d := startRealDeckMode(t, "colours")
	glyphs := "●" + strings.Join(spinner, "")
	mark := func(s, name string) string { // a row's state glyph and the SGR that colours it
		for _, l := range strings.Split(s, "\n") {
			if !strings.Contains(l, name+" ") {
				continue
			}
			if i := strings.IndexAny(l, glyphs); i >= 0 {
				if j := strings.LastIndex(l[:i], "\x1b["); j >= 0 {
					_, n := utf8.DecodeRuneInString(l[i:])
					return l[j : i+n]
				}
			}
		}
		return ""
	}
	var green, wait, red string
	d.waitFor("the three marks", func(string) bool {
		s := d.tm("capture-pane", "-p", "-e", "-t", d.pane)
		green, wait, red = mark(s, "green"), mark(s, "wait"), mark(s, "red")
		return strings.Contains(green, "●") && strings.Contains(red, "●") && strings.ContainsAny(wait, strings.Join(spinner, ""))
	})
	sgr := func(m string) string { return m[:strings.IndexByte(m, 'm')+1] }
	if sgr(green) == sgr(red) || sgr(wait) == sgr(green) {
		t.Fatalf("one colour for two states: green %q wait %q red %q", green, wait, red)
	}
	time.Sleep(300 * time.Millisecond)
	if next := mark(d.tm("capture-pane", "-p", "-e", "-t", d.pane), "wait"); next == wait {
		t.Fatalf("the waiting spinner does not turn: %q", next)
	}
}

// Folders (#30), typed and clicked as the user does: f makes one and names it, o moves an agent
// in, enter folds it, ctrl + right click (SGR 18) makes another around the agent under the
// pointer; the layout is in its file and the deck, started again, shows it as it was.
func TestRealFoldersPersistAcrossRestarts(t *testing.T) {
	d := startRealDeck(t)
	d.tm("select-pane", "-t", d.pane) // the deck has the keyboard
	d.typed("f\x15web\r")             // f, ctrl+u, the name, enter
	d.waitFor("the folder web", func(s string) bool { return strings.Contains(s, "▾ web") && !strings.Contains(s, "folder:") })
	d.keys("j", "o", "1") // a, into the first folder
	d.waitFor("a in web", func(s string) bool {
		return strings.Contains(s, "╎") && strings.Contains(s, "web                      1")
	})
	d.keys("k", "\r") // the folder's row, folded
	d.waitFor("web folded", func(s string) bool { return strings.Contains(s, "▸ web") })
	// web is line 1, b's row lines 2-3: a ctrl + right click on b.
	time.Sleep(400 * time.Millisecond)
	d.mouse(18, 2, 'M')
	time.Sleep(20 * time.Millisecond)
	d.mouse(18, 2, 'm')
	d.waitFor("the folder field", func(s string) bool { return strings.Contains(s, "folder: folder 2") })
	d.typed("\r")
	d.waitFor("folder 2", func(s string) bool { return strings.Contains(s, "▾ folder 2") })

	b, err := os.ReadFile(d.layout)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{`"name": "web"`, `"folded": true`, `"lea-a/a"`, `"name": "folder 2"`, `"lea-a/b"`} {
		if !strings.Contains(string(b), w) {
			t.Fatalf("layout lacks %s:\n%s", w, b)
		}
	}
	d.tm(append([]string{"respawn-pane", "-k", "-t", d.pane}, d.cmd...)...)
	d.waitFor("the folders after a restart", func(s string) bool {
		return strings.Contains(s, "▸ web") && strings.Contains(s, "▾ folder 2") && strings.Contains(s, "3 agents")
	})
}
