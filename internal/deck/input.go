package deck

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The rename field (e), the new folder's name (f) and a text setting's value are typed in a modal
// drawn over the deck, like the stop confirmation: a title, the field and two buttons, Save and
// Cancel. The keys are the field's (editKey, settingFieldKey): enter saves, esc cancels. A click
// on a button picks it, a click outside the box cancels, a click inside does nothing.

const (
	saveLabel      = "[enter] Save"
	cancelLabelEsc = "[esc] Cancel"
)

// inputOpen tells whether an input modal is open, and gives its title and its text.
func (m Model) inputOpen() (title string, draft []rune, ok bool) {
	switch {
	case m.setEditing:
		name := "setting"
		if m.setSel < len(m.setList) {
			name = m.setList[m.setSel].Name
		}
		return "Edit " + name, m.setDraft, true
	case m.editFor != "":
		title = m.editTitle
		if title == "" {
			title = "Rename agent"
		}
		return title, m.draft, true
	}
	return "", nil, false
}

// inputAnswer closes the input modal: when save, as enter does, else as esc does.
func (m Model) inputAnswer(save bool) (tea.Model, tea.Cmd) {
	k := tea.KeyMsg{Type: tea.KeyEsc}
	if save {
		k = tea.KeyMsg{Type: tea.KeyEnter}
	}
	if m.setEditing {
		return m.settingFieldKey(k)
	}
	return m.editKey(k)
}

// inputMouse is a mouse event while the input modal is open.
func (m Model) inputMouse(e tea.MouseMsg) (tea.Model, tea.Cmd) {
	if e.Action != tea.MouseActionPress || (e.Button != tea.MouseButtonLeft && e.Button != tea.MouseButtonRight) {
		return m, nil
	}
	inner := m.innerWidth()
	top, lines, save, cancel := m.inputModal(inner, len(m.body(inner)))
	row, col := e.Y-1-top, e.X-1 // the frame's border is line 0 and column 0
	if row < 0 || row >= len(lines) || col < 1 || col > inner-2 {
		return m.inputAnswer(false)
	}
	if e.Button == tea.MouseButtonLeft && row == len(lines)-2 {
		switch {
		case col >= save[0] && col < save[1]:
			return m.inputAnswer(true)
		case col >= cancel[0] && col < cancel[1]:
			return m.inputAnswer(false)
		}
	}
	return m, nil
}

// inputModal lays the input modal out over a body of n lines, inner cells wide: the body line it
// starts at, its lines, and the columns [from, to) of the Save and Cancel buttons.
func (m Model) inputModal(inner, n int) (top int, lines []string, save, cancel [2]int) {
	c := m.cs()
	title, draft, _ := m.inputOpen()
	w := inner - 4 // a margin and a border on each side
	side := m.frameStyle().Render(c.v)
	title = fit(title, w-3, c)
	field := tail(safe(string(draft), c), max(1, w-3))
	lines = append(lines,
		" "+m.frameStyle().Render(c.tl+c.h+" ")+lipgloss.NewStyle().Foreground(theme.Accent).Bold(true).Render(title)+
			m.frameStyle().Render(" "+strings.Repeat(c.h, max(0, w-3-lipgloss.Width(title)))+c.tr)+" ",
		" "+side+strings.Repeat(" ", w)+side+" ",
		" "+side+padRight(" "+lipgloss.NewStyle().Foreground(theme.Text).Render(field)+
			lipgloss.NewStyle().Foreground(theme.Accent).Render(c.bar), w)+side+" ",
		" "+side+strings.Repeat(" ", w)+side+" ")

	// A margin and the border, then the Save button, a space and the Cancel button.
	save = [2]int{2, 2 + 1 + len(saveLabel)}
	cancel = [2]int{save[1] + 1, save[1] + 2 + len(cancelLabelEsc)}
	buttons := selected(lipgloss.NewStyle().Foreground(theme.Accent)).Bold(true).Render(c.mark+saveLabel) +
		" " + muted.Render(" "+cancelLabelEsc)
	if cancel[1]-2 > w { // too narrow for both buttons: the keys alone
		buttons, save, cancel = fit(" enter save, esc cancel", w, c), [2]int{}, [2]int{}
	}
	lines = append(lines,
		" "+side+buttons+strings.Repeat(" ", max(0, w-lipgloss.Width(buttons)))+side+" ",
		" "+m.frameStyle().Render(c.bl+strings.Repeat(c.h, w)+c.br)+" ")
	if n > 0 && len(lines) > n { // a short deck: the blank lines go, the field and the buttons stay
		lines = []string{lines[0], lines[2], lines[4], lines[5]}
		lines = lines[max(0, len(lines)-n):]
	}
	top = max(0, (n-len(lines))/2)
	return top, lines, save, cancel
}

// inputOverlay draws the input modal over the body lines.
func (m Model) inputOverlay(body []string, inner int) []string {
	top, lines, _, _ := m.inputModal(inner, len(body))
	out := append([]string(nil), body...)
	for len(out) < top+len(lines) {
		out = append(out, strings.Repeat(" ", inner))
	}
	copy(out[top:], lines)
	return out
}
