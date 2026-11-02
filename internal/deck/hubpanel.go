package deck

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The hub panel (#244): every ticket of the hub's queue, as lox approve lists it, docked above the
// rows while the queue holds one, whatever agent it is for or none. The daemon fills it from the
// hub's queue only: what an agent did (its tool, the requester it wrote, a wait it registered)
// changes only the attribution a line shows, never which tickets show nor their order. H moves the
// selection into the panel, where a, d, v, o and r act on its ticket as on a card; esc leaves.

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

// selectPanel puts the panel's selection on h: a key acts on it lockAskDelay later.
func (m *Model) selectPanel(h HubCard) {
	m.panelSel, m.panelAt = h.Instance+"/"+h.ID, m.now()
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
// esc and H leave it, and the keys of a ticket (a, d, A, v, o, r) and the deck's own (q, ?, S,
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
	case "esc", "H":
		m.panel = false
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

// panelLines are the hub panel's lines, inner cells wide: none while the queue is empty, else its
// title (red with a production ticket pending), a line per ticket, and in the panel the selected
// ticket's card under its line.
func (m Model) panelLines(inner int) []string {
	if len(m.hubQueue) == 0 {
		return nil
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
	if !m.panel {
		title += " " + c.dot + " H to act"
	}
	st := lipgloss.NewStyle().Foreground(theme.Working).Bold(true)
	if prod {
		st = alert.Bold(true)
	}
	out := []string{padRight(" "+st.Render(fit(title, inner-1, c)), inner)}
	sel := m.panelIndex()
	start := 0
	if m.panel && sel >= hubPanelRows {
		start = sel - hubPanelRows + 1
	}
	end := min(len(m.hubQueue), start+hubPanelRows)
	if start > 0 {
		out = append(out, padRight("   "+muted.Render(fmt.Sprintf("… %d above", start)), inner))
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
		if on {
			out = append(out, m.hubCardLines(h, inner, "    ", bg0, false, true, 0)...)
		}
	}
	if end < len(m.hubQueue) {
		out = append(out, padRight("   "+muted.Render(fmt.Sprintf("… %d more", len(m.hubQueue)-end)), inner))
	}
	return out
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
		a := age(max(0, now-h.Created))
		if a != "now" {
			a += " ago"
		}
		s += " " + c.dot + " " + a
	}
	switch {
	case h.Expired:
		s += " " + c.dot + " EXPIRED"
	case h.Expires > 0:
		s += " " + c.dot + " " + leftText(h.Expires-now)
	}
	return s
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
