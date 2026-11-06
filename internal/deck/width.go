package deck

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

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
	// The deck's own pane (TMUX_PANE): a command without a target acts on the client's active
	// pane, which may be the agent's.
	// The target comes first: tmux reads no flag after an argument.
	target := func(cmd string, args ...string) []string {
		if p := getenv("TMUX_PANE"); p != "" {
			return append([]string{cmd, "-t", p}, args...)
		}
		return append([]string{cmd}, args...)
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
			return run(append([]string{"set-option", "-s", tmux.DeckWidthOption, strconv.Itoa(w), ";"}, target("resize-pane", "-x", strconv.Itoa(w))...)...)
		},
		window: func() int {
			out, err := exec.Command(bin, target("display-message", "-p", "#{window_width}")...).Output()
			if err != nil {
				return 0
			}
			n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
			return n
		},
		read: func() (int, bool) {
			out, err := exec.Command(bin, target("display-message", "-p", "#{@lea_deck}|#{@lea_deck_width}")...).Output()
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

// widthSettle is how long the deck's pane width must hold still before the deck reads the width
// the tmux side holds: a border drag resizes the pane at each step while @lea_deck_width keeps
// the old width, and only releasing the drag stores the new one (asynchronously, run-shell -b).
const widthSettle = 500 * time.Millisecond

// widthTries bounds how many more reads a settled pane gets while the option still holds the
// width the deck knew and differs from the pane's: the drag is still held, or its release is
// still storing the width. 60 reads, half a second apart, are 30 seconds.
const widthTries = 60

// widthSettleMsg says the deck's pane width held still for widthSettle since resize seq.
type widthSettleMsg struct{ seq, try int }

// widthReadMsg is the tmux server's @lea_deck_width once the deck's pane width settled (a border
// drag's release stores it there), and whether the deck's window is the lea deck window.
type widthReadMsg struct {
	option  int
	deckWin bool
	seq     int
	try     int
}

// settleWidth (re)arms the read of the tmux side's width after a change of the pane's width: the
// read happens widthSettle after the LAST change, so a drag's intermediate widths are never read.
// nil without a store and a reader.
func (m Model) settleWidth() (Model, tea.Cmd) {
	if m.settings == nil || m.readWidth == nil {
		return m, nil
	}
	m.widthSeq++
	return m, m.settleAfter(m.widthSeq, 0)
}

func (m Model) settleAfter(seq, try int) tea.Cmd {
	d := m.widthDelay
	if d <= 0 {
		d = widthSettle
	}
	return tea.Tick(d, func(time.Time) tea.Msg { return widthSettleMsg{seq, try} })
}

// noteWidth reads the width the tmux side holds once the pane's width settled; a settle of an
// older resize reads nothing.
func (m Model) noteWidth(msg widthSettleMsg) tea.Cmd {
	if msg.seq != m.widthSeq || m.readWidth == nil {
		return nil
	}
	read := m.readWidth
	return func() tea.Msg {
		option, deckWin := read()
		return widthReadMsg{option, deckWin, msg.seq, msg.try}
	}
}

// keepDraggedWidth saves a width the tmux side chose (a border drag's release stores it in the
// server's option) as LEA_DECK_WIDTH. Only a width the user chose is saved: the option differs
// from the one the deck knows (knownWidth: the width it applied itself, from the < > keys, the
// setting or the saved width, possibly clamped to a narrow window, or the one it last read). So
// a transient layout (the repaint nudge, a terminal's resize before the window-resized hook) and
// a window too narrow for the saved width save nothing. While the option is still the known one
// but the pane has another width, the drag is held or its release is still storing the width:
// the read is tried again. Set writes the file but does not resize the pane: no loop.
func (m Model) keepDraggedWidth(msg widthReadMsg) (tea.Model, tea.Cmd) {
	if msg.seq != m.widthSeq || !msg.deckWin || msg.option <= 0 || m.settings == nil {
		return m, nil
	}
	if msg.option == m.knownWidth {
		if msg.option != m.width && msg.try < widthTries {
			return m, m.settleAfter(msg.seq, msg.try+1)
		}
		return m, nil
	}
	m.knownWidth = msg.option
	// The release clamped the width to the window it was dragged in; only the fixed bounds here,
	// never the current window's half.
	w := tmux.ClampDeckWidth(msg.option, 0)
	if saved, ok := m.savedWidth(); ok && saved == w {
		return m, nil
	}
	if err := m.settings.Set(deckWidthKey, strconv.Itoa(w)); err != nil {
		m.status = oneLine(err.Error())
	}
	return m, nil
}

// applyDeckWidth applies w (already clamped) to the pane and the server's option, and remembers
// it as the width the deck knows the option holds.
func (m Model) applyDeckWidth(w int) (Model, error) {
	if m.applyWidth == nil {
		return m, nil
	}
	if err := m.applyWidth(w); err != nil {
		return m, err
	}
	m.knownWidth = w
	return m, nil
}

// restoreWidth applies the saved deck width (LEA_DECK_WIDTH) at start: the session may be new,
// and the daemon lays the deck out at the default width. A width chosen by a border drag was
// saved once the pane settled (keepDraggedWidth), so the file holds it here. With no saved width
// (or none applied) the deck takes the option's width as the known one, so that a later read of
// the same width saves nothing.
func (m Model) restoreWidth() Model {
	if m.settings == nil || m.applyWidth == nil {
		return m
	}
	if w, ok := m.savedWidth(); ok {
		if n, err := m.applyDeckWidth(tmux.ClampDeckWidth(w, m.window())); err == nil {
			return n
		}
	}
	if m.readWidth != nil {
		if option, deckWin := m.readWidth(); deckWin {
			m.knownWidth = option
		}
	}
	return m
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
	m, err := m.applyDeckWidth(n)
	if err != nil {
		m.status = "deck width: " + oneLine(err.Error())
		return m, nil
	}
	m.status = fmt.Sprintf("deck width %d", n)
	return m, nil
}
