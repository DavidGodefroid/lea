package deck

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type fakeAPI struct {
	mu       sync.Mutex
	calls    []string
	snap     Snapshot
	err      error
	newAgent Agent
	newShell Agent
	// clear and renewed answer HubReveal and HubRenew.
	clear   HubClear
	renewed HubRenewed
}

func (f *fakeAPI) rec(s string) { f.mu.Lock(); f.calls = append(f.calls, s); f.mu.Unlock() }
func (f *fakeAPI) Watch(ctx context.Context, c string, since int64) (Snapshot, error) {
	f.rec(fmt.Sprintf("watch %s %d", c, since))
	return f.snap, f.err
}
func (f *fakeAPI) New(ctx context.Context, c, provider string) (Agent, error) {
	if provider != "" {
		c += " " + provider
	}
	f.rec("new " + c)
	return f.newAgent, f.err
}
func (f *fakeAPI) Providers(ctx context.Context) ([]string, error) {
	f.rec("providers")
	return []string{"claude", "codex", "ollama"}, f.err
}
func (f *fakeAPI) Show(ctx context.Context, k string, focus bool) error {
	f.rec(fmt.Sprintf("show %s %v", k, focus))
	return f.err
}
func (f *fakeAPI) Stop(ctx context.Context, k string) error { f.rec("stop " + k); return f.err }
func (f *fakeAPI) RemoveEnded(ctx context.Context, k string) error {
	f.rec("remove-ended " + k)
	return f.err
}
func (f *fakeAPI) Detach(ctx context.Context, c string) error { f.rec("detach " + c); return f.err }
func (f *fakeAPI) Popup(ctx context.Context, k string, compose bool) error {
	f.rec(fmt.Sprintf("popup %s %v", k, compose))
	return f.err
}
func (f *fakeAPI) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

var clock = time.Unix(10_000, 0)

func ag(name, state string, updated int64) Agent {
	return Agent{Key: "lea-a/" + name, Name: name, Profile: "claude", State: state, Workdir: "/home/u/p/shop", Updated: updated,
		CtxPct: -1, CostUSD: -1}
}

func newModel(api *fakeAPI) Model { return New(api, "lea-a", false, func() time.Time { return clock }) }

func step(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	nm, cmd := m.Update(msg)
	return nm.(Model), cmd
}

func key(k string) tea.KeyMsg {
	switch k {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

func loaded(t *testing.T, api *fakeAPI, shown string, as ...Agent) Model {
	t.Helper()
	m, _ := step(t, newModel(api), snapshotMsg{snap: Snapshot{Version: 7, Shown: shown, Agents: as}})
	return m
}

func names(m Model) string {
	var n []string
	for _, a := range m.agents {
		n = append(n, a.Name)
	}
	return strings.Join(n, ",")
}

func TestInitWatchesFromVersionZero(t *testing.T) {
	api := &fakeAPI{snap: Snapshot{Version: 3}}
	msg := newModel(api).Init()()
	if s, ok := msg.(snapshotMsg); !ok || s.snap.Version != 3 {
		t.Fatalf("msg=%#v", msg)
	}
	if got := api.got(); len(got) != 1 || got[0] != "watch lea-a 0" {
		t.Fatalf("calls=%q", got)
	}
}

func launched(a Agent, at int64) Agent { a.Created = at; return a }

func TestAgentsAreListedInLaunchOrder(t *testing.T) {
	api := &fakeAPI{}
	m := loaded(t, api, "", launched(ag("a", "idle", 1), 30), launched(ag("b", "working", 1), 10), launched(ag("c", "blocked", 1), 20))
	if names(m) != "b,c,a" {
		t.Fatalf("order %s", names(m))
	}
	// A row keeps its place when its state changes.
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Agents: []Agent{launched(ag("a", "blocked", 2), 30), launched(ag("b", "done", 2), 10), launched(ag("c", "idle", 2), 20)}}})
	if names(m) != "b,c,a" {
		t.Fatalf("order after state changes %s", names(m))
	}
}

func TestSelectionStartsOnTheShownAgentAndFollowsIt(t *testing.T) {
	api := &fakeAPI{}
	m := loaded(t, api, "lea-a/b", ag("a", "idle", 1), ag("b", "idle", 1), ag("c", "idle", 1))
	if m.selected != "lea-a/b" {
		t.Fatalf("selected %q", m.selected)
	}
	// b turns idle → blocked: it keeps its row, the selection stays on b.
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Shown: "lea-a/b", Agents: []Agent{ag("a", "idle", 1), ag("b", "blocked", 2), ag("c", "idle", 1)}}})
	if m.selected != "lea-a/b" || m.index(m.selected) != 1 {
		t.Fatalf("selected %q at %d", m.selected, m.index(m.selected))
	}
	// The selected agent disappears: the selection moves to the next row (a,b,c → c).
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 9, Shown: "", Agents: []Agent{ag("a", "idle", 1), ag("c", "idle", 1)}}})
	if m.selected != "lea-a/c" {
		t.Fatalf("selected %q", m.selected)
	}
}

func TestMovingShowsOnlyTheLastSelectionAfterADelay(t *testing.T) {
	api := &fakeAPI{}
	m := loaded(t, api, "lea-a/a", ag("a", "idle", 1), ag("b", "idle", 1), ag("c", "idle", 1))
	m, cmd1 := step(t, m, key("j"))
	m, cmd2 := step(t, m, key("down"))
	if cmd1 == nil || cmd2 == nil || m.selected != "lea-a/c" {
		t.Fatalf("selected %q", m.selected)
	}
	m, cmd := step(t, m, showMsg{seq: m.seq - 1}) // the first, superseded tick
	if cmd != nil {
		t.Fatal("a superseded selection was shown")
	}
	m, cmd = step(t, m, showMsg{seq: m.seq})
	if cmd == nil {
		t.Fatal("the last selection was not shown")
	}
	cmd()
	if got := api.got(); len(got) != 1 || got[0] != "show lea-a/c false" {
		t.Fatalf("calls=%q", got)
	}
	m, _ = step(t, m, key("k"))
	m, _ = step(t, m, key("up"))
	m, _ = step(t, m, key("up")) // already on the first row
	if m.selected != "lea-a/a" {
		t.Fatalf("selected %q", m.selected)
	}
}

func TestEnterFocusesAndDropsThePendingShow(t *testing.T) {
	api := &fakeAPI{}
	m := loaded(t, api, "lea-a/a", ag("a", "idle", 1), ag("b", "idle", 1))
	m, _ = step(t, m, key("j"))
	pending := m.seq
	m, cmd := step(t, m, key("enter"))
	cmd()
	if got := api.got(); len(got) != 1 || got[0] != "show lea-a/b true" {
		t.Fatalf("calls=%q", got)
	}
	if _, cmd := step(t, m, showMsg{seq: pending}); cmd != nil {
		t.Fatal("the delayed show fired after enter")
	}
}

func TestDigitsJumpToARow(t *testing.T) {
	api := &fakeAPI{}
	m := loaded(t, api, "", ag("a", "idle", 1), ag("b", "idle", 1), ag("c", "idle", 1))
	m, _ = step(t, m, key("3"))
	if m.selected != "lea-a/c" {
		t.Fatalf("selected %q", m.selected)
	}
	m, cmd := step(t, m, key("9"))
	if m.selected != "lea-a/c" || cmd != nil {
		t.Fatal("a row past the end was selected")
	}
}

func TestNewSelectsTheNewAgent(t *testing.T) {
	api := &fakeAPI{newAgent: ag("claude-2", "unknown", 1)}
	m := loaded(t, api, "", ag("a", "idle", 1))
	m, cmd := step(t, m, key("n"))
	msg := cmd()
	m, _ = step(t, m, msg)
	if m.selected != "lea-a/claude-2" || m.status != "" {
		t.Fatalf("selected %q status %q", m.selected, m.status)
	}
	if got := api.got(); got[0] != "new lea-a" {
		t.Fatalf("calls=%q", got)
	}
}

// N lists the providers, then a digit starts an agent on one; any other key cancels.
func TestNewOnAnotherProvider(t *testing.T) {
	api := &fakeAPI{newAgent: ag("codex-1", "unknown", 1)}
	m := loaded(t, api, "", ag("a", "idle", 1))
	m, cmd := step(t, m, key("N"))
	m, _ = step(t, m, cmd())
	if m.status != "new agent on: 1 claude 2 codex 3 ollama" {
		t.Fatalf("status %q", m.status)
	}
	m, cmd = step(t, m, key("2"))
	m, _ = step(t, m, cmd())
	if m.selected != "lea-a/codex-1" || m.providers != nil {
		t.Fatalf("selected %q providers %v", m.selected, m.providers)
	}
	if got := api.got(); len(got) != 2 || got[1] != "new lea-a codex" {
		t.Fatalf("calls=%q", got)
	}
	m, cmd = step(t, m, key("N"))
	m, _ = step(t, m, cmd())
	m, cmd = step(t, m, key("9")) // past the list: cancels
	if cmd != nil || m.providers != nil || m.status != "" {
		t.Fatalf("cancel: cmd %v providers %v status %q", cmd != nil, m.providers, m.status)
	}
	if got := api.got(); len(got) != 3 {
		t.Fatalf("calls=%q", got)
	}
}

func TestStopNeedsConfirmation(t *testing.T) {
	api := &fakeAPI{}
	m := loaded(t, api, "lea-a/b", ag("a", "idle", 1), ag("b", "idle", 1))
	m, cmd := step(t, m, key("x"))
	if cmd != nil || m.confirm != "lea-a/b" {
		t.Fatalf("confirm %q", m.confirm)
	}
	m, cmd = step(t, m, key("n")) // n cancels, and does nothing else
	if cmd != nil || m.confirm != "" || len(api.got()) != 0 {
		t.Fatalf("cancel: confirm %q calls %q", m.confirm, api.got())
	}
	m, _ = step(t, m, key("x"))
	m, cmd = step(t, m, key("y"))
	cmd()
	if got := api.got(); len(got) != 1 || got[0] != "stop lea-a/b" {
		t.Fatalf("calls=%q", got)
	}
}

// An ended agent has nothing left to stop: x removes it at once, with no y step. The daemon
// removes it only while it is still ended: a lead may have started a new agent of that name in
// the meantime, which no x without y may stop.
func TestXRemovesAnEndedAgentAtOnce(t *testing.T) {
	api := &fakeAPI{}
	m := loaded(t, api, "lea-a/b", ag("a", "idle", 1), ag("b", "done", 1))
	m, cmd := step(t, m, key("x"))
	if cmd == nil || m.confirm != "" {
		t.Fatalf("confirm %q status %q: an ended agent needs no confirmation", m.confirm, m.status)
	}
	cmd()
	if got := api.got(); len(got) != 1 || got[0] != "remove-ended lea-a/b" {
		t.Fatalf("calls=%q", got)
	}
}

// The row x removed was in the slot: the placeholder took its place, and the row now selected
// follows into the slot, as a move would.
func TestAfterXTheSlotShowsTheNewlySelectedRow(t *testing.T) {
	api := &fakeAPI{}
	m := loaded(t, api, "lea-a/b", ag("a", "idle", 1), ag("b", "done", 1))
	m, _ = step(t, m, key("x"))
	seq := m.seq
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Shown: "", Agents: []Agent{ag("a", "idle", 1)}}})
	if m.selected != "lea-a/a" || m.seq == seq {
		t.Fatalf("selected %q, seq %d: no show pending", m.selected, m.seq)
	}
	m, cmd := step(t, m, showMsg{seq: m.seq})
	if cmd == nil {
		t.Fatal("the new selection was not shown")
	}
	cmd()
	if got := api.got(); got[len(got)-1] != "show lea-a/a false" {
		t.Fatalf("calls=%q", got)
	}
	// Another agent in the slot stays there when the selected row goes.
	m = loaded(t, &fakeAPI{}, "lea-a/a", ag("a", "idle", 1), ag("b", "idle", 1), ag("c", "idle", 1))
	m, _ = step(t, m, key("j"))
	seq = m.seq
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Shown: "lea-a/a", Agents: []Agent{ag("a", "idle", 1), ag("c", "idle", 1)}}})
	if m.seq != seq {
		t.Fatal("a show was scheduled while another agent is in the slot")
	}
}

func TestQuitDetachesTheTerminalAndKeepsTheDeck(t *testing.T) {
	for _, k := range []string{"q", "ctrl+c"} {
		api := &fakeAPI{}
		m := loaded(t, api, "", ag("a", "idle", 1))
		_, cmd := step(t, m, key(k))
		if _, ok := cmd().(doneMsg); !ok {
			t.Fatalf("%s: the deck quit instead of detaching", k)
		}
		if got := api.got(); len(got) != 1 || got[0] != "detach lea-a" {
			t.Fatalf("%s: calls=%q", k, got)
		}
	}
}

func TestAnUnreachableDaemonIsRetried(t *testing.T) {
	api := &fakeAPI{}
	m := loaded(t, api, "", ag("a", "idle", 1))
	m, cmd := step(t, m, snapshotMsg{err: errors.New("dial unix: connection refused")})
	if cmd == nil || !strings.HasPrefix(m.status, "daemon unreachable") || names(m) != "a" {
		t.Fatalf("status %q, agents %s", m.status, names(m))
	}
	m, cmd = step(t, m, retryMsg{})
	api.snap = Snapshot{Version: 1, Agents: []Agent{ag("a", "working", 2)}}
	msg := cmd()
	if got := api.got(); got[len(got)-1] != "watch lea-a 7" {
		t.Fatalf("calls=%q", got)
	}
	m, cmd = step(t, m, msg)
	if m.status != "" || m.version != 1 || m.agents[0].State != "working" || cmd == nil {
		t.Fatalf("status %q version %d", m.status, m.version)
	}
}

func TestErrorsAreShownOnOneCleanLine(t *testing.T) {
	m := loaded(t, &fakeAPI{}, "", ag("a", "idle", 1))
	m, _ = step(t, m, doneMsg{err: errors.New("bad\nthing \x1b[31mred")})
	if strings.ContainsAny(m.status, "\n\x1b") {
		t.Fatalf("status %q", m.status)
	}
}

func TestWindowSize(t *testing.T) {
	m, _ := step(t, newModel(&fakeAPI{}), tea.WindowSizeMsg{Width: 34, Height: 40})
	if m.width != 34 || m.height != 40 {
		t.Fatalf("size %dx%d", m.width, m.height)
	}
}

func TestStopConfirmationIsBoundToTheAgentChosenAtX(t *testing.T) {
	api := &fakeAPI{}
	m := loaded(t, api, "lea-a/b", ag("a", "idle", 1), ag("b", "idle", 1), ag("c", "idle", 1))
	m, _ = step(t, m, key("x"))
	// Between x and y a new agent is listed before b and another agent is shown: b is still the
	// one stopped.
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Shown: "lea-a/c", Agents: []Agent{launched(ag("n", "idle", 2), -1), ag("a", "blocked", 2), ag("b", "idle", 1), ag("c", "working", 2)}}})
	if m.index("lea-a/b") == 1 {
		t.Fatalf("the list did not move: %s", names(m))
	}
	m, cmd := step(t, m, key("y"))
	if cmd == nil {
		t.Fatal("y did not stop anything")
	}
	cmd()
	if got := api.got(); len(got) != 1 || got[0] != "stop lea-a/b" {
		t.Fatalf("calls=%q", got)
	}
}

func TestAfterARemovalTheNextRowIsSelected(t *testing.T) {
	api := &fakeAPI{}
	m := loaded(t, api, "lea-a/b", ag("a", "idle", 1), ag("b", "idle", 1), ag("c", "idle", 1))
	// b is stopped: the slot shows the placeholder and the next row in display order is selected.
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Shown: "", Agents: []Agent{ag("a", "idle", 1), ag("c", "idle", 1)}}})
	if m.selected != "lea-a/c" {
		t.Fatalf("selected %q, want the next row c", m.selected)
	}
	// The last row is removed: the previous row is selected.
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 9, Shown: "", Agents: []Agent{ag("a", "idle", 1)}}})
	if m.selected != "lea-a/a" {
		t.Fatalf("selected %q, want the previous row a", m.selected)
	}
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 10, Shown: ""}})
	if m.selected != "" {
		t.Fatalf("selected %q with no agent", m.selected)
	}
}

func TestSelectionFollowsAnAgentShownFromElsewhere(t *testing.T) {
	api := &fakeAPI{}
	m := loaded(t, api, "lea-a/a", ag("a", "idle", 1), ag("b", "idle", 1))
	m, _ = step(t, m, key("j"))
	if m.selected != "lea-a/b" {
		t.Fatalf("selected %q", m.selected)
	}
	// `lea new rev` from another terminal shows rev in the slot: the selection moves to it.
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Shown: "lea-a/rev", Agents: []Agent{ag("a", "idle", 1), ag("b", "idle", 1), ag("rev", "unknown", 2)}}})
	if m.selected != "lea-a/rev" {
		t.Fatalf("selected %q, want the newly shown rev", m.selected)
	}
	// A snapshot with the same shown agent leaves a moved selection alone.
	m, _ = step(t, m, key("1"))
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 9, Shown: "lea-a/rev", Agents: []Agent{ag("a", "idle", 1), ag("b", "idle", 1), ag("rev", "idle", 3)}}})
	if m.selected != "lea-a/a" {
		t.Fatalf("selected %q", m.selected)
	}
}

func TestADelayedShowOfThisDeckDoesNotPullTheSelectionBack(t *testing.T) {
	api := &fakeAPI{}
	m := loaded(t, api, "lea-a/a", ag("a", "idle", 1), ag("b", "idle", 1), ag("c", "idle", 1))
	m, _ = step(t, m, key("j"))
	m, cmd := step(t, m, showMsg{seq: m.seq}) // the deck shows b
	cmd()
	m, _ = step(t, m, key("j")) // the user already moved on to c
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Shown: "lea-a/b", Agents: []Agent{ag("a", "idle", 1), ag("b", "idle", 1), ag("c", "idle", 1)}}})
	if m.selected != "lea-a/c" {
		t.Fatalf("selected %q, want c", m.selected)
	}
}

func TestTwoQuickMovesAndAStaleSnapshotKeepTheLastSelection(t *testing.T) {
	api := &fakeAPI{}
	m := loaded(t, api, "lea-a/a", ag("a", "idle", 1), ag("b", "idle", 1), ag("c", "idle", 1), ag("d", "idle", 1))
	m, _ = step(t, m, key("j"))
	m, cmd := step(t, m, showMsg{seq: m.seq}) // asks to show b
	cmd()
	m, _ = step(t, m, key("j"))
	m, cmd = step(t, m, showMsg{seq: m.seq}) // asks to show c
	cmd()
	m, _ = step(t, m, key("j")) // the cursor is on d
	all := []Agent{ag("a", "idle", 1), ag("b", "idle", 1), ag("c", "idle", 1), ag("d", "idle", 1)}
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Shown: "lea-a/b", Agents: all}})
	if m.selected != "lea-a/d" {
		t.Fatalf("after the stale snapshot for b: selected %q, want d", m.selected)
	}
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 9, Shown: "lea-a/c", Agents: all}})
	if m.selected != "lea-a/d" {
		t.Fatalf("after the snapshot for c: selected %q, want d", m.selected)
	}
}

func TestAMessageForTheUserRanksAfterDoneUnseen(t *testing.T) {
	msg := ag("a", "idle", 1)
	msg.ForUser = 1
	m := loaded(t, &fakeAPI{}, "", msg, ag("b", "working", 1), ag("c", "done", 5), ag("d", "blocked", 1))
	if got := m.label(m.agents[0]); got != "message for you" {
		t.Fatalf("label %q", got)
	}
	blocked := ag("e", "blocked", 1)
	if got := m.label(blocked); got != "see terminal" {
		t.Fatalf("blocked without a card: %q", got)
	}
	blocked.Approval = &Approval{ID: 1, Tool: "Bash"}
	if got := m.label(blocked); got != "needs you" {
		t.Fatalf("blocked with a card: %q", got)
	}
}

// An agent whose container lea stop ended says so: the next lea resumes it.
func TestAStoppedAgentIsLabelledStopped(t *testing.T) {
	m := loaded(t, &fakeAPI{}, "", ag("a", "stopped", 1))
	if got := m.label(m.agents[0]); got != "stopped" {
		t.Fatalf("label %q", got)
	}
}

func TestSpaceJumpsToTheNextAgentThatNeedsYou(t *testing.T) {
	msg := ag("m", "idle", 1)
	msg.ForUser = 2
	// Order: b (blocked), m (message for you), w (working), z (idle).
	m := loaded(t, &fakeAPI{}, "lea-a/w", ag("b", "blocked", 1), msg, ag("w", "working", 1), ag("z", "idle", 1))
	m, cmd := step(t, m, key(" "))
	if m.selected != "lea-a/b" || cmd == nil {
		t.Fatalf("selected %q (wraps from w to b)", m.selected)
	}
	m, _ = step(t, m, key(" "))
	if m.selected != "lea-a/m" {
		t.Fatalf("selected %q", m.selected)
	}
	m, _ = step(t, m, key(" "))
	if m.selected != "lea-a/b" {
		t.Fatalf("selected %q", m.selected)
	}
	calm := loaded(t, &fakeAPI{}, "lea-a/z", ag("w", "working", 1), ag("z", "idle", 1))
	calm, cmd = step(t, calm, key(" "))
	if calm.selected != "lea-a/z" || cmd != nil || calm.status != "nothing needs you" {
		t.Fatalf("selected %q status %q", calm.selected, calm.status)
	}
	calm, _ = step(t, calm, key("k"))
	if calm.status != "" {
		t.Fatalf("status %q stayed after a move", calm.status)
	}
}

func (f *fakeAPI) Decide(ctx context.Context, id int64, verdict string) error {
	f.rec(fmt.Sprintf("decide %d %s", id, verdict))
	return f.err
}

func carded(name string, id int64, always bool) Agent {
	a := ag(name, "blocked", 1)
	a.Approval = &Approval{ID: id, Tool: "Bash", Summary: "make test", Always: always}
	return a
}

func TestApprovalKeysDecideTheSelectedCard(t *testing.T) {
	for _, tc := range []struct {
		k, want string
	}{{"a", "decide 7 allow"}, {"d", "decide 7 deny"}, {"A", "decide 7 always"}} {
		api := &fakeAPI{}
		m := loaded(t, api, "lea-a/b", carded("b", 7, true), ag("c", "idle", 1))
		_, cmd := step(t, m, key(tc.k))
		if cmd == nil {
			t.Fatalf("%s: no call", tc.k)
		}
		cmd()
		if got := api.got(); len(got) != 1 || got[0] != tc.want {
			t.Fatalf("%s: calls=%q", tc.k, got)
		}
	}
}

func TestAlwaysNeedsAnOfferedRule(t *testing.T) {
	api := &fakeAPI{}
	m := loaded(t, api, "lea-a/b", carded("b", 7, false))
	m, cmd := step(t, m, key("A"))
	if cmd != nil || len(api.got()) != 0 || !strings.Contains(m.status, "a allows once") {
		t.Fatalf("cmd=%v calls=%q status=%q", cmd != nil, api.got(), m.status)
	}
}

func TestApprovalKeysWithoutACardDoNothing(t *testing.T) {
	api := &fakeAPI{}
	m := loaded(t, api, "lea-a/c", carded("b", 7, true), ag("c", "idle", 1))
	for _, k := range []string{"a", "d", "A"} {
		if _, cmd := step(t, m, key(k)); cmd != nil {
			t.Fatalf("%s on a row without a card made a call", k)
		}
	}
}

func TestALateDecisionShowsTheDaemonsReason(t *testing.T) {
	api := &fakeAPI{err: errors.New("approval #7 expired: answer it in the agent's terminal")}
	m := loaded(t, api, "lea-a/b", carded("b", 7, true))
	m, cmd := step(t, m, key("a"))
	m, _ = step(t, m, cmd())
	if !strings.Contains(m.status, "expired") {
		t.Fatalf("status %q", m.status)
	}
}

func TestVerdictActsOnTheIDCapturedAtKeyTime(t *testing.T) {
	api := &fakeAPI{}
	m := loaded(t, api, "lea-a/b", carded("b", 7, true))
	_, cmd := step(t, m, key("a"))
	// a newer approval replaces the card before the call runs: the verdict stays on #7
	m.agents[0].Approval = &Approval{ID: 8, Tool: "Bash", Always: true}
	cmd()
	if got := api.got(); len(got) != 1 || got[0] != "decide 7 allow" {
		t.Fatalf("calls=%q", got)
	}
}

func TestMAndTOpenTheSelectedAgentsThread(t *testing.T) {
	for k, want := range map[string]string{"m": "popup lea-a/b true", "t": "popup lea-a/b false"} {
		api := &fakeAPI{}
		m := loaded(t, api, "lea-a/b", ag("a", "idle", 1), ag("b", "idle", 1))
		_, cmd := step(t, m, key(k))
		if cmd == nil {
			t.Fatalf("%s: no call", k)
		}
		cmd()
		if got := api.got(); len(got) != 1 || got[0] != want {
			t.Fatalf("%s: calls=%q", k, got)
		}
	}
	if _, cmd := step(t, newModel(&fakeAPI{}), key("m")); cmd != nil {
		t.Fatal("m without agents made a call")
	}
}

func (f *fakeAPI) SetRole(ctx context.Context, k, r string) error {
	f.rec("role " + k + " " + r)
	return f.err
}

func TestRoleKeyAsksThenSetsTheRole(t *testing.T) {
	api := &fakeAPI{}
	m := loaded(t, api, "lea-a/a", ag("a", "idle", 1))
	m, cmd := step(t, m, key("r"))
	if cmd != nil || !strings.Contains(m.status, "role of a: l lead, d dev, r reviewer") {
		t.Fatalf("status %q", m.status)
	}
	m, cmd = step(t, m, key("l"))
	if cmd == nil || m.roleFor != "" || m.status != "" {
		t.Fatalf("roleFor %q status %q", m.roleFor, m.status)
	}
	cmd()
	if got := api.got(); len(got) != 1 || got[0] != "role lea-a/a lead" {
		t.Fatalf("calls=%q", got)
	}
}

func TestRoleChooserCancelsAndWinsOverOtherKeys(t *testing.T) {
	api := &fakeAPI{}
	m := loaded(t, api, "lea-a/a", carded("a", 7, true))
	m, _ = step(t, m, key("r"))
	m, cmd := step(t, m, key("x")) // not a role key: cancels, and is not a stop
	if cmd != nil || m.confirm != "" || m.roleFor != "" || len(api.got()) != 0 {
		t.Fatalf("confirm %q roleFor %q calls %q", m.confirm, m.roleFor, api.got())
	}
	m, _ = step(t, m, key("r"))
	m, cmd = step(t, m, key("d")) // dev, not deny
	cmd()
	m, _ = step(t, m, key("r"))
	_, cmd = step(t, m, key("r"))
	cmd()
	if got := api.got(); len(got) != 2 || got[0] != "role lea-a/a dev" || got[1] != "role lea-a/a reviewer" {
		t.Fatalf("calls=%q", got)
	}
}

func TestRoleChooserEndsWhenTheAgentGoes(t *testing.T) {
	m := loaded(t, &fakeAPI{}, "lea-a/a", ag("a", "idle", 1), ag("b", "idle", 1))
	m, _ = step(t, m, key("r"))
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Agents: []Agent{ag("b", "idle", 1)}}})
	if m.roleFor != "" || m.status != "" {
		t.Fatalf("roleFor %q status %q", m.roleFor, m.status)
	}
}
