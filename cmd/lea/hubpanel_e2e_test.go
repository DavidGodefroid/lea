package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/agents"
	"github.com/DavidGodefroid/lea/internal/daemon"
	"github.com/DavidGodefroid/lea/internal/store"
	"github.com/DavidGodefroid/lea/internal/tmux"
)

// The hub panel end to end (#284, #289): the lea binary's deck and ticket pane in a real tmux
// server with lea's configuration, driven by the real daemon and its agents manager, a test hub
// whose tickets the test may approve, and a client attached through a pseudo-terminal into which
// keys and mouse events are typed as a terminal sends them. make go-test-tmux runs it.

// e2eHub is a hub facade with its own tickets: Queue long-polls as lockhub's does, Decide
// records the answer and drops the ticket.
type e2eHub struct {
	mu      sync.Mutex
	queue   []daemon.HubTicket
	version uint64
	changed chan struct{}
	decided []string // instance/id:approved|denied
}

func newE2EHub(ts ...daemon.HubTicket) *e2eHub {
	return &e2eHub{queue: ts, version: 1, changed: make(chan struct{})}
}

func (h *e2eHub) Queue(ctx context.Context, since uint64, wait int) ([]daemon.HubTicket, uint64, error) {
	t := time.NewTimer(time.Duration(wait) * time.Second)
	defer t.Stop()
	for {
		h.mu.Lock()
		q, v, ch := append([]daemon.HubTicket(nil), h.queue...), h.version, h.changed
		h.mu.Unlock()
		if v != since {
			return q, v, nil
		}
		select {
		case <-ch:
		case <-t.C:
			return q, v, nil
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		}
	}
}

func (h *e2eHub) Decide(ctx context.Context, instance, id string, v daemon.HubVerdict) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	verdict := "denied"
	if v.Approved {
		verdict = "approved"
	}
	h.decided = append(h.decided, instance+"/"+id+":"+verdict)
	for i, t := range h.queue {
		if t.Instance == instance && t.ID == id {
			h.queue = append(h.queue[:i:i], h.queue[i+1:]...)
			break
		}
	}
	h.version++
	close(h.changed)
	h.changed = make(chan struct{})
	return nil
}

func (h *e2eHub) answers() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.decided...)
}

func (h *e2eHub) Decided(ctx context.Context, since int64) ([]daemon.HubDecided, int64, error) {
	return nil, since, nil
}
func (h *e2eHub) TicketState(ctx context.Context, instance, ticket string) (string, error) {
	return "pending", nil
}
func (h *e2eHub) Reveal(ctx context.Context, instance, id string, key []byte) ([]byte, error) {
	return nil, fmt.Errorf("no reveal in this test")
}
func (h *e2eHub) Renew(ctx context.Context, instance, id string) (daemon.HubRenewed, error) {
	return daemon.HubRenewed{}, fmt.Errorf("no renewal in this test")
}

// e2eContainers is a container with no agent and no shell: the deck runs beside the placeholder.
type e2eContainers struct{}

func (e2eContainers) Live(context.Context, string) ([]string, error)              { return nil, nil }
func (e2eContainers) Stop(context.Context, string, string) error                  { return nil }
func (e2eContainers) Projects(context.Context, string) (string, error)            { return "/tmp", nil }
func (e2eContainers) Shells(context.Context, string) ([]string, error)            { return nil, nil }
func (e2eContainers) StopShell(context.Context, string, string) error             { return nil }
func (e2eContainers) Pause(context.Context, string, string, bool) error           { return nil }
func (e2eContainers) Teardown(context.Context, string, string) error              { return nil }
func (e2eContainers) Park(context.Context, string, string) (string, error)        { return "", nil }
func (e2eContainers) Unpark(context.Context, string, string) (string, error)      { return "", nil }
func (e2eContainers) Sweep(context.Context, string, []string) (string, error)     { return "", nil }
func (e2eContainers) HasTranscript(context.Context, string, string) (bool, error) { return true, nil }

// panelE2E is lea's tmux session for container lea-a, a client attached to it.
type panelE2E struct {
	t   *testing.T
	tm  func(args ...string) string
	in  io.Writer
	hub *e2eHub
}

const panelE2ESock = "lea-e2e-panel"

func startPanelE2E(t *testing.T, ts ...daemon.HubTicket) *panelE2E {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the binary")
	}
	for _, bin := range []string{"tmux", "script"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not installed: make go-test-tmux runs this test", bin)
		}
	}
	dir, err := os.MkdirTemp("", "lp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	bin := filepath.Join(dir, "lea")
	if out, err := exec.Command("go", "build", "-buildvcs=false", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	sock := filepath.Join(dir, "d.sock")
	// The panes tmux starts inherit these: the deck and the ticket pane reach this daemon.
	t.Setenv("TMUX_TMPDIR", dir)
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("LEA_DAEMON_SOCK", sock)
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	conf := filepath.Join(dir, "tmux.conf")
	if err := tmux.WriteConf(conf); err != nil {
		t.Fatal(err)
	}
	tm := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-L", panelE2ESock, "-f", conf}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	t.Cleanup(func() { exec.Command("tmux", "-L", panelE2ESock, "kill-server").Run() })

	st, err := store.Open(filepath.Join(dir, "lea.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d := daemon.New(st, filepath.Join(dir, "run"), func() int64 { return time.Now().Unix() })
	l, err := daemon.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	go d.Serve(ctx, l, daemon.Origin{})
	if _, err := d.AttachContainer(ctx, "lea-a"); err != nil {
		t.Fatal(err)
	}
	hub := newE2EHub(ts...)
	d.SetHubFacade(hub)
	go d.RunHubQueue(ctx)
	m := agents.NewManager(agents.Config{Store: st, Term: agents.Tmux(tmux.Server{Socket: panelE2ESock, Conf: conf}),
		Containers: e2eContainers{}, LeaBin: bin, UID: os.Getuid(), GID: os.Getgid()})
	d.SetAgents(m)
	if err := m.Ensure(ctx, "lea-a", ""); err != nil {
		t.Fatal(err)
	}
	client := exec.Command("script", "-qfc", "stty rows 30 cols 120; tmux -L "+panelE2ESock+" attach -t =lea-a:deck", "/dev/null")
	in, err := client.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	client.Stdout, client.Stderr = io.Discard, io.Discard
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { in.Close(); client.Process.Kill(); client.Wait() })
	e := &panelE2E{t: t, tm: tm, in: in, hub: hub}
	e.waitFor("the client", func() bool { return tm("list-clients", "-F", "#{client_name}") != "" })
	e.waitFor("the hub panel in the deck", func() bool { return strings.Contains(e.screen(roleOf("deck")), "pending") })
	return e
}

// roleOf names a pane of the deck's window by its role.
type roleOf string

// pane is the id, left column and top line (0-based, on the client's screen) of the pane of the
// deck's window that has role r.
func (e *panelE2E) pane(r roleOf) (id string, left, top int) {
	e.t.Helper()
	for _, l := range strings.Split(e.tm("list-panes", "-t", "=lea-a:deck", "-F", "#{@lea_role}|#{pane_id}|#{pane_left}|#{pane_top}"), "\n") {
		f := strings.Split(l, "|")
		if f[0] == string(r) {
			left, _ = strconv.Atoi(f[2])
			top, _ = strconv.Atoi(f[3])
			return f[1], left, top
		}
	}
	return "", 0, 0
}

func (e *panelE2E) screen(r roleOf) string {
	e.t.Helper()
	id, _, _ := e.pane(r)
	if id == "" {
		return ""
	}
	return e.tm("capture-pane", "-p", "-t", id)
}

func (e *panelE2E) waitFor(what string, ok func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting for %s\ndeck:\n%s\nslot:\n%s\npanes:\n%s", what, e.screen(roleOf("deck")),
				e.screen(roleOf("ticket")), e.tm("list-panes", "-t", "=lea-a:deck", "-F", "#{@lea_role} #{pane_id} active=#{pane_active}"))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (e *panelE2E) typed(s string) {
	e.t.Helper()
	if _, err := io.WriteString(e.in, s); err != nil {
		e.t.Fatal(err)
	}
}

// click is a left click (press, release) at column x, line y of pane r (0-based in the pane), as
// the terminal sends it to tmux (SGR).
func (e *panelE2E) click(r roleOf, x, y int) { e.press(0, r, x, y) }

// press is a press and release of mouse button code (0 left, 2 right) at column x, line y of
// pane r.
func (e *panelE2E) press(code int, r roleOf, x, y int) {
	e.t.Helper()
	_, left, top := e.pane(r)
	time.Sleep(400 * time.Millisecond) // past tmux's double-click time: a second press is not a double click
	e.typed(fmt.Sprintf("\x1b[<%d;%d;%dM", code, left+x+1, top+y+1))
	time.Sleep(20 * time.Millisecond)
	e.typed(fmt.Sprintf("\x1b[<%d;%d;%dm", code, left+x+1, top+y+1))
}

// keyboardOnTicket puts the keyboard on the ticket's pane, as something other than lea's bindings may,
// and waits until tmux says so: the click that follows must be what moves it.
func (e *panelE2E) keyboardOnTicket() {
	e.t.Helper()
	id, _, _ := e.pane(roleOf("ticket"))
	e.tm("select-pane", "-t", id)
	e.waitFor("the keyboard on the ticket's pane", func() bool { return e.active() == "ticket" })
}

// lineOf is the line of pane r that shows s, -1 when none does.
func (e *panelE2E) lineOf(r roleOf, s string) int {
	for i, l := range strings.Split(e.screen(r), "\n") {
		if strings.Contains(l, s) {
			return i
		}
	}
	return -1
}

func (e *panelE2E) active() string {
	return e.tm("display-message", "-p", "-t", "=lea-a:deck", "#{@lea_role}")
}

func e2eTicket(id, lock string) daemon.HubTicket {
	now := time.Now().Unix()
	return daemon.HubTicket{ID: id, Instance: "crm", Lock: lock, Risk: "write", Created: now, Expires: now + 3600,
		Header: "UPDATE customers SET tier = 'gold'", Body: []string{"1 row"}}
}

// deckStatus is the deck's status line, above its bottom border.
func (e *panelE2E) deckStatus() string {
	ls := strings.Split(e.screen(roleOf("deck")), "\n")
	for i := len(ls) - 1; i > 0; i-- {
		if strings.Contains(ls[i], "╰") {
			return strings.Trim(ls[i-1], "│ ")
		}
	}
	return ""
}

// selectByClick clicks the line of ticket id's Lock in the hub panel and waits for the slot to
// show it, past the delay before a key acts on it.
func (e *panelE2E) selectByClick(id string) {
	e.t.Helper()
	y := e.lineOf(roleOf("deck"), "locksql crm")
	if y < 0 {
		e.t.Fatalf("no ticket line in the panel:\n%s", e.screen(roleOf("deck")))
	}
	e.click(roleOf("deck"), 5, y)
	e.waitForTicket(id)
}

// waitForTicket waits for the slot to show ticket id, then past the deck's ticketDelay (500 ms),
// before which a key does not act on it by design.
func (e *panelE2E) waitForTicket(id string) {
	e.t.Helper()
	e.waitFor("ticket "+id+" in the slot", func() bool { return strings.Contains(e.screen(roleOf("ticket")), "ticket: "+id) })
	time.Sleep(time.Second)
}

// TestRealPanelClickThenApprove is the reported path (#289): a click on a ticket's line in the hub
// panel shows it in the slot, then a approves it, with the keyboard on the deck.
func TestRealPanelClickThenApprove(t *testing.T) {
	e := startPanelE2E(t, e2eTicket("T1", "locksql"))
	e.selectByClick("T1")
	if got := e.active(); got != "deck" {
		t.Fatalf("the keyboard is on %q after the click in the panel", got)
	}
	e.typed("a")
	e.waitFor("the approval", func() bool { return len(e.hub.answers()) == 1 })
	if got := e.hub.answers(); got[0] != "crm/T1:approved" {
		t.Fatalf("hub answers %v", got)
	}
	e.waitFor("the status", func() bool { return e.deckStatus() == "approved" })
}

// The other reported path (#289): the ticket clicked in the panel, then a click on the ticket
// itself in the right pane, then a. The click took the keyboard to the ticket's pane, which
// answers nothing and said nothing: a was lost. Now a click (left or right) on the ticket gives
// the keyboard to the deck, even from the ticket's pane, ctrl+space never moves it there, and a
// and d act on the ticket shown.
func TestRealClickOnTheShownTicketThenApprove(t *testing.T) {
	e := startPanelE2E(t, e2eTicket("T1", "locksql"), e2eTicket("T2", "locksql"))
	e.selectByClick("T1")
	e.keyboardOnTicket()
	e.click(roleOf("ticket"), 20, 5)
	e.waitFor("a click on the ticket to give the keyboard to the deck", func() bool { return e.active() == "deck" })
	e.typed("a")
	e.waitFor("the approval", func() bool { return len(e.hub.answers()) == 1 })
	e.waitFor("the status", func() bool { return e.deckStatus() == "approved" })
	// The next ticket slides under the selection and shows: a right click on it.
	e.waitForTicket("T2")
	e.keyboardOnTicket()
	e.press(2, roleOf("ticket"), 20, 5)
	e.waitFor("a right click on the ticket to give the keyboard to the deck", func() bool { return e.active() == "deck" })
	// ctrl+space from the deck, as the terminal sends it, then d: tmux runs them in order, so d
	// reaches the deck only if ctrl+space left the keyboard there.
	e.typed("\x00")
	e.typed("d")
	e.waitFor("the denial", func() bool { return len(e.hub.answers()) == 2 })
	if got := e.hub.answers(); got[0] != "crm/T1:approved" || got[1] != "crm/T2:denied" {
		t.Fatalf("hub answers %v", got)
	}
	e.waitFor("the status", func() bool { return e.deckStatus() == "denied" })
	if got := e.active(); got != "deck" {
		t.Fatalf("the keyboard is on %q", got)
	}
}

// A production ticket that asks for its name (#289): a opens the field on the deck's bottom
// line, and the ticket on the right says it waits for the name; typed, enter approves it.
func TestRealProductionTicketAsksItsNameOnTheRight(t *testing.T) {
	tk := e2eTicket("T1", "locksql")
	tk.Production, tk.Retype = true, "crm"
	e := startPanelE2E(t, tk)
	e.selectByClick("T1")
	e.typed("a")
	e.waitFor("the slot's banner to say the name is awaited", func() bool {
		return strings.Contains(e.screen(roleOf("ticket")), `▌Not approved yet: type the instance name "crm" in the deck's bottom line`)
	})
	if !strings.Contains(e.deckStatus(), "type crm:") {
		t.Fatalf("no field in the deck: %q", e.deckStatus())
	}
	if got := e.hub.answers(); len(got) != 0 {
		t.Fatalf("decided before the name: %v", got)
	}
	e.typed("crm\r")
	e.waitFor("the approval", func() bool { return len(e.hub.answers()) == 1 })
	if got := e.hub.answers(); got[0] != "crm/T1:approved" {
		t.Fatalf("hub answers %v", got)
	}
}
