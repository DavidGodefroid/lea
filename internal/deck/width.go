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
	// read is the @lea_deck_width option and whether the deck's window is the lea deck window.
	read func() (option int, deckWin bool)
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
		read: func() (int, bool) {
			out, err := exec.Command(bin, "display-message", "-p", "#{@lea_deck}|#{@lea_deck_width}").Output()
			if err != nil {
				return 0, false
			}
			mark, vs, _ := strings.Cut(strings.TrimSpace(string(out)), "|")
			n, _ := strconv.Atoi(vs)
			return n, mark == "1"
		},
	}
}

// WithWidthDrive lets the deck change its own width (the < and > keys, the live LEA_DECK_WIDTH
// setting, the saved width restored at start): apply resizes the pane and stores the width,
// window is the tmux window's width for the half-window bound (0 when unknown), read is the
// width the tmux side last chose (a border drag) and the deck window's mark.
func (m Model) WithWidthDrive(apply func(w int) error, window func() int, read func() (int, bool)) Model {
	m.applyWidth, m.windowWidth, m.readWidth = apply, window, read
	return m
}

// window is the tmux window's width for the half-window bound, 0 when unknown.
func (m Model) window() int {
	if m.windowWidth == nil {
		return 0
	}
	return m.windowWidth()
}

// savedWidth is the stored LEA_DECK_WIDTH; ok is false when there is none to read.
func (m Model) savedWidth() (int, bool) {
	if m.settings == nil {
		return 0, false
	}
	ss, err := m.settings.List()
	if err != nil {
		return 0, false
	}
	for _, s := range ss {
		if s.Key == deckWidthKey {
			w, err := strconv.Atoi(s.Value)
			return w, err == nil
		}
	}
	return 0, false
}

// widthReadMsg is the tmux server's @lea_deck_width after a resize of the deck's pane (a border
// drag stores it there), and whether the deck's window is the lea deck window.
type widthReadMsg struct {
	option  int
	deckWin bool
}

// noteWidth reads the width the tmux side holds after the deck's pane was resized, so a width
// chosen there (a border drag) can be saved; nil without a store and a reader.
func (m Model) noteWidth() tea.Cmd {
	if m.settings == nil || m.readWidth == nil {
		return nil
	}
	read := m.readWidth
	return func() tea.Msg {
		option, deckWin := read()
		return widthReadMsg{option, deckWin}
	}
}

// keepDraggedWidth saves a width the tmux side chose (a border drag stores it in the server's
// option) as LEA_DECK_WIDTH, clamped to the bounds. Transient widths save nothing: the repaint
// nudge and a terminal's resize before the window-resized hook leave the option unchanged. Nor
// does a loop result: Set writes the file but does not resize the pane.
func (m Model) keepDraggedWidth(msg widthReadMsg) (tea.Model, tea.Cmd) {
	if !msg.deckWin || msg.option <= 0 || m.settings == nil {
		return m, nil
	}
	w := tmux.ClampDeckWidth(msg.option, m.window())
	if saved, ok := m.savedWidth(); ok && saved == w {
		return m, nil // already recorded (the deck's own < > keys and settings apply)
	}
	if err := m.settings.Set(deckWidthKey, strconv.Itoa(w)); err != nil {
		m.status = oneLine(err.Error())
	}
	return m, nil
}

// restoreWidth applies the saved deck width (LEA_DECK_WIDTH) at start: the session may be new,
// and the daemon lays the deck out at the default width. A width chosen by a border drag was
// saved when the pane resized (keepDraggedWidth), so the file holds it here.
func (m Model) restoreWidth() {
	if m.settings == nil || m.applyWidth == nil {
		return
	}
	if w, ok := m.savedWidth(); ok {
		_ = m.applyWidth(tmux.ClampDeckWidth(w, m.window()))
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
