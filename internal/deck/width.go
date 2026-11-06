package deck

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DavidGodefroid/lea/internal/tmux"
)

// deckWidthKey is the settings key of the deck's width.
const deckWidthKey = "LEA_DECK_WIDTH"

// widthDrive is how the deck changes its own pane's width: the tmux server it runs in (the deck
// is a pane of it) resizes the pane and keeps the width in its @lea_deck_width option, which
// lea.tmux.conf's window-resized hook and the daemon's layout repair follow.
type widthDrive struct {
	apply  func(w int) error // resize the deck's pane and set @lea_deck_width
	window func() int        // the tmux window's width, 0 when unknown
}

// tmuxWidthDrive drives the tmux server the deck runs in, through the tmux binary LEA_TMUX_BIN
// names ("tmux" otherwise); the server's socket is the pane's own (the TMUX environment). The
// drive only resizes and sets lea's option: lea never types into a terminal.
func tmuxWidthDrive(getenv func(string) string) widthDrive {
	bin := getenv("LEA_TMUX_BIN")
	if bin == "" {
		bin = "tmux"
	}
	run := func(args ...string) error {
		out, err := exec.Command(bin, args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
		}
		return nil
	}
	return widthDrive{
		apply: func(w int) error {
			return run("set-option", "-s", tmux.DeckWidthOption, strconv.Itoa(w), ";", "resize-pane", "-x", strconv.Itoa(w))
		},
		window: func() int {
			out, err := exec.Command(bin, "display-message", "-p", "#{window_width}").Output()
			if err != nil {
				return 0
			}
			n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
			return n
		},
	}
}

// WithWidthDrive lets the deck change its own width (the < and > keys, the live LEA_DECK_WIDTH
// setting, the saved width restored at start): apply resizes the pane and stores the width,
// window is the tmux window's width for the half-window bound (0 when unknown).
func (m Model) WithWidthDrive(apply func(w int) error, window func() int) Model {
	m.applyWidth, m.windowWidth = apply, window
	return m
}

// window is the tmux window's width for the half-window bound, 0 when unknown.
func (m Model) window() int {
	if m.windowWidth == nil {
		return 0
	}
	return m.windowWidth()
}

// restoreWidth applies the saved deck width (LEA_DECK_WIDTH) at start: the session may be new,
// and the daemon lays the deck out at the default width.
func (m Model) restoreWidth() {
	if m.settings == nil || m.applyWidth == nil {
		return
	}
	ss, err := m.settings.List()
	if err != nil {
		return
	}
	for _, s := range ss {
		if s.Key == deckWidthKey {
			if w, err := strconv.Atoi(s.Value); err == nil {
				_ = m.applyWidth(tmux.ClampDeckWidth(w, m.window()))
			}
			return
		}
	}
}

// deckBy grows (delta > 0) or shrinks the deck by delta columns (the < and > keys), clamped to
// the width's bounds, then persists and applies the new width. At a bound it says so and nothing
// changes.
func (m Model) deckBy(delta int) (tea.Model, tea.Cmd) {
	w := m.width
	if w <= 0 {
		w = Width // no size yet: the default
	}
	n := tmux.ClampDeckWidth(w+delta, m.window())
	if n == w {
		bound := "narrowest"
		if delta > 0 {
			bound = "widest"
		}
		m.status = fmt.Sprintf("deck width %d, the %s", w, bound)
		return m, nil
	}
	if m.settings != nil {
		if err := m.settings.Set(deckWidthKey, strconv.Itoa(n)); err != nil {
			m.status = oneLine(err.Error())
			return m, nil
		}
	}
	if m.applyWidth != nil {
		if err := m.applyWidth(n); err != nil {
			m.status = "deck width: " + oneLine(err.Error())
			return m, nil
		}
	}
	m.status = fmt.Sprintf("deck width %d", n)
	return m, nil
}
