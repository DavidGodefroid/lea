package agents

import (
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/store"
)

func TestCheckLabel(t *testing.T) {
	for in, want := range map[string]string{
		"":                        "",
		"   ":                     "",
		"auth refactor":           "auth refactor",
		"  trimmed  ":             "trimmed",
		"été ☺ 日本":                "été ☺ 日本",
		strings.Repeat("é", 24):   strings.Repeat("é", 24),
		"#1 issue; $(rm -rf) 'x'": "#1 issue; $(rm -rf) 'x'",
	} {
		got, err := CheckLabel(in)
		if err != nil || got != want {
			t.Errorf("CheckLabel(%q)=%q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		strings.Repeat("a", 25), strings.Repeat("é", 25),
		"a\nb", "a\tb", "a\x1b[31mb", "a\x7fb", "a\u0085b", // controls
		"a\u200bb", "a\u202eb", "a\u2066b", "\ufeffa", "a\U000e0041", "a\u00adb", // invisible
		"a\xffb", // not UTF-8
	} {
		if got, err := CheckLabel(in); err == nil {
			t.Errorf("CheckLabel(%q)=%q, want an error", in, got)
		}
	}
}

func TestSetLabelChangesOnlyTheDisplayName(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	r := e.spawn(t, "")
	if err := e.m.SetLabel(bg, r.Key, "  auth refactor "); err != nil {
		t.Fatal(err)
	}
	snap, err := e.m.Watch(bg, C, 0, 0)
	if err != nil || len(snap.Agents) != 1 {
		t.Fatalf("snap=%+v err=%v", snap, err)
	}
	if a := snap.Agents[0]; a.Label != "auth refactor" || a.Name != "claude-1" || a.Key != C+"/claude-1" {
		t.Fatalf("row=%+v", a)
	}
	if p := s.byAgent("claude-1"); p == nil || !slices.Contains(p.cmd, "LEA_AGENT=claude-1") {
		t.Fatalf("the agent's pane changed: %+v", p)
	}
	if err := e.m.SetLabel(bg, r.Key, ""); err != nil {
		t.Fatal(err)
	}
	if a, _ := e.st.AgentByKey(r.Key); a.Label != "" {
		t.Fatalf("label not cleared: %+v", a)
	}
	if err := e.m.SetLabel(bg, r.Key, "bad\x1b]0;x\x07"); err == nil {
		t.Fatal("a label with control characters was accepted")
	}
	if err := e.m.SetLabel(bg, C+"/nobody", "x"); err == nil {
		t.Fatal("a label for an unknown agent was accepted")
	}
	if err := e.m.SetLabel(bg, ShellKey(C, "sh-1"), "x"); err == nil {
		t.Fatal("a label for a shell was accepted")
	}
}

// shellCmd is what a shell pane runs: docker exec into the container, abduco, then lea-agent's
// shell; never LEA_AGENT, MCP or hooks.
func shellCmd(name, workdir string, attach bool) []string {
	cmd := []string{"docker", "exec", "-it", "-u", "1000:1000", "-e", "TERM=xterm-256color", "-e", "COLORTERM=truecolor",
		"-e", "FORCE_HYPERLINK=1"}
	if workdir != "" {
		cmd = append(cmd, "-w", workdir)
	}
	cmd = append(cmd, C, "abduco")
	if attach {
		return append(cmd, "-a", name)
	}
	return append(cmd, "-c", name, "lea-agent", "shell", name)
}

func (e env) openShell(t *testing.T) Row {
	t.Helper()
	r, err := e.m.OpenShell(bg, C)
	if err != nil {
		t.Fatal(err)
	}
	e.ctr.mu.Lock()
	if e.ctr.shells == nil {
		e.ctr.shells = map[string][]string{}
	}
	e.ctr.shells[C] = append(e.ctr.shells[C], r.Name) // lea-agent shell recorded it
	e.ctr.mu.Unlock()
	return r
}

func shellRows(snap Snapshot) []string {
	var out []string
	for _, r := range snap.Agents {
		if r.Kind == KindShell {
			out = append(out, r.Name)
		}
	}
	return out
}

func TestOpenShellRunsAShellInTheContainerAndShowsIt(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/home/u/p/shop")
	r := e.openShell(t)
	if r.Kind != KindShell || r.Name != "sh-1" || r.Key != ShellKey(C, "sh-1") || r.Key != "shell:"+C+"/sh-1" {
		t.Fatalf("row=%+v", r)
	}
	p := s.byShell("sh-1")
	if p == nil || !reflect.DeepEqual(p.cmd, shellCmd("sh-1", "/home/u/p/shop", false)) {
		t.Fatalf("pane=%+v", p)
	}
	if !p.remainOff {
		t.Fatal("a shell pane must close when the shell exits (remain-on-exit off), never stay dead")
	}
	for _, a := range p.cmd {
		if strings.Contains(a, "LEA_AGENT") || strings.Contains(a, "LEA_APPROVAL") {
			t.Fatalf("a shell is not an agent: %q", p.cmd)
		}
	}
	if s.slot() != p || !s.logged("select-pane -t "+p.id) {
		t.Fatalf("the shell is not shown and focused: slot=%+v log=%q", s.slot(), s.log)
	}
	checkLayout(t, s)
	snap, _ := e.m.Watch(bg, C, 0, 0)
	if snap.Shown != r.Key || !reflect.DeepEqual(shellRows(snap), []string{"sh-1"}) {
		t.Fatalf("snap=%+v", snap)
	}
	// Not an agent: no row in the store, so not on the board, not in agents_*, not under max_agents.
	if as, _ := e.st.Agents(); len(as) != 0 {
		t.Fatalf("agents=%+v", as)
	}
	if last, _ := e.st.LastShown(C); last != "" {
		t.Fatalf("the last shown agent is an agent's, never a shell's: %q", last)
	}
}

func TestShellsAreCappedPerContainerButNotByMaxAgents(t *testing.T) {
	e := newEnv(t)
	e.m.cfg.MaxAgents = 1
	e.ensure(t, "/p")
	e.spawn(t, "")
	for i := 1; i <= MaxShells; i++ {
		if r := e.openShell(t); r.Name != "sh-"+string(rune('0'+i)) {
			t.Fatalf("shell %d named %q", i, r.Name)
		}
	}
	if _, err := e.m.OpenShell(bg, C); err == nil || !errors.Is(err, store.ErrLimit) {
		t.Fatalf("a fifth shell: %v", err)
	} else if n := len([]rune(err.Error())); n > 31 {
		// The deck's status line holds 31 characters.
		t.Fatalf("the cap message (%d characters) does not fit the deck's status line: %q", n, err)
	}
	// A shell whose pane is gone still runs in the container: it counts.
	e.ctr.setShells(C, "sh-1", "sh-2", "sh-3", "sh-4", "sh-9")
	if _, err := e.m.OpenShell(bg, C); err == nil {
		t.Fatal("a shell over the cap was opened")
	}
}

func TestOpenShellTakesTheLowestFreeName(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	e.openShell(t)
	e.openShell(t)
	if err := e.m.Stop(bg, ShellKey(C, "sh-1")); err != nil {
		t.Fatal(err)
	}
	// sh-3 still runs in the container (its pane is gone): never reuse a live shell's name.
	e.ctr.setShells(C, "sh-2", "sh-3")
	if r := e.openShell(t); r.Name != "sh-1" {
		t.Fatalf("name=%q", r.Name)
	}
	if _, err := e.m.OpenShell(bg, C); err != nil {
		t.Fatal(err)
	}
	snap, _ := e.m.Watch(bg, C, 0, 0)
	if got := shellRows(snap); !slices.Contains(got, "sh-4") || slices.Contains(got, "sh-5") {
		t.Fatalf("shells=%v", got)
	}
}

func TestAShellThatExitsLeavesNoRowAndNoPane(t *testing.T) {
	for _, dies := range []bool{false, true} { // closed (remain-on-exit off), or left dead
		e := newEnv(t)
		s := e.ensure(t, "/p")
		r := e.openShell(t)
		s.locked(func() {
			p := s.byShell("sh-1")
			if dies {
				p.remainOff = false
			}
			s.exit(p)
		})
		e.ctr.setShells(C)
		if err := e.m.Ensure(bg, C, ""); err != nil {
			t.Fatal(err)
		}
		if s.byShell("sh-1") != nil {
			t.Fatalf("dies=%v: the shell's pane stays: %+v", dies, s.panes)
		}
		checkLayout(t, s)
		if s.slot() != s.byRole(rolePlace) {
			t.Fatalf("dies=%v: slot=%+v", dies, s.slot())
		}
		snap, _ := e.m.Watch(bg, C, 0, 0)
		if len(shellRows(snap)) != 0 || snap.Shown == r.Key {
			t.Fatalf("dies=%v: snap=%+v", dies, snap)
		}
		if slices.Contains(e.ctr.stopped, C+"/sh-1") {
			t.Fatal("a shell was stopped as an agent")
		}
	}
}

func TestStopOfAShellKillsItWithoutTouchingAgents(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	e.spawn(t, "")
	r := e.openShell(t)
	if err := e.m.Stop(bg, r.Key); err != nil {
		t.Fatal(err)
	}
	if s.byShell("sh-1") != nil || !slices.Contains(e.ctr.stopped, C+"/shell:sh-1") {
		t.Fatalf("panes=%+v stopped=%v", s.panes, e.ctr.stopped)
	}
	if len(e.ctr.stopped) != 1 {
		t.Fatalf("stopped=%v", e.ctr.stopped)
	}
	checkLayout(t, s)
	snap, _ := e.m.Watch(bg, C, 0, 0)
	if len(shellRows(snap)) != 0 || len(snap.Agents) != 1 || snap.Shown == r.Key {
		t.Fatalf("snap=%+v", snap)
	}
	if err := e.m.Stop(bg, r.Key); err == nil {
		t.Fatal("stopping a shell that is gone succeeded")
	}
}

func TestFocusShowsAShell(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	a := e.spawn(t, "")
	sh := e.openShell(t)
	if err := e.m.Focus(bg, a.Key, true); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Focus(bg, sh.Key, false); err != nil {
		t.Fatal(err)
	}
	if s.slot() != s.byShell("sh-1") {
		t.Fatalf("slot=%+v", s.slot())
	}
	if snap, _ := e.m.Watch(bg, C, 0, 0); snap.Shown != sh.Key {
		t.Fatalf("shown=%q", snap.Shown)
	}
	if last, _ := e.st.LastShown(C); last != a.Key {
		t.Fatalf("last shown=%q", last)
	}
	if err := e.m.Focus(bg, ShellKey(C, "sh-7"), false); err == nil {
		t.Fatal("focus of a shell that does not exist succeeded")
	}
}

func TestShellsSurviveADaemonRestart(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	e.openShell(t)
	e.openShell(t)
	before := len(s.panes)
	// A new daemon: same tmux server, same store, a manager that never saw the shells.
	m2 := NewManager(Config{Store: e.st, Term: e.term, Containers: e.ctr, LeaBin: leaBin, UID: 1000, GID: 1000,
		Now: func() int64 { return 1000 }})
	if err := m2.Ensure(bg, C, ""); err != nil {
		t.Fatal(err)
	}
	if len(s.panes) != before || s.byShell("sh-1") == nil || s.byShell("sh-2") == nil {
		t.Fatalf("panes=%+v", s.panes)
	}
	checkLayout(t, s)
	snap, _ := m2.Watch(bg, C, 0, 0)
	if got := shellRows(snap); !reflect.DeepEqual(got, []string{"sh-1", "sh-2"}) {
		t.Fatalf("shells=%v", got)
	}
	if snap.Shown != ShellKey(C, "sh-2") {
		t.Fatalf("shown=%q", snap.Shown)
	}
}

func TestARebuiltSessionReattachesLiveShells(t *testing.T) {
	e := newEnv(t)
	e.ctr.setShells(C, "sh-2")
	s := e.ensure(t, "/p") // a new session: the tmux server was killed, the container kept sh-2
	p := s.byShell("sh-2")
	if p == nil || !reflect.DeepEqual(p.cmd, shellCmd("sh-2", "", true)) || !p.remainOff {
		t.Fatalf("pane=%+v", p)
	}
	checkLayout(t, s)
}

func TestReconcileWithoutTheContainersShells(t *testing.T) {
	e := newEnv(t)
	e.ctr.shellErr = errors.New("lea-agent shells failed (an image older than the deck?)")
	s := e.ensure(t, "/p") // a container of an older image: the deck still works
	checkLayout(t, s)
	if _, err := e.m.OpenShell(bg, C); err == nil || !strings.Contains(err.Error(), "older") {
		t.Fatalf("open: %v", err)
	}
}

func TestShellKeys(t *testing.T) {
	c, n, ok := parseShellKey(ShellKey(C, "sh-3"))
	if !ok || c != C || n != "sh-3" {
		t.Fatalf("%q %q %v", c, n, ok)
	}
	for _, k := range []string{C + "/sh-3", "shell:" + C, "shell:/sh-1", "shell:" + C + "/claude-1", "shell:Bad/sh-1",
		"shell:" + C + "/sh-1/x", filepath.Join("shell:", C, "sh-0x")} {
		if _, _, ok := parseShellKey(k); ok {
			t.Errorf("%q parsed as a shell key", k)
		}
	}
}

// abduco's detach key (ctrl+\\) or a killed docker exec closes a shell's pane while the shell
// still runs: the reconcile gives it a pane back, parked, so its row stays and x reaches it.
func TestAShellWhosePaneClosedGetsAPaneBack(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	e.openShell(t)
	s.locked(func() { s.exit(s.byShell("sh-1")) }) // closes (remain-on-exit off); sh-1 still runs
	if err := e.m.Ensure(bg, C, ""); err != nil {
		t.Fatal(err)
	}
	p := s.byShell("sh-1")
	if p == nil || !reflect.DeepEqual(p.cmd, shellCmd("sh-1", "", true)) || !p.remainOff {
		t.Fatalf("pane=%+v", p)
	}
	if s.slot() == p {
		t.Fatal("the reattached shell took the slot")
	}
	checkLayout(t, s)
	checkSettled(t, e, s)
	snap, _ := e.m.Watch(bg, C, 0, 0)
	if got := shellRows(snap); !reflect.DeepEqual(got, []string{"sh-1"}) {
		t.Fatalf("shells=%v", got)
	}
	// A shell that really ended takes its row with it, and nothing reattaches.
	s.locked(func() { s.exit(s.byShell("sh-1")) })
	e.ctr.setShells(C)
	if err := e.m.Ensure(bg, C, ""); err != nil {
		t.Fatal(err)
	}
	if s.byShell("sh-1") != nil {
		t.Fatalf("panes=%+v", s.panes)
	}
	if snap, _ := e.m.Watch(bg, C, 0, 0); len(shellRows(snap)) != 0 {
		t.Fatalf("snap=%+v", snap)
	}
}

// A restarted daemon never saw a shell whose pane closed while it was down: its first reconcile
// asks the container.
func TestADaemonRestartGivesAPaneToAShellThatLostIt(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	e.ctr.setShells(C, "sh-3")
	m2 := NewManager(Config{Store: e.st, Term: e.term, Containers: e.ctr, LeaBin: leaBin, UID: 1000, GID: 1000,
		Now: func() int64 { return 1000 }})
	if err := m2.Ensure(bg, C, ""); err != nil {
		t.Fatal(err)
	}
	if p := s.byShell("sh-3"); p == nil || !reflect.DeepEqual(p.cmd, shellCmd("sh-3", "", true)) {
		t.Fatalf("panes=%+v", s.panes)
	}
	checkLayout(t, s)
}

// x reaches a shell that has no pane (it closed before any reconcile): the shell is ended.
func TestStopEndsAShellThatHasNoPane(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	r := e.openShell(t)
	s.locked(func() { s.exit(s.byShell("sh-1")) })
	if err := e.m.Stop(bg, r.Key); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(e.ctr.stopped, C+"/shell:sh-1") {
		t.Fatalf("stopped=%v", e.ctr.stopped)
	}
	if err := e.m.Stop(bg, ShellKey(C, "sh-2")); err == nil {
		t.Fatal("stopping a shell that neither has a pane nor runs succeeded")
	}
}

// A dead shell pane holds its name until the reconcile removes it, and the reconcile removes
// that very pane, never another of the same name: a live shell keeps its pane.
func TestADeadShellPaneIsRemovedByItsIdentity(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	e.openShell(t)
	s.locked(func() { // left dead (a tmux without the pane option): the shell ended
		p := s.byShell("sh-1")
		p.remainOff = false
		s.exit(p)
	})
	e.ctr.setShells(C)
	if r := e.openShell(t); r.Name != "sh-2" {
		t.Fatalf("a dead pane's name was taken again: %q", r.Name)
	}
	// An older daemon reused the name: a live sh-1 pane listed before the dead one.
	var live *fpane
	s.locked(func() {
		live = &fpane{id: "%90", win: "@90", shell: "sh-1", remainOff: true, cmd: shellCmd("sh-1", "", true)}
		s.names["@90"] = "sh-1"
		s.panes = append([]*fpane{live}, s.panes...)
	})
	e.ctr.setShells(C, "sh-1", "sh-2")
	if err := e.m.Ensure(bg, C, ""); err != nil {
		t.Fatal(err)
	}
	if s.pane("%90") == nil {
		t.Fatal("the live sh-1 pane was removed in place of the dead one")
	}
	for _, p := range s.panes {
		if p.shell != "" && p.dead {
			t.Fatalf("a dead shell pane stays: %+v", p)
		}
	}
	checkLayout(t, s)
}

// A dropped connection takes the shell rows with it: no deck keeps showing shells nobody tracks.
func TestALostConnectionDropsTheShellRows(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	e.openShell(t)
	s.closeEvents()
	deadline := time.Now().Add(2 * time.Second)
	for {
		snap, _ := e.m.Watch(bg, C, 0, 0)
		if len(shellRows(snap)) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("shell rows after the connection dropped: %+v", snap)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
