package agents

import (
	"reflect"
	"strings"
	"testing"
)

// checkLayout asserts the deck window lea wants: one live deck pane running the deck, on the
// left, at DeckWidth, beside exactly one slot; no pane lea did not tag; one window named deck.
func checkLayout(t *testing.T, s *fakeSession) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var decks, inDeck []*fpane
	for _, p := range s.panes {
		if p.role == "" && p.agent == "" && p.shell == "" {
			t.Errorf("untagged pane %s (%q) is left", p.id, p.cmd)
		}
		if p.role == roleDeck {
			decks = append(decks, p)
		}
	}
	if len(decks) != 1 {
		t.Fatalf("%d deck panes: %+v", len(decks), s.panes)
	}
	d := decks[0]
	if d.dead || !reflect.DeepEqual(d.cmd, []string{leaBin, "deck", "--container", C}) {
		t.Errorf("deck pane %+v", d)
	}
	for _, p := range s.panes {
		if p.win == d.win {
			inDeck = append(inDeck, p)
		}
	}
	if len(inDeck) != 2 {
		t.Fatalf("the deck window holds %d panes, want the deck and a slot: %+v", len(inDeck), inDeck)
	}
	slot := inDeck[0]
	if slot == d {
		slot = inDeck[1]
	}
	if !s.atLeft(d) || s.atLeft(slot) {
		t.Errorf("deck at left %v, slot at left %v: the deck must be the left column", s.atLeft(d), s.atLeft(slot))
	}
	if d.width != DeckWidth {
		t.Errorf("deck width %d, want %d", d.width, DeckWidth)
	}
	if w := s.markedWindows(); len(w) != 1 || w[0] != d.win {
		t.Errorf("windows marked @lea_deck %q, want only the deck's %s (the conf's resize hook follows the mark)", w, d.win)
	}
}

// mutating reports whether a tmux command changes the session (anything but a query).
func mutating(line string) bool {
	for _, q := range []string{"list-panes", "display-message", "list-clients"} {
		if strings.HasPrefix(line, q+" ") {
			return false
		}
	}
	return true
}

// checkSettled asserts that a repair converged: another reconcile finds nothing to change, so the
// notifications of a repair never bring an endless chain of repairs.
func checkSettled(t *testing.T, e env, s *fakeSession) {
	t.Helper()
	mark := s.mark()
	if err := e.m.Ensure(bg, C, ""); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	for _, l := range s.since(mark) {
		if mutating(l) {
			t.Errorf("a second reconcile changed the session: %q", l)
		}
	}
}

func spawnedLive(e env, names ...string) {
	for _, n := range names {
		e.st.RegisterAgent(C, n, "claude", "/p", 900)
	}
	e.ctr.setLive(C, names...)
}

// The state the user's host was left in: the deck pane gone, a single untagged pane running the
// placeholder's command, no agent pane, while claude-1 still runs in its container. lea repairs
// it, and claude-1 gets its terminal back (attached, never started again).
func TestEnsureRepairsASessionThatLostItsDeck(t *testing.T) {
	e := newEnv(t)
	spawnedLive(e, "claude-1")
	e.st.SetLastShown(C, C+"/claude-1")
	s := e.term.put(C, map[string]string{"@0": "deck"},
		&fpane{id: "%1", win: "@0", cmd: []string{leaBin, "deck", "--placeholder"}, width: 200})
	if err := e.m.Ensure(bg, C, ""); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	checkLayout(t, s)
	checkSettled(t, e, s)
	if s.pane("%1") != nil {
		t.Error("the untagged pane is still there")
	}
	if !s.logged("select-window -t " + s.byRole(roleDeck).win) {
		t.Errorf("the user's terminal is not brought to the new deck window: %q", s.log)
	}
	p := s.byAgent("claude-1")
	if p == nil || !reflect.DeepEqual(p.cmd[len(p.cmd)-3:], []string{"abduco", "-a", "ag-claude-1"}) {
		t.Fatalf("claude-1 pane %+v", p)
	}
	if s.slot() != p {
		t.Errorf("slot %+v, want claude-1 shown again", s.slot())
	}
	if s.byRole(rolePlace) == nil {
		t.Error("no placeholder")
	}
	if e.state(t, "claude-1") == "done" {
		t.Error("claude-1 was marked done")
	}
}

// The deck pane is gone and the agent it showed is alone in the old deck window: a new deck
// window is made, and the old one is renamed so only one window is named deck.
func TestEnsureRebuildsTheDeckBesideTheShownAgent(t *testing.T) {
	e := newEnv(t)
	spawnedLive(e, "a1")
	e.st.SetLastShown(C, C+"/a1")
	s := e.term.put(C, map[string]string{"@0": "deck", "@1": ""},
		&fpane{id: "%1", win: "@0", agent: "a1", width: 200},
		&fpane{id: "%2", win: "@1", role: rolePlace, cmd: []string{leaBin, "deck", "--placeholder"}, width: 200})
	if err := e.m.Ensure(bg, C, ""); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	checkLayout(t, s)
	checkSettled(t, e, s)
	if slot := s.slot(); slot == nil || slot.agent != "a1" {
		t.Errorf("slot %+v, want a1", slot)
	}
}

// "split right" from the old pane menu left a shell (or its dead pane) in the deck window.
func TestEnsureRemovesAPaneTheUserSplit(t *testing.T) {
	for _, dead := range []bool{false, true} {
		e := newEnv(t)
		s := e.ensure(t, "/p")
		s.locked(func() {
			p := &fpane{id: "%40", win: s.byRole(roleDeck).win, cmd: []string{"/bin/sh"}, dead: dead}
			s.panes = append(s.panes, p)
		})
		if err := e.m.Ensure(bg, C, ""); err != nil {
			t.Fatal(err)
		}
		checkLayout(t, s)
		checkSettled(t, e, s)
		checkSettled(t, e, s)
		if s.pane("%40") != nil {
			t.Errorf("dead=%v: the split pane is still there", dead)
		}
	}
}

// A stray pane in a window of its own (an old server's) is not lea's either.
func TestEnsureRemovesAStrayWindow(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	s.locked(func() {
		s.names["@40"] = "sh"
		s.panes = append(s.panes, &fpane{id: "%40", win: "@40", cmd: []string{"/bin/sh"}})
	})
	if err := e.m.Ensure(bg, C, ""); err != nil {
		t.Fatal(err)
	}
	checkLayout(t, s)
	checkSettled(t, e, s)
	if s.pane("%40") != nil {
		t.Error("the stray pane is still there")
	}
	// The user's terminal may have been on the window that went: it is brought to the deck.
	if !s.logged("select-window -t " + s.byRole(roleDeck).win) {
		t.Errorf("the user's terminal is not brought back to the deck: %q", s.log)
	}
}

// An agent's pane joined into the deck window beside the slot goes back to a window of its own,
// named after it; the agent keeps running.
func TestEnsureParksAnExtraAgentPane(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	e.spawn(t, "a1")
	e.spawn(t, "a2")
	e.ctr.setLive(C, "a1", "a2")
	// a2 is in the slot; a1 is moved into the deck window too.
	s.locked(func() {
		a1 := s.byAgent("a1")
		a1.win = s.byRole(roleDeck).win
		s.move(a1, nil)
	})
	if err := e.m.Ensure(bg, C, ""); err != nil {
		t.Fatal(err)
	}
	checkLayout(t, s)
	checkSettled(t, e, s)
	a1 := s.byAgent("a1")
	if a1 == nil || a1.win == s.byRole(roleDeck).win || s.names[a1.win] != "a1" {
		t.Fatalf("a1 %+v in window %q", a1, s.names[a1.win])
	}
	if !s.logged("break-pane -d -s " + a1.id + " -n a1") {
		t.Errorf("log %q", s.log)
	}
	if s.slot().agent != "a2" {
		t.Errorf("slot %+v, want a2 kept", s.slot())
	}
}

// The pane menu's swap put the agent on the left and the deck on the right.
func TestEnsurePutsTheDeckBackOnTheLeft(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	e.spawn(t, "a1")
	e.ctr.setLive(C, "a1")
	s.locked(func() { s.move(s.byRole(roleDeck), nil) })
	if s.atLeft(s.byRole(roleDeck)) {
		t.Fatal("the test did not move the deck")
	}
	if err := e.m.Ensure(bg, C, ""); err != nil {
		t.Fatal(err)
	}
	checkLayout(t, s)
	checkSettled(t, e, s)
	if s.slot().agent != "a1" {
		t.Errorf("slot %+v, want a1", s.slot())
	}
}

// The deck and the slot stacked top to bottom are laid out side by side again.
func TestEnsureLaysTheDeckAndTheSlotSideBySide(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	s.locked(func() { s.stacked[s.byRole(roleDeck).win] = true })
	if err := e.m.Ensure(bg, C, ""); err != nil {
		t.Fatal(err)
	}
	checkLayout(t, s)
	checkSettled(t, e, s)
}

// A second deck pane (a leftover) is removed; the layout keeps one.
func TestEnsureKeepsOneDeck(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	s.locked(func() {
		s.names["@40"] = "x"
		s.panes = append(s.panes, &fpane{id: "%40", win: "@40", role: roleDeck, cmd: []string{leaBin, "deck", "--container", C}})
	})
	if err := e.m.Ensure(bg, C, ""); err != nil {
		t.Fatal(err)
	}
	checkLayout(t, s)
	checkSettled(t, e, s)
}

// A healthy session is left alone: no pane is created, killed, moved or broken out.
func TestEnsureLeavesAGoodLayoutAlone(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	e.spawn(t, "a1")
	e.ctr.setLive(C, "a1")
	if err := e.m.Ensure(bg, C, ""); err != nil {
		t.Fatal(err)
	}
	mark := s.mark()
	if err := e.m.Ensure(bg, C, ""); err != nil {
		t.Fatal(err)
	}
	for _, l := range s.since(mark) {
		if !strings.HasPrefix(l, "list-panes") {
			t.Errorf("a good layout was changed: %q", l)
		}
	}
	checkLayout(t, s)
}

// An agent's parked pane sits in a window named deck (an older lea named windows after agents,
// and the old deck window keeps its name when the deck is rebuilt). The deck window is the
// marked one: the agent is left where it is, and the repair settles at once.
func TestEnsureLeavesAPaneInAnotherWindowNamedDeckAlone(t *testing.T) {
	e := newEnv(t)
	spawnedLive(e, "a1", "a2")
	s := e.term.put(C, map[string]string{"@0": "deck", "@1": "deck", "@2": "placeholder"},
		&fpane{id: "%0", win: "@0", role: roleDeck, cmd: []string{leaBin, "deck", "--container", C}, width: DeckWidth},
		&fpane{id: "%1", win: "@0", agent: "a2", width: 165},
		&fpane{id: "%2", win: "@1", agent: "a1", width: 200},
		&fpane{id: "%3", win: "@2", role: rolePlace, cmd: []string{leaBin, "deck", "--placeholder"}, width: 200})
	s.marked["@1"] = true // a stale mark on the agent's window
	if err := e.m.Ensure(bg, C, ""); err != nil {
		t.Fatal(err)
	}
	checkLayout(t, s)
	checkSettled(t, e, s)
	if p := s.byAgent("a1"); p == nil || p.win != "@1" {
		t.Errorf("a1 was moved: %+v", p)
	}
	for _, l := range s.log {
		if strings.HasPrefix(l, "break-pane") {
			t.Errorf("a pane was broken out: %q", l)
		}
	}
}

// A session an older lea made has no mark on its deck window: the repair sets it, once.
func TestEnsureMarksTheDeckWindowOfAnOlderSession(t *testing.T) {
	e := newEnv(t)
	s := e.term.put(C, map[string]string{"@0": "deck", "@2": "placeholder"},
		&fpane{id: "%0", win: "@0", role: roleDeck, cmd: []string{leaBin, "deck", "--container", C}, width: DeckWidth},
		&fpane{id: "%3", win: "@0", role: rolePlace, cmd: []string{leaBin, "deck", "--placeholder"}, width: 165})
	if err := e.m.Ensure(bg, C, ""); err != nil {
		t.Fatal(err)
	}
	checkLayout(t, s)
	checkSettled(t, e, s)
}

// deck and placeholder are lea's own pane roles: no agent takes either name.
func TestNewRefusesTheReservedNames(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	for _, n := range []string{"deck", "placeholder"} {
		if _, err := e.m.New(bg, NewRequest{Container: C, Name: n}); err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Errorf("%s: err=%v", n, err)
		}
	}
	if as, _ := e.st.Agents(); len(as) != 0 {
		t.Fatalf("agents=%+v", as)
	}
}

// An agent named deck, before names were reserved: parked by a hidden spawn or a reattach in a
// window named deck. Nothing loops: the second reconcile changes nothing.
func TestEnsureSettlesWithAnAgentNamedDeck(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	spawnedLive(e, "deck")
	if err := e.m.Ensure(bg, C, ""); err != nil { // reattach: a parked window named deck
		t.Fatal(err)
	}
	if p := s.byAgent("deck"); p == nil || s.names[p.win] != "deck" {
		t.Fatalf("pane %+v", p)
	}
	checkLayout(t, s)
	checkSettled(t, e, s)
}

// lea (the launcher's Ensure, with a workdir) gives the keyboard to the deck: the pane that was
// active at the last detach may be the agent's, and a deck key typed there would reach the agent.
// The daemon's own reconciles (no workdir) never move the keyboard.
func TestEnsureFromTheLauncherFocusesTheDeck(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	e.spawn(t, "a1")
	e.ctr.setLive(C, "a1")
	if err := e.m.Ensure(bg, C, ""); err != nil {
		t.Fatal(err)
	}
	mark := s.mark()
	if err := e.m.Ensure(bg, C, ""); err != nil {
		t.Fatal(err)
	}
	for _, l := range s.since(mark) {
		if strings.HasPrefix(l, "select-pane") {
			t.Errorf("a daemon reconcile moved the keyboard: %q", l)
		}
	}
	mark = s.mark()
	if err := e.m.Ensure(bg, C, "/p"); err != nil {
		t.Fatal(err)
	}
	if !s.logged("select-pane -t " + s.byRole(roleDeck).id) {
		t.Errorf("lea did not give the keyboard to the deck: %q", s.since(mark))
	}
}
