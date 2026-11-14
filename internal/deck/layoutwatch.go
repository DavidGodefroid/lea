package deck

import (
	"reflect"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// layoutEvery is how often a deck that watches its layout (WithLayoutWatch) reads it again: lea
// web edits the same file (#416), and the deck shows its folders as the browser left them.
const layoutEvery = 2 * time.Second

type layoutMsg struct {
	layout Layout
	err    error
}

// WithLayoutWatch reads the layout's store again every layoutEvery and takes the folders it
// holds when they changed there.
func (m Model) WithLayoutWatch() Model {
	m.layoutWatch = true
	return m
}

// readLayout reads the store after layoutEvery.
func (m Model) readLayout() tea.Cmd {
	store := m.store
	if !m.layoutWatch || store == nil {
		return nil
	}
	return tea.Tick(layoutEvery, func(time.Time) tea.Msg {
		l, err := store.Load()
		return layoutMsg{l, err}
	})
}

// takeLayout adopts a layout another writer (lea web) saved, unless the user is editing a folder
// here (its name field, a menu or a confirmation open): the next read takes it then. A row the
// new layout no longer shows (its folder gone or folded) gives the selection to the first row.
func (m Model) takeLayout(msg layoutMsg) (Model, tea.Cmd) {
	next := m.readLayout()
	if msg.err != nil || m.editFor != "" || m.menuFor != "" || m.confirm != "" {
		return m, next
	}
	if reflect.DeepEqual(msg.layout.Folders, m.layout.Folders) || (len(msg.layout.Folders) == 0 && len(m.layout.Folders) == 0) {
		return m, next
	}
	m.layout = cloneLayout(msg.layout)
	if m.selected != "" && m.pos(m.selected) < 0 {
		m.selected = ""
		if es := m.entries(false); len(es) > 0 {
			m.selected = es[0].key
		}
	}
	return m, next
}
