package deck

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The hub panel (#244): every ticket of the hub's queue, as lox approve lists it, docked above the
// rows while the queue holds one, whatever agent it is for or none. The daemon fills it from the
// hub's queue only: what an agent did (its tool, the requester it wrote, a wait it registered)
// changes only the attribution a line shows, never which tickets show nor their order. H, or a click
// on a ticket, moves the selection into the panel, where a, d, v, o and r act on its ticket as on a
// card, enter (or a click on it) shows its details; esc leaves (#256).

// hubPanel owns the panel's lines on screen (lineOwner): no row has this key.
const hubPanel = "\x00hub"

// hubPanelRows is how many tickets the panel lists at once; the rest scroll under the selection.
const hubPanelRows = 5

// queuePending counts the hub's tickets that wait for a decision (an expired one waits on no one).
func (m Model) queuePending() int {
	n := 0
	for _, h := range m.hubQueue {
		if !h.Expired {
			n++
		}
	}
	return n
}

// enterPanel moves the selection into the hub panel (H), on its oldest pending ticket; H in the
// panel leaves it.
func (m Model) enterPanel() Model {
	if m.panel {
		m.panel = false
		return m
	}
	if len(m.hubQueue) == 0 {
		m.status = "no ticket waits in the hub"
		return m
	}
	m.panel, m.menuFor, m.menuPicker = true, "", false
	m.selectPanel(m.hubQueue[0])
	for _, h := range m.hubQueue {
		if !h.Expired {
			m.selectPanel(h)
			break
		}
	}
	return m
}

// selectPanel puts the panel's selection on h: a key acts on it lockAskDelay later. Its details
// show only once asked for.
func (m *Model) selectPanel(h HubCard) {
	m.panelSel, m.panelAt, m.panelDetail = h.Instance+"/"+h.ID, m.now(), false
}

// panelIndex is the index of the panel's selected ticket, -1 when none.
func (m Model) panelIndex() int {
	for i, h := range m.hubQueue {
		if h.Instance+"/"+h.ID == m.panelSel {
			return i
		}
	}
	return -1
}

// keepPanel follows a new queue: an empty queue closes the panel; a selected ticket gone (decided
// here or elsewhere) leaves the selection on the ticket now at its place.
func (m *Model) keepPanel() {
	if len(m.hubQueue) == 0 {
		m.panel, m.panelSel = false, ""
		return
	}
	if m.panel && m.panelIndex() < 0 {
		m.selectPanel(m.hubQueue[0])
	}
}

// panelKey is a key typed while the selection is in the hub panel: j, k and the arrows move in it,
// enter shows or hides the selected ticket's details, esc and H leave it, and the keys of a ticket (a, d, A, v, o, r) and the deck's own (q, ?, S,
// space, settings) do what they do elsewhere (ok false). Every other key would act on a row the
// user does not look at: nothing.
func (m Model) panelKey(k string) (tea.Model, tea.Cmd, bool) {
	switch k {
	case "j", "down", "k", "up":
		i, step := m.panelIndex(), 1
		if k == "k" || k == "up" {
			step = -1
		}
		if j := i + step; j >= 0 && j < len(m.hubQueue) {
			m.selectPanel(m.hubQueue[j])
		}
		return m, nil, true
	case "enter":
		m.panelDetail = !m.panelDetail
		return m, nil, true
	case "esc", "H":
		m.panel, m.panelDetail = false, false
		return m, nil, true
	case "r":
		if h := m.selectedHub(); h != nil && !h.Expired {
			m.status = "r asks the lock again for an expired ticket only"
			return m, nil, true
		}
		return m, nil, false
	case "a", "d", "A", "v", "o", "ctrl+o", "q", "ctrl+c", "?", "S", ",", " ":
		return m, nil, false
	}
	m.status = "in the hub panel: esc leaves it"
	return m, nil, true
}

// panelClick is a click on the panel's line of ticket i (its line or its card; -1: the title or a
// scroll mark): it closes an open menu and moves the selection into the panel, on that ticket; a
// left click on the selected ticket shows or hides its details. A click on the title enters the
// panel as H does.
func (m Model) panelClick(i int, left bool) Model {
	m.menuFor, m.menuPicker = "", false
	switch {
	case i < 0 || i >= len(m.hubQueue):
		if !m.panel {
			return m.enterPanel()
		}
	case m.panel && i == m.panelIndex():
		if left {
			m.panelDetail = !m.panelDetail
		}
	default:
		m.panel = true
		m.selectPanel(m.hubQueue[i])
	}
	return m
}

// panelLines are the hub panel's lines, inner cells wide: none while the queue is empty, else its
// title (red with a production ticket pending) with the keys that act on it, a line per ticket,
// and in the panel the selected ticket's card under its line, its details first when asked for.
func (m Model) panelLines(inner int) []string {
	out, _ := m.panelOwned(inner)
	return out
}

// panelOwned is panelLines and, line for line, the index in hubQueue of the ticket each shows (its
// line, its card), -1 on the others (the title, the scroll marks).
func (m Model) panelOwned(inner int) ([]string, []int) {
	if len(m.hubQueue) == 0 {
		return nil, nil
	}
	c := m.cs()
	warn := "⚠"
	if m.ascii {
		warn = "!"
	}
	title, prod := fmt.Sprintf("%s hub %s %d pending", warn, c.dot, m.queuePending()), false
	if n := len(m.hubQueue) - m.queuePending(); n > 0 {
		title += fmt.Sprintf(" %s %d expired", c.dot, n)
	}
	for _, h := range m.hubQueue {
		prod = prod || (h.Production && !h.Expired)
	}
	updown, enter := "↑↓", "⏎"
	if m.ascii {
		updown, enter = "j/k", "enter"
	}
	if m.panel {
		title += fmt.Sprintf(" %[1]s %[2]s select %[1]s %[3]s details %[1]s esc leave", c.dot, updown, enter)
	} else {
		title += fmt.Sprintf(" %[1]s H open %[1]s %[2]s select %[1]s %[3]s details %[1]s a approve %[1]s d deny", c.dot, updown, enter)
	}
	st := lipgloss.NewStyle().Foreground(theme.Working).Bold(true)
	if prod {
		st = alert.Bold(true)
	}
	out := []string{padRight(" "+st.Render(fit(title, inner-1, c)), inner)}
	own := []int{-1}
	sel := m.panelIndex()
	start := 0
	if m.panel && sel >= hubPanelRows {
		start = sel - hubPanelRows + 1
	}
	end := min(len(m.hubQueue), start+hubPanelRows)
	if start > 0 {
		out = append(out, padRight("   "+muted.Render(fmt.Sprintf("… %d above", start)), inner))
		own = append(own, -1)
	}
	for i := start; i < end; i++ {
		h := m.hubQueue[i]
		on := m.panel && i == sel
		bg := func(s lipgloss.Style) lipgloss.Style {
			if on {
				return s.Background(theme.Sel)
			}
			return s
		}
		mark := " "
		if on {
			mark = c.mark
		}
		lst := lipgloss.NewStyle().Foreground(theme.Text)
		if h.Production && !h.Expired {
			lst = alert
		}
		out = append(out, bg(lipgloss.NewStyle().Foreground(theme.Accent)).Render(mark)+
			bg(plain).Render("  ")+bg(lst.Bold(on)).Render(padRight(fit(m.panelLine(h), inner-3, c), inner-3)))
		own = append(own, i)
		if on {
			if m.panelDetail {
				for _, l := range m.panelDetails(h) {
					out = append(out, "    "+muted.Render(padRight(fit(l, inner-4, c), inner-4)))
					own = append(own, i)
				}
			}
			for _, l := range m.hubCardLines(h, inner, "    ", bg0, muted, false, true, 0) {
				out, own = append(out, l), append(own, i)
			}
		}
	}
	if end < len(m.hubQueue) {
		out = append(out, padRight("   "+muted.Render(fmt.Sprintf("… %d more", len(m.hubQueue)-end)), inner))
		own = append(own, -1)
	}
	return out, own
}

// panelDetails are the details of a panel ticket the card does not show: whom it is for, when it
// was asked and how long ago, when it expires. The card under them shows the rest (the Lock's
// screen, the agent's note, production, the keys). Raw: the caller fits them.
func (m Model) panelDetails(h HubCard) []string {
	now := m.now()
	when := func(s int64) string { return time.Unix(s, 0).In(now.Location()).Format("2006-01-02 15:04") }
	lines := []string{"requested by: " + m.attribution(h)}
	if h.Created > 0 {
		lines = append(lines, "created: "+when(h.Created)+" ("+agoText(now.Unix()-h.Created)+")")
	}
	switch {
	case h.Expired:
		lines = append(lines, "expires: expired")
	case h.Expires > 0:
		lines = append(lines, "expires: "+when(h.Expires)+" ("+leftText(h.Expires-now.Unix())+")")
	default:
		lines = append(lines, "expires: never")
	}
	return lines
}

func bg0(s lipgloss.Style) lipgloss.Style { return s }

// panelLine is a ticket's line in the panel: the Lock, the instance, production, the risk, whom it
// is for (the agent whose own wait names it: verified; the agent its requester claims, the
// agent's own words: unverified; or none), its age and the time left.
func (m Model) panelLine(h HubCard) string {
	c := m.cs()
	s := clean(h.Lock) + " " + clean(h.Instance)
	if h.Production {
		s += " " + c.dot + " PROD"
	}
	s += " " + c.dot + " " + clean(h.Risk) + " " + c.dot + " " + m.attribution(h)
	now := m.now().Unix()
	if h.Created > 0 {
		s += " " + c.dot + " " + agoText(now-h.Created)
	}
	switch {
	case h.Expired:
		s += " " + c.dot + " EXPIRED"
	case h.Expires > 0:
		s += " " + c.dot + " " + leftText(h.Expires-now)
	}
	return s
}

// agoText is how long ago a ticket was asked, s seconds ago.
func agoText(s int64) string {
	if a := age(max(0, s)); a != "now" {
		return a + " ago"
	}
	return "now"
}

// leftText is the time left before a ticket expires.
func leftText(s int64) string {
	if s <= 0 {
		return "expiring"
	}
	if s < 60 {
		return "<1m left"
	}
	return age(s) + " left"
}

// attribution is whom a panel ticket is for, by the name this deck shows for the agent.
func (m Model) attribution(h HubCard) string {
	if h.Agent == "" {
		return "no agent"
	}
	name := clean(h.AgentName)
	if i := m.index(h.Agent); i >= 0 {
		name = display(m.agents[i])
	}
	if name == "" {
		name = "?"
	}
	if h.Verified {
		return "waiting: " + name + " (verified)"
	}
	return "claims: " + name + " (unverified)"
}
