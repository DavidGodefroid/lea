package deck

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func (f *fakeAPI) SetLabel(ctx context.Context, k, label string) error {
	f.rec(fmt.Sprintf("label %s %q", k, label))
	return f.err
}

func (f *fakeAPI) OpenShell(ctx context.Context, c string) (Agent, error) {
	f.rec("shell " + c)
	return f.newShell, f.err
}

func (f *fakeAPI) SplitShell(ctx context.Context, k string, below bool) (Agent, error) {
	f.rec(fmt.Sprintf("split %s below=%v", k, below))
	return f.newShell, f.err
}

func (f *fakeAPI) LockDecide(ctx context.Context, c string, id int64, open bool) (string, error) {
	f.rec(fmt.Sprintf("lock-decide %s %d %v", c, id, open))
	if !open {
		return "declined", f.err
	}
	return "opened", f.err
}

func (f *fakeAPI) LockOpen(ctx context.Context, c, tool string) (string, error) {
	f.rec("lock-open " + c + " " + tool)
	return "opened", f.err
}

func shell(name string) Agent {
	return Agent{Key: "shell:lea-a/" + name, Name: name, Kind: KindShell, State: "running", CtxPct: -1, CostUSD: -1}
}

func labelled(name, label string) Agent {
	a := ag(name, "idle", 1)
	a.Label = label
	return a
}

// typeKeys sends s one rune at a time, as a terminal does.
func typeKeys(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		k := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
		if r == ' ' {
			k = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
		}
		m, _ = step(t, m, k)
	}
	return m
}

func TestERenamesTheSelectedAgent(t *testing.T) {
	api := &fakeAPI{}
	m := sized(loaded(t, api, "lea-a/a", labelled("a", "old")), 34, 10)
	m, cmd := step(t, m, key("e"))
	if cmd != nil || m.editFor != "lea-a/a" || string(m.draft) != "old" {
		t.Fatalf("editFor %q draft %q", m.editFor, string(m.draft))
	}
	for range 3 {
		m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyBackspace})
	}
	// Keys that act on rows are text while the field is open: x, q, n, space and digits.
	m = typeKeys(t, m, "x q n 1")
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("\x1b[31m\u202e!"), Paste: true})
	if string(m.draft) != "x q n 1[31m!" || len(api.got()) != 0 {
		t.Fatalf("draft %q calls %q", string(m.draft), api.got())
	}
	ls := viewLines(m)
	if !strings.Contains(ls[len(ls)-2], "name: x q n 1[31m!") || !strings.HasPrefix(ls[len(ls)-1], "╰─ enter save · esc cancel ") {
		t.Fatalf("view:\n%s", strings.Join(ls, "\n"))
	}
	m, cmd = step(t, m, key("enter"))
	if cmd == nil || m.editFor != "" || m.status != "" {
		t.Fatalf("enter: editFor %q status %q", m.editFor, m.status)
	}
	cmd()
	if got := api.got(); len(got) != 1 || got[0] != `label lea-a/a "x q n 1[31m!"` {
		t.Fatalf("calls=%q", got)
	}
}

func TestRenameFieldIsCappedAndEscCancels(t *testing.T) {
	api := &fakeAPI{}
	m := loaded(t, api, "lea-a/a", ag("a", "idle", 1))
	m, _ = step(t, m, key("e"))
	m = typeKeys(t, m, strings.Repeat("é", 30))
	if n := len(m.draft); n != 24 {
		t.Fatalf("draft of %d characters", n)
	}
	m, cmd := step(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil || m.editFor != "" || m.status != "" || len(api.got()) != 0 {
		t.Fatalf("esc: editFor %q status %q calls %q", m.editFor, m.status, api.got())
	}
	// An empty field clears the label.
	m, _ = step(t, m, key("e"))
	_, cmd = step(t, m, key("enter"))
	cmd()
	if got := api.got(); len(got) != 1 || got[0] != `label lea-a/a ""` {
		t.Fatalf("calls=%q", got)
	}
}

func TestRenameEndsWhenTheAgentGoes(t *testing.T) {
	m := loaded(t, &fakeAPI{}, "lea-a/a", ag("a", "idle", 1), ag("b", "idle", 1))
	m, _ = step(t, m, key("e"))
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Agents: []Agent{ag("b", "idle", 1)}}})
	if m.editFor != "" || m.status != "" {
		t.Fatalf("editFor %q status %q", m.editFor, m.status)
	}
}

func TestSOpensAShellAndSelectsIt(t *testing.T) {
	api := &fakeAPI{newShell: shell("sh-1")}
	m := loaded(t, api, "lea-a/a", ag("a", "idle", 1))
	m, cmd := step(t, m, key("s"))
	if cmd == nil {
		t.Fatal("s made no call")
	}
	m, _ = step(t, m, cmd())
	if got := api.got(); len(got) != 1 || got[0] != "shell lea-a" {
		t.Fatalf("calls=%q", got)
	}
	if m.selected != "shell:lea-a/sh-1" {
		t.Fatalf("selected %q", m.selected)
	}
}

func TestShellRowsSortLastAndAreNotCountedAsAgents(t *testing.T) {
	m := sized(loaded(t, &fakeAPI{}, "", shell("sh-1"), ag("a", "idle", 1), ag("b", "blocked", 1)), 34, 10)
	if names(m) != "a,b,sh-1" {
		t.Fatalf("order %s", names(m))
	}
	ls := viewLines(m)
	if !strings.HasSuffix(ls[0], " 2 agents ─╮") {
		t.Fatalf("top %q", ls[0])
	}
	all := strings.Join(ls, "\n")
	if !strings.Contains(all, "$ sh-1") || !strings.Contains(all, "shell · exit or x ends it") {
		t.Fatalf("view:\n%s", all)
	}
	// space never stops on a shell: nothing about it needs the user.
	m, _ = step(t, loaded(t, &fakeAPI{}, "", shell("sh-1")), key(" "))
	if m.status != nothingNeedsYou {
		t.Fatalf("status %q", m.status)
	}
}

func TestXKillsAShellWithoutConfirmation(t *testing.T) {
	api := &fakeAPI{}
	m := loaded(t, api, "shell:lea-a/sh-1", ag("a", "idle", 1), shell("sh-1"))
	m, cmd := step(t, m, key("x"))
	if cmd == nil || m.confirm != "" {
		t.Fatalf("confirm %q", m.confirm)
	}
	cmd()
	if got := api.got(); len(got) != 1 || got[0] != "stop shell:lea-a/sh-1" {
		t.Fatalf("calls=%q", got)
	}
}

func TestAgentKeysDoNothingOnAShell(t *testing.T) {
	api := &fakeAPI{}
	m := loaded(t, api, "shell:lea-a/sh-1", shell("sh-1"))
	for _, k := range []string{"m", "t", "r", "e", "a", "d", "A"} {
		nm, cmd := step(t, m, key(k))
		if cmd != nil || nm.editFor != "" || nm.roleFor != "" {
			t.Fatalf("%s on a shell: editFor %q roleFor %q", k, nm.editFor, nm.roleFor)
		}
	}
	if len(api.got()) != 0 {
		t.Fatalf("calls=%q", api.got())
	}
	// enter shows it like an agent.
	_, cmd := step(t, m, key("enter"))
	cmd()
	if got := api.got(); len(got) != 1 || got[0] != "show shell:lea-a/sh-1 true" {
		t.Fatalf("calls=%q", got)
	}
}

func TestQuestionMarkShowsTheKeysAndAnyKeyClosesThem(t *testing.T) {
	api := &fakeAPI{}
	m := sized(loaded(t, api, "lea-a/a", ag("a", "idle", 1)), 34, 24)
	if ls := viewLines(m); !strings.Contains(ls[len(ls)-1], "? keys") {
		t.Fatalf("the footer does not name ?: %q", ls[len(ls)-1])
	}
	m, cmd := step(t, m, key("?"))
	if cmd != nil || !m.help {
		t.Fatal("? did not open the key list")
	}
	ls := viewLines(m)
	all := strings.Join(ls, "\n")
	for _, want := range []string{"enter", "show", "n", "new agent", "s", "new shell", "e", "rename", "x", "stop", "?", "any key closes"} {
		if !strings.Contains(all, want) {
			t.Fatalf("key list lacks %q:\n%s", want, all)
		}
	}
	if len(ls) != 24 || strings.Contains(all, "…") {
		t.Fatalf("%d lines, a cut line?\n%s", len(ls), all)
	}
	for i, l := range ls {
		if w := len([]rune(sgr.ReplaceAllString(l, ""))); w != 34 {
			t.Fatalf("line %d is %d wide: %q", i, w, l)
		}
	}
	// The closing key does nothing else: x does not ask to stop the agent.
	m, cmd = step(t, m, key("x"))
	if cmd != nil || m.help || m.confirm != "" || len(api.got()) != 0 {
		t.Fatalf("help %v confirm %q calls %q", m.help, m.confirm, api.got())
	}
}

func (f *fakeAPI) SetSound(ctx context.Context, on bool) (string, error) {
	f.rec(fmt.Sprintf("sound %v", on))
	if f.err != nil {
		return "", f.err
	}
	if on {
		return "on", nil
	}
	return "off", nil
}

// S flips the daemon's sounds; the top border says muted while they are off.
func TestSSwitchesTheSounds(t *testing.T) {
	api := &fakeAPI{}
	m, _ := step(t, newModel(api), snapshotMsg{snap: Snapshot{Version: 7, Shown: "lea-a/a", Agents: []Agent{ag("a", "idle", 1)}, Sound: "on"}})
	m = sized(m, 34, 10)
	if top := viewLines(m)[0]; strings.Contains(top, "muted") {
		t.Fatalf("top %q", top)
	}
	m, cmd := step(t, m, key("S"))
	if cmd == nil {
		t.Fatal("S did nothing")
	}
	m, _ = step(t, m, cmd())
	if got := api.got(); len(got) != 1 || got[0] != "sound false" {
		t.Fatalf("calls %q", got)
	}
	ls := viewLines(m)
	if !strings.Contains(ls[0], "muted") || !strings.Contains(strings.Join(ls, "\n"), "sounds off") {
		t.Fatalf("view:\n%s", strings.Join(ls, "\n"))
	}
	m, cmd = step(t, m, key("S"))
	m, _ = step(t, m, cmd())
	if got := api.got(); len(got) != 2 || got[1] != "sound true" || strings.Contains(viewLines(m)[0], "muted") {
		t.Fatalf("calls %q top %q", got, viewLines(m)[0])
	}
}

// The switch made elsewhere (lea sound off) shows at the next snapshot.
func TestTheDeckFollowsTheSoundSwitch(t *testing.T) {
	m := sized(loaded(t, &fakeAPI{}, "lea-a/a", ag("a", "idle", 1)), 34, 10)
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Shown: "lea-a/a", Agents: []Agent{ag("a", "idle", 1)}, Sound: "off"}})
	if top := viewLines(m)[0]; !strings.Contains(top, "muted") {
		t.Fatalf("top %q", top)
	}
}

// Without a sound player, S says so and calls nothing.
func TestSWithoutASoundPlayer(t *testing.T) {
	api := &fakeAPI{}
	m := loaded(t, api, "lea-a/a", ag("a", "idle", 1))
	m, cmd := step(t, m, key("S"))
	if cmd != nil || len(api.got()) != 0 || !strings.Contains(m.status, "no sound player") {
		t.Fatalf("cmd %v calls %q status %q", cmd != nil, api.got(), m.status)
	}
}

// L opens the locksql console under the agent and leaves the keyboard on the deck.
func TestLOpensTheLocksqlConsole(t *testing.T) {
	api := &fakeAPI{}
	m := loaded(t, api, "lea-a/a", ag("a", "idle", 1))
	m, cmd := step(t, m, key("L"))
	if cmd == nil {
		t.Fatal("L made no call")
	}
	m, _ = step(t, m, cmd())
	if got := api.got(); len(got) != 1 || got[0] != "lock-open lea-a locksql" {
		t.Fatalf("calls=%q", got)
	}
	if m.selected != "lea-a/a" || !strings.Contains(m.status, "locksql console opened") {
		t.Fatalf("selected %q status %q", m.selected, m.status)
	}
}
