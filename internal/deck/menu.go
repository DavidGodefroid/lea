package deck

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The mouse in the column: a left click selects a row, the wheel moves the selection, a right
// click opens the row's menu under it. The menu lists what the keys do for that row; j/k or the
// wheel move in it, enter or a click picks, esc, q or a click elsewhere closes it.

type menuItem struct{ label, key string }

// menuItems are the actions of a row, in the order the menu shows them.
func menuItems(a Agent) []menuItem {
	if isShell(a) {
		return []menuItem{{"Show", "enter"}, {"Close", "x"}, {"Settings", ","}}
	}
	var items []menuItem
	if ap := a.Approval; ap != nil {
		items = append(items, menuItem{"Allow", "a"}, menuItem{"Deny", "d"})
		if ap.Always {
			items = append(items, menuItem{"Always allow", "A"})
		}
	}
	items = append(items, menuItem{"Show", "enter"}, menuItem{"Message", "m"}, menuItem{"Thread", "t"},
		menuItem{"Rename", "e"}, menuItem{"Role", "r"}, menuItem{"Split horizontally", "-"},
		menuItem{"Split vertically", "|"})
	switch {
	case a.Paused:
		items = append(items, menuItem{"Resume", "p"})
	case pausable(a):
		items = append(items, menuItem{"Pause", "p"})
	}
	return append(items, menuItem{"Stop", "x"}, menuItem{"Settings", ","})
}

// menuAgent is the agent whose menu is open, nil when none.
func (m Model) menuAgent() *Agent {
	if i := m.index(m.menuFor); m.menuFor != "" && i >= 0 {
		return &m.agents[i]
	}
	return nil
}

// openMenu opens key's menu on its first item.
func (m *Model) openMenu(key string) {
	m.menuFor, m.menuSel = key, 0
	m.confirm, m.confirmYes, m.roleFor = "", false, ""
}

// pick runs item i of the open menu as its key on the menu's row.
func (m Model) pick(i int) (tea.Model, tea.Cmd) {
	a := m.menuAgent()
	m.menuFor = ""
	if a == nil {
		return m, nil
	}
	items := menuItems(*a)
	if i < 0 || i >= len(items) {
		return m, nil
	}
	m.selected = a.Key
	if items[i].key == "enter" {
		return m.key("enter")
	}
	return m.key(items[i].key)
}

// menuKey is a key typed while the menu is open.
func (m Model) menuKey(k string) (tea.Model, tea.Cmd) {
	a := m.menuAgent()
	if a == nil {
		m.menuFor = ""
		return m, nil
	}
	items := menuItems(*a)
	switch k {
	case "j", "down":
		m.menuSel = min(m.menuSel+1, len(items)-1)
		return m, nil
	case "k", "up":
		m.menuSel = max(m.menuSel-1, 0)
		return m, nil
	case "enter":
		return m.pick(m.menuSel)
	case "esc", "q", "ctrl+c":
		m.menuFor = ""
		return m, nil
	}
	for i, it := range items {
		if it.key == k {
			return m.pick(i)
		}
	}
	return m, nil
}

// mouse handles a mouse event over the deck.
func (m Model) mouse(e tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.help || m.editFor != "" || m.setOpen {
		return m, nil
	}
	if m.confirm != "" {
		return m.confirmMouse(e)
	}
	switch e.Button {
	case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
		if e.Action != tea.MouseActionPress {
			return m, nil
		}
		step := 1
		if e.Button == tea.MouseButtonWheelUp {
			step = -1
		}
		if m.menuFor != "" {
			return m.menuKey(map[int]string{1: "j", -1: "k"}[step])
		}
		return m.move(m.index(m.selected) + step)
	case tea.MouseButtonLeft, tea.MouseButtonRight:
	default:
		return m, nil
	}
	if e.Action != tea.MouseActionPress {
		return m, nil
	}
	key, item := m.hit(e.Y)
	if m.menuFor != "" {
		if key == m.menuFor && item >= 0 && e.Button == tea.MouseButtonLeft {
			return m.pick(item)
		}
		m.menuFor = ""
		if key == "" || item >= 0 {
			return m, nil
		}
	}
	i := m.index(key)
	if i < 0 {
		return m, nil
	}
	nm, cmd := m.move(i)
	if e.Button == tea.MouseButtonRight {
		mm := nm.(Model)
		mm.openMenu(key)
		return mm, cmd
	}
	return nm, cmd
}

// hit is what screen line y shows: the row's agent key and, on a menu line, the item's index
// (-1 on the row's own lines); "" outside the rows.
func (m Model) hit(y int) (string, int) {
	if m.help || len(m.agents) == 0 {
		return "", -1
	}
	_, owners := m.rowLines(m.innerWidth())
	if y < 1 || y > len(owners) {
		return "", -1
	}
	o := owners[y-1]
	return o.key, o.item
}

// menuLines are the open menu's lines for row a, inner cells wide.
func (m Model) menuLines(a Agent, inner int) []string {
	c := m.cs()
	items := menuItems(a)
	w := inner - 4
	var out []string
	for i, it := range items {
		hint := it.key
		label := " " + it.label
		line := padRight(label, w-len(hint)-1) + hint + " "
		st := lipgloss.NewStyle().Foreground(theme.Text).Background(theme.Sel)
		if i == m.menuSel {
			st = lipgloss.NewStyle().Foreground(theme.Accent).Background(theme.Sel).Bold(true)
			line = c.mark + line[len(" "):]
		}
		out = append(out, plain.Render("    ")+st.Render(fit(line, w, c)))
	}
	return out
}
