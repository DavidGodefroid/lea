package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/providers"
	"github.com/DavidGodefroid/lea/internal/roles"
	"github.com/DavidGodefroid/lea/internal/store"
	"github.com/DavidGodefroid/lea/internal/tmux"
)

const C = "lea-0123456789"
const leaBin = "/opt/lea/build/lea"

type env struct {
	m    *Manager
	term *fakeTerm
	ctr  *fakeContainers
	st   *store.Store
}

func newEnv(t *testing.T) env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "lea.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	term := &fakeTerm{sessions: map[string]*fakeSession{}}
	ctr := &fakeContainers{live: map[string][]string{}, projects: map[string]string{}}
	m := NewManager(Config{Store: st, Term: term, Containers: ctr, LeaBin: leaBin, UID: 1000, GID: 1000,
		Now: func() int64 { return 1000 }})
	return env{m, term, ctr, st}
}

var bg = context.Background()

func (e env) ensure(t *testing.T, workdir string) *fakeSession {
	t.Helper()
	if err := e.m.Ensure(bg, C, workdir); err != nil {
		t.Fatal(err)
	}
	return e.term.get(C)
}

func (e env) spawn(t *testing.T, name string, args ...string) Row {
	t.Helper()
	r, err := e.m.New(bg, NewRequest{Container: C, Name: name, Args: args})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (e env) state(t *testing.T, name string) string {
	t.Helper()
	a, err := e.st.AgentByKey(C + "/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return a.State
}

func TestEnsureBuildsTheDeckLayoutOnce(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/home/u/p/shop")
	deck, ph := s.byRole(roleDeck), s.byRole(rolePlace)
	if deck == nil || deck.id != "%0" || !reflect.DeepEqual(deck.cmd, []string{leaBin, "deck", "--container", C}) {
		t.Fatalf("deck=%+v", deck)
	}
	if ph == nil || ph.win != deck.win || !reflect.DeepEqual(ph.cmd, []string{leaBin, "deck", "--placeholder"}) {
		t.Fatalf("placeholder=%+v", ph)
	}
	if s.opts[optWorkdir] != "/home/u/p/shop" {
		t.Fatalf("workdir option=%q", s.opts[optWorkdir])
	}
	for _, l := range []string{"refresh-client -B lea-dead:%*:#{pane_dead}", "resize-pane -t %0 -x 34"} {
		if !s.logged(l) {
			t.Fatalf("missing %q in %q", l, s.log)
		}
	}
	n := len(s.panes)
	if err := e.m.Ensure(bg, C, ""); err != nil {
		t.Fatal(err)
	}
	if len(s.panes) != n || e.term.opens != 1 {
		t.Fatalf("second Ensure: %d panes (was %d), %d opens", len(s.panes), n, e.term.opens)
	}
}

// TestConfKeepsTheDeckWidth ties the width lea.tmux.conf restores on a resize to DeckWidth.
func TestConfKeepsTheDeckWidth(t *testing.T) {
	m := regexp.MustCompile(`resize-pane -t :\.0 -x (\d+)`).FindAllStringSubmatch(tmux.Conf(), -1)
	if len(m) != 1 || m[0][1] != fmt.Sprint(DeckWidth) {
		t.Fatalf("lea.tmux.conf resizes the deck with %q, want exactly one -x %d", m, DeckWidth)
	}
}

func TestNewSpawnsClaudeUnderAbducoAndShowsIt(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/home/u/p/it's here")
	r := e.spawn(t, "", "--resume")
	want := Row{Key: C + "/claude-1", Name: "claude-1", Profile: "claude", State: "unknown", Workdir: "/home/u/p/it's here", Updated: 1000,
		Created: 1000, Role: "dev", CtxPct: -1, CostUSD: -1}
	if r != want {
		t.Fatalf("row=%+v", r)
	}
	p := s.byAgent("claude-1")
	cmd := []string{"docker", "exec", "-it", "-u", "1000:1000", "-e", "TERM=xterm-256color", "-e", "COLORTERM=truecolor",
		"-e", "FORCE_HYPERLINK=1", "-e", "LEA_AGENT=claude-1", "-e", "LEA_APPROVALS=deck", "-w", "/home/u/p/it's here", C,
		"abduco", "-c", "ag-claude-1", "lea-agent", "run", "claude-1", "--", "--resume"}
	if p == nil || !reflect.DeepEqual(p.cmd, cmd) {
		t.Fatalf("pane=%+v", p)
	}
	if s.slot() != p {
		t.Fatal("the new agent is not shown in the slot")
	}
	if ph := s.byRole(rolePlace); ph == nil || ph.win == s.byRole(roleDeck).win {
		t.Fatal("the placeholder was not parked")
	}
	joined := strings.Join(p.cmd, " ")
	for _, bad := range []string{"TMUX", "docker.sock", "daemon.sock", "bus.sock"} {
		if strings.Contains(joined, bad) {
			t.Fatalf("the agent's command mentions %s", bad)
		}
	}
	snap, err := e.m.Watch(bg, C, 0, time.Second)
	if err != nil || snap.Shown != r.Key || len(snap.Agents) != 1 || snap.Agents[0] != want {
		t.Fatalf("snapshot=%+v err=%v", snap, err)
	}
}

func TestNewFocusSelectsTheAgentPane(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	if _, err := e.m.New(bg, NewRequest{Container: C, Focus: true}); err != nil {
		t.Fatal(err)
	}
	if !s.logged("select-pane -t " + s.byAgent("claude-1").id) {
		t.Fatalf("log=%q", s.log)
	}
}

func TestNewNamesAndRefusals(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	e.spawn(t, "")
	if r := e.spawn(t, ""); r.Name != "claude-2" {
		t.Fatalf("second default name %q", r.Name)
	}
	if _, err := e.m.New(bg, NewRequest{Container: C, Name: "claude-1"}); !errors.Is(err, store.ErrExists) {
		t.Fatalf("duplicate name: err=%v", err)
	}
	for _, r := range []NewRequest{
		{Container: C, Profile: "gemini"},
		{Container: C, Name: "x", Model: "-rf"},
		{Container: C, Name: "Bad Name"},
		{Container: C, Name: "x", Args: []string{"a\nb"}},
		{Container: C, Name: "x", Args: make([]string, MaxArgs+1)},
		{Container: C, Name: "x", Args: []string{strings.Repeat("a", MaxArgBytes+1)}},
		{Container: C, Name: "x", Workdir: "relative/dir"},
		{Container: "Not A Container", Name: "x"},
	} {
		if _, err := e.m.New(bg, r); err == nil {
			t.Fatalf("accepted %+v", r)
		}
	}
	if _, err := e.m.New(bg, NewRequest{Container: C, Profile: "gemini"}); err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("other profile: err=%v", err)
	}
}

func TestNewReusesTheNameOfAnEndedAgent(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	e.spawn(t, "")
	old := s.byAgent("claude-1")
	s.locked(func() { old.dead = true })
	e.m.Ensure(bg, C, "") // not live, dead pane: done
	if e.state(t, "claude-1") != "done" {
		t.Fatal("ended agent not marked done")
	}
	r := e.spawn(t, "")
	if r.Name != "claude-1" || r.State != "unknown" {
		t.Fatalf("row=%+v", r)
	}
	if s.pane(old.id) != nil {
		t.Fatal("the ended agent's old pane was not removed")
	}
}

func TestNewRollsBackWhenThePaneCannotStart(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	s.locked(func() { s.fail = "new-window" })
	if _, err := e.m.New(bg, NewRequest{Container: C}); err == nil {
		t.Fatal("no error")
	}
	if as, _ := e.st.SpawnedAgents(C); len(as) != 0 {
		t.Fatalf("agent row left behind: %+v", as)
	}
}

func TestNewWithoutAWorkingDirectory(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "")
	if _, err := e.m.New(bg, NewRequest{Container: C}); err == nil || !strings.Contains(err.Error(), "working directory") {
		t.Fatalf("err=%v", err)
	}
}

func TestFocusSwapsTheAgentIntoTheSlot(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	a := e.spawn(t, "a1")
	b := e.spawn(t, "b1") // shown last
	if err := e.m.Focus(bg, a.Key, false); err != nil {
		t.Fatal(err)
	}
	if s.slot() != s.byAgent("a1") || s.byAgent("b1").win == s.byRole(roleDeck).win {
		t.Fatal("a1 not in the slot, or b1 not parked")
	}
	if s.logged("select-pane -t " + s.byAgent("a1").id) {
		t.Fatal("a show without focus moved the keyboard")
	}
	if err := e.m.Focus(bg, b.Key, true); err != nil {
		t.Fatal(err)
	}
	if s.slot() != s.byAgent("b1") || !s.logged("select-pane -t "+s.byAgent("b1").id) {
		t.Fatalf("focus b1: log=%q", s.log)
	}
	snap, _ := e.m.Watch(bg, C, 0, time.Second)
	if snap.Shown != b.Key {
		t.Fatalf("shown=%q", snap.Shown)
	}
	if err := e.m.Focus(bg, C+"/nobody", false); err == nil {
		t.Fatal("unknown agent accepted")
	}
}

func TestFocusOfAnAgentWithoutTerminal(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	e.st.RegisterAgent(C, "gone", "claude", "/p", 900) // not live, no pane
	if err := e.m.Focus(bg, C+"/gone", true); err == nil || !strings.Contains(err.Error(), "x removes it") {
		t.Fatalf("err=%v", err)
	}
}

func TestStopEndsKillsAndForgets(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	r := e.spawn(t, "")
	e.ctr.setLive(C, "claude-1")
	if err := e.m.Stop(bg, r.Key); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e.ctr.stopped, []string{C + "/claude-1"}) {
		t.Fatalf("stopped=%q", e.ctr.stopped)
	}
	if s.byAgent("claude-1") != nil || s.slot() != s.byRole(rolePlace) {
		t.Fatal("pane left, or the placeholder is not back in the slot")
	}
	if _, err := e.st.AgentByKey(r.Key); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("row left: err=%v", err)
	}
	if snap, _ := e.m.Watch(bg, C, 0, time.Second); snap.Shown != "" || len(snap.Agents) != 0 {
		t.Fatalf("snapshot=%+v", snap)
	}
}

func TestReconcileReattachesALiveAgentWithoutPane(t *testing.T) {
	e := newEnv(t)
	e.st.RegisterAgent(C, "claude-1", "claude", "/p", 900)
	e.ctr.setLive(C, "claude-1")
	s := e.ensure(t, "/p")
	p := s.byAgent("claude-1")
	if p == nil || !reflect.DeepEqual(p.cmd[len(p.cmd)-3:], []string{"abduco", "-a", "ag-claude-1"}) {
		t.Fatalf("pane=%+v", p)
	}
	if strings.Contains(strings.Join(p.cmd, " "), "lea-agent run") {
		t.Fatal("a reattach must never start a new agent")
	}
}

func TestReconcileRespawnsTheDeadPaneOfALiveAgent(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	e.spawn(t, "")
	e.ctr.setLive(C, "claude-1")
	p := s.byAgent("claude-1")
	s.locked(func() { p.dead = true }) // ctrl+\ detached abduco, or docker exec died
	e.m.Ensure(bg, C, "")
	if p.dead || !reflect.DeepEqual(p.cmd[len(p.cmd)-3:], []string{"abduco", "-a", "ag-claude-1"}) {
		t.Fatalf("pane=%+v", p)
	}
	if e.state(t, "claude-1") == "done" {
		t.Fatal("a live agent was marked done")
	}
}

func TestReconcileMarksEndedAgentsDone(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	e.spawn(t, "a1")
	e.spawn(t, "a2")
	e.st.RegisterAgent(C, "a3", "claude", "/p", 900)
	s.locked(func() { s.byAgent("a1").dead = true }) // ended: dead pane, not live
	// a2: pane alive, not live yet (abduco still starting): left alone. a3: no pane, not live.
	e.m.Ensure(bg, C, "")
	if e.state(t, "a1") != "done" || e.state(t, "a2") != "unknown" || e.state(t, "a3") != "done" {
		t.Fatalf("states a1=%s a2=%s a3=%s", e.state(t, "a1"), e.state(t, "a2"), e.state(t, "a3"))
	}
	if s.byAgent("a1") == nil {
		t.Fatal("an ended agent's last screen must stay until it is stopped")
	}
}

func TestReconcileRepairsTheDeckAndThePlaceholder(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	s.locked(func() {
		s.byRole(roleDeck).dead = true
		ph := s.byRole(rolePlace)
		for i, p := range s.panes {
			if p == ph {
				s.panes = append(s.panes[:i], s.panes[i+1:]...)
			}
		}
	})
	e.m.Ensure(bg, C, "")
	if s.byRole(roleDeck).dead || s.byRole(rolePlace) == nil || s.slot() != s.byRole(rolePlace) {
		t.Fatalf("panes=%+v", s.panes)
	}
}

func TestADeadPaneTriggersAReconcile(t *testing.T) {
	old := debounce
	debounce = time.Millisecond
	t.Cleanup(func() { debounce = old })
	e := newEnv(t)
	s := e.ensure(t, "/p")
	r := e.spawn(t, "")
	s.locked(func() { s.byAgent("claude-1").dead = true })
	s.emit(tmux.Event{Name: "%subscription-changed", Args: []string{"lea-dead", "$0", "@1", "1", "%2", ":", "1"}})
	deadline := time.Now().Add(2 * time.Second)
	for e.state(t, "claude-1") != "done" {
		if time.Now().After(deadline) {
			t.Fatal("no reconcile after a pane died")
		}
		time.Sleep(5 * time.Millisecond)
	}
	_ = r
}

func TestAClosedConnectionIsReopened(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	s.closeEvents() // %exit: the control client ended
	deadline := time.Now().Add(2 * time.Second)
	for {
		e.m.mu.Lock()
		n := len(e.m.conns)
		e.m.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("connection not dropped")
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.ensure(t, "")
	if e.term.opens != 2 {
		t.Fatalf("opens=%d", e.term.opens)
	}
}

func TestDetachLeavesControlClients(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	s.locked(func() { s.clients = []string{"/dev/pts/3|0", "client-77|1"} })
	if err := e.m.Detach(bg, C); err != nil {
		t.Fatal(err)
	}
	if !s.logged("detach-client -t /dev/pts/3") || s.logged("detach-client -t client-77") {
		t.Fatalf("log=%q", s.log)
	}
}

func TestWatch(t *testing.T) {
	e := newEnv(t)
	start := time.Now()
	snap, err := e.m.Watch(bg, C, 0, 5*time.Second)
	if err != nil || time.Since(start) > time.Second {
		t.Fatalf("a first watch must return at once: %v", err)
	}
	// A daemon restart resets the version: an older client's higher version returns at once.
	if s2, _ := e.m.Watch(bg, C, snap.Version+40, 5*time.Second); s2.Version != snap.Version {
		t.Fatalf("version=%d", s2.Version)
	}
	done := make(chan Snapshot)
	go func() { s, _ := e.m.Watch(bg, C, snap.Version, 5*time.Second); done <- s }()
	time.Sleep(20 * time.Millisecond)
	e.m.Changed()
	select {
	case s := <-done:
		if s.Version <= snap.Version {
			t.Fatalf("version did not move: %d", s.Version)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Changed did not wake the watch")
	}
	s3, _ := e.m.Watch(bg, C, snap.Version+1, 30*time.Millisecond)
	if s3.Version != snap.Version+1 {
		t.Fatalf("timeout version=%d", s3.Version)
	}
}

func TestResumeReattachesEverySession(t *testing.T) {
	e := newEnv(t)
	e.term.names = []string{C, "lea-other", "Bad Name"}
	e.m.Resume(bg)
	if e.term.get(C) == nil || e.term.get("lea-other") == nil || e.term.get("Bad Name") != nil {
		t.Fatalf("sessions=%v", e.term.sessions)
	}
}

func TestNewStopsTheAgentWhenItCannotBeShown(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	s.locked(func() { s.fail = "swap-pane" }) // the pane started: abduco runs lea-agent
	if _, err := e.m.New(bg, NewRequest{Container: C}); err == nil {
		t.Fatal("no error")
	}
	if !reflect.DeepEqual(e.ctr.stopped, []string{C + "/claude-1"}) {
		t.Fatalf("stopped=%q: the agent outlives its pane", e.ctr.stopped)
	}
	if as, _ := e.st.SpawnedAgents(C); len(as) != 0 {
		t.Fatalf("agent row left behind: %+v", as)
	}
	if s.byAgent("claude-1") != nil {
		t.Fatal("pane left behind")
	}
}

func TestNewKeepsTheRowOfAnAgentItCannotStop(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	s.locked(func() { s.fail = "swap-pane" })
	e.ctr.fail(nil, errors.New("docker down"))
	if _, err := e.m.New(bg, NewRequest{Container: C}); err == nil {
		t.Fatal("no error")
	}
	if as, _ := e.st.SpawnedAgents(C); len(as) != 1 {
		t.Fatalf("a possibly running agent was forgotten: %+v", as)
	}
}

func TestReconcileLeavesStatesWhenLiveAgentsAreUnknown(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	e.spawn(t, "")
	s.locked(func() { s.byAgent("claude-1").dead = true })
	e.ctr.fail(errors.New("docker down"), nil)
	if err := e.m.Ensure(bg, C, ""); err == nil {
		t.Fatal("no error")
	}
	if st := e.state(t, "claude-1"); st != "unknown" {
		t.Fatalf("state=%s: an agent was marked done without knowing it ended", st)
	}
	if r := e.spawn(t, ""); r.Name != "claude-2" {
		t.Fatalf("name %q reused", r.Name)
	}
}

func TestRespawnsOfAPaneThatKeepsDyingAreSpacedOut(t *testing.T) {
	e := newEnv(t)
	clk := &fakeClock{now: time.Unix(5000, 0)}
	e.m.clock, e.m.schedule = clk.Now, clk.schedule
	s := e.ensure(t, "/p")
	e.spawn(t, "")
	e.ctr.setLive(C, "claude-1")
	p := s.byAgent("claude-1")
	respawns := func() int {
		n := 0
		s.locked(func() {
			for _, l := range s.log {
				if strings.HasPrefix(l, "respawn-pane -k -t "+p.id+" ") {
					n++
				}
			}
		})
		return n
	}
	die := func() { s.locked(func() { p.dead = true }) }
	check := func(step string, want int, waits ...time.Duration) {
		t.Helper()
		if err := e.m.Ensure(bg, C, ""); err != nil {
			t.Fatal(err)
		}
		got, _ := clk.scheduled()
		if respawns() != want || !reflect.DeepEqual(got, waits) {
			t.Fatalf("%s: %d respawns (want %d), waits %v (want %v)", step, respawns(), want, got, waits)
		}
	}
	die()
	check("first death", 1) // at once
	die()
	check("immediate second death", 1, time.Second)
	check("still waiting, no second retry", 1, time.Second)
	clk.advance(time.Second)
	_, retry := clk.scheduled()
	retry() // the scheduled reconcile
	if respawns() != 2 {
		t.Fatalf("the scheduled retry did not respawn: %d", respawns())
	}
	die()
	check("third death", 2, time.Second, 2*time.Second)
	clk.advance(time.Second)
	check("half way", 2, time.Second, 2*time.Second)
	clk.advance(time.Second)
	check("after 2 s", 3, time.Second, 2*time.Second)
	clk.advance(11 * time.Second) // the pane stayed alive: the backoff starts over
	die()
	check("death after a stable run", 4, time.Second, 2*time.Second)
	die()
	check("immediate death after the reset", 4, time.Second, 2*time.Second, time.Second)
	if e.state(t, "claude-1") == "done" {
		t.Fatal("a live agent was marked done while waiting")
	}
}

// namesTypingOnPurpose are the only sources allowed to name the typing commands: this test, the
// internal/tmux tests that check the allow-list refuses them, and its real tests, which press the
// user's keys (send-keys -K) to run the conf's bindings.
var namesTypingOnPurpose = map[string]bool{
	filepath.Join("internal", "agents", "manager_test.go"): true,
	filepath.Join("internal", "tmux", "conn_test.go"):      true,
	filepath.Join("internal", "tmux", "real_test.go"):      true,
}

// confFile may forward the user's own input and nothing else: send-keys -M passes the user's
// mouse event on to the pane under the pointer, send-keys -X runs a copy-mode command (scroll,
// cancel), and paste-buffer of the lea-paste buffer pastes what the user copied, when the user
// presses ctrl+v. None sends text of lea's.
var (
	confFile     = filepath.Join("internal", "tmux", "lea.tmux.conf")
	confForwards = regexp.MustCompile(`send-keys -[MX]\b|paste-buffer -p -d -b lea-paste$`)
)

// TestNothingTypesIntoATerminal enforces spec §7.1.8 on every Go and shell source of lea, tests
// included.
func TestNothingTypesIntoATerminal(t *testing.T) {
	root := filepath.Join("..", "..")
	scanned := map[string]bool{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "build":
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if namesTypingOnPurpose[rel] || !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if !(strings.HasSuffix(p, ".go") || strings.HasSuffix(p, ".sh") || strings.HasSuffix(p, ".conf") ||
			shellScript(b)) {
			return nil
		}
		scanned[rel] = true
		if rel == confFile {
			b = regexp.MustCompile(`(?m)`+confForwards.String()).ReplaceAll(b, nil)
		}
		for _, bad := range []string{"send-keys", "send-prefix", "paste-buffer"} {
			if bytes.Contains(b, []byte(bad)) {
				t.Errorf("%s contains %q: no lea code may type into a terminal", p, bad)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Extension-less shell scripts are found by their shebang.
	for _, f := range []string{filepath.Join("bin", "lea"), filepath.Join("image", "entrypoint"),
		filepath.Join("image", "lea-agent"), filepath.Join("tests", "fakes", "docker")} {
		if !scanned[f] {
			t.Errorf("%s was not scanned", f)
		}
	}
}

// shellScript reports whether b starts with a shebang naming a shell (#!/bin/sh, #!/usr/bin/env bash).
func shellScript(b []byte) bool {
	line, _, _ := bytes.Cut(b, []byte("\n"))
	if !bytes.HasPrefix(line, []byte("#!")) {
		return false
	}
	for _, f := range strings.Fields(string(line[2:])) {
		switch filepath.Base(f) {
		case "sh", "bash", "dash", "ash", "ksh", "zsh":
			return true
		}
	}
	return false
}

func TestShellScript(t *testing.T) {
	for in, want := range map[string]bool{
		"#!/bin/sh\nset -eu\n": true, "#!/usr/bin/env bash\n": true, "#! /bin/bash -e\n": true,
		"#!/usr/bin/env python3\n": false, "#!/bin/shellcheck\n": false, "echo hi\n#!/bin/sh\n": false, "": false,
	} {
		if got := shellScript([]byte(in)); got != want {
			t.Errorf("shellScript(%q)=%v", in, got)
		}
	}
}

// laterRec records what the manager runs later.
type laterRec struct {
	waits []time.Duration
	fns   []func()
}

func (l *laterRec) later(d time.Duration, f func()) {
	l.waits, l.fns = append(l.waits, d), append(l.fns, f)
}

func (s *fakeSession) since(mark int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.log[mark:]...)
}

func (s *fakeSession) mark() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.log) }

func TestAReattachedAgentInTheSlotIsMadeToRedraw(t *testing.T) {
	e := newEnv(t)
	rec := &laterRec{}
	e.m.later = rec.later
	s := e.ensure(t, "/p")
	e.spawn(t, "")
	e.ctr.setLive(C, "claude-1")
	p := s.byAgent("claude-1")
	deck := s.byRole(roleDeck).id
	s.locked(func() { p.dead = true }) // ctrl+\: abduco detached; the agent repaints only on SIGWINCH
	e.m.Ensure(bg, C, "")
	if len(rec.fns) != 1 || rec.waits[0] != redrawDelay {
		t.Fatalf("scheduled %v", rec.waits)
	}
	mark := s.mark()
	rec.fns[0]() // abduco -a is attached by now: one column less, then back
	if got := s.since(mark); !slices.Contains(got, "resize-pane -t "+deck+" -x 35") || len(rec.fns) != 2 {
		t.Fatalf("log %q, scheduled %v", got, rec.waits)
	}
	mark = s.mark()
	rec.fns[1]()
	if got := s.since(mark); !slices.Contains(got, "resize-pane -t "+deck+" -x 34") {
		t.Fatalf("log %q", got)
	}
}

func TestAReattachedAgentOutOfTheSlotIsNotResized(t *testing.T) {
	e := newEnv(t)
	rec := &laterRec{}
	e.m.later = rec.later
	s := e.ensure(t, "/p")
	e.spawn(t, "a1")
	e.spawn(t, "a2") // a2 takes the slot, a1 is parked
	e.ctr.setLive(C, "a1", "a2")
	p := s.byAgent("a1")
	s.locked(func() { p.dead = true })
	e.m.Ensure(bg, C, "")
	if len(rec.fns) != 1 {
		t.Fatalf("scheduled %v", rec.waits)
	}
	mark := s.mark()
	rec.fns[0]()
	for _, l := range s.since(mark) {
		if strings.HasPrefix(l, "resize-pane") {
			t.Fatalf("a parked pane was resized: %q", l)
		}
	}
	if len(rec.fns) != 1 {
		t.Fatal("a restore was scheduled for nothing")
	}
}

const (
	sid1 = "0b6f5a1e-4c1d-4f7a-9d2e-1a2b3c4d5e6f"
	sid2 = "9c8d7e6f-5a4b-4c3d-8e2f-6a5b4c3d2e1f"
)

// restarted is e after lea stop --all and lea: a new daemon (manager), a new tmux server and a new
// container that runs no agent, over the same store.
func (e env) restarted(t *testing.T) env {
	t.Helper()
	term := &fakeTerm{sessions: map[string]*fakeSession{}}
	ctr := &fakeContainers{live: map[string][]string{}, projects: map[string]string{}}
	m := NewManager(Config{Store: e.st, Term: term, Containers: ctr, LeaBin: leaBin, UID: 1000, GID: 1000,
		Now: func() int64 { return 2000 }})
	return env{m, term, ctr, e.st}
}

// agentArgs are the arguments lea-agent run passes to Claude in an agent's pane.
func agentArgs(t *testing.T, s *fakeSession, name string) []string {
	t.Helper()
	var cmd []string
	s.locked(func() {
		if p := s.byAgent(name); p != nil {
			cmd = p.cmd
		}
	})
	i := slices.Index(cmd, "--")
	if i < 0 || i < 2 || cmd[i-2] != "run" || cmd[i-1] != name {
		t.Fatalf("pane of %s: %q", name, cmd)
	}
	return cmd[i+1:]
}

func TestStoppedAgentsResumeTheirConversationInANewContainer(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	e.spawn(t, "lead-1", "--model", "opus")
	if _, err := e.m.New(bg, NewRequest{Container: C, Name: "fresh", Brief: "do it", Hidden: true}); err != nil {
		t.Fatal(err)
	}
	e.spawn(t, "old")
	e.st.SetState(C+"/lead-1", "idle", sid1, 1001)
	e.st.SetState(C+"/old", "done", sid2, 1001)
	e.st.UpsertAgent(C, "hook-only", 900) // not spawned by lea: not the deck's business
	if err := e.m.MarkStopped(bg, C, false); err != nil {
		t.Fatal(err)
	}
	for n, want := range map[string]string{"lead-1": "stopped", "fresh": "stopped", "old": "done"} {
		if got := e.state(t, n); got != want {
			t.Fatalf("%s is %s after lea stop, want %s", n, got, want)
		}
	}
	// The container is gone: a reconcile meanwhile must not take the stopped agents for ended.
	e.m.Ensure(bg, C, "")
	if got := e.state(t, "lead-1"); got != "stopped" {
		t.Fatalf("lead-1 is %s after a reconcile, want stopped", got)
	}

	r := e.restarted(t)
	if err := r.m.ResumeStopped(bg, C); err != nil {
		t.Fatal(err)
	}
	s := r.term.get(C)
	if got := agentArgs(t, s, "lead-1"); !reflect.DeepEqual(got, []string{"--resume", sid1, "--model", "opus"}) {
		t.Fatalf("lead-1 restarted with %q", got)
	}
	// Never prompted: started again as it was first, its brief still waiting.
	if got := agentArgs(t, s, "fresh"); len(got) != 1 || !strings.Contains(got[0], "Your brief is waiting") {
		t.Fatalf("fresh restarted with %q", got)
	}
	if _, err := e.st.AgentByKey(C + "/old"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an ended agent was kept: err=%v", err)
	}
	if s.byAgent("old") != nil {
		t.Fatal("an ended agent was started again")
	}
	if _, err := e.st.AgentByKey(C + "/hook-only"); err != nil {
		t.Fatalf("hook-only forgotten: %v", err)
	}
	for _, n := range []string{"lead-1", "fresh"} {
		a, err := e.st.AgentByKey(C + "/" + n)
		if err != nil || a.State != "unknown" || a.Created != 1000 || a.Workdir != "/p" {
			t.Fatalf("%s after resume: %+v err=%v", n, a, err)
		}
	}
	// A reconcile while Claude starts leaves the panes alone.
	r.m.Ensure(bg, C, "")
	if r.ctr.setLive(C, "lead-1", "fresh"); r.m.Ensure(bg, C, "") != nil || s.byAgent("lead-1") == nil {
		t.Fatal("lead-1 lost its pane")
	}
}

func TestResumeStoppedReplacesTheDeadPaneOfASurvivingSession(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	e.spawn(t, "dev-1")
	e.st.SetState(C+"/dev-1", "idle", sid1, 1001)
	if err := e.m.MarkStopped(bg, C, false); err != nil {
		t.Fatal(err)
	}
	s.locked(func() { s.byAgent("dev-1").dead = true })
	if err := e.m.ResumeStopped(bg, C); err != nil {
		t.Fatal(err)
	}
	n := 0
	s.locked(func() {
		for _, p := range s.panes {
			if p.agent == "dev-1" {
				n++
				if p.dead {
					t.Error("the dead pane was kept")
				}
			}
		}
	})
	if n != 1 {
		t.Fatalf("%d panes of dev-1", n)
	}
	if got := agentArgs(t, s, "dev-1"); !reflect.DeepEqual(got, []string{"--resume", sid1}) {
		t.Fatalf("dev-1 restarted with %q", got)
	}
}

// The session id comes from the agent's own hooks: one that is not Claude's UUID never reaches
// the command line (it could pass for a flag), and the agent starts again as it first did.
func TestResumeStoppedNeverPassesAForgedSessionID(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	e.spawn(t, "dev-1", "--model", "opus")
	e.st.SetState(C+"/dev-1", "idle", "--dangerously-skip-permissions", 1001)
	if err := e.m.MarkStopped(bg, C, false); err != nil {
		t.Fatal(err)
	}
	r := e.restarted(t)
	if err := r.m.ResumeStopped(bg, C); err != nil {
		t.Fatal(err)
	}
	if got := agentArgs(t, r.term.get(C), "dev-1"); !reflect.DeepEqual(got, []string{"--model", "opus"}) {
		t.Fatalf("dev-1 restarted with %q", got)
	}
}

func TestResumeStoppedLeavesALiveAgentAlone(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	e.spawn(t, "dev-1")
	e.ctr.setLive(C, "dev-1")
	before := agentArgs(t, s, "dev-1")
	if err := e.m.ResumeStopped(bg, C); err != nil {
		t.Fatal(err)
	}
	if got := agentArgs(t, s, "dev-1"); !reflect.DeepEqual(got, before) || e.state(t, "dev-1") != "unknown" {
		t.Fatalf("a live agent was restarted: %q", got)
	}
}

func TestMarkStoppedForgetDropsTheAgentsForGood(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	e.spawn(t, "dev-1")
	e.st.UpsertAgent(C, "hook-only", 900)
	if err := e.m.MarkStopped(bg, C, true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.AgentByKey(C + "/dev-1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("dev-1 kept: err=%v", err)
	}
	if _, err := e.st.AgentByKey(C + "/hook-only"); err != nil {
		t.Fatalf("hook-only forgotten: %v", err)
	}
	r := e.restarted(t)
	if err := r.m.ResumeStopped(bg, C); err != nil {
		t.Fatal(err)
	}
	if s := r.term.get(C); s != nil && s.byAgent("dev-1") != nil {
		t.Fatal("a forgotten agent was started again")
	}
}

func TestResumeStoppedWhenLiveAgentsAreUnknownDoesNothing(t *testing.T) {
	e := newEnv(t)
	e.st.RegisterAgent(C, "a1", "claude", "/p", 900)
	e.st.SetState(C+"/a1", "done", "", 901)
	e.ctr.fail(errors.New("docker: cannot connect"), nil)
	if err := e.m.ResumeStopped(bg, C); err == nil {
		t.Fatal("no error")
	}
	if _, err := e.st.AgentByKey(C + "/a1"); err != nil {
		t.Fatalf("a1 forgotten: %v", err)
	}
}

func TestAttachedCountsOnlyTheUsersTerminals(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	s.locked(func() { s.clients = []string{"ctl|1"} }) // the daemon's own control client
	if a, err := e.m.Attached(bg, C); err != nil || a {
		t.Fatalf("attached=%v err=%v", a, err)
	}
	s.locked(func() { s.clients = []string{"ctl|1", "/dev/pts/3|0"} })
	if a, err := e.m.Attached(bg, C); err != nil || !a {
		t.Fatalf("attached=%v err=%v", a, err)
	}
}

func TestARebuiltSessionShowsTheLastShownAgentAgain(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	e.spawn(t, "a1")
	e.spawn(t, "a2")
	e.ctr.setLive(C, "a1", "a2")
	if err := e.m.Focus(bg, C+"/a1", false); err != nil {
		t.Fatal(err)
	}
	// tmux kill-server, and the daemon restarted too: a new tmux server and a new manager on the
	// same store. The agents still run (abduco).
	term := &fakeTerm{sessions: map[string]*fakeSession{}}
	m := NewManager(Config{Store: e.st, Term: term, Containers: e.ctr, LeaBin: leaBin, UID: 1000, GID: 1000,
		Now: func() int64 { return 1000 }})
	if err := m.Ensure(bg, C, "/p"); err != nil {
		t.Fatal(err)
	}
	s := term.get(C)
	if slot := s.slot(); slot == nil || slot.agent != "a1" {
		t.Fatalf("slot=%+v, want a1 shown again", slot)
	}
	if snap, _ := m.Watch(bg, C, 0, time.Second); snap.Shown != C+"/a1" {
		t.Fatalf("shown %q", snap.Shown)
	}
	// An existing session is left as it is: the placeholder chosen by the user stays.
	if err := m.Stop(bg, C+"/a1"); err != nil {
		t.Fatal(err)
	}
	if err := m.Ensure(bg, C, ""); err != nil {
		t.Fatal(err)
	}
	if s.slot() != s.byRole(rolePlace) {
		t.Fatalf("slot=%+v, want the placeholder", s.slot())
	}
}

func TestARebuiltSessionWithoutTheLastShownAgentKeepsThePlaceholder(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	e.spawn(t, "a1")
	e.ctr.setLive(C) // a1 ended
	term := &fakeTerm{sessions: map[string]*fakeSession{}}
	m := NewManager(Config{Store: e.st, Term: term, Containers: e.ctr, LeaBin: leaBin, UID: 1000, GID: 1000,
		Now: func() int64 { return 1000 }})
	if err := m.Ensure(bg, C, "/p"); err != nil {
		t.Fatal(err)
	}
	if s := term.get(C); s.slot() != s.byRole(rolePlace) {
		t.Fatalf("slot=%+v", s.slot())
	}
}

// If the second resize of the repaint fails, the deck is left one column wider: the next
// reconcile restores its width.
func TestReconcileRestoresTheDeckWidth(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	deck := s.byRole(roleDeck)
	s.locked(func() { deck.width = DeckWidth + 1 })
	if err := e.m.Ensure(bg, C, ""); err != nil {
		t.Fatal(err)
	}
	if deck.width != DeckWidth {
		t.Fatalf("deck width %d, want %d", deck.width, DeckWidth)
	}
}

func TestUserClientsMostRecentFirst(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	s.locked(func() { s.clients = []string{"/dev/pts/1|0|10", "/dev/pts/2|0|30", "client-9|1|99", "/dev/pts/4|0"} })
	got, err := userClients(bg, s, C)
	if err != nil || !reflect.DeepEqual(got, []string{"/dev/pts/2", "/dev/pts/1", "/dev/pts/4"}) {
		t.Fatalf("clients=%q err=%v", got, err)
	}
}

func TestAttachedNeverCreatesASession(t *testing.T) {
	e := newEnv(t)
	if ok, err := e.m.Attached(bg, C); ok || err != nil {
		t.Fatalf("no session: ok=%v err=%v", ok, err)
	}
	if e.term.opens != 0 || e.term.get(C) != nil {
		t.Fatalf("Attached opened a session: opens=%d", e.term.opens)
	}
}

func TestAttachedReconnectsToAnExistingSessionAfterARestart(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	s.locked(func() { s.clients = []string{"ctl|1", "/dev/pts/3|0|5"} })
	m2 := NewManager(Config{Store: e.st, Term: e.term, Containers: e.ctr, LeaBin: leaBin, UID: 1000, GID: 1000, Now: func() int64 { return 1000 }})
	e.term.mu.Lock()
	e.term.names = []string{C}
	e.term.mu.Unlock()
	if ok, err := m2.Attached(bg, C); !ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestApprovalSettingsReachTheAgent(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "lea.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	term := &fakeTerm{sessions: map[string]*fakeSession{}}
	m := NewManager(Config{Store: st, Term: term, Containers: &fakeContainers{live: map[string][]string{}},
		LeaBin: leaBin, UID: 1000, GID: 1000, Now: func() int64 { return 1000 }, Approvals: "pane", ApprovalTimeout: 90})
	if err := m.Ensure(bg, C, "/p"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.New(bg, NewRequest{Container: C, Name: "a1"}); err != nil {
		t.Fatal(err)
	}
	cmd := strings.Join(term.get(C).byAgent("a1").cmd, " ")
	if !strings.Contains(cmd, "-e LEA_APPROVALS=pane") || !strings.Contains(cmd, "-e LEA_APPROVAL_TIMEOUT=90") {
		t.Fatalf("cmd=%s", cmd)
	}
}

func TestLockHTTPTokenReachesTheAgent(t *testing.T) {
	for _, on := range []bool{false, true} {
		st, err := store.Open(filepath.Join(t.TempDir(), "lea.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		term := &fakeTerm{sessions: map[string]*fakeSession{}}
		cfg := Config{Store: st, Term: term, Containers: &fakeContainers{live: map[string][]string{}},
			LeaBin: leaBin, UID: 1000, GID: 1000, Now: func() int64 { return 1000 }}
		if on {
			cfg.ProxyToken = func(container, agent string) string { return "tok-" + container + "-" + agent }
		}
		m := NewManager(cfg)
		if err := m.Ensure(bg, C, "/p"); err != nil {
			t.Fatal(err)
		}
		if _, err := m.New(bg, NewRequest{Container: C, Name: "a1"}); err != nil {
			t.Fatal(err)
		}
		cmd := strings.Join(term.get(C).byAgent("a1").cmd, " ")
		if got := strings.Contains(cmd, "-e LEA_PROXY_TOKEN=tok-"+C+"-a1 "); got != on {
			t.Fatalf("LockHTTP %v: cmd=%s", on, cmd)
		}
	}
}

func TestRequesterTokenReachesTheAgent(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "lea.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	term := &fakeTerm{sessions: map[string]*fakeSession{}}
	key := []byte("0123456789abcdef0123456789abcdef")
	cfg := Config{Store: st, Term: term, Containers: &fakeContainers{live: map[string][]string{}},
		LeaBin: leaBin, UID: 1000, GID: 1000, Now: func() int64 { return 1000 },
		RequesterToken: func(container, agent string) string { return RequesterToken(key, container, agent) }}
	m := NewManager(cfg)
	if err := m.Ensure(bg, C, "/p"); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a1", "a2"} {
		if _, err := m.New(bg, NewRequest{Container: C, Name: n}); err != nil {
			t.Fatal(err)
		}
	}
	t1, t2 := RequesterToken(key, C, "a1"), RequesterToken(key, C, "a2")
	if t1 == t2 || len(t1) != 32 || t1 != RequesterToken(key, C, "a1") || t1 == RequesterToken([]byte("another key, 32 bytes long......"), C, "a1") {
		t.Fatalf("tokens %q %q", t1, t2)
	}
	for n, tok := range map[string]string{"a1": t1, "a2": t2} {
		if cmd := strings.Join(term.get(C).byAgent(n).cmd, " "); !strings.Contains(cmd, "-e LEA_AGENT_TOKEN="+tok+" ") {
			t.Fatalf("%s: cmd=%s", n, cmd)
		}
	}
}

func TestSnapshotCarriesUnreadUsageAndTheOldestApproval(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	e.spawn(t, "a1")
	e.spawn(t, "a2")
	k1, k2 := C+"/a1", C+"/a2"
	for _, m := range []store.Message{
		{From: "user", To: k1, Kind: "note", Body: "x", At: 1},
		{From: k2, To: k1, Kind: "note", Body: "y", At: 1},
		{From: k2, To: "user", Kind: "result", Body: "done", At: 1},
	} {
		if _, err := e.st.Send(m); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.st.SetUsage(k1, store.Usage{CtxPct: 42, CostUSD: 1.2}); err != nil {
		t.Fatal(err)
	}
	id1, _ := e.st.AddApproval(k1, "Bash", `{"command":"make\n  test"}`, true, 2)
	e.st.AddApproval(k1, "Write", `{"file_path":"/p/x"}`, false, 3)
	snap, err := e.m.Watch(bg, C, 0, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]Row{}
	for _, r := range snap.Agents {
		rows[r.Key] = r
	}
	r1, r2 := rows[k1], rows[k2]
	if r1.Unread != 2 || r1.ForUser != 0 || r1.CtxPct != 42 || r1.CostUSD != 1.2 {
		t.Fatalf("a1=%+v", r1)
	}
	if r1.Approval == nil || *r1.Approval != (Approval{ID: id1, Tool: "Bash", Summary: "make test", Always: true}) {
		t.Fatalf("a1 approval=%+v", r1.Approval)
	}
	if r2.Unread != 0 || r2.ForUser != 1 || r2.CtxPct != -1 || r2.CostUSD != -1 || r2.Approval != nil {
		t.Fatalf("a2=%+v", r2)
	}
}

func TestSummary(t *testing.T) {
	long := strings.Repeat("x", 300)
	for _, tc := range []struct{ tool, input, want string }{
		{"Bash", `{"command":"make test","description":"run"}`, "make test"},
		{"Bash", "{\"command\":\"a\\n\\tb   c\"}", "a b c"},
		{"Write", `{"file_path":"/p/x.go","content":"secret"}`, "/p/x.go"},
		{"WebFetch", `{"url":"https://example.org"}`, "https://example.org"},
		{"Other", `{"q":1}`, `{"q":1}`},
		{"Other", `not json`, "not json"},
		{"Bash", `{"command":"` + long + `"}`, strings.Repeat("x", 200) + "…"},
	} {
		if got := Summary(tc.tool, tc.input); got != tc.want {
			t.Errorf("Summary(%s, %q)=%q, want %q", tc.tool, tc.input, got, tc.want)
		}
	}
}

func TestSnapshotStaysSmallWithOversizedToolNames(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	for i := 0; i < 20; i++ {
		name := fmt.Sprintf("a%d", i)
		e.spawn(t, name)
		e.st.AddApproval(C+"/"+name, strings.Repeat("T\n", 400000), "{\"command\":\""+strings.Repeat("\x1b", 300)+"\"}", false, 1)
	}
	snap, err := e.m.Watch(bg, C, 0, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(snap)
	if len(b) > 64*1024 {
		t.Fatalf("snapshot is %d bytes", len(b))
	}
	if sm := snap.Agents[0].Approval.Summary; len([]rune(sm)) != 201 || strings.Contains(sm, "\x1b") {
		t.Fatalf("summary %q", sm)
	}
	if tool := snap.Agents[0].Approval.Tool; strings.ContainsAny(tool, "\n") || len([]rune(tool)) > 65 {
		t.Fatalf("tool %q", tool)
	}
}

func TestSummaryOfHostileInput(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"{\"command\":\"a\\u0007b\\u001b[31m\"}", "a?b?[31m"},
		{"{\"command\":\"a\\u202eb\\u200bc\\u0085d\\u007fe\"}", "a?b?c d?e"},
		{"{\"command\":\"\xff\xfe\"}", "\ufffd\ufffd"},
		{`[1,2]`, "[1,2]"},
		{`"str"`, `"str"`},
		{`null`, "null"},
		{``, ""},
		{"a\x00b\x1b]0;x\x07", "a?b?]0;x?"},
	} {
		if got := Summary("Bash", tc.in); got != tc.want {
			t.Errorf("Summary(%q)=%q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPopupOpensTheThreadOnTheLatestTerminal(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	e.spawn(t, "a1")
	s.locked(func() { s.clients = []string{"/dev/pts/1|0|10", "/dev/pts/2|0|30", "client-9|1|99"} })
	if err := e.m.Popup(bg, C+"/a1", true); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Popup(bg, C+"/a1", false); err != nil {
		t.Fatal(err)
	}
	want := []string{"/dev/pts/2 " + leaBin + " deck --thread " + C + "/a1 --compose", "/dev/pts/2 " + leaBin + " deck --thread " + C + "/a1"}
	if !reflect.DeepEqual(e.term.popups, want) {
		t.Fatalf("popups=%q", e.term.popups)
	}
}

func TestPopupNeedsAnAgentAndATerminal(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	e.spawn(t, "a1")
	if err := e.m.Popup(bg, C+"/nobody", false); err == nil {
		t.Fatal("a popup for an unknown agent")
	}
	s.locked(func() { s.clients = []string{"client-9|1|99"} })
	if err := e.m.Popup(bg, C+"/a1", false); err == nil || !strings.Contains(err.Error(), "no terminal") {
		t.Fatalf("err=%v", err)
	}
	if len(e.term.popups) != 0 {
		t.Fatalf("popups=%q", e.term.popups)
	}
}

func lastArg(s *fakeSession, name string) string {
	p := s.byAgent(name)
	if p == nil || len(p.cmd) == 0 {
		return ""
	}
	return p.cmd[len(p.cmd)-1]
}

func TestNewRecordsTheRoleAndOpensATask(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	r := e.spawn(t, "")
	if r.Role != "dev" || r.Parent != "" {
		t.Fatalf("row=%+v", r)
	}
	a, _ := e.st.AgentByKey(r.Key)
	tk, err := e.st.TaskOf(r.Key)
	if err != nil || a.TaskID != tk.ID || tk.Title != "" || tk.Container != C {
		t.Fatalf("agent=%+v task=%+v err=%v", a, tk, err)
	}
}

func TestNewWithABriefPostsItAndPointsTheAgentAtIt(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	r, err := e.m.New(bg, NewRequest{Container: C, Role: "reviewer", Brief: "  fix the cart\nthen run the tests"})
	if err != nil {
		t.Fatal(err)
	}
	ms, _ := e.st.Unread(r.Key, 8)
	if len(ms) != 1 || ms[0].From != "user" || ms[0].Kind != "request" || ms[0].Body != "  fix the cart\nthen run the tests" {
		t.Fatalf("inbox=%+v", ms)
	}
	last := lastArg(s, r.Name)
	if !strings.HasPrefix(last, "You are the lea agent claude-1 (role reviewer).") || strings.ContainsAny(last, "\r\n\x1b") ||
		!strings.Contains(last, "msg_inbox") || !strings.Contains(last, "agents_request_spawn") {
		t.Fatalf("first prompt %q", last)
	}
	if tk, _ := e.st.TaskOf(r.Key); tk.Title != "fix the cart" {
		t.Fatalf("title %q", tk.Title)
	}
	plain := e.spawn(t, "plain")
	if got := lastArg(s, plain.Name); got != "--" {
		t.Fatalf("an agent without a brief got a prompt: %q", got)
	}
}

func TestNewForALeadsChildRecordsTheParent(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	lead, err := e.m.New(bg, NewRequest{Container: C, Name: "boss", Role: "lead", Brief: "plan the work"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lastArg(s, "boss"), "agents_spawn") {
		t.Fatalf("lead prompt %q", lastArg(s, "boss"))
	}
	kid, err := e.m.New(bg, NewRequest{Container: C, Role: "dev", Parent: lead.Key, NamePrefix: "dev", Brief: "do it",
		BriefFrom: lead.Key, Hidden: true})
	if err != nil {
		t.Fatal(err)
	}
	if kid.Name != "dev-1" || kid.Parent != lead.Key || kid.Role != "dev" {
		t.Fatalf("kid=%+v", kid)
	}
	if ms, _ := e.st.Unread(kid.Key, 8); len(ms) != 1 || ms[0].From != lead.Key {
		t.Fatalf("kid inbox=%+v", ms)
	}
	// The child reports to its lead in the lead's inbox, not only on the board.
	if p := lastArg(s, "dev-1"); !strings.Contains(p, "Your lead is boss:") || !strings.Contains(p, "msg_send (to boss,") {
		t.Fatalf("kid prompt %q", p)
	}
	if strings.Contains(lastArg(s, "boss"), "Your lead is") {
		t.Fatalf("the user's lead got a lead: %q", lastArg(s, "boss"))
	}
	if _, err := e.m.New(bg, NewRequest{Container: C, Role: "lead", Parent: lead.Key}); err == nil || !strings.Contains(err.Error(), "never spawns") {
		t.Fatalf("a lead's child made a lead: err=%v", err)
	}
	if _, err := e.m.New(bg, NewRequest{Container: C, Role: "admin"}); err == nil || !strings.Contains(err.Error(), "unknown role") {
		t.Fatalf("unknown role: err=%v", err)
	}
}

func TestNewHiddenLeavesTheSlotAlone(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	shown := e.spawn(t, "a1")
	mark := s.mark()
	if _, err := e.m.New(bg, NewRequest{Container: C, Name: "b1", Hidden: true}); err != nil {
		t.Fatal(err)
	}
	if s.slot() != s.byAgent("a1") {
		t.Fatal("a hidden agent took the slot")
	}
	if p := s.byAgent("b1"); p == nil || p.win == s.byRole(roleDeck).win {
		t.Fatalf("hidden agent pane=%+v", p)
	}
	for _, l := range s.since(mark) {
		for _, bad := range []string{"swap-pane", "select-pane", "select-window"} {
			if strings.HasPrefix(l, bad) {
				t.Fatalf("a hidden spawn ran %q", l)
			}
		}
	}
	if snap, _ := e.m.Watch(bg, C, 0, time.Second); snap.Shown != shown.Key {
		t.Fatalf("shown=%q", snap.Shown)
	}
}

func TestNewInARepoOnABranch(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	projects := t.TempDir()
	if err := os.Mkdir(filepath.Join(projects, "shop"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.ctr.projects[C] = projects
	r, err := e.m.New(bg, NewRequest{Container: C, Repo: "shop", Branch: "feat/cart", Brief: "x"})
	if err != nil {
		t.Fatal(err)
	}
	// Claude trusts a directory, not its subdirectories: the agent starts in the projects
	// directory (trusted, and holding both the repo and the worktrees) and is told its repo.
	if r.Workdir != projects || r.Repo != "shop" || r.Branch != "feat/cart" {
		t.Fatalf("row=%+v", r)
	}
	wt := filepath.Join(projects, ".lea", "worktrees", "shop", r.Name)
	last := lastArg(s, r.Name)
	if !strings.Contains(last, "branch feat/cart in your own git worktree "+wt) ||
		!strings.Contains(last, "repository "+filepath.Join(projects, "shop")) {
		t.Fatalf("first prompt %q", last)
	}
}

func TestNewInARepoWithoutBranchOrBriefIsToldItsRepo(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	projects := t.TempDir()
	if err := os.Mkdir(filepath.Join(projects, "shop"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.ctr.projects[C] = projects
	r, err := e.m.New(bg, NewRequest{Container: C, Repo: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Workdir != projects {
		t.Fatalf("workdir %q, want the projects directory", r.Workdir)
	}
	if last := lastArg(s, r.Name); !strings.Contains(last, "Your repository is "+filepath.Join(projects, "shop")) {
		t.Fatalf("first prompt %q", last)
	}
}

func TestNewRefusesBadReposAndBranches(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	projects := t.TempDir()
	os.Mkdir(filepath.Join(projects, "shop"), 0o755)
	os.WriteFile(filepath.Join(projects, "file"), nil, 0o644)
	os.Symlink(filepath.Join(projects, "shop"), filepath.Join(projects, "link"))
	e.ctr.projects[C] = projects
	for _, repo := range []string{".lea", "..", "a/b", "nope", "file", "link", "-x", strings.Repeat("a", 65)} {
		if _, err := e.m.New(bg, NewRequest{Container: C, Repo: repo}); err == nil {
			t.Fatalf("repo %q accepted", repo)
		}
	}
	for _, br := range []string{"a..b", "-x", "x/", "x.lock", "a b", "a\nb", "a//b", "x/.y", "a@{1}", "x.", strings.Repeat("b", 101)} {
		if _, err := e.m.New(bg, NewRequest{Container: C, Branch: br}); err == nil {
			t.Fatalf("branch %q accepted", br)
		}
	}
	if as, _ := e.st.SpawnedAgents(C); len(as) != 0 {
		t.Fatalf("refused spawns left rows: %+v", as)
	}
	delete(e.ctr.projects, C)
	if _, err := e.m.New(bg, NewRequest{Container: C, Repo: "shop"}); err == nil || !strings.Contains(err.Error(), "lea.projects") {
		t.Fatalf("no projects dir: err=%v", err)
	}
}

func TestNewCapsChildren(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	lead, err := e.m.New(bg, NewRequest{Container: C, Name: "boss", Role: "lead"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < roles.MaxChildren; i++ {
		if _, err := e.m.New(bg, NewRequest{Container: C, Parent: lead.Key, NamePrefix: "dev", Hidden: true}); err != nil {
			t.Fatalf("child %d: %v", i, err)
		}
	}
	if _, err := e.m.New(bg, NewRequest{Container: C, Parent: lead.Key, NamePrefix: "dev", Hidden: true}); !errors.Is(err, store.ErrLimit) ||
		!strings.Contains(err.Error(), "max_children 20") {
		t.Fatalf("one child too many: err=%v", err)
	}
}

func TestNewRollsBackTheTaskAndTheBrief(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	s.locked(func() { s.fail = "new-window" })
	if _, err := e.m.New(bg, NewRequest{Container: C, Name: "x", Brief: "do it"}); err == nil {
		t.Fatal("no error")
	}
	if _, err := e.st.AgentByKey(C + "/x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("agent row left: err=%v", err)
	}
	if n, _ := e.st.UnreadCount(C + "/x"); n != 0 {
		t.Fatalf("%d unread brief(s) left for a later agent of the same name", n)
	}
	if ts, _ := e.st.Tasks(C, 10); len(ts) != 0 {
		t.Fatalf("tasks left: %+v", ts)
	}
}

func TestNewChecksTheParentAndTheBrief(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	dev := e.spawn(t, "plain")
	lead, err := e.m.New(bg, NewRequest{Container: C, Name: "boss", Role: "lead", Hidden: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		r    NewRequest
		want string
	}{
		{NewRequest{Container: C, Parent: dev.Key}, "cannot start agents"},
		{NewRequest{Container: C, Parent: C + "/nobody"}, "unknown parent"},
		{NewRequest{Container: C, Parent: "lea-other/boss"}, "unknown parent"},
		{NewRequest{Container: C, Parent: lead.Key, Brief: "a\x1b[2Jb"}, "control character"},
		{NewRequest{Container: C, Parent: lead.Key, Brief: "\xff"}, "UTF-8"},
		{NewRequest{Container: C, Parent: lead.Key, Brief: strings.Repeat("b", MaxBrief+1)}, "limit"},
	} {
		if _, err := e.m.New(bg, tc.r); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%+v: err=%v, want %q", tc.r.Parent, err, tc.want)
		}
	}
	if kids, _ := e.st.Children(lead.Key); len(kids) != 0 {
		t.Fatalf("refused children left rows: %+v", kids)
	}
	if err := e.st.SetState(lead.Key, "done", "", 5); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.New(bg, NewRequest{Container: C, Parent: lead.Key}); err == nil || !strings.Contains(err.Error(), "has ended") {
		t.Fatalf("ended parent: err=%v", err)
	}
}

func TestNewChecksWhereTheBriefComesFrom(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	lead, err := e.m.New(bg, NewRequest{Container: C, Name: "boss", Role: "lead", Hidden: true})
	if err != nil {
		t.Fatal(err)
	}
	other, err := e.m.New(bg, NewRequest{Container: C, Name: "other", Hidden: true})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := e.st.Register(store.NewAgent{Container: "lea-other", Name: "x", Profile: "claude", Workdir: "/p", Role: "dev"}, store.Caps{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		r    NewRequest
		want string
	}{
		{"a child's brief is never from the user", NewRequest{Container: C, Parent: lead.Key, Brief: "x", BriefFrom: "user"}, "comes from its parent"},
		{"nor by default", NewRequest{Container: C, Parent: lead.Key, Brief: "x"}, "comes from its parent"},
		{"unknown sender", NewRequest{Container: C, Brief: "x", BriefFrom: C + "/ghost"}, "no such agent"},
		{"another container's agent", NewRequest{Container: C, Brief: "x", BriefFrom: foreign.Key}, "no such agent"},
	} {
		if _, err := e.m.New(bg, tc.r); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err=%v, want %q", tc.name, err, tc.want)
		}
	}
	if _, err := e.m.New(bg, NewRequest{Container: C, Brief: "from a peer", BriefFrom: other.Key, Hidden: true}); err != nil {
		t.Fatalf("a registered agent of the container may send a brief: %v", err)
	}
	// BriefReplyTo: a message of BriefFrom, whose hops the brief carries.
	mine, _ := e.st.Send(store.Message{From: other.Key, To: lead.Key, Kind: "request", Body: "q", Hops: 3, At: 5})
	theirs, _ := e.st.Send(store.Message{From: lead.Key, To: C + "/x", Kind: "note", Body: "q", At: 5})
	if _, err := e.m.New(bg, NewRequest{Container: C, Name: "k1", Brief: "x", BriefFrom: other.Key, BriefReplyTo: theirs.ID, Hidden: true}); err == nil ||
		!strings.Contains(err.Error(), "not a message of") {
		t.Fatalf("a reply to someone else's message: err=%v", err)
	}
	if _, err := e.m.New(bg, NewRequest{Container: C, Name: "k2", Brief: "x", BriefFrom: other.Key, BriefReplyTo: 9999, Hidden: true}); err == nil {
		t.Fatal("a reply to a missing message was accepted")
	}
	k, err := e.m.New(bg, NewRequest{Container: C, Name: "k3", Brief: "x", BriefFrom: other.Key, BriefReplyTo: mine.ID, Hidden: true})
	if err != nil {
		t.Fatal(err)
	}
	ms, _ := e.st.Unread(k.Key, 8)
	if len(ms) != 1 || ms[0].ReplyTo != mine.ID || ms[0].Hops != 4 || ms[0].Thread != mine.Thread {
		t.Fatalf("brief=%+v", ms)
	}
	deep, _ := e.st.Send(store.Message{From: other.Key, To: lead.Key, Kind: "request", Body: "q", Hops: 8, At: 5})
	if _, err := e.m.New(bg, NewRequest{Container: C, Name: "k4", Brief: "x", BriefFrom: other.Key, BriefReplyTo: deep.ID, Hidden: true}); err == nil ||
		!strings.Contains(err.Error(), "hops") {
		t.Fatalf("an over-long reply chain: err=%v", err)
	}
}

func TestNewOnABranchWithoutABriefStillTellsTheBranch(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	projects := t.TempDir()
	if err := os.Mkdir(filepath.Join(projects, "shop"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.ctr.projects[C] = projects
	r, err := e.m.New(bg, NewRequest{Container: C, Repo: "shop", Branch: "feat/cart"})
	if err != nil {
		t.Fatal(err)
	}
	last := lastArg(s, r.Name)
	wt := filepath.Join(projects, ".lea", "worktrees", "shop", r.Name)
	if !strings.Contains(last, "branch feat/cart in your own git worktree "+wt) || strings.Contains(last, "msg_inbox") {
		t.Fatalf("first prompt %q", last)
	}
	if n, _ := e.st.UnreadCount(r.Key); n != 0 {
		t.Fatalf("%d messages for an agent without a brief", n)
	}
}

func TestNewRollsBackEvenWhenTheRequestIsCancelled(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	// The pane exists (abduco runs lea-agent) when showing it fails and the request is cancelled:
	// the cleanup must still kill the pane, stop the agent and forget the row.
	s.locked(func() { s.fail = "swap-pane"; s.onFail = cancel })
	if _, err := e.m.New(ctx, NewRequest{Container: C, Name: "x", Brief: "do it"}); err == nil {
		t.Fatal("no error")
	}
	if ctx.Err() == nil {
		t.Fatal("the request was not cancelled: the test proves nothing")
	}
	if !reflect.DeepEqual(e.ctr.stopped, []string{C + "/x"}) {
		t.Fatalf("stopped=%q", e.ctr.stopped)
	}
	if _, err := e.st.AgentByKey(C + "/x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("agent row left after a cancelled spawn: err=%v", err)
	}
}

func TestSnapshotCarriesTheTaskStatus(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	r := e.spawn(t, "a1")
	if _, err := e.st.UpdateTask(r.Key, "writing tests", "", "", 2); err != nil {
		t.Fatal(err)
	}
	snap, err := e.m.Watch(bg, C, 0, time.Second)
	if err != nil || len(snap.Agents) != 1 || snap.Agents[0].Status != "writing tests" {
		t.Fatalf("snapshot=%+v err=%v", snap, err)
	}
	if a := snap.Agents[0]; a.Waiting || a.BlockedOn {
		t.Fatalf("nothing next nor blocking: %+v", a)
	}
	// A next step and a blocker reach the row as flags, never as text.
	if _, err := e.st.UpdateTask(r.Key, "waiting", "CI", "a token", 3); err != nil {
		t.Fatal(err)
	}
	snap, err = e.m.Watch(bg, C, 0, time.Second)
	if err != nil || !snap.Agents[0].Waiting || !snap.Agents[0].BlockedOn {
		t.Fatalf("snapshot=%+v err=%v", snap, err)
	}
	if b, _ := json.Marshal(snap.Agents[0]); strings.Contains(string(b), "a token") || strings.Contains(string(b), "CI") {
		t.Fatalf("the row carries the task text: %s", b)
	}
}

// The "agent ended" line is an agent pane's own: a dead deck, placeholder or shell pane keeps
// tmux's line, which says nothing about x.
func TestOnlyAgentPanesSayHowToRemoveThem(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	e.spawn(t, "a1")
	spawnedLive(e, "a2") // a pane given back by a reattach
	if err := e.m.Ensure(bg, C, ""); err != nil {
		t.Fatal(err)
	}
	e.openShell(t)
	for _, n := range []string{"a1", "a2"} {
		if p := s.byAgent(n); p == nil || !strings.Contains(p.remainFormat, "x in the deck removes it") {
			t.Errorf("%s: pane %+v", n, p)
		}
	}
	for _, p := range []*fpane{s.byRole(roleDeck), s.byRole(rolePlace), s.byShell("sh-1")} {
		if p.remainFormat != "" {
			t.Errorf("pane %+v has the agents' line", p)
		}
	}
}

// x without y on a done row: the agent goes only while it is still ended. A lead that spawned
// that name again in the meantime reused the row: the new, live agent is refused, not stopped.
func TestRemoveEndedRefusesAnAgentThatRunsAgain(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	r := e.spawn(t, "a1")
	if err := e.m.RemoveEnded(bg, r.Key); err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("a live agent was removed without confirmation: %v", err)
	}
	if len(e.ctr.stopped) != 0 || s.byAgent("a1") == nil {
		t.Fatalf("stopped=%v", e.ctr.stopped)
	}
	e.ctr.setLive(C)
	s.locked(func() { s.byAgent("a1").dead = true })
	if err := e.m.Ensure(bg, C, ""); err != nil {
		t.Fatal(err)
	}
	if e.state(t, "a1") != "done" {
		t.Fatalf("state %q", e.state(t, "a1"))
	}
	if err := e.m.RemoveEnded(bg, r.Key); err != nil {
		t.Fatal(err)
	}
	if s.byAgent("a1") != nil {
		t.Fatal("the ended agent's pane stays")
	}
	if _, err := e.st.AgentByKey(r.Key); err == nil {
		t.Fatal("the ended agent is still registered")
	}
	if err := e.m.RemoveEnded(bg, ShellKey(C, "sh-1")); err == nil {
		t.Fatal("RemoveEnded took a shell's key")
	}
}

// codexArgs are what follows lea-agent run <name> --cli codex in an agent's pane.
func codexArgs(t *testing.T, s *fakeSession, name string) []string {
	t.Helper()
	var cmd []string
	s.locked(func() {
		if p := s.byAgent(name); p != nil {
			cmd = p.cmd
		}
	})
	i := slices.Index(cmd, "run")
	if i < 0 || len(cmd) < i+5 || cmd[i+1] != name || cmd[i+2] != "--cli" || cmd[i+3] != "codex" || cmd[i+4] != "--" {
		t.Fatalf("pane of %s: %q", name, cmd)
	}
	return cmd[i+5:]
}

// An agent on Codex runs codex with lea's flags (MCP server, notify, sandbox), its model and its
// first prompt; started again, it resumes its thread with the same model.
func TestCodexAgentRunsCodexAndResumesItsThread(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	r, err := e.m.New(bg, NewRequest{Container: C, Name: "cx", Profile: "codex", Model: "gpt-5.1-codex", Brief: "do it", Hidden: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Profile != "codex" || r.Model != "gpt-5.1-codex" {
		t.Fatalf("row=%+v", r)
	}
	s := e.term.get(C)
	got := codexArgs(t, s, "cx")
	j := strings.Join(got, " ")
	for _, w := range []string{"--sandbox workspace-write --ask-for-approval on-request", `mcp_servers.lea.env.LEA_AGENT="cx"`,
		`notify=["/usr/local/bin/lea","hook","codex","notify"]`, `projects."/p".trust_level="untrusted"`, "--model gpt-5.1-codex"} {
		if !strings.Contains(j, w) {
			t.Fatalf("missing %s in %q", w, got)
		}
	}
	if !strings.Contains(got[len(got)-1], "Your brief is waiting") {
		t.Fatalf("no first prompt: %q", got)
	}
	e.st.SetState(C+"/cx", "idle", sid1, 1001)
	if err := e.m.MarkStopped(bg, C, false); err != nil {
		t.Fatal(err)
	}
	re := e.restarted(t)
	if err := re.m.ResumeStopped(bg, C); err != nil {
		t.Fatal(err)
	}
	got = codexArgs(t, re.term.get(C), "cx")
	if got[0] != "resume" || got[1] != sid1 || !strings.Contains(strings.Join(got, " "), "--model gpt-5.1-codex") ||
		strings.Contains(got[len(got)-1], "Your brief") {
		t.Fatalf("restarted with %q", got)
	}
}

// LEA_PROVIDER and LEA_MODEL apply to an agent started without a provider; an endpoint provider
// of the providers file runs Codex against the forwarder, never with its key or upstream.
func TestDefaultProviderAndEndpointProviders(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	reg := providers.Builtin()
	ep, err := providers.Parse(strings.NewReader("lan http://10.0.0.5:11434/v1 model=qwen3\n"),
		providers.Options{Internal: providers.ParseHosts(strings.NewReader("10.0.0.5 11434\n")), UID: -1})
	if err != nil {
		t.Fatal(err)
	}
	e.m.cfg.Providers = func() (*providers.Registry, error) { return providers.WithEndpoints(reg, ep), nil }
	e.m.cfg.DefaultProvider, e.m.cfg.DefaultModel = "codex", "gpt-5.1-codex"
	r, err := e.m.New(bg, NewRequest{Container: C, Hidden: true})
	if err != nil || r.Profile != "codex" || r.Model != "gpt-5.1-codex" || r.Name != "codex-1" {
		t.Fatalf("row=%+v err=%v", r, err)
	}
	r, err = e.m.New(bg, NewRequest{Container: C, Profile: "lan", Hidden: true})
	if err != nil || r.Profile != "lan" || r.Model != "qwen3" {
		t.Fatalf("row=%+v err=%v", r, err)
	}
	j := strings.Join(codexArgs(t, e.term.get(C), r.Name), " ")
	if !strings.Contains(j, `base_url="http://lea-proxy:4400/lan"`) || strings.Contains(j, "10.0.0.5") {
		t.Fatalf("args %s", j)
	}
	if _, err := e.m.New(bg, NewRequest{Container: C, Profile: "gone", Hidden: true}); err == nil {
		t.Fatal("an unknown provider was started")
	}
}
