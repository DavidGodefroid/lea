package deck

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The mouse in the column: a left click selects a row, the wheel moves the selection, a right
// click opens the row's menu under it. The menu lists what the keys do for that row; j/k or the
// wheel move in it, enter or a click picks, esc, q or a click elsewhere closes it. A right click
// anywhere else in the column (below the rows, on the frame) opens the deck's own menu under the
// last row: every right click opens a menu. A ctrl + right click makes a folder, holding the agent
// under the pointer when there is one.

// menuItem is an action of a menu: the key it types, or, in the folder picker (o), the folder it
// moves the agent to (target).
type menuItem struct{ label, key, target string }

// hints are the lines a menu shows under its items while the item typing that key is
// highlighted: what the action does, when its label alone does not say. A message is not
// typing into the agent's pane. No other menu uses these keys.
var hints = map[string]string{
	"m": "a note to its inbox, read on its next turn",
	"t": "its notes, questions and answers",
}

// hintLine is the owner item of a menu's hint line: inside the menu, on no item.
const hintLine = -2

// The picker's targets besides a folder's row key: the root, and a new folder.
const (
	rootTarget      = "\x00root"
	newFolderTarget = "\x00new"
)

// menuItems are the actions of a row, in the order the menu shows them.
func menuItems(a Agent, inFolder bool) []menuItem {
	if isShell(a) {
		return []menuItem{{"Show", "enter", ""}, {"Close", "x", ""}, {"Settings", ",", ""}}
	}
	var items []menuItem
	if ap := a.Approval; ap != nil {
		items = append(items, menuItem{"Allow", "a", ""}, menuItem{"Deny", "d", ""})
		if ap.Always {
			items = append(items, menuItem{"Always allow", "A", ""})
		}
	}
	if a.LockAsk != nil && a.Approval == nil {
		items = append(items, menuItem{"Open console", "a", ""}, menuItem{"Decline console", "d", ""})
	}
	items = append(items, menuItem{"Show", "enter", ""}, menuItem{"Send message", "m", ""}, menuItem{"Messages", "t", ""},
		menuItem{"Rename", "e", ""}, menuItem{"Role", "r", ""}, menuItem{"Move to folder", "o", ""})
	if inFolder {
		items = append(items, menuItem{"Move up", "K", ""}, menuItem{"Move down", "J", ""})
	}
	items = append(items, menuItem{"Split horizontally", "-", ""}, menuItem{"Split vertically", "|", ""})
	switch {
	case a.Paused:
		items = append(items, menuItem{"Resume", "p", ""})
	case pausable(a):
		items = append(items, menuItem{"Pause", "p", ""})
	}
	return append(items, menuItem{"Stop", "x", ""}, menuItem{"Settings", ",", ""})
}

// deckMenu is menuFor while the deck's own menu is open. No agent key is empty or holds a NUL.
const deckMenu = "\x00deck"

// deckMenuItems are the deck's own actions, those of no row; S only when the host plays sounds.
func deckMenuItems(sound string) []menuItem {
	items := []menuItem{{"New agent", "n", ""}, {"New shell", "s", ""}, {"New folder", "f", ""}}
	switch sound {
	case "on":
		items = append(items, menuItem{"Mute sounds", "S", ""})
	case "off":
		items = append(items, menuItem{"Sounds on", "S", ""})
	}
	return append(items, menuItem{"Keys", "?", ""}, menuItem{"Detach", "q", ""})
}

// folderMenuItems are the actions of a folder's row.
func folderMenuItems(f Folder) []menuItem {
	fold := menuItem{"Fold", "enter", ""}
	if f.Folded {
		fold.label = "Unfold"
	}
	return []menuItem{fold, {"Rename", "e", ""}, {"Move up", "K", ""}, {"Move down", "J", ""}, {"Delete", "x", ""}}
}

// pickerItems are the places agent key can move to (o): every other folder, numbered, then the
// root when it is in a folder, then a new folder.
func (m Model) pickerItems(key string) []menuItem {
	in := m.folderOf(key)
	var items []menuItem
	for i, f := range m.layout.Folders {
		if i == in {
			continue
		}
		k := ""
		if n := len(items) + 1; n <= 9 {
			k = string(rune('0' + n))
		}
		items = append(items, menuItem{f.Name, k, folderKey(f.ID)})
	}
	if in >= 0 {
		items = append(items, menuItem{"No folder", "0", rootTarget})
	}
	return append(items, menuItem{"New folder", "f", newFolderTarget})
}

// openItems are the open menu's items, nil when none is open.
func (m Model) openItems() []menuItem {
	if m.menuFor == deckMenu {
		return deckMenuItems(m.sound)
	}
	if fi := m.folderAt(m.menuFor); fi >= 0 {
		return folderMenuItems(m.layout.Folders[fi])
	}
	if a := m.menuAgent(); a != nil {
		if m.menuPicker {
			return m.pickerItems(a.Key)
		}
		return menuItems(*a, m.folderOf(a.Key) >= 0)
	}
	return nil
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
	m.menuFor, m.menuSel, m.menuPicker = key, 0, false
	m.confirm, m.confirmYes, m.roleFor = "", false, ""
}

// pick runs item i of the open menu as its key on the menu's row (the deck's menu: as a key typed
// in the deck).
func (m Model) pick(i int) (tea.Model, tea.Cmd) {
	items, a, on := m.openItems(), m.menuAgent(), m.menuFor
	m.menuFor, m.menuPicker = "", false
	if i < 0 || i >= len(items) {
		return m, nil
	}
	if a != nil {
		m.selected = a.Key
	} else if isFolderKey(on) {
		m.selected = on
	}
	if t := items[i].target; t != "" && a != nil {
		switch t {
		case rootTarget:
			return m.moveAgent(a.Key, -1), nil
		case newFolderTarget:
			nm, _ := m.newFolder(a.Key)
			return nm, nil
		}
		return m.moveAgent(a.Key, m.folderAt(t)), nil
	}
	if items[i].key == "enter" {
		return m.key("enter")
	}
	return m.key(items[i].key)
}

// menuKey is a key typed while the menu is open.
func (m Model) menuKey(k string) (tea.Model, tea.Cmd) {
	items := m.openItems()
	if items == nil {
		m.menuFor, m.menuPicker = "", false
		return m, nil
	}
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
		m.menuFor, m.menuPicker = "", false
		return m, nil
	}
	for i, it := range items {
		if it.key != "" && it.key == k {
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
	if e.Action == tea.MouseActionMotion {
		if e.Button != tea.MouseButtonNone { // a drag
			return m, nil
		}
		return m.hoverMove(e.Y)
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
		return m.move(m.pos(m.selected) + step)
	case tea.MouseButtonLeft, tea.MouseButtonRight:
	default:
		return m, nil
	}
	if e.Action != tea.MouseActionPress {
		return m, nil
	}
	key, item := m.hit(e.Y)
	right := e.Button == tea.MouseButtonRight
	if right && e.Ctrl {
		if item >= 0 || m.index(key) < 0 || isShell(m.agents[m.index(key)]) {
			key = ""
		}
		nm, _ := m.newFolder(key)
		return nm, nil
	}
	if m.menuFor != "" {
		if key == m.menuFor && item == hintLine {
			return m, nil
		}
		if key == m.menuFor && item >= 0 {
			if right { // on its own menu: the item under the pointer is highlighted
				m.menuSel = item
				return m, nil
			}
			return m.pick(item)
		}
		m.menuFor, m.menuPicker = "", false
		if !right && (key == "" || item >= 0) {
			return m, nil
		}
	}
	i := m.pos(key)
	if i < 0 {
		if right {
			m.openMenu(deckMenu)
		}
		return m, nil
	}
	nm, cmd := m.move(i)
	if right {
		mm := nm.(Model)
		mm.openMenu(key)
		return mm, cmd
	}
	return nm, cmd
}

// hit is what screen line y shows: the row's agent key (deckMenu on the deck's menu) and, on a
// menu line, the item's index (-1 on the row's own lines); "" outside the rows.
func (m Model) hit(y int) (string, int) {
	if m.help {
		return "", -1
	}
	_, owners := m.rowLines(m.innerWidth())
	if len(m.agents) == 0 {
		y-- // the body's first line says there is no agent yet
	}
	if y < 1 || y > len(owners) {
		return "", -1
	}
	o := owners[y-1]
	return o.key, o.item
}

// menuOwner is what line j of a menu of n items shows: the item, or the hint under them.
func menuOwner(j, n int) int {
	if j < n {
		return j
	}
	return hintLine
}

// menuLines are the lines of a menu of items, inner cells wide: one per item, then the
// highlighted item's hint, wrapped, when it has one.
func (m Model) menuLines(items []menuItem, inner int) []string {
	c := m.cs()
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
	if m.menuSel >= 0 && m.menuSel < len(items) && items[m.menuSel].target == "" && hints[items[m.menuSel].key] != "" {
		st := lipgloss.NewStyle().Foreground(theme.Muted).Background(theme.Sel)
		for _, l := range wrapWords(hints[items[m.menuSel].key], w-2, 3) {
			out = append(out, plain.Render("    ")+st.Render(fit(padRight(" "+l, w), w, c)))
		}
	}
	return out
}
