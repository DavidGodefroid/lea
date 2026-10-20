package deck

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Width is the deck pane's width, border included (agents.DeckWidth).
const Width = 34

type charset struct {
	tl, tr, bl, br, h, v, bar, mark, dot, more string
	ascii                                      bool
}

var (
	unicodeSet = charset{"╭", "╮", "╰", "╯", "─", "│", "▍", "›", "·", "…", false}
	asciiSet   = charset{"+", "+", "+", "+", "-", "|", "|", ">", "-", "~", true}
)

func charsetFor(ascii bool) charset {
	if ascii {
		return asciiSet
	}
	return unicodeSet
}

func (m Model) cs() charset { return charsetFor(m.ascii) }

// safe cleans s and, in ASCII mode, replaces every non-ASCII character.
func safe(s string, c charset) string {
	s = clean(s)
	if c.ascii {
		s = strings.Map(func(r rune) rune {
			if r > 0x7e {
				return '?'
			}
			return r
		}, s)
	}
	return s
}

// innerWidth is the width inside the frame.
func (m Model) innerWidth() int {
	if m.width < 20 {
		return Width - 2
	}
	return m.width - 2
}

func (m Model) View() string {
	c := m.cs()
	inner := m.innerWidth()
	w := inner + 2
	body := m.body(inner)
	if m.confirm != "" {
		body = m.overlay(body, inner)
	}
	if status := m.statusLine(inner); status != "" {
		body = append(body, status)
	}
	var b strings.Builder
	b.WriteString(m.top(w) + "\n")
	for _, l := range body {
		b.WriteString(m.frameStyle().Render(c.v) + l + m.frameStyle().Render(c.v) + "\n")
	}
	b.WriteString(m.bottom(w))
	return b.String()
}

// body is the lines between the borders above the status line: the key list or the rows, filled
// or cut to the deck's height.
func (m Model) body(inner int) []string {
	c := m.cs()
	var body []string
	switch {
	case m.help:
		for _, l := range helpLines {
			body = append(body, padRight(" "+fit(l, inner-1, c), inner))
		}
	case len(m.agents) == 0:
		body = append(body, padRight(" "+muted.Render(fit("No agents yet: press n.", inner-1, c)), inner))
	}
	if !m.help {
		body = append(body, m.rows(inner)...)
	}
	if m.height > 2 {
		room := m.height - 2
		if m.statusLine(inner) != "" {
			room--
		}
		for len(body) < room {
			body = append(body, strings.Repeat(" ", inner))
		}
		if len(body) > room {
			body = body[:room]
		}
	}
	return body
}

// helpLines is the key list (?).
var helpLines = []string{
	"j k, arrows move",
	"1-9         jump to a row",
	"enter       show and focus",
	"space       next needing you",
	"n           new agent",
	"s           new shell",
	"- |         split with a shell",
	"m           message the agent",
	"t           its thread",
	"e           rename",
	"r           its role",
	"a d A       allow deny always",
	"x           stop or remove",
	"S           sounds on or off",
	"ctrl+space  deck or terminal",
	"q           detach",
	"?           these keys",
}

// statusLine is the line above the bottom border: the rename field while it is open, else the
// status, else the update notice ("" when none).
func (m Model) statusLine(inner int) string {
	c := m.cs()
	if m.editFor != "" {
		const prompt = "name: "
		field := tail(safe(string(m.draft), c), inner-1-len(prompt)-1)
		return padRight(" "+lipgloss.NewStyle().Foreground(theme.Text).Render(prompt+field)+
			lipgloss.NewStyle().Foreground(theme.Accent).Render(c.bar), inner)
	}
	if m.status == "" {
		if m.noticeText == "" {
			return ""
		}
		return padRight(" "+muted.Render(fit(m.noticeText, inner-1, c)), inner)
	}
	return padRight(" "+alert.Render(fit(m.status, inner-1, c)), inner)
}

// frameStyle is the style of the deck's frame: the accent while the deck has the keyboard, so the
// focused pane is plain to see.
func (m Model) frameStyle() lipgloss.Style {
	if m.focused {
		return lipgloss.NewStyle().Foreground(theme.Accent)
	}
	return border
}

// logo is lea's mark on one line, the deck's three states in their colours (needs you, working,
// done: assets/logo-mark.svg), then the name. It moves only while the deck animates anyway: at
// start the dots light up one by one, then the name; while an agent works, a wave runs through
// them (one full dot, the others small). Still otherwise.
func (m Model) logo() string {
	full, small := "●", "•"
	if m.ascii {
		full, small = "O", "o"
	}
	wave := -1
	if m.working() {
		wave = (m.frame / waveStep) % 3
	}
	elapsed := introFrames - m.intro
	var b strings.Builder
	for i, col := range []lipgloss.AdaptiveColor{theme.Blocked, theme.Working, theme.Done} {
		switch {
		case m.intro > 0 && elapsed < (i+1)*introStep:
			b.WriteString(" ")
		case wave >= 0 && i != wave:
			b.WriteString(lipgloss.NewStyle().Foreground(col).Render(small))
		default:
			b.WriteString(lipgloss.NewStyle().Foreground(col).Render(full))
		}
	}
	name := "lea"
	if m.intro > 0 && elapsed < 4*introStep {
		name = "   "
	}
	return b.String() + " " + lipgloss.NewStyle().Foreground(theme.Accent).Bold(true).Render(name)
}

func (m Model) top(w int) string {
	c := m.cs()
	title := m.logo()
	tw := lipgloss.Width(title)
	n := 0
	for _, a := range m.agents {
		if !isShell(a) {
			n++
		}
	}
	count := fmt.Sprintf("%d agents", n)
	if n == 1 {
		count = "1 agent"
	}
	if m.sound == "off" {
		count = "muted " + c.dot + " " + count
	}
	// c.tl+c.h+" " (3) + title + " " + fill + " " + count + " "+c.h+c.tr (3)
	fill := w - 8 - tw - lipgloss.Width(count)
	if fill < 1 {
		return m.frameStyle().Render(c.tl+c.h+" ") + title + m.frameStyle().Render(" "+strings.Repeat(c.h, max(0, w-5-tw))+c.tr)
	}
	return m.frameStyle().Render(c.tl+c.h+" ") + title + m.frameStyle().Render(" "+strings.Repeat(c.h, fill)+" ") +
		muted.Render(count) + m.frameStyle().Render(" "+c.h+c.tr)
}

func (m Model) bottom(w int) string {
	c := m.cs()
	hint := fmt.Sprintf("n new %[1]s m msg %[1]s ? keys", c.dot)
	if a := m.selectedAgent(); a != nil && a.Approval != nil {
		hint = fmt.Sprintf("a allow %s d deny", c.dot)
		if a.Approval.Always {
			hint += fmt.Sprintf(" %s A always", c.dot)
		}
	}
	if m.confirm != "" {
		hint = fmt.Sprintf("y stop %s esc cancel", c.dot)
	}
	if m.roleFor != "" {
		hint = fmt.Sprintf("l lead %[1]s d dev %[1]s r reviewer", c.dot)
	}
	if m.editFor != "" {
		hint = fmt.Sprintf("enter save %s esc cancel", c.dot)
	}
	if m.menuFor != "" {
		hint = fmt.Sprintf("enter pick %s esc close", c.dot)
	}
	if m.help {
		hint = "any key closes"
	}
	// c.bl+c.h+" " (3) + hint + " " + fill + c.br (1)
	fill := w - 5 - lipgloss.Width(hint)
	if fill < 1 {
		return m.frameStyle().Render(c.bl + strings.Repeat(c.h, w-2) + c.br)
	}
	return m.frameStyle().Render(c.bl+c.h+" ") + muted.Render(hint) + m.frameStyle().Render(" "+strings.Repeat(c.h, fill)+c.br)
}

// rows renders the agents that fit, scrolled by whole rows so that the selected row (with its
// approval card and its open menu) is on screen.
func (m Model) rows(inner int) []string {
	out, _ := m.rowLines(inner)
	return out
}

// lineOwner is what a line of the rows shows: an agent's row (item -1) or an item of its menu.
type lineOwner struct {
	key  string
	item int
}

// rowLines are the lines rows renders and, line for line, what each shows.
func (m Model) rowLines(inner int) ([]string, []lineOwner) {
	rendered := make([][]string, len(m.agents))
	owners := make([][]lineOwner, len(m.agents))
	for i, a := range m.agents {
		rendered[i] = m.row(a, inner)
		for range rendered[i] {
			owners[i] = append(owners[i], lineOwner{a.Key, -1})
		}
		if a.Key == m.menuFor {
			for j, l := range m.menuLines(a, inner) {
				rendered[i] = append(rendered[i], l)
				owners[i] = append(owners[i], lineOwner{a.Key, j})
			}
		}
	}
	room := -1 // no height yet: everything
	if m.height > 0 {
		room = m.height - 2
		if m.status != "" || m.editFor != "" {
			room--
		}
		room = max(1, room)
	}
	start := 0
	if sel := m.index(m.selected); sel >= 0 && room > 0 {
		used := 0
		for j := sel; j >= 0; j-- {
			if j < sel && used+len(rendered[j]) > room {
				break
			}
			used += len(rendered[j])
			start = j
		}
	}
	var out []string
	var own []lineOwner
	for i := start; i < len(rendered); i++ {
		if room > 0 && len(out) > 0 && len(out)+len(rendered[i]) > room {
			break
		}
		out = append(out, rendered[i]...)
		own = append(own, owners[i]...)
	}
	return out, own
}

// row is two lines of exactly inner cells: mark, bar, glyph, name, profile; then the detail.
func (m Model) row(a Agent, inner int) []string {
	c := m.cs()
	sel := a.Key == m.selected
	bg := func(s lipgloss.Style) lipgloss.Style {
		if sel {
			return s.Background(theme.Sel)
		}
		return s
	}
	mark := " "
	if sel {
		mark = c.mark
	}
	const profW, ctxW = 6, 4
	nameW := inner - 4 - (1 + profW) - (1 + ctxW) // mark, space, glyph, space
	g, gc := stateMark(a.State, a.Updated, m.now().Unix(), m.frame, m.ascii)
	if isShell(a) {
		g, gc = "$", theme.Unknown
	}
	line1 := bg(lipgloss.NewStyle().Foreground(theme.Accent)).Render(mark) +
		bg(plain).Render(" ") +
		bg(lipgloss.NewStyle().Foreground(gc)).Render(g) +
		bg(plain).Render(" ") +
		bg(lipgloss.NewStyle().Foreground(theme.Text).Bold(sel)).Render(padRight(fit(display(a), nameW, c), nameW)) +
		bg(plain).Render(" ") +
		bg(muted).Render(padLeft(fit(a.Profile, profW, c), profW)) +
		bg(plain).Render(" ") +
		bg(muted).Render(padLeft(ctxText(a.CtxPct), ctxW))
	detail := fit(m.detail(a, inner-4), inner-4, c)
	line2 := bg(plain).Render("    ") + bg(muted).Render(padRight(detail, inner-4))
	lines := []string{line1, line2}
	if ap := a.Approval; ap != nil {
		warn := "⚠"
		if m.ascii {
			warn = "!"
		}
		what := clean(ap.Tool)
		if ap.Summary != "" {
			what += ": " + clean(ap.Summary)
		}
		keys := "[a]llow [d]eny"
		if ap.Always {
			keys += " [A]lways"
		}
		lines = append(lines,
			bg(plain).Render("    ")+bg(alert).Render(padRight(fit(warn+" "+what, inner-4, c), inner-4)),
			bg(plain).Render("    ")+bg(lipgloss.NewStyle().Foreground(theme.Text)).Render(padRight(fit(keys, inner-4, c), inner-4)))
	}
	return lines
}

// detail is the second line of a row, for width cells (0: unbounded): the role (dev, the default,
// is not shown), the directory, the branch, the short status and the age. When it does not fit,
// the directory and the branch go first so the status and the age stay.
func (m Model) detail(a Agent, width int) string {
	c := m.cs()
	if isShell(a) {
		return "shell " + c.dot + " exit or x ends it"
	}
	sep := " " + c.dot + " "
	build := func(path, branch, cut bool) string {
		var before, after []string
		if a.Role != "" && a.Role != "dev" {
			before = append(before, a.Role)
		}
		if path {
			dir := a.Workdir
			if a.Repo != "" { // an agent in a repo runs in the projects directory
				dir = filepath.Join(dir, a.Repo)
			}
			before = append(before, shortPath(dir))
		}
		if branch && a.Branch != "" {
			before = append(before, a.Branch)
		}
		if a.Updated > 0 {
			after = append(after, age(m.now().Unix()-a.Updated))
		}
		status := m.shortStatus(a)
		if cut && width > 0 {
			// The status is the agent's and unbounded: it is what gets cut, never the age.
			used := 0
			for _, p := range append(append([]string(nil), before...), after...) {
				used += lipgloss.Width(safe(p, c)) + lipgloss.Width(sep)
			}
			if w := width - used; w >= 1 {
				status = fit(status, w, c)
			} else {
				status = "" // no cell left for it: the age wins
			}
		}
		parts := before
		if status != "" {
			parts = append(parts, status)
		}
		return strings.Join(append(parts, after...), sep)
	}
	for _, v := range [][2]bool{{true, true}, {false, true}} {
		if d := build(v[0], v[1], false); width <= 0 || lipgloss.Width(safe(d, c)) <= width {
			return d
		}
	}
	return build(false, false, true)
}

// shortStatus is the row's label, or the agent's own task status (task_update) while it works or
// idles and nothing needs the user. The text is the agent's: fit cleans it with the whole line.
func (m Model) shortStatus(a Agent) string {
	if a.Status != "" && (a.State == "working" || a.State == "idle") && rank(a, m.seen) >= 3 {
		if st := strings.TrimLeft(a.Status, leaGlyphs); st != "" {
			return st
		}
	}
	return m.label(a)
}

// leaGlyphs are the marks lea draws itself (state, warning, unread, child): a status that starts
// with one would pass for a label of lea's, so the agent's text loses them at its start.
const leaGlyphs = " \t◆●◐○⚠✉↳⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"

// ctxText is a context percentage ("42%"), empty when unknown.
func ctxText(p int) string {
	if p < 0 || p > 100 {
		return ""
	}
	return fmt.Sprintf("%d%%", p)
}

// shortPath is the last two components of a directory: enough to tell projects apart.
func shortPath(p string) string {
	p = filepath.Clean(clean(p))
	parent, base := filepath.Base(filepath.Dir(p)), filepath.Base(p)
	if parent == "/" || parent == "." {
		return base
	}
	return parent + "/" + base
}

func age(s int64) string {
	switch {
	case s < 60:
		return "now"
	case s < 3600:
		return fmt.Sprintf("%dm", s/60)
	case s < 86400:
		return fmt.Sprintf("%dh", s/3600)
	}
	return fmt.Sprintf("%dd", s/86400)
}

// fit cleans s and cuts it to at most w cells, ending with c.more when cut.
func fit(s string, w int, c charset) string {
	s = safe(s, c)
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	return head(s, w-lipgloss.Width(c.more)) + c.more
}

func padRight(s string, w int) string {
	if d := w - lipgloss.Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

func padLeft(s string, w int) string {
	if d := w - lipgloss.Width(s); d > 0 {
		return strings.Repeat(" ", d) + s
	}
	return s
}
