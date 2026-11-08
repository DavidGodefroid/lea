package deck

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Width is the deck pane's default width, border included (agents.DeckWidth): the fallback
// while the deck does not know its size yet. The width the user chooses is applied to the pane
// itself (width.go); the rendering follows the real pane width.
const Width = 34

// folderIndent is how many cells an agent in a folder is shifted right of a root agent.
const folderIndent = 2

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
	if _, _, ok := m.inputOpen(); ok {
		body = m.inputOverlay(body, inner)
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
	case m.setOpen:
		room := 0
		if m.height > 2 {
			room = m.height - 2
			if m.statusLine(inner) != "" {
				room--
			}
		}
		body = m.settingsLines(inner, room)
	case m.help:
		for _, l := range helpLines {
			body = append(body, padRight(" "+fit(l, inner-1, c), inner))
		}
	case len(m.agents) == 0:
		body = append(body, padRight(" "+muted.Render(fit("No agents yet: press n.", inner-1, c)), inner))
	}
	if !m.help && !m.setOpen {
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
	"enter       show / fold folder",
	"space       next needing you",
	"n           new agent",
	"N           new, other provider",
	"s           new shell",
	"- |         split with a shell",
	"m           note to its inbox",
	"t           its messages",
	"e c         rename, colour",
	"f o u       new folder, in, out",
	"J K h l     reorder, fold",
	"r           its role",
	"p           pause (y), resume",
	"a d A       allow deny always",
	"x           stop or remove",
	"< >         deck width",
	"S T         sounds, theme",
	",           settings",
	"ctrl+space  deck or terminal",
	"q ?         detach, these keys",
}

// loxRunHint is what the status line says while the workspace has locks and no lox run serves
// them (#300): approvals live outside lea, in lox run.
const loxRunHint = "no lox run: open another terminal (outside lea) and run lox run to approve the agents' connections and writes"

// statusLine is the line above the bottom border: the status, else the lox run hint, else the
// update notice ("" when none). The fields are in a modal (input.go).
func (m Model) statusLine(inner int) string {
	c := m.cs()
	if m.status == "" {
		if _, _, ok := m.inputOpen(); ok { // the modal holds the field: nothing under it
			return ""
		}
		// Near or over a threshold, and nothing else to say: the gauge, amber or red.
		if m.noticeText == "" && m.pressure != nil && m.pressure.Level != "ok" {
			st := lipgloss.NewStyle().Foreground(theme.Working)
			if m.pressure.Level == "over" {
				st = alert
			}
			return padRight(" "+st.Render(fit(safe(m.pressure.Short, c), inner-1, c)), inner)
		}
		// The agents' Lock requests wait until lox run serves (#300): say where they are decided.
		if m.loxRunOff {
			return padRight(" "+lipgloss.NewStyle().Foreground(theme.Working).Render(fit(loxRunHint, inner-1, c)), inner)
		}
		if h := m.folderHint(); h != "" {
			return padRight(" "+muted.Render(fit(h, inner-1, c)), inner)
		}
		if m.noticeText == "" {
			return ""
		}
		return padRight(" "+muted.Render(fit(m.noticeText, inner-1, c)), inner)
	}
	return padRight(" "+alert.Render(fit(m.status, inner-1, c)), inner)
}

// folderHint is the keys of the selected row that the bottom border has no room for: a folder's,
// or u on an agent in a folder; "" on any other row.
func (m Model) folderHint() string {
	d := m.cs().dot
	switch {
	case m.folderAt(m.selected) >= 0:
		return fmt.Sprintf("enter fold %[1]s e name %[1]s c colour %[1]s x delete %[1]s J K move", d)
	case m.folderOf(m.selected) >= 0 && m.index(m.selected) >= 0:
		return "u out of folder"
	}
	return ""
}

// frameStyle is the style of the deck's frame: the accent while the deck has the keyboard, so the
// focused pane is plain to see.
func (m Model) frameStyle() lipgloss.Style {
	if m.focused && current.mono { // no accent: the focused frame is bold
		return lipgloss.NewStyle().Bold(true)
	}
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
	// The requests waiting for lox run, or a console that is down.
	if l := m.lockLine(); l != "" {
		count = l + " " + c.dot + " " + count
	}
	// c.tl+c.h+" " (3) + title + " " + fill + " " + count + " "+c.h+c.tr (3)
	fill := w - 8 - tw - lipgloss.Width(count)
	if fill < 1 {
		return m.frameStyle().Render(c.tl+c.h+" ") + title + m.frameStyle().Render(" "+strings.Repeat(c.h, max(0, w-5-tw))+c.tr)
	}
	return m.frameStyle().Render(c.tl+c.h+" ") + title + m.frameStyle().Render(" "+strings.Repeat(c.h, fill)+" ") +
		muted.Render(count) + m.frameStyle().Render(" "+c.h+c.tr)
}

// lockLine is what the top border says of the Locks' approvals, which lox run decides (#300):
// "locksql 2 wait for lox run" when one Lock's requests wait, "5 wait for lox run" when several
// Locks' do, else "lockhttp console down" for the first console down, else "lockhub unreachable"
// when lea follows the hub but cannot reach it (no agent is suspended on a Lock call then); ""
// when all is quiet. The Lock names are the daemon's, checked.
func (m Model) lockLine() string {
	total, one, down := 0, "", ""
	n := 0
	for _, l := range m.locks {
		if !lockNameRe.MatchString(l.Lock) {
			continue
		}
		if l.Pending > 0 {
			total += l.Pending
			n++
			one = l.Lock
		} else if l.Console == "down" && down == "" {
			down = l.Lock
		}
	}
	switch {
	case n == 1:
		return fmt.Sprintf("%s %d wait for lox run", one, total)
	case n > 1:
		return fmt.Sprintf("%d wait for lox run", total)
	case down != "":
		return down + " console down"
	case m.hubDown:
		return "lockhub unreachable"
	}
	return ""
}

var lockNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// lockInstanceRe is a lock's instance name, as lox run names it (internal/launcher's
// lockUpInstance).
var lockInstanceRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// lockAddedHint is the deck's hint for a lock lox run runs that the container does not mount:
// Docker cannot add a mount to a running container.
func lockAddedHint(name string) string {
	return "lock " + name + " added: restart lea to use it (lea stop, then lea)"
}

// lockHintLines are the hints of the locks added while the container runs, wrapped to the rows'
// width (inner cells, indented as a row's detail); a name that is not an instance is left out.
func (m Model) lockHintLines(inner int) []string {
	st := lipgloss.NewStyle().Foreground(theme.Working)
	var out []string
	for _, n := range m.locksAdded {
		if !lockInstanceRe.MatchString(n) {
			continue
		}
		for _, l := range wrapWords(lockAddedHint(n), inner-4, 0) {
			out = append(out, padRight("    "+st.Render(head(l, inner-4)), inner))
		}
	}
	return out
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
		if isFolderKey(m.confirm) {
			hint = fmt.Sprintf("y delete %s esc cancel", c.dot)
		}
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
	text := muted.Render(hint)
	if a := m.hovered(); a != nil {
		hint = fit(hoverText(*a), w-5, c)
		text = lipgloss.NewStyle().Foreground(theme.Text).Render(hint)
	}
	// c.bl+c.h+" " (3) + hint + " " + fill + c.br (1)
	fill := w - 5 - lipgloss.Width(hint)
	if fill < 1 && m.hovered() == nil {
		return m.frameStyle().Render(c.bl + strings.Repeat(c.h, w-2) + c.br)
	}
	return m.frameStyle().Render(c.bl+c.h+" ") + text + m.frameStyle().Render(" "+strings.Repeat(c.h, max(0, fill))+c.br)
}

// rows renders the agents that fit, scrolled by whole rows so that the selected row (with its
// approval card and its open menu) is on screen.
func (m Model) rows(inner int) []string {
	out, _ := m.rowLines(inner)
	return out
}

// lineOwner is what a line of the rows shows: an agent's or a folder's row (item -1) or an item
// of its menu.
type lineOwner struct {
	key  string
	item int
}

// rowLines are the lines rows renders and, line for line, what each shows.
func (m Model) rowLines(inner int) ([]string, []lineOwner) {
	es := m.entries(false)
	rendered := make([][]string, len(es))
	owners := make([][]lineOwner, len(es))
	for i, e := range es {
		if e.agent < 0 {
			rendered[i] = m.folderRow(e.in, inner)
		} else {
			rendered[i] = m.row(m.agents[e.agent], inner, e.in)
		}
		// A lock added while lea runs, on the selected agent: the same for every agent of the
		// container (its mounts), said once.
		if e.agent >= 0 && e.key == m.selected && !isShell(m.agents[e.agent]) {
			rendered[i] = append(rendered[i], m.lockHintLines(inner)...)
		}
		for range rendered[i] {
			owners[i] = append(owners[i], lineOwner{e.key, -1})
		}
		if e.key == m.menuFor {
			items := m.openItems()
			for j, l := range m.menuLines(items, inner) {
				rendered[i] = append(rendered[i], l)
				owners[i] = append(owners[i], lineOwner{e.key, menuOwner(j, len(items))})
			}
		}
		// A calm separation between folders: one blank line above each folder past the first
		// row, and above the first root agent after the folders, so that it does not read as
		// the last folder's. It owns no row: a click on it selects nothing, folds nothing.
		afterFolders := e.agent >= 0 && e.in < 0 && i > 0 && (es[i-1].agent < 0 || es[i-1].in >= 0)
		if (e.agent < 0 && i > 0) || afterFolders {
			rendered[i] = append([]string{strings.Repeat(" ", inner)}, rendered[i]...)
			owners[i] = append([]lineOwner{{"", -1}}, owners[i]...)
		}
	}
	// The deck's menu comes after the last row, as one more block that scrolls into view.
	focus := m.pos(m.selected) // the block kept on screen
	if m.menuFor == deckMenu {
		var r []string
		var o []lineOwner
		items := m.openItems() // its own actions, or the theme picker (T)
		for j, l := range m.menuLines(items, inner) {
			r, o = append(r, l), append(o, lineOwner{deckMenu, menuOwner(j, len(items))})
		}
		rendered, owners = append(rendered, r), append(owners, o)
		focus = len(rendered) - 1
	}
	room := -1 // no height yet: everything
	if m.height > 0 {
		room = m.height - 2
		if m.statusLine(inner) != "" { // the status, the rename field or the update notice
			room--
		}
		if len(m.agents) == 0 { // the body's line saying there is no agent yet
			room--
		}
		room = max(1, room)
	}
	start := 0
	if sel := focus; sel >= 0 && room > 0 {
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

// folderRow is a folder's header, one line of exactly inner cells, folded or not: the selection
// mark, the fold arrow and the name in bold — arrow and name in the folder's colour when it has
// one — then, on the right, the agent count and a compact status summary (folderSummary). An
// unfolded folder's agents follow on their own rows; nothing of the header repeats there.
func (m Model) folderRow(fi int, inner int) []string {
	c := m.cs()
	f := m.layout.Folders[fi]
	sel := folderKey(f.ID) == m.selected
	bg := func(s lipgloss.Style) lipgloss.Style {
		if sel {
			return selected(s)
		}
		return s
	}
	mark := " "
	if sel {
		mark = c.mark
	}
	arrow := "▾"
	if f.Folded {
		arrow = "▸"
	}
	if m.ascii {
		arrow = map[bool]string{true: ">", false: "v"}[f.Folded]
	}
	// The folder's colour, when it has one, is its arrow's and its name's.
	arrowC, nameC := lipgloss.TerminalColor(theme.Accent), lipgloss.TerminalColor(theme.Text)
	if col, ok := folderColour(f.Color); ok {
		arrowC, nameC = col, col
	}
	// mark, arrow, space, name, space, summary; the summary yields its cells to the name first.
	sum, sumW := m.folderSummary(fi, c, inner-8, bg)
	nameW := inner - 4 - sumW
	return []string{bg(lipgloss.NewStyle().Foreground(theme.Accent)).Render(mark) +
		bg(lipgloss.NewStyle().Foreground(arrowC)).Render(arrow) +
		bg(plain).Render(" ") +
		bg(lipgloss.NewStyle().Foreground(nameC).Bold(true)).Render(padRight(fit(f.Name, nameW, c), nameW)) +
		bg(plain).Render(" ") +
		sum}
}

// folderSummary is the compact status of folder fi's agents for its header: how many agents it
// holds, then, per state present (worst first: needs you, working, limited, done), the state's
// mark and how many, in the state's colour. Paused agents count in the total only. At most maxW
// cells: the calmest states go first when narrowed, the count never does. bg is the header's
// background.
func (m Model) folderSummary(fi int, c charset, maxW int, bg func(lipgloss.Style) lipgloss.Style) (string, int) {
	blocked, working, limited, done, total := 0, 0, 0, 0, 0
	for _, a := range m.folderMembers(fi) {
		total++
		switch s := shownState(a); {
		case a.Paused:
		case s == "blocked":
			blocked++
		case busy(a):
			working++
		case s == "limited":
			limited++
		case s == "idle" || s == "done":
			done++
		} // an unknown state counts in the total only, as the folder's dot ignored it
	}
	pick := func(u, a string) string {
		if c.ascii {
			return a
		}
		return u
	}
	type part struct {
		text string
		st   lipgloss.Style
	}
	var parts []part
	add := func(g string, st lipgloss.Style, n int) {
		if n > 0 {
			// A space between mark and count: the marks are East Asian Ambiguous width, and a
			// terminal that draws them wide spills them onto a digit glued to them (#355).
			parts = append(parts, part{g + " " + strconv.Itoa(n), st})
		}
	}
	// Needs you is bold as well as red: it is what the header is for. In ASCII each state keeps
	// its own glyph ("~" is limited's, as on a row), so they read apart without colour.
	add(pick("●", "!"), lipgloss.NewStyle().Foreground(theme.Blocked).Bold(true), blocked)
	add(pick("◐", "+"), lipgloss.NewStyle().Foreground(theme.Working), working)
	add(pick("◷", "~"), lipgloss.NewStyle().Foreground(theme.Idle), limited)
	add(pick("✓", "="), lipgloss.NewStyle().Foreground(theme.Done), done)
	count := strconv.Itoa(total)
	width := func(ps []part) int {
		w := len(count)
		for _, p := range ps {
			w += 1 + lipgloss.Width(p.text)
		}
		return w
	}
	for len(parts) > 0 && width(parts) > maxW {
		parts = parts[:len(parts)-1]
	}
	var b strings.Builder
	b.WriteString(bg(muted).Render(count))
	for _, p := range parts {
		b.WriteString(bg(plain).Render(" "))
		b.WriteString(bg(p.st).Render(p.text))
	}
	return b.String(), width(parts)
}

// row is two lines of exactly inner cells: mark, glyph, name, profile; then the detail. An
// agent in folder fi (-1: at the root) is shifted folderIndent cells right, its name and its
// detail under the folder's name — belonging shown by indentation alone, no guide glyph (#295).
func (m Model) row(a Agent, inner int, fi int) []string {
	c := m.cs()
	sel := a.Key == m.selected
	gap, pad := " ", "    "
	if fi >= 0 {
		gap = strings.Repeat(" ", folderIndent+1)
		pad = " " + gap + "  "
	}
	bg := func(s lipgloss.Style) lipgloss.Style {
		if sel {
			return selected(s)
		}
		return s
	}
	mark := " "
	if sel {
		mark = c.mark
	}
	const profW, ctxW = 6, 4
	in := lipgloss.Width(pad)                      // where the detail starts
	nameW := inner - in - (1 + profW) - (1 + ctxW) // mark, gap, glyph, space
	g, gc := stateMark(shownState(a), a.Updated, m.now().Unix(), m.frame, m.ascii)
	if isShell(a) {
		g, gc = "$", theme.Unknown
	} else if a.Paused {
		g, gc = "‖", theme.Muted
		if m.ascii {
			g = "="
		}
	}
	line1 := bg(lipgloss.NewStyle().Foreground(theme.Accent)).Render(mark) +
		bg(plain).Render(gap) +
		bg(lipgloss.NewStyle().Foreground(gc)).Render(g) +
		bg(plain).Render(" ") +
		m.nameCell(a, nameW, bg, sel) +
		bg(plain).Render(" ") +
		bg(muted).Render(padLeft(fit(a.Profile, profW, c), profW)) +
		bg(plain).Render(" ") +
		bg(muted).Render(padLeft(ctxText(a.CtxPct), ctxW))
	detail := fit(m.detail(a, inner-in), inner-in, c)
	line2 := bg(plain).Render(pad) + bg(muted).Render(padRight(detail, inner-in))
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
			bg(plain).Render(pad)+bg(alert).Render(padRight(fit(warn+" "+what, inner-in, c), inner-in)),
			bg(plain).Render(pad)+bg(lipgloss.NewStyle().Foreground(theme.Text)).Render(padRight(fit(keys, inner-in, c), inner-in)))
	}
	return lines
}

// nameCell is a row's name, exactly w cells: the agent's name, or its label then the name
// dimmed (#356), the handle the user and the tower share. When they do not both fit, the label is
// cut first: the name, what the tools take, stays whole as long as it fits alone.
func (m Model) nameCell(a Agent, w int, bg func(lipgloss.Style) lipgloss.Style, sel bool) string {
	c := m.cs()
	text := lipgloss.NewStyle().Foreground(theme.Text).Bold(sel)
	const gap = "  "
	name := safe(a.Name, c)
	room := w - lipgloss.Width(name) - len(gap) // what the label may take
	if a.Label == "" || a.Label == a.Name || room < 2 {
		return bg(text).Render(padRight(fit(a.Name, w, c), w))
	}
	label := fit(a.Label, room, c)
	return bg(text).Render(label) + bg(plain).Render(gap) + bg(muted).Render(padRight(name, w-lipgloss.Width(label)-len(gap)))
}

// detail is the second line of a row, for width cells (0: unbounded): the role (dev, the default,
// is not shown), the model (when one was asked for), the directory, the branch, the short status and the age. When it does not fit,
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
		if a.Model != "" { // the provider is on the first line
			before = append(before, a.Model)
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
// idles, unpaused, not woken by lea (the user sees why it started), and nothing needs the user.
// The text is the agent's: fit cleans it with the
// whole line.
func (m Model) shortStatus(a Agent) string {
	if a.Status != "" && !a.Paused && (a.State == "working" || a.State == "idle") && shownState(a) != "limited" && !a.Woken && rank(a, m.seen) >= 3 {
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
