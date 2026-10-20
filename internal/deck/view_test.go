package deck

import (
	"encoding/json"
	"fmt"
	"github.com/DavidGodefroid/lea/internal/textsafe"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/DavidGodefroid/lea/internal/agents"
)

var sgr = regexp.MustCompile("\x1b\\[[0-9;]*m")

func viewLines(m Model) []string { return strings.Split(sgr.ReplaceAllString(m.View(), ""), "\n") }

func sized(m Model, w, h int) Model { m.width, m.height = w, h; return m }

func TestViewIsAFramedColumnOfTheGivenSize(t *testing.T) {
	m := sized(loaded(t, &fakeAPI{}, "lea-a/b", ag("a", "idle", 1), ag("b", "working", 1)), 34, 12)
	ls := viewLines(m)
	if len(ls) != 12 {
		t.Fatalf("%d lines:\n%s", len(ls), strings.Join(ls, "\n"))
	}
	for i, l := range ls {
		if w := lipgloss.Width(l); w != 34 {
			t.Fatalf("line %d is %d wide: %q", i, w, l)
		}
	}
	if !strings.HasPrefix(ls[0], "╭─ ●•• lea ") || !strings.HasSuffix(ls[0], " 2 agents ─╮") {
		t.Fatalf("top %q", ls[0])
	}
	if !strings.HasPrefix(ls[11], "╰─ n new · m msg · ? keys ") || !strings.HasSuffix(ls[11], "╯") {
		t.Fatalf("bottom %q", ls[11])
	}
}

func TestViewRows(t *testing.T) {
	m := sized(loaded(t, &fakeAPI{}, "lea-a/b", ag("a", "idle", clock.Unix()-30), ag("b", "working", clock.Unix()-120)), 34, 12)
	ls := viewLines(m)
	// a, launched first, stays first; b is selected.
	if !strings.HasPrefix(ls[1], "│  ● a ") || !strings.Contains(ls[2], "p/shop · idle · now") {
		t.Fatalf("row a %q / %q", ls[1], ls[2])
	}
	if !strings.HasPrefix(ls[3], "│› ⠋ b ") || !strings.HasSuffix(ls[3], " claude     │") {
		t.Fatalf("row b %q", ls[3])
	}
	if !strings.Contains(ls[4], "p/shop · working · 2m") {
		t.Fatalf("detail b %q", ls[4])
	}
}

// The user's label replaces the name in the row; the name stays the agent's identity elsewhere.
func TestViewRowShowsTheLabelInsteadOfTheName(t *testing.T) {
	m := sized(loaded(t, &fakeAPI{}, "lea-a/claude-1", labelled("claude-1", "auth work"), ag("claude-2", "idle", 1)), 34, 10)
	ls := viewLines(m)
	if !strings.HasPrefix(ls[1], "│› ○ auth work ") || strings.Contains(ls[1], "claude-1") {
		t.Fatalf("row %q", ls[1])
	}
	if !strings.HasPrefix(ls[3], "│  ○ claude-2 ") {
		t.Fatalf("row %q", ls[3])
	}
}

func TestViewLabelsDoneAgentsUntilSeen(t *testing.T) {
	m := sized(loaded(t, &fakeAPI{}, "", ag("d", "done", 5)), 34, 8)
	if !strings.Contains(strings.Join(viewLines(m), "\n"), "done, unseen") {
		t.Fatal("unseen done agent not labelled")
	}
	m.seen["lea-a/d"] = 5
	if s := strings.Join(viewLines(m), "\n"); strings.Contains(s, "unseen") || !strings.Contains(s, "│    p/shop · done · ") {
		t.Fatalf("seen done agent:\n%s", s)
	}
}

func TestViewCutsLongNamesAndPaths(t *testing.T) {
	long := ag("a-very-long-agent-name-x", "idle", 1)
	long.Workdir = "/home/u/projects/a-directory-with-a-very-long-name"
	m := sized(loaded(t, &fakeAPI{}, "", long), 34, 8)
	for i, l := range viewLines(m) {
		if lipgloss.Width(l) != 34 {
			t.Fatalf("line %d is %d wide: %q", i, lipgloss.Width(l), l)
		}
	}
	if s := strings.Join(viewLines(m), "\n"); !strings.Contains(s, "a-very-long-age…") || !strings.Contains(s, "…") {
		t.Fatalf("not cut:\n%s", s)
	}
}

func TestViewScrollsToTheSelection(t *testing.T) {
	var as []Agent
	for i := 0; i < 20; i++ {
		as = append(as, ag(fmt.Sprintf("a%02d", i), "idle", 1))
	}
	m := sized(loaded(t, &fakeAPI{}, "lea-a/a19", as...), 34, 10)
	s := strings.Join(viewLines(m), "\n")
	if len(viewLines(m)) != 10 || !strings.Contains(s, "a19") || strings.Contains(s, "a00") {
		t.Fatalf("view:\n%s", s)
	}
}

func TestViewConfirmationAndEmptyState(t *testing.T) {
	m := sized(loaded(t, &fakeAPI{}, "lea-a/a", ag("a", "idle", 1)), 34, 8)
	m, _ = step(t, m, key("x"))
	ls := viewLines(m)
	if !strings.Contains(strings.Join(ls, "\n"), "Stop a?") || !strings.HasPrefix(ls[len(ls)-1], "╰─ y stop · esc cancel ") {
		t.Fatalf("view:\n%s", strings.Join(ls, "\n"))
	}
	if s := strings.Join(viewLines(sized(newModel(&fakeAPI{}), 34, 6)), "\n"); !strings.Contains(s, "No agents yet: press n.") || !strings.Contains(s, " 0 agents ") {
		t.Fatalf("empty:\n%s", s)
	}
}

func TestViewASCII(t *testing.T) {
	m := sized(loaded(t, &fakeAPI{}, "lea-a/b", ag("b", "working", 1)), 34, 6)
	m.ascii = true
	ls := viewLines(m)
	if !strings.HasPrefix(ls[0], "+- Ooo lea ") || !strings.HasPrefix(ls[1], "|> . b ") {
		t.Fatalf("view:\n%s", strings.Join(ls, "\n"))
	}
	for _, l := range ls {
		for _, r := range l {
			if r > 0x7e {
				t.Fatalf("non-ASCII %q in %q", r, l)
			}
		}
	}
}

// Red when it needs you, an orange spinner while it works, green when its turn is over, empty
// after 30 minutes of quiet.
func TestStateMarks(t *testing.T) {
	const now = 10_000
	for _, c := range []struct {
		state      string
		updated    int64
		frame      int
		uni, ascii string
		colour     lipgloss.AdaptiveColor
	}{
		{"blocked", now, 0, "●", "!", theme.Blocked},
		{"working", now, 0, "⠋", ".", theme.Working},
		{"working", now, 1, "⠙", "o", theme.Working},
		{"working", now, 12, "⠹", ".", theme.Working},
		{"idle", now - 60, 0, "●", "*", theme.Done},
		{"done", now - restAfter + 1, 0, "●", "*", theme.Done},
		{"idle", now - restAfter, 0, "○", "o", theme.Muted},
		{"done", 1, 0, "○", "o", theme.Muted},
		{"unknown", now, 0, "?", "?", theme.Unknown},
		{"other", now, 0, "?", "?", theme.Unknown},
	} {
		u, uc := stateMark(c.state, c.updated, now, c.frame, false)
		a, ac := stateMark(c.state, c.updated, now, c.frame, true)
		if u != c.uni || a != c.ascii || uc != c.colour || ac != c.colour {
			t.Errorf("%s updated %d frame %d: %q %q %v", c.state, c.updated, c.frame, u, a, uc)
		}
	}
}

// The spinner turns only while an agent works: one frame tick at a time, none when all rest.
func TestTheSpinnerTicksOnlyWhileAnAgentWorks(t *testing.T) {
	m := loaded(t, &fakeAPI{}, "", ag("a", "working", 1))
	if !m.ticking {
		t.Fatal("no frame tick while an agent works")
	}
	if cmd := m.animate(); cmd != nil {
		t.Fatal("a second frame tick was scheduled")
	}
	m, cmd := step(t, m, frameMsg{})
	if m.frame != 1 || cmd == nil || !m.ticking {
		t.Fatalf("frame %d, next tick %v", m.frame, cmd != nil)
	}
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Agents: []Agent{ag("a", "idle", 2)}}})
	m, cmd = step(t, m, frameMsg{})
	if cmd != nil || m.ticking {
		t.Fatal("the spinner still ticks with no agent working")
	}
}

func TestAgentColourIsStableAndSpread(t *testing.T) {
	if AgentColor("lea-a/claude-1") != AgentColor("lea-a/claude-1") {
		t.Fatal("unstable colour")
	}
	seen := map[lipgloss.AdaptiveColor]bool{}
	for i := 0; i < 60; i++ {
		seen[AgentColor(fmt.Sprintf("lea-a/claude-%d", i))] = true
	}
	if len(seen) < 8 {
		t.Fatalf("only %d colours for 60 agents", len(seen))
	}
}

func TestPlaceholderIsCentred(t *testing.T) {
	ls := strings.Split(sgr.ReplaceAllString(placeholder{width: 60, height: 9}.View(), ""), "\n")
	if len(ls) != 9 || !strings.Contains(strings.Join(ls, "\n"), "No agent shown.") || strings.HasPrefix(ls[3], "No agent") {
		t.Fatalf("placeholder:\n%s", strings.Join(ls, "\n"))
	}
}

func TestViewCleansEveryAgentControlledString(t *testing.T) {
	a := ag("a", "idle", 1)
	a.Name, a.Profile, a.Workdir = "a\x1b[31mb", "cl\x07aude", "/home/u/p\x1b]0;x\x07/sh\x9bop"
	m := sized(loaded(t, &fakeAPI{}, "", a), 34, 8)
	m.confirm = a.Key
	raw := m.View()
	if strings.Contains(raw, "\x07") || strings.Contains(raw, "\x9b") || strings.Contains(sgr.ReplaceAllString(raw, ""), "\x1b") {
		t.Fatalf("control characters reach the terminal: %q", raw)
	}
	for i, l := range viewLines(m) {
		if lipgloss.Width(l) != 34 {
			t.Fatalf("line %d is %d wide: %q", i, lipgloss.Width(l), l)
		}
	}
}

func TestWidthMatchesAgentsDeckWidth(t *testing.T) {
	if Width != agents.DeckWidth {
		t.Fatalf("deck.Width %d != agents.DeckWidth %d", Width, agents.DeckWidth)
	}
}

func TestViewKeepsWidthWithWideCombiningAndBidi(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		cjk := ag("日本語日本語日本語日本語日本語日本語", "idle", 1)
		cjk.Workdir = "/home/u/日本語日本語日本語日本語日本語日本語/日本語日本語日本語日本語日本語日本語"
		comb := ag("e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301", "working", 1)
		bidi := ag("a\u202eb\u2066c\u200bd\ufeffe\u2028f", "done", 1)
		vs16 := ag(strings.Repeat("☺\ufe0f", 30), "idle", 1)
		vs16.Workdir = "/p/" + strings.Repeat("☺\ufe0f", 30)
		zwj := ag(strings.Repeat("👩\u200d💻", 15), "idle", 1)
		m := sized(loaded(t, &fakeAPI{}, "", cjk, comb, bidi, vs16, zwj), 34, 12)
		m.ascii = ascii
		ls := viewLines(m)
		if len(ls) != 12 {
			t.Fatalf("ascii=%v: %d lines", ascii, len(ls))
		}
		for i, l := range ls {
			if lipgloss.Width(l) != 34 {
				t.Fatalf("ascii=%v line %d is %d wide: %q", ascii, i, lipgloss.Width(l), l)
			}
			for _, r := range l {
				if textsafe.Invisible(r) || (ascii && r > 0x7e) {
					t.Fatalf("ascii=%v: bad rune %U in %q", ascii, r, l)
				}
			}
		}
		if !strings.HasPrefix(ls[0], map[bool]string{false: "╭", true: "+"}[ascii]) || !strings.HasSuffix(ls[1], map[bool]string{false: "│", true: "|"}[ascii]) {
			t.Fatalf("border damaged: %q / %q", ls[0], ls[1])
		}
	}
}

// The row shows the context; neither the cost nor the unread count is shown.
func TestViewShowsContextNotUnreadNorCost(t *testing.T) {
	a := ag("a", "idle", clock.Unix())
	a.CtxPct, a.Unread, a.CostUSD = 42, 3, 1.2
	b := ag("b", "idle", clock.Unix())
	b.Unread, b.CostUSD = 12, 0.5
	m := sized(loaded(t, &fakeAPI{}, "lea-a/a", a, b), 34, 8)
	ls := viewLines(m)
	if !strings.HasSuffix(ls[0], " 2 agents ─╮") {
		t.Fatalf("top %q", ls[0])
	}
	if !strings.HasSuffix(ls[1], " claude  42%│") || !strings.Contains(ls[2], "p/shop · idle · now") {
		t.Fatalf("row a %q / %q", ls[1], ls[2])
	}
	if !strings.HasSuffix(ls[3], " claude     │") {
		t.Fatalf("row b %q", ls[3])
	}
	if s := strings.Join(ls, "\n"); strings.Contains(s, "$") || strings.Contains(s, "✉") {
		t.Fatalf("cost or unread shown:\n%s", s)
	}
	m.ascii = true
	if ls := viewLines(m); !strings.HasSuffix(ls[1], " claude  42%|") || !strings.HasSuffix(ls[0], " 2 agents -+") {
		t.Fatalf("ascii %q / %q", ls[0], ls[1])
	}
	for i, l := range viewLines(m) {
		if lipgloss.Width(l) != 34 {
			t.Fatalf("line %d is %d wide: %q", i, lipgloss.Width(l), l)
		}
	}
}

func TestViewShowsTheApprovalCard(t *testing.T) {
	m := sized(loaded(t, &fakeAPI{}, "lea-a/b", carded("b", 7, true), ag("c", "idle", 1)), 34, 10)
	ls := viewLines(m)
	if !strings.Contains(ls[3], "⚠ Bash: make test") || !strings.Contains(ls[4], "[a]llow [d]eny [A]lways") {
		t.Fatalf("card:\n%s", strings.Join(ls, "\n"))
	}
	if !strings.Contains(ls[2], "needs you") {
		t.Fatalf("detail %q", ls[2])
	}
	if !strings.HasPrefix(ls[9], "╰─ a allow · d deny · A always ") {
		t.Fatalf("bottom %q", ls[9])
	}
	for i, l := range ls {
		if lipgloss.Width(l) != 34 {
			t.Fatalf("line %d is %d wide: %q", i, lipgloss.Width(l), l)
		}
	}
	once := sized(loaded(t, &fakeAPI{}, "lea-a/b", carded("b", 7, false)), 34, 8)
	if s := strings.Join(viewLines(once), "\n"); strings.Contains(s, "[A]lways") || strings.Contains(s, "A always") {
		t.Fatalf("always offered without a rule:\n%s", s)
	}
	once.ascii = true
	if s := strings.Join(viewLines(once), "\n"); !strings.Contains(s, "! Bash: make test") {
		t.Fatalf("ascii card:\n%s", s)
	}
}

func TestViewCleansTheApprovalCard(t *testing.T) {
	a := carded("b", 7, true)
	a.Approval.Tool = "Ba\x1b[2Jsh"
	a.Approval.Summary = "rm -rf /\x07‮" + strings.Repeat("x", 500)
	m := sized(loaded(t, &fakeAPI{}, "lea-a/b", a), 34, 8)
	raw := m.View()
	if strings.Contains(raw, "\x07") || strings.Contains(raw, "‮") || strings.Contains(sgr.ReplaceAllString(raw, ""), "\x1b") {
		t.Fatalf("control characters reach the terminal: %q", raw)
	}
	for i, l := range viewLines(m) {
		if lipgloss.Width(l) != 34 {
			t.Fatalf("line %d is %d wide: %q", i, lipgloss.Width(l), l)
		}
	}
}

func TestViewScrollKeepsACardedRowWhole(t *testing.T) {
	var as []Agent
	for i := 0; i < 5; i++ {
		as = append(as, ag(fmt.Sprintf("a%d", i), "idle", 1))
	}
	as = append([]Agent{carded("z", 9, true)}, as...) // launched first: the first row
	m := sized(loaded(t, &fakeAPI{}, "lea-a/a4", as...), 34, 10)
	m, _ = step(t, m, key("1")) // select the carded row
	s := strings.Join(viewLines(m), "\n")
	if !strings.Contains(s, "[a]llow") || !strings.Contains(s, "⚠ Bash") {
		t.Fatalf("the selected card is cut:\n%s", s)
	}
	m, _ = step(t, m, key("6")) // the last row: the card scrolls away whole
	s = strings.Join(viewLines(m), "\n")
	if !strings.Contains(s, "a4") || (strings.Contains(s, "⚠ Bash") != strings.Contains(s, "[a]llow")) {
		t.Fatalf("a card is half shown:\n%s", s)
	}
}

// The detail line has 28 cells in the 34-column deck: what the user acts on (the label, the age)
// comes first, the workdir last, so a real path never hides them.
func TestViewDetailKeepsLabelVisibleWithALongWorkdir(t *testing.T) {
	a := ag("a", "idle", clock.Unix()-120)
	a.Workdir, a.ForUser, a.CostUSD = "/home/someone/Documents/projects", 1, 0.48
	m := sized(loaded(t, &fakeAPI{}, "lea-a/a", a), 34, 6)
	ls := viewLines(m)
	if !strings.Contains(ls[2], "message for you · 2m") {
		t.Fatalf("detail %q", ls[2])
	}
}

func TestRowShowsRoleBranchAndStatus(t *testing.T) {
	a := ag("a", "idle", clock.Unix())
	a.Role, a.Branch, a.Status = "lead", "feat/x", "reviewing\x1b[2J the cart‮"
	d := ag("d", "working", clock.Unix())
	d.Role, d.Status = "dev", "writing tests"
	m := sized(loaded(t, &fakeAPI{}, "", a, d), 64, 10)
	ls := viewLines(m)
	s := strings.Join(ls, "\n")
	if !strings.Contains(s, "lead · p/shop · feat/x · reviewing?[2J the cart? · now") {
		t.Fatalf("lead row:\n%s", s)
	}
	if !strings.Contains(s, "p/shop · writing tests · now") || strings.Contains(s, "dev ·") {
		t.Fatalf("dev row:\n%s", s)
	}
	for i, l := range ls {
		if lipgloss.Width(l) != 64 {
			t.Fatalf("line %d is %d wide: %q", i, lipgloss.Width(l), l)
		}
	}
	b := ag("b", "blocked", 1)
	b.Status = "busy"
	if s := strings.Join(viewLines(sized(loaded(t, &fakeAPI{}, "", b), 64, 8)), "\n"); !strings.Contains(s, "see terminal") || strings.Contains(s, "busy") {
		t.Fatalf("a status hid what needs the user:\n%s", s)
	}
	m.roleFor = a.Key
	if ls := viewLines(m); !strings.HasPrefix(ls[len(ls)-1], "╰─ l lead · d dev · r reviewer ") {
		t.Fatalf("bottom %q", ls[len(ls)-1])
	}
}

func TestLongStatusAndBranchNeverHideTheAge(t *testing.T) {
	a := ag("a", "working", clock.Unix()-120)
	a.CostUSD, a.Status = 0.48, strings.Repeat("reviewing the whole cart ", 5)
	ls := viewLines(sized(loaded(t, &fakeAPI{}, "", a), 34, 8))
	if !strings.Contains(ls[2], "… · 2m") || lipgloss.Width(ls[2]) != 34 {
		t.Fatalf("long status: %q", ls[2])
	}
	b := ag("b", "working", clock.Unix()-120)
	b.CostUSD, b.Branch = 0.48, "feature/"+strings.Repeat("very-long-", 6)
	ls = viewLines(sized(loaded(t, &fakeAPI{}, "", b), 34, 8))
	if !strings.Contains(ls[2], "working · 2m") || strings.Contains(ls[2], "p/shop") || strings.Contains(ls[2], "feature") {
		t.Fatalf("long branch: %q", ls[2])
	}
	e := ag("e", "idle", clock.Unix()-120)
	e.CostUSD, e.Branch, e.Status = 0.48, "feat/é", "révision ✓ "+strings.Repeat("é", 40)
	m := sized(loaded(t, &fakeAPI{}, "", e), 34, 8)
	m.ascii = true
	for i, l := range viewLines(m) {
		for _, r := range l {
			if r > 0x7e {
				t.Fatalf("line %d not ASCII: %q", i, l)
			}
		}
		if lipgloss.Width(l) != 34 {
			t.Fatalf("line %d is %d wide: %q", i, lipgloss.Width(l), l)
		}
	}
	n := ag("n", "working", clock.Unix())
	n.Role, n.CostUSD, n.Status = "lead", 10.48, strings.Repeat("busy ", 10)
	ls = viewLines(sized(loaded(t, &fakeAPI{}, "", n), 26, 8))
	if !strings.Contains(ls[2], "lead · busy") || !strings.HasSuffix(strings.TrimSuffix(ls[2], "│"), "· now") {
		t.Fatalf("narrow: %q", ls[2])
	}
}

func TestCleanUsesTheSharedInvisibleSet(t *testing.T) {
	if got := clean("a­b؜c\U000e0041d￺"); got != "a?b?c?d?" {
		t.Fatalf("clean=%q", got)
	}
}

// An agent in a repo runs in the projects directory (Claude trusts a directory, not its
// subdirectories): its row names the repo, not the projects directory.
func TestRowOfAnAgentInARepoShowsTheRepo(t *testing.T) {
	a := ag("a", "idle", clock.Unix())
	a.Workdir, a.Repo, a.Role = "/home/u/projects", "shop", "reviewer"
	m := sized(loaded(t, &fakeAPI{}, "", a), 64, 6)
	if s := strings.Join(viewLines(m), "\n"); !strings.Contains(s, "reviewer · projects/shop · idle") {
		t.Fatalf("row:\n%s", s)
	}
}

// The provider is on the first line, the model the agent asked for on the second.
func TestRowShowsProviderAndModel(t *testing.T) {
	a := ag("a", "idle", clock.Unix())
	a.Workdir, a.Profile, a.Model = "/home/u/projects", "codex", "gpt-5.1-codex"
	s := strings.Join(viewLines(sized(loaded(t, &fakeAPI{}, "", a), 64, 6)), "\n")
	if !strings.Contains(s, "codex") || !strings.Contains(s, "gpt-5.1-codex · u/projects · idle") {
		t.Fatalf("row:\n%s", s)
	}
}

func TestAgentStatusCannotImitateLeasOwnLabels(t *testing.T) {
	a := ag("a", "idle", clock.Unix())
	a.Status = "◆ ⚠ ✉ ↳ needs you"
	s := strings.Join(viewLines(sized(loaded(t, &fakeAPI{}, "", a), 64, 8)), "\n")
	if !strings.Contains(s, "needs you") {
		t.Fatalf("the status text vanished:\n%s", s)
	}
	for _, g := range []string{"◆", "⚠", "✉", "↳"} {
		if strings.Contains(s, g+" needs you") || strings.Contains(s, "· "+g) {
			t.Fatalf("an agent status starts with lea's glyph %s:\n%s", g, s)
		}
	}
	if got := (Model{}).shortStatus(Agent{State: "idle", Status: "● ◐ ○ ok"}); got != "ok" {
		t.Fatalf("state glyphs kept: %q", got)
	}
}

// The frame takes the accent while the deck has the keyboard, and gives it back on blur.
func TestTheFrameShowsTheFocus(t *testing.T) {
	m := sized(loaded(t, &fakeAPI{}, "", ag("a", "idle", 1)), 34, 6)
	if m.frameStyle().GetForeground() != theme.Border {
		t.Fatal("an unfocused deck has a coloured frame")
	}
	m, _ = step(t, m, tea.FocusMsg{})
	if m.frameStyle().GetForeground() != theme.Accent {
		t.Fatal("the focused deck's frame is not the accent")
	}
	m, _ = step(t, m, tea.BlurMsg{})
	if m.frameStyle().GetForeground() != theme.Border {
		t.Fatal("the frame kept the accent after blur")
	}
}

// The logo is still while nothing works, a wave runs through its dots while an agent works, and
// the start animation lights the dots, then the name.
func TestViewLogoMoves(t *testing.T) {
	still := sized(loaded(t, &fakeAPI{}, "lea-a/a", ag("a", "idle", 1)), 34, 6)
	if ls := viewLines(still); !strings.HasPrefix(ls[0], "╭─ ●●● lea ") {
		t.Fatalf("still logo %q", ls[0])
	}
	m := sized(loaded(t, &fakeAPI{}, "lea-a/a", ag("a", "working", 1)), 34, 6)
	var tops []string
	for i := 0; i < 3; i++ {
		tops = append(tops, viewLines(m)[0][:len("╭─ ●•• lea")])
		nm, _ := m.Update(frameMsg{})
		for j := 1; j < waveStep; j++ {
			nm, _ = nm.Update(frameMsg{})
		}
		m = nm.(Model)
	}
	if tops[0] != "╭─ ●•• lea" || tops[1] != "╭─ •●• lea" || tops[2] != "╭─ ••● lea" {
		t.Fatalf("wave %q", tops)
	}
	in := sized(loaded(t, &fakeAPI{}, "lea-a/a", ag("a", "idle", 1)), 34, 6).WithIntro()
	if ls := viewLines(in); !strings.HasPrefix(ls[0], "╭─     ") {
		t.Fatalf("intro start %q", ls[0])
	}
	var nm tea.Model = in
	for i := 0; i < introFrames; i++ {
		nm, _ = nm.Update(frameMsg{})
	}
	if ls := viewLines(nm.(Model)); !strings.HasPrefix(ls[0], "╭─ ●●● lea ") {
		t.Fatalf("intro end %q", ls[0])
	}
}

func withSubagents(a Agent, n int) Agent { a.Subagents = n; return a }

// rowOf is the rendered line of the agent named name.
func rowOf(t *testing.T, m Model, name string) string {
	t.Helper()
	for _, l := range viewLines(m) {
		if strings.Contains(l, " "+name+" ") {
			return l
		}
	}
	t.Fatalf("no row for %s in:\n%s", name, strings.Join(viewLines(m), "\n"))
	return ""
}

// An idle agent whose subagents still run shows the spinner, ranks and animates as working; with
// none it rests on the green bullet. blocked, a message for the user and paused keep precedence.
func TestSubagentsShowAnIdleAgentWorking(t *testing.T) {
	busy := withSubagents(ag("busy", "idle", clock.Unix()), 2)
	rest := ag("rest", "idle", clock.Unix())
	m := loaded(t, &fakeAPI{}, "", busy, rest)
	if !m.ticking || !m.working() {
		t.Fatal("no animation while subagents run")
	}
	if r := rowOf(t, m, "busy"); !strings.Contains(r, "⠋") || strings.Contains(r, "●") {
		t.Fatalf("busy row %q", r)
	}
	if r := rowOf(t, m, "rest"); !strings.Contains(r, "●") {
		t.Fatalf("idle row %q", r)
	}
	if rank(busy, nil) != 3 || rank(rest, nil) != 4 || m.label(busy) != "working" || m.label(rest) != "idle" {
		t.Fatalf("rank %d %d label %q %q", rank(busy, nil), rank(rest, nil), m.label(busy), m.label(rest))
	}
	blocked := withSubagents(ag("b", "blocked", 1), 1)
	msg := withSubagents(ag("m", "idle", 1), 1)
	msg.ForUser = 1
	paused := withSubagents(ag("p", "idle", 1), 1)
	paused.Paused = true
	done := withSubagents(ag("d", "done", 1), 1)
	if rank(blocked, nil) != 0 || rank(msg, nil) != 2 || rank(paused, nil) != 4 || shownState(done) != "done" {
		t.Fatalf("precedence: %d %d %d %s", rank(blocked, nil), rank(msg, nil), rank(paused, nil), shownState(done))
	}
	m = loaded(t, &fakeAPI{}, "", paused)
	if m.working() {
		t.Fatal("a paused agent animates")
	}
	m = loaded(t, &fakeAPI{}, "", withSubagents(ag("x", "idle", 1), 0))
	if m.working() {
		t.Fatal("no subagents, still animating")
	}
}

func TestSnapshotWithoutSubagentsDecodes(t *testing.T) {
	var a Agent
	if err := json.Unmarshal([]byte(`{"key":"lea-a/x","state":"idle"}`), &a); err != nil || a.Subagents != 0 || shownState(a) != "idle" {
		t.Fatalf("agent=%+v err=%v", a, err)
	}
	if err := json.Unmarshal([]byte(`{"key":"lea-a/x","state":"idle","subagents":3}`), &a); err != nil || shownState(a) != "working" {
		t.Fatalf("agent=%+v err=%v", a, err)
	}
}

func withTask(a Agent, waiting, blocked bool) Agent {
	a.Waiting, a.BlockedOn = waiting, blocked
	return a
}

// Issue #72: one meaning per colour. Green only when nothing is pending, red when a human is
// needed (the agent's dialog or its task's blocked_on), the spinner while it works or waits
// (subagents, or a next step on its task). Each row of the issue's table, in order.
func TestStateColoursMeanOneThing(t *testing.T) {
	now := clock.Unix()
	paused := withTask(ag("p", "idle", now), true, true)
	paused.Paused = true
	for _, c := range []struct {
		name  string
		a     Agent
		shown string
		label string
		rank  int
	}{
		{"paused", paused, "idle", "paused", 4},
		{"done ignores the task", withTask(ag("d", "done", now), true, true), "done", "done, unseen", 1},
		{"stopped ignores the task", withTask(ag("s", "stopped", now), true, true), "stopped", "stopped", 5},
		{"own dialog", withTask(ag("b", "blocked", now), true, false), "blocked", "see terminal", 0},
		{"blocked_on while idle", withTask(ag("bi", "idle", now), true, true), "blocked", "blocked", 0},
		{"blocked_on beats working", withTask(ag("bw", "working", now), false, true), "blocked", "blocked", 0},
		{"blocked_on beats subagents", withSubagents(withTask(ag("bs", "idle", now), false, true), 2), "blocked", "blocked", 0},
		{"working", withTask(ag("w", "working", now), true, false), "working", "working", 3},
		{"subagents", withSubagents(ag("sa", "idle", now), 1), "working", "working", 3},
		{"next step", withTask(ag("n", "idle", now), true, false), "waiting", "waiting", 3},
		{"nothing pending", ag("g", "idle", now), "idle", "idle", 4},
	} {
		m := loaded(t, &fakeAPI{}, "", c.a)
		if got := shownState(c.a); !c.a.Paused && got != c.shown {
			t.Errorf("%s: shown %q, want %q", c.name, got, c.shown)
		}
		if got := m.label(c.a); got != c.label {
			t.Errorf("%s: label %q, want %q", c.name, got, c.label)
		}
		if got := rank(c.a, m.seen); got != c.rank {
			t.Errorf("%s: rank %d, want %d", c.name, got, c.rank)
		}
	}
}

func TestWaitingSpinsBlockedOnIsRedIdleIsGreen(t *testing.T) {
	now := clock.Unix()
	wait := withTask(ag("wait", "idle", now), true, false)
	red := withTask(ag("red", "idle", now), true, true)
	green := ag("green", "idle", now)
	m := loaded(t, &fakeAPI{}, "", wait, red, green)
	if !m.ticking || !m.working() {
		t.Fatal("a waiting agent does not animate")
	}
	if r := rowOf(t, m, "wait"); !strings.Contains(r, "⠋") || strings.Contains(r, "●") {
		t.Fatalf("waiting row %q", r)
	}
	for _, c := range []struct {
		state  string
		colour lipgloss.AdaptiveColor
	}{{"waiting", theme.Working}, {"blocked", theme.Blocked}, {"idle", theme.Done}} {
		if _, col := stateMark(c.state, now, now, 0, false); col != c.colour {
			t.Errorf("%s: colour %v, want %v", c.state, col, c.colour)
		}
	}
	if g, _ := stateMark("waiting", now-restAfter-1, now, 0, true); g != "." {
		t.Errorf("waiting rests: %q", g)
	}
	if g, _ := stateMark(shownState(red), now, now, 0, false); g != "●" {
		t.Errorf("blocked_on glyph %q", g)
	}
	if m.nextNeedingYou() != red.Key {
		t.Fatal("space does not find the blocked agent")
	}
	m = loaded(t, &fakeAPI{}, "", green)
	if m.working() {
		t.Fatal("an idle agent with nothing pending animates")
	}
}

func TestSnapshotTaskFlagsDecode(t *testing.T) {
	var a Agent
	if err := json.Unmarshal([]byte(`{"key":"lea-a/x","state":"idle","waiting":true,"blocked_on":true}`), &a); err != nil || !a.Waiting || !a.BlockedOn {
		t.Fatalf("agent=%+v err=%v", a, err)
	}
}
