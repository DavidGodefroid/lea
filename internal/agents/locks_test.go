package agents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeConsoles struct {
	mu      sync.Mutex
	state   map[string]ConsoleState
	calls   []string
	stopErr error
}

func (f *fakeConsoles) rec(s string) { f.calls = append(f.calls, s) }

func (f *fakeConsoles) ConsoleState(ctx context.Context, name string) (ConsoleState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state[name], nil
}
func (f *fakeConsoles) RemoveConsole(ctx context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rec("rm " + name)
	delete(f.state, name)
	return nil
}
func (f *fakeConsoles) StopConsole(ctx context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rec("stop " + name)
	delete(f.state, name)
	return f.stopErr
}
func (f *fakeConsoles) StopConsolesOf(ctx context.Context, owner string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rec("stop-of " + owner)
	return f.stopErr
}

// The deck's projects directory (its container's lea.projects label) maps to the deck's
// container C; any other directory to another container.
func nameOf(projects string) string {
	if projects == "/home/u/p" {
		return C
	}
	return "lea-other"
}

var sqlSpec = LockSpec{Enabled: true, Image: "locksql:0.8.0", ContainerName: nameOf, ConsoleDir: okDir}

// okDir is a ConsoleDir that accepts any directory without touching the disk.
func okDir(string) error { return nil }

func lockEnv(t *testing.T, spec LockSpec) (env, *fakeConsoles) {
	t.Helper()
	e := newEnv(t)
	e.ctr.projects[C] = "/home/u/p"
	fc := &fakeConsoles{state: map[string]ConsoleState{}}
	e.m.cfg.Consoles = fc
	e.m.cfg.LockSpec = func(tool string) (LockSpec, error) {
		if tool != "locksql" {
			return LockSpec{}, errors.New("unexpected tool " + tool)
		}
		return spec, nil
	}
	return e, fc
}

func lockPane(s *fakeSession, id string) *fpane {
	return s.find(func(p *fpane) bool { return p.role == roleLock && p.lock == id })
}

// A console opens from the fixed template only: no console flag, no log driver, labelled with the
// deck's container; in the lock slot under the agent's slot, with a short history, closing when
// the console exits, and without the keyboard.
func TestLockOpenRunsTheTemplateUnderTheSlotWithoutFocus(t *testing.T) {
	e, _ := lockEnv(t, LockSpec{Enabled: true, Image: "locksql:0.8.0", VaultAddr: "https://vault.example.com:8200", ContainerName: nameOf, ConsoleDir: okDir})
	s := e.ensure(t, "/p")
	slot := s.slot()
	n := len(s.log)
	st, err := e.m.LockOpen(bg, C, "locksql", "prod")
	if err != nil || st != "opened" {
		t.Fatalf("LockOpen: %q %v", st, err)
	}
	p := lockPane(s, "locksql/prod")
	if p == nil {
		t.Fatalf("no lock pane: %+v", s.panes)
	}
	want := []string{"docker", "run", "-it", "--rm", "--log-driver", "none", "--user", "locksql",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--read-only", "--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=64m",
		"--pids-limit", "256", "--memory", "1g", "--name", "locksql-console-prod",
		"--label", "lea.lock-for=" + C, "--add-host=host.docker.internal:host-gateway",
		"-v", "/home/u/p:/home/u/p:ro", "-v", "/home/u/p/.locksql:/home/u/p/.locksql", "-v", "locksql-run:/run/locksql", "-v", "locksql-home:/home/locksql",
		"-e", "VAULT_ADDR=https://vault.example.com:8200", "locksql:0.8.0", "console", "--project", "/home/u/p", "--profile", "prod"}
	if !reflect.DeepEqual(p.cmd, want) {
		t.Fatalf("console argv\n got %q\nwant %q", p.cmd, want)
	}
	if !p.remainOff || p.dead {
		t.Fatalf("lock pane %+v: remain-on-exit must be off", p)
	}
	deck := s.byRole(roleDeck)
	if p.win != deck.win || s.slot() != slot || s.index(p) != s.index(slot)+1 {
		t.Fatalf("the console is not under the slot: %+v", s.panes)
	}
	log := s.log[n:]
	hist := slices.Index(log, "set-option -t ="+C+": history-limit "+lockHistory)
	nw := slices.IndexFunc(log, func(l string) bool { return strings.HasPrefix(l, "new-window -d -t ="+C+": -n lock-locksql-prod ") })
	unset := slices.Index(log, "set-option -u -t ="+C+": history-limit")
	if hist < 0 || nw < hist || unset < nw {
		t.Fatalf("history-limit is not set around the pane's creation: %q", log)
	}
	// The pane is made running the placeholder, tagged and set to close on exit, and only then
	// respawned into the console: a console that exits at once leaves no dead pane.
	if !strings.HasSuffix(log[nw], " -- "+strings.Join(e.m.placeholderCmd(), " ")) {
		t.Fatalf("the pane is not made on the placeholder: %q", log[nw])
	}
	roe := slices.Index(log, "set-option -p -t "+p.id+" remain-on-exit off")
	resp := slices.Index(log, "respawn-pane -k -t "+p.id+" -- "+strings.Join(want, " "))
	tag := slices.Index(log, "set-option -p -t "+p.id+" "+optRole+" "+roleLock)
	if roe < nw || tag < nw || resp < roe || resp < tag {
		t.Fatalf("the console runs before its pane is tagged and set to close: %q", log)
	}
	if !slices.Contains(log, "join-pane -d -v -l "+lockSlotSize+" -s "+p.id+" -t "+slot.id) {
		t.Fatalf("not joined under the slot: %q", log)
	}
	for _, l := range log {
		if strings.HasPrefix(l, "select-pane") || strings.HasPrefix(l, "select-window") {
			t.Fatalf("opening a console moved the keyboard: %q", l)
		}
	}
	if _, ok := s.opts["history-limit"]; ok {
		t.Fatal("the session's history-limit is left")
	}
}

// Without a profile the console chooses (as lea locksql does): no --profile, the default name.
func TestLockOpenWithoutAProfile(t *testing.T) {
	e, _ := lockEnv(t, sqlSpec)
	s := e.ensure(t, "/p")
	if _, err := e.m.LockOpen(bg, C, "locksql", ""); err != nil {
		t.Fatal(err)
	}
	p := lockPane(s, "locksql")
	if p == nil || slices.Contains(p.cmd, "--profile") || !slices.Contains(p.cmd, "locksql-console") || slices.Contains(p.cmd, "-e") {
		t.Fatalf("pane %+v", p)
	}
}

func TestLockOpenTwiceShowsTheSameConsole(t *testing.T) {
	e, _ := lockEnv(t, sqlSpec)
	s := e.ensure(t, "/p")
	if _, err := e.m.LockOpen(bg, C, "locksql", "prod"); err != nil {
		t.Fatal(err)
	}
	n := len(s.panes)
	st, err := e.m.LockOpen(bg, C, "locksql", "prod")
	if err != nil || st != "already-open" || len(s.panes) != n {
		t.Fatalf("second open: %q %v, %d panes (was %d)", st, err, len(s.panes), n)
	}
}

// The lock slot holds one console: the last opened; the other parks in a window of its own and
// comes back when opened again.
func TestLockSlotHoldsTheLastOpenedConsole(t *testing.T) {
	e, _ := lockEnv(t, sqlSpec)
	s := e.ensure(t, "/p")
	deck := s.byRole(roleDeck)
	for _, prof := range []string{"prod", "staging"} {
		if _, err := e.m.LockOpen(bg, C, "locksql", prof); err != nil {
			t.Fatal(err)
		}
	}
	prod, stg := lockPane(s, "locksql/prod"), lockPane(s, "locksql/staging")
	if stg.win != deck.win || prod.win == deck.win || s.names[prod.win] != "lock-locksql-prod" {
		t.Fatalf("prod in %s (%q), staging in %s, deck in %s", prod.win, s.names[prod.win], stg.win, deck.win)
	}
	rows, err := e.m.LockList(bg, C)
	if err != nil || !reflect.DeepEqual(rows, []LockRow{{ID: "locksql/staging", Shown: true}, {ID: "locksql/prod"}}) &&
		!reflect.DeepEqual(rows, []LockRow{{ID: "locksql/prod"}, {ID: "locksql/staging", Shown: true}}) {
		t.Fatalf("LockList: %+v %v", rows, err)
	}
	if st, err := e.m.LockOpen(bg, C, "locksql", "prod"); err != nil || st != "already-open" {
		t.Fatalf("reopen: %q %v", st, err)
	}
	if prod.win != deck.win || stg.win == deck.win {
		t.Fatalf("prod is not back in the slot: %+v", s.panes)
	}
}

// Showing another agent, and the layout repair, leave the console in the lock slot; a console
// that died before its remain-on-exit was off goes at the next repair.
func TestLockSlotSurvivesShowAndRepair(t *testing.T) {
	e, _ := lockEnv(t, sqlSpec)
	s := e.ensure(t, "/p")
	e.ctr.live[C] = []string{"dev-1", "dev-2"}
	a := e.spawn(t, "dev-1")
	b := e.spawn(t, "dev-2")
	if _, err := e.m.LockOpen(bg, C, "locksql", "prod"); err != nil {
		t.Fatal(err)
	}
	deck := s.byRole(roleDeck)
	for _, k := range []string{a.Key, b.Key, a.Key} {
		if err := e.m.Focus(bg, k, true); err != nil {
			t.Fatal(err)
		}
		if err := e.m.Ensure(bg, C, ""); err != nil {
			t.Fatal(err)
		}
		p := lockPane(s, "locksql/prod")
		if p == nil || p.win != deck.win || s.slot() == p {
			t.Fatalf("after showing %s: lock pane %+v, slot %+v", k, p, s.slot())
		}
		if sl := s.slot(); sl.agent != strings.TrimPrefix(k, C+"/") {
			t.Fatalf("slot shows %+v, want %s", sl, k)
		}
	}
	s.locked(func() { lockPane(s, "locksql/prod").dead = true })
	if err := e.m.Ensure(bg, C, ""); err != nil {
		t.Fatal(err)
	}
	if p := lockPane(s, "locksql/prod"); p != nil {
		t.Fatalf("a dead console's pane is left: %+v", p)
	}
}

// A deck without a slot (no placeholder, no agent) keeps no console in its window.
func TestLockPaneParksWhenTheSlotIsGone(t *testing.T) {
	e, _ := lockEnv(t, sqlSpec)
	s := e.ensure(t, "/p")
	if _, err := e.m.LockOpen(bg, C, "locksql", "prod"); err != nil {
		t.Fatal(err)
	}
	s.locked(func() {
		s.exit(s.byRole(rolePlace))
		s.panes = slices.DeleteFunc(s.panes, func(p *fpane) bool { return p.role == rolePlace })
	})
	if err := e.m.Ensure(bg, C, ""); err != nil {
		t.Fatal(err)
	}
	deck := s.byRole(roleDeck)
	if sl := s.slot(); sl == nil || sl.role == roleLock {
		t.Fatalf("slot %+v: %+v", sl, s.panes)
	}
	if p := lockPane(s, "locksql/prod"); p == nil || p.win == deck.win && s.index(p) < s.index(s.slot()) {
		t.Fatalf("lock pane %+v in %+v", p, s.panes)
	}
}

func TestLockOpenRefusals(t *testing.T) {
	anyIsC := func(string) string { return C } // the projects directory is checked, not its mapping
	cases := []struct {
		name, tool, profile string
		spec                LockSpec
		want                string
		projects            string // the deck container's lea.projects ("": /home/u/p)
	}{
		{"lockmq has no container console", "lockmq", "", sqlSpec, "no lock console", ""},
		{"an unknown tool", "bash", "", sqlSpec, "no lock console", ""},
		{"a flag as profile", "locksql", "--auto", sqlSpec, "invalid profile", ""},
		{"a path as profile", "locksql", "../x", sqlSpec, "invalid profile", ""},
		{"upper case", "locksql", "Prod", sqlSpec, "invalid profile", ""},
		{"too long", "locksql", strings.Repeat("a", 33), sqlSpec, "invalid profile", ""},
		{"a newline", "locksql", "a\nb", sqlSpec, "invalid profile", ""},
		{"off", "locksql", "", LockSpec{Image: "locksql:0.8.0", ContainerName: nameOf}, "locksql is off", ""},
		{"an option as image", "locksql", "", LockSpec{Enabled: true, Image: "--privileged", ContainerName: nameOf}, "invalid locksql console image", ""},
		{"a relative projects dir", "locksql", "", LockSpec{Enabled: true, Image: "locksql:1", ContainerName: anyIsC}, "invalid projects directory", "p"},
		{"a mount spec in the projects dir", "locksql", "", LockSpec{Enabled: true, Image: "locksql:1", ContainerName: anyIsC}, "invalid projects directory", "/p:/etc"},
		{"a vault address with a space", "locksql", "", LockSpec{Enabled: true, Image: "locksql:1", VaultAddr: "https://v x", ContainerName: nameOf}, "invalid vault address", ""},
		// One daemon, several decks: a deck's console serves its own projects directory only.
		{"another deck's projects dir", "locksql", "", sqlSpec, "is not the one of " + C, "/home/u/other"},
		{"no container mapping", "locksql", "", LockSpec{Enabled: true, Image: "locksql:1"}, "is not the one of " + C, ""},
		// The console's writable directory: checked by the host, or no console.
		{"no console dir check", "locksql", "", LockSpec{Enabled: true, Image: "locksql:1", ContainerName: nameOf}, "no check of the console's directory", ""},
		{"a console dir refused", "locksql", "", LockSpec{Enabled: true, Image: "locksql:1", ContainerName: nameOf,
			ConsoleDir: func(d string) error { return errors.New("refused " + d) }}, "refused /home/u/p/.locksql", ""},
	}
	for _, c := range cases {
		e, fc := lockEnv(t, c.spec)
		if c.projects != "" {
			e.ctr.projects[C] = c.projects
		}
		s := e.ensure(t, "/p")
		n := len(s.panes)
		_, err := e.m.LockOpen(bg, C, c.tool, c.profile)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err=%v, want %q", c.name, err, c.want)
		}
		if len(s.panes) != n || len(fc.calls) != 0 {
			t.Errorf("%s: a refused open did something: %d panes (was %d), consoles %q", c.name, len(s.panes), n, fc.calls)
		}
	}
	if _, err := lockEnvNoDeck(t).m.LockOpen(bg, C, "locksql", ""); !errors.Is(err, errNoDeck) {
		t.Errorf("no deck: %v", err)
	}
}

func lockEnvNoDeck(t *testing.T) env {
	e, _ := lockEnv(t, sqlSpec)
	return e
}

// No deck runs: no session is made for a console, and nothing is listed or closed.
func TestLockNeedsARunningDeck(t *testing.T) {
	e, fc := lockEnv(t, sqlSpec)
	if _, err := e.m.LockOpen(bg, C, "locksql", "prod"); !errors.Is(err, errNoDeck) {
		t.Fatalf("open: %v", err)
	}
	if _, err := e.m.LockList(bg, C); !errors.Is(err, errNoDeck) {
		t.Fatalf("list: %v", err)
	}
	if _, err := e.m.LockClose(bg, C, "locksql", "prod"); !errors.Is(err, errNoDeck) {
		t.Fatalf("close: %v", err)
	}
	if e.term.get(C) != nil || len(fc.calls) != 0 {
		t.Fatalf("a session was made (%v) or a console touched (%q)", e.term.get(C) != nil, fc.calls)
	}
}

// A console that already runs without this deck's pane is neither taken over nor stopped; a
// stopped one in the way is removed.
func TestLockOpenNeverTakesOverARunningConsole(t *testing.T) {
	for owner, want := range map[string]string{"": "outside lea's decks", "lea-other": "in the deck of lea-other", C: "without its pane"} {
		e, fc := lockEnv(t, sqlSpec)
		s := e.ensure(t, "/p")
		fc.state["locksql-console-prod"] = ConsoleState{Exists: true, Running: true, Owner: owner}
		_, err := e.m.LockOpen(bg, C, "locksql", "prod")
		if err == nil || !strings.Contains(err.Error(), want) || lockPane(s, "locksql/prod") != nil || len(fc.calls) != 0 {
			t.Errorf("owner %q: err=%v, calls %q", owner, err, fc.calls)
		}
	}
	e, fc := lockEnv(t, sqlSpec)
	s := e.ensure(t, "/p")
	fc.state["locksql-console-prod"] = ConsoleState{Exists: true}
	if st, err := e.m.LockOpen(bg, C, "locksql", "prod"); err != nil || st != "opened" || lockPane(s, "locksql/prod") == nil {
		t.Fatalf("over a stopped console: %q %v", st, err)
	}
	if !reflect.DeepEqual(fc.calls, []string{"rm locksql-console-prod"}) {
		t.Fatalf("calls %q", fc.calls)
	}
}

func TestLockCloseStopsTheConsoleAndClosesItsPane(t *testing.T) {
	e, fc := lockEnv(t, sqlSpec)
	s := e.ensure(t, "/p")
	if _, err := e.m.LockOpen(bg, C, "locksql", "prod"); err != nil {
		t.Fatal(err)
	}
	fc.state["locksql-console-prod"] = ConsoleState{Exists: true, Running: true, Owner: C}
	if st, err := e.m.LockClose(bg, C, "locksql", "prod"); err != nil || st != "closed" {
		t.Fatalf("close: %q %v", st, err)
	}
	if lockPane(s, "locksql/prod") != nil || !reflect.DeepEqual(fc.calls, []string{"stop locksql-console-prod"}) {
		t.Fatalf("pane left or calls %q", fc.calls)
	}
	if st, err := e.m.LockClose(bg, C, "locksql", "prod"); err != nil || st != "not-open" {
		t.Fatalf("second close: %q %v", st, err)
	}
	// Another deck's console, or one of a terminal, is not this deck's to stop.
	fc.state["locksql-console"] = ConsoleState{Exists: true, Running: true, Owner: "lea-other"}
	if _, err := e.m.LockClose(bg, C, "locksql", ""); err == nil || !strings.Contains(err.Error(), "not this deck's") {
		t.Fatalf("foreign close: %v", err)
	}
	if slices.Contains(fc.calls, "stop locksql-console") {
		t.Fatal("a foreign console was stopped")
	}
}

// lea stop stops the consoles the deck opened.
func TestMarkStoppedStopsTheDecksConsoles(t *testing.T) {
	e, fc := lockEnv(t, sqlSpec)
	e.ensure(t, "/p")
	if err := e.m.MarkStopped(bg, C, false); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fc.calls, []string{"stop-of " + C}) {
		t.Fatalf("calls %q", fc.calls)
	}
}

// Without the lock configuration (a daemon of tests, or an older one) nothing opens.
func TestLockOpenWithoutConsolesIsRefused(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	if _, err := e.m.LockOpen(bg, C, "locksql", ""); err == nil {
		t.Fatal("opened without a configuration")
	}
}

// An agent's lock request is checked against the user's allow-list and needs a deck to show it;
// the check opens, lists and stops nothing, and makes no pane.
func TestLockCheckRequest(t *testing.T) {
	allowed := sqlSpec
	allowed.AgentProfiles = []string{"staging", "prod"}
	e, fc := lockEnv(t, allowed)
	s := e.ensure(t, "/p")
	n := len(s.log)
	if err := e.m.LockCheckRequest(bg, C, "locksql", "prod"); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct{ tool, profile, want string }{
		"no profile":      {"locksql", "", "a profile is required"},
		"not allowed":     {"locksql", "dev", `profile "dev" is not one agents may ask for`},
		"bad profile":     {"locksql", "Prod;x", "invalid profile name"},
		"option profile":  {"locksql", "--auto", "invalid profile name"},
		"host tool":       {"lockmq", "prod", "no lock console"},
		"unknown tool":    {"docker", "prod", "no lock console"},
		"control in tool": {"locksql\x1b", "prod", "no lock console"},
	} {
		if err := e.m.LockCheckRequest(bg, C, c.tool, c.profile); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := e.m.LockCheckRequest(bg, "bad name", "locksql", "prod"); err == nil {
		t.Error("bad container accepted")
	}
	if len(s.log) != n || len(fc.calls) != 0 || lockPane(s, "locksql/prod") != nil {
		t.Fatalf("a check touched tmux (%q) or a console (%q)", s.log[n:], fc.calls)
	}
	// Empty allow-list (the default): agents may ask for nothing.
	e2, _ := lockEnv(t, sqlSpec)
	e2.ensure(t, "/p")
	if err := e2.m.LockCheckRequest(bg, C, "locksql", "prod"); err == nil || !strings.Contains(err.Error(), "LEA_LOCKSQL_AGENT_PROFILES") {
		t.Fatalf("empty allow-list: %v", err)
	}
	off := allowed
	off.Enabled = false
	e3, _ := lockEnv(t, off)
	e3.ensure(t, "/p")
	if err := e3.m.LockCheckRequest(bg, C, "locksql", "prod"); err == nil || !strings.Contains(err.Error(), "locksql is off") {
		t.Fatalf("off: %v", err)
	}
	// No deck runs: nobody would see the request, and no session is made for it.
	e4, _ := lockEnv(t, allowed)
	if err := e4.m.LockCheckRequest(bg, C, "locksql", "prod"); !errors.Is(err, errNoDeck) {
		t.Fatalf("no deck: %v", err)
	}
	if e4.term.get(C) != nil {
		t.Fatal("a session was made for a request")
	}
}

// The deck's row carries the agent's oldest pending lock request: only its id, tool and profile.
func TestSnapshotCarriesTheOldestLockRequest(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	e.spawn(t, "a1")
	k1 := C + "/a1"
	r, err := e.st.AddLockRequest(C, k1, "locksql", "prod", LockRequestTTL, 60, 900)
	if err != nil {
		t.Fatal(err)
	}
	// Another container's request is not this deck's.
	if _, err := e.st.AddLockRequest("lea-other", k1, "locksql", "staging", LockRequestTTL, 60, 900); err != nil {
		t.Fatal(err)
	}
	snap, err := e.m.Watch(bg, C, 0, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var row Row
	for _, a := range snap.Agents {
		if a.Key == k1 {
			row = a
		}
	}
	if row.LockAsk == nil || *row.LockAsk != (LockAsk{ID: r.ID, Tool: "locksql", Profile: "prod"}) {
		t.Fatalf("lock ask=%+v", row.LockAsk)
	}
	// Past its time it is no longer shown (Now is 1000 in tests).
	e.m.cfg.Now = func() int64 { return 900 + LockRequestTTL }
	e.m.Changed()
	snap, err = e.m.Watch(bg, C, snap.Version, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range snap.Agents {
		if a.Key == k1 && a.LockAsk != nil {
			t.Fatalf("expired request shown: %+v", a.LockAsk)
		}
	}
}

// The console writes only its own directory of the projects directory, which agents also write:
// a missing one is made, a symbolic link (a bind mount follows it on the host) or a file refused.
func TestEnsureConsoleDir(t *testing.T) {
	p := t.TempDir()
	d := filepath.Join(p, ".locksql")
	if err := EnsureConsoleDir(d); err != nil {
		t.Fatalf("missing: %v", err)
	}
	if fi, err := os.Lstat(d); err != nil || !fi.IsDir() {
		t.Fatalf("not made: %v %v", fi, err)
	}
	if err := EnsureConsoleDir(d); err != nil {
		t.Fatalf("existing: %v", err)
	}
	if err := os.Remove(d); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), d); err != nil {
		t.Fatal(err)
	}
	if err := EnsureConsoleDir(d); err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("symlink to a directory: %v", err)
	}
	if err := os.Remove(d); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(d, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureConsoleDir(d); err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("file: %v", err)
	}
}
