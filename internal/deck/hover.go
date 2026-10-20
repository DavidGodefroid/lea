package deck

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The pointer over a row (no button held) shows the row's whole name, cut in the row when long,
// with the agent's name, role, profile and state, in the bottom border in place of the key hints:
// the rows never move under the pointer. tmux tells the deck nothing when the pointer leaves its
// pane, so the hover fades hoverLinger after the last move.

const hoverLinger = 4 * time.Second

// hoverEndMsg fades the hover of move seq; a later move makes it stale.
type hoverEndMsg struct{ seq int }

// hoverMove is the pointer moving over line y with no button held.
func (m Model) hoverMove(y int) (tea.Model, tea.Cmd) {
	key, item := m.hit(y)
	if item >= 0 || m.index(key) < 0 {
		key = ""
	}
	m.hoverFor = key
	m.hoverSeq++
	if key == "" {
		return m, nil
	}
	seq := m.hoverSeq
	return m, tea.Tick(hoverLinger, func(time.Time) tea.Msg { return hoverEndMsg{seq} })
}

// hovered is the agent under the pointer, nil when none or while something else owns the
// bottom line (a menu, a field, the key list, the modal).
func (m Model) hovered() *Agent {
	if m.hoverFor == "" || m.help || m.editFor != "" || m.menuFor != "" || m.confirm != "" || m.roleFor != "" {
		return nil
	}
	if i := m.index(m.hoverFor); i >= 0 {
		return &m.agents[i]
	}
	return nil
}

// hoverText is what the hover shows of a: the name it shows, whole, then the agent's name when a
// label hides it, the role, the profile and the state. Raw: fit makes it safe when drawn.
func hoverText(a Agent) string {
	parts := []string{display(a)}
	if a.Label != "" {
		parts = append(parts, a.Name)
	}
	if isShell(a) {
		return strings.Join(append(parts, "shell"), " · ")
	}
	if a.Role != "" {
		parts = append(parts, a.Role)
	}
	if a.Profile != "" {
		parts = append(parts, a.Profile)
	}
	state := shownState(a)
	if a.Paused {
		state = "paused"
	}
	return strings.Join(append(parts, state), " · ")
}
