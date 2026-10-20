package deck

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The stop confirmation is a modal drawn over the agent list: a title, what will happen and two
// buttons, Confirm and Cancel. The focus starts on Cancel. y confirms; n, esc and ctrl+c cancel;
// the arrows and tab move the focus, enter picks it. Any other key does nothing: the modal stays
// until the user answers. A click on a button picks it, a click outside the box cancels.

const (
	confirmLabel = "[y] Confirm"
	cancelLabel  = "[n/esc] Cancel"
)

// confirmKey is a key typed while the modal is open.
func (m Model) confirmKey(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "y":
		return m.answer(true)
	case "n", "esc", "ctrl+c":
		return m.answer(false)
	case "enter":
		return m.answer(m.confirmYes)
	case "left", "right", "h", "l", "tab", "shift+tab":
		m.confirmYes = !m.confirmYes
	}
	return m, nil
}

// answer closes the modal, and stops its agent when yes.
func (m Model) answer(yes bool) (tea.Model, tea.Cmd) {
	key, api := m.confirm, m.api
	m.confirm, m.confirmYes = "", false
	if !yes {
		return m, nil
	}
	return m, m.call(func(ctx context.Context) error { return api.Stop(ctx, key) })
}

// confirmMouse is a mouse event while the modal is open.
func (m Model) confirmMouse(e tea.MouseMsg) (tea.Model, tea.Cmd) {
	if e.Action != tea.MouseActionPress || (e.Button != tea.MouseButtonLeft && e.Button != tea.MouseButtonRight) {
		return m, nil
	}
	top, lines, yes, no := m.modal(m.innerWidth(), len(m.body(m.innerWidth())))
	row, col := e.Y-1-top, e.X-1 // the frame's border is line 0 and column 0
	if row < 0 || row >= len(lines) || col < 1 || col > m.innerWidth()-2 {
		return m.answer(false)
	}
	if e.Button == tea.MouseButtonLeft && row == len(lines)-2 {
		switch {
		case col >= yes[0] && col < yes[1]:
			return m.answer(true)
		case col >= no[0] && col < no[1]:
			return m.answer(false)
		}
	}
	return m, nil
}

// modal lays the modal out over a body of n lines, inner cells wide: the body line it starts at,
// its lines, and the columns [from, to) of the Confirm and Cancel buttons on its buttons line.
func (m Model) modal(inner, n int) (top int, lines []string, yes, no [2]int) {
	c := m.cs()
	name := "?"
	if i := m.index(m.confirm); i >= 0 {
		name = display(m.agents[i])
	}
	w := inner - 4 // a margin and a border on each side
	text := lipgloss.NewStyle().Foreground(theme.Text)
	line := func(s string, st lipgloss.Style) string {
		return " " + m.frameStyle().Render(c.v) + st.Render(padRight(" "+fit(s, w-1, c), w)) + m.frameStyle().Render(c.v) + " "
	}
	title := fit("Stop agent", w-3, c)
	lines = append(lines,
		" "+m.frameStyle().Render(c.tl+c.h+" ")+lipgloss.NewStyle().Foreground(theme.Accent).Bold(true).Render(title)+
			m.frameStyle().Render(" "+strings.Repeat(c.h, max(0, w-3-lipgloss.Width(title)))+c.tr)+" ",
		line("Stop "+name+"?", text.Bold(true)),
		line("Its process ends and its", text),
		line("row goes.", text),
		line("", text))

	mark := func(on bool) string {
		if on {
			return c.mark
		}
		return " "
	}
	button := func(label string, on bool) string {
		st := muted
		if on {
			st = lipgloss.NewStyle().Foreground(theme.Accent).Background(theme.Sel).Bold(true)
		}
		return st.Render(mark(on) + label)
	}
	// A margin and the border, then the Confirm button, a space and the Cancel button.
	yes = [2]int{2, 2 + 1 + len(confirmLabel)}
	no = [2]int{yes[1] + 1, yes[1] + 2 + len(cancelLabel)}
	buttons := button(confirmLabel, m.confirmYes) + " " + button(cancelLabel, !m.confirmYes)
	if no[1]-2 > w { // too narrow for both buttons: the keys alone
		buttons, yes, no = fit(" y confirm, esc cancel", w, c), [2]int{}, [2]int{}
	}
	lines = append(lines,
		" "+m.frameStyle().Render(c.v)+buttons+strings.Repeat(" ", max(0, w-lipgloss.Width(buttons)))+m.frameStyle().Render(c.v)+" ",
		" "+m.frameStyle().Render(c.bl+strings.Repeat(c.h, w)+c.br)+" ")
	if n > 0 && len(lines) > n { // a short deck: the explanation goes, the buttons stay
		lines = append(lines[:2], lines[len(lines)-2:]...)
		lines = lines[max(0, len(lines)-n):]
	}
	top = max(0, (n-len(lines))/2)
	return top, lines, yes, no
}

// overlay draws the modal over the body lines.
func (m Model) overlay(body []string, inner int) []string {
	top, lines, _, _ := m.modal(inner, len(body))
	out := append([]string(nil), body...)
	for len(out) < top+len(lines) {
		out = append(out, strings.Repeat(" ", inner))
	}
	copy(out[top:], lines)
	return out
}
