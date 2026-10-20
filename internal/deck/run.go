package deck

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Run draws the deck until its pane is closed. st is its settings screen's store (nil: none, off
// says why). All motion: the pointer over a row, no button held, shows its whole name (hover.go).
func Run(api API, container string, ascii bool, notice func() string, st SettingsStore, off string) error {
	m := New(api, container, ascii, time.Now).WithIntro().WithNotice(notice).WithSettings(st, off)
	_, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithReportFocus(), tea.WithMouseAllMotion()).Run()
	return err
}

// RunThread draws the thread popup of one agent until it is closed.
func RunThread(api ThreadAPI, key string, compose, ascii bool) error {
	_, err := tea.NewProgram(NewThread(api, key, compose, ascii, time.Now), tea.WithAltScreen()).Run()
	return err
}

const placeholderText = "No agent shown.\n\nIn the deck: j/k pick an agent, n starts one."

// placeholder fills the slot when no agent is shown. It ignores every key: it never quits on
// its own, so the slot never collapses.
type placeholder struct{ width, height int }

func (p placeholder) Init() tea.Cmd { return nil }

func (p placeholder) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if s, ok := msg.(tea.WindowSizeMsg); ok {
		p.width, p.height = s.Width, s.Height
	}
	return p, nil
}

func (p placeholder) View() string {
	if p.width == 0 || p.height == 0 {
		return placeholderText
	}
	return lipgloss.Place(p.width, p.height, lipgloss.Center, lipgloss.Center, muted.Render(placeholderText))
}

func RunPlaceholder() error {
	_, err := tea.NewProgram(placeholder{}, tea.WithAltScreen()).Run()
	return err
}
