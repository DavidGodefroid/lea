package deck

import (
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DavidGodefroid/lea/internal/settings"
)

// widthRec records the widths a test's drive is asked to apply.
type widthRec struct{ applied []int }

func (r *widthRec) apply(w int) error { r.applied = append(r.applied, w); return nil }

// < and > move the deck two columns, persist the width and apply it; the pane's resize then
// comes back as a size message.
func TestAngleBracketsResizeTheDeckAndKeepTheWidth(t *testing.T) {
	st := someSettings()
	m := withSettings(t, &fakeAPI{}, st)
	rec := &widthRec{}
	m.applyWidth, m.windowWidth = rec.apply, func() int { return 200 }
	m, _ = step(t, m, key(">"))
	if m.status != "deck width 36" || len(st.sets) != 1 || st.sets[0] != "LEA_DECK_WIDTH=36" || !reflect.DeepEqual(rec.applied, []int{36}) {
		t.Fatalf("status %q sets %q applied %v", m.status, st.sets, rec.applied)
	}
	m.width = 36 // tmux resized the pane; the size message follows
	m, _ = step(t, m, key("<"))
	if m.status != "deck width 34" || len(st.sets) != 2 || st.sets[1] != "LEA_DECK_WIDTH=34" || !reflect.DeepEqual(rec.applied, []int{36, 34}) {
		t.Fatalf("status %q sets %q applied %v", m.status, st.sets, rec.applied)
	}
}

// The keys keep the width within its bounds: 24 at least, 60 or half the window at most.
func TestAngleBracketsKeepTheDeckWithinItsBounds(t *testing.T) {
	for _, c := range []struct {
		name       string
		start, win int
		k          string
		wantStatus string
		applied    []int
	}{
		{"at the minimum", 24, 200, "<", "deck width 24, the narrowest", nil},
		{"to the minimum", 26, 200, "<", "deck width 24", []int{24}},
		{"at the maximum", 60, 200, ">", "deck width 60, the widest", nil},
		{"to the maximum", 59, 200, ">", "deck width 60", []int{60}},
		{"to half the window", 38, 80, ">", "deck width 40", []int{40}},
		{"at half the window", 40, 80, ">", "deck width 40, the widest", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := withSettings(t, &fakeAPI{}, someSettings())
			rec := &widthRec{}
			m.applyWidth, m.windowWidth = rec.apply, func() int { return c.win }
			m.width = c.start
			m, _ = step(t, m, key(c.k))
			if m.status != c.wantStatus || !reflect.DeepEqual(rec.applied, c.applied) {
				t.Fatalf("status %q, applied %v, want %q, %v", m.status, rec.applied, c.wantStatus, c.applied)
			}
		})
	}
}

// The width setting applies live, clamped to the window's half.
func TestTheDeckWidthAppliesLive(t *testing.T) {
	st := someSettings()
	st.list = append(st.list, settings.Setting{Key: deckWidthKey, Group: "Deck", Name: "Deck width", Help: "the deck column's width",
		Kind: settings.Text, Value: "34", Source: settings.SourceDefault, Apply: settings.ApplyNow, Live: true})
	m := withSettings(t, &fakeAPI{}, st)
	rec := &widthRec{}
	m.applyWidth, m.windowWidth = rec.apply, func() int { return 100 }
	m, _ = step(t, m, key(","))
	for range 4 {
		m, _ = step(t, m, key("j"))
	}
	m, _ = step(t, m, key("enter"))
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyCtrlU})
	m = typeIn(t, m, "60")
	m, cmd := step(t, m, key("enter"))
	if cmd != nil || len(st.sets) != 1 || st.sets[0] != "LEA_DECK_WIDTH=60" {
		t.Fatalf("cmd %v sets %q", cmd != nil, st.sets)
	}
	// 60 is past half the 100-column window: 50 is applied.
	if !reflect.DeepEqual(rec.applied, []int{50}) || !strings.Contains(m.status, "60") || !strings.Contains(m.status, "applies now") {
		t.Fatalf("applied %v status %q", rec.applied, m.status)
	}
}

// The saved width is applied to the pane the deck starts in (a session is laid out at the
// default width).
func TestTheSavedDeckWidthIsRestoredAtStart(t *testing.T) {
	st := someSettings()
	st.list = append(st.list, settings.Setting{Key: deckWidthKey, Group: "Deck", Name: "Deck width", Help: "the deck column's width",
		Kind: settings.Text, Value: "48", Source: settings.SourceFile, Apply: settings.ApplyNow, Live: true})
	rec := &widthRec{}
	m := newModel(&fakeAPI{}).WithSettings(st, "").WithWidthDrive(rec.apply, func() int { return 200 }, nil)
	m = m.restoreWidth()
	if !reflect.DeepEqual(rec.applied, []int{48}) {
		t.Fatalf("applied %v", rec.applied)
	}
	// No store (inside lea): nothing is applied, nothing panics.
	newModel(&fakeAPI{}).WithWidthDrive(rec.apply, nil, nil).restoreWidth()
	if len(rec.applied) != 1 {
		t.Fatalf("applied %v", rec.applied)
	}
}

// withWidth adds the deck width setting, at value, to st.
func withWidth(st *fakeSettings, value string) *fakeSettings {
	st.list = append(st.list, settings.Setting{Key: deckWidthKey, Group: "Deck", Name: "Deck width", Help: "the deck column's width",
		Kind: settings.Text, Value: value, Source: settings.SourceDefault, Apply: settings.ApplyNow, Live: true})
	return st
}

// tmuxSide is a fake tmux server: the @lea_deck_width option and the deck window's mark, with
// the reads counted.
type tmuxSide struct {
	option  int
	deckWin bool
	reads   int
}

func (s *tmuxSide) read() (int, bool) { s.reads++; return s.option, s.deckWin }

// dragDeck is a deck whose width drive is side's, settling after a millisecond.
func dragDeck(t *testing.T, st *fakeSettings, side *tmuxSide, rec *widthRec, window int) Model {
	t.Helper()
	m := withSettings(t, &fakeAPI{}, st)
	m = m.WithWidthDrive(rec.apply, func() int { return window }, side.read)
	m.widthDelay = time.Millisecond
	return m
}

// run runs cmd and feeds what it returns back into m until no command is left, as the program
// does; it returns m and how many settles were waited for.
func run(t *testing.T, m Model, cmd tea.Cmd) (Model, int) {
	t.Helper()
	settles := 0
	for cmd != nil {
		msg := cmd()
		if _, ok := msg.(widthSettleMsg); ok {
			settles++
		}
		m, cmd = step(t, m, msg)
	}
	return m, settles
}

// A border drag resizes the pane at each step while the server's option still holds the old
// width; the deck reads the option only once the width holds still, after the LAST change, and
// saves the width the drag's release stored there. A deck started again restores it.
func TestADraggedWidthIsSavedOnceTheDragEnds(t *testing.T) {
	st := withWidth(someSettings(), "34")
	side := &tmuxSide{option: 34, deckWin: true}
	rec := &widthRec{}
	m := dragDeck(t, st, side, rec, 200)
	m = m.restoreWidth() // 34 saved: applied, the option holds it
	if m.knownWidth != 34 {
		t.Fatalf("known width %d", m.knownWidth)
	}
	// The drag's steps: each resize re-arms the settle, so the older ones read nothing.
	var cmds []tea.Cmd
	for _, w := range []int{38, 42, 46, 49} {
		var cmd tea.Cmd
		m, cmd = step(t, m, tea.WindowSizeMsg{Width: w, Height: 30})
		cmds = append(cmds, cmd)
	}
	for _, c := range cmds[:3] {
		if _, cmd := step(t, m, c()); cmd != nil {
			t.Fatal("the settle of an older resize read the width")
		}
	}
	if side.reads != 0 {
		t.Fatalf("%d reads during the drag", side.reads)
	}
	// The last width held still, but the release has not stored it yet (run-shell -b): the
	// option is still 34. The read is tried again, and the release then stores 49.
	cmd := cmds[3]
	msg := cmd()
	m, cmd = step(t, m, msg)
	m, cmd = step(t, m, cmd()) // the read: 34, the pane 49
	if cmd == nil || len(st.sets) != 0 {
		t.Fatalf("a read before the release: retry %v, sets %q", cmd != nil, st.sets)
	}
	side.option = 49 // the release stored the width
	m, _ = run(t, m, cmd)
	if len(st.sets) != 1 || st.sets[0] != "LEA_DECK_WIDTH=49" {
		t.Fatalf("sets %q, want LEA_DECK_WIDTH=49", st.sets)
	}
	// A deck started again restores the dragged width from the file.
	rec2 := &widthRec{}
	m2 := newModel(&fakeAPI{}).WithSettings(st, "").WithWidthDrive(rec2.apply, func() int { return 200 }, side.read)
	m2 = m2.restoreWidth()
	if !reflect.DeepEqual(rec2.applied, []int{49}) || m2.knownWidth != 49 {
		t.Fatalf("after a restart: applied %v known %d, want [49] 49", rec2.applied, m2.knownWidth)
	}
}

// A drag held still (or a release slower than a settle) is read again, a bounded number of times.
func TestAHeldDragIsReadAgainBoundedly(t *testing.T) {
	st := withWidth(someSettings(), "34")
	side := &tmuxSide{option: 34, deckWin: true}
	m := dragDeck(t, st, side, &widthRec{}, 200)
	m = m.restoreWidth()
	m, cmd := step(t, m, tea.WindowSizeMsg{Width: 44, Height: 30})
	_, settles := run(t, m, cmd)
	if settles != widthTries+1 || side.reads != widthTries+1 || len(st.sets) != 0 {
		t.Fatalf("settles %d reads %d sets %q", settles, side.reads, st.sets)
	}
}

// Widths the user did not choose save nothing: the repaint nudge and a terminal's resize (the
// window-resized hook puts the pane back to the option), a window that is not the lea deck
// window, and a height-only change.
func TestTransientWidthsSaveNothing(t *testing.T) {
	st := withWidth(someSettings(), "34")
	side := &tmuxSide{option: 34, deckWin: true}
	m := dragDeck(t, st, side, &widthRec{}, 200)
	m = m.restoreWidth()
	// The nudge: one column more, then back.
	m, c1 := step(t, m, tea.WindowSizeMsg{Width: 35, Height: 30})
	m, c2 := step(t, m, tea.WindowSizeMsg{Width: 34, Height: 30})
	m, _ = step(t, m, c1())
	m, _ = run(t, m, c2)
	if len(st.sets) != 0 || side.reads != 1 {
		t.Fatalf("the nudge: sets %q reads %d", st.sets, side.reads)
	}
	// Not the deck window: nothing saved, nothing read again.
	side.option, side.deckWin = 52, false
	m, cmd := step(t, m, tea.WindowSizeMsg{Width: 52, Height: 30})
	m, _ = run(t, m, cmd)
	if len(st.sets) != 0 || side.reads != 2 {
		t.Fatalf("outside the deck window: sets %q reads %d", st.sets, side.reads)
	}
	// A height-only change reads nothing.
	if _, cmd := step(t, m, tea.WindowSizeMsg{Width: 52, Height: 25}); cmd != nil {
		t.Fatal("a height-only change read the width")
	}
	// No saved width: the option's width at start is the known one, and a nudge saves nothing.
	st2 := someSettings()
	side2 := &tmuxSide{option: 34, deckWin: true}
	m2 := dragDeck(t, st2, side2, &widthRec{}, 200).restoreWidth()
	m2, cmd = step(t, m2, tea.WindowSizeMsg{Width: 35, Height: 30})
	m2, cmd = step(t, m2, tea.WindowSizeMsg{Width: 34, Height: 30})
	run(t, m2, cmd)
	if len(st2.sets) != 0 {
		t.Fatalf("with no saved width the default was saved: %q", st2.sets)
	}
}

// A window too narrow for the saved width clamps the applied width to half the window, and that
// clamp is not saved: 60 stays the user's width, applied again in a wider window.
func TestANarrowWindowKeepsTheSavedWidth(t *testing.T) {
	st := withWidth(someSettings(), "60")
	side := &tmuxSide{deckWin: true}
	rec := &widthRec{}
	m := dragDeck(t, st, side, rec, 100)
	m.applyWidth = func(w int) error { side.option = w; return rec.apply(w) } // apply sets the option
	m = m.restoreWidth()
	if !reflect.DeepEqual(rec.applied, []int{50}) || side.option != 50 {
		t.Fatalf("applied %v option %d", rec.applied, side.option)
	}
	m, cmd := step(t, m, tea.WindowSizeMsg{Width: 50, Height: 30})
	run(t, m, cmd)
	if len(st.sets) != 0 {
		t.Fatalf("the window's clamp was saved: %q", st.sets)
	}
	// The width setting set past the window's half: its clamp is not read back as a drag either.
	m.windowWidth = func() int { return 80 }
	m, _ = m.applyDeckWidth(40)
	m, cmd = step(t, m, tea.WindowSizeMsg{Width: 40, Height: 30})
	run(t, m, cmd)
	if len(st.sets) != 0 {
		t.Fatalf("an applied width was saved as a drag: %q", st.sets)
	}
	// A drag in that window is the user's: saved, clamped only to the fixed bounds.
	m, cmd = step(t, m, tea.WindowSizeMsg{Width: 30, Height: 30})
	side.option = 30
	run(t, m, cmd)
	if len(st.sets) != 1 || st.sets[0] != "LEA_DECK_WIDTH=30" {
		t.Fatalf("sets %q, want LEA_DECK_WIDTH=30", st.sets)
	}
}

// The key list names < and >.
func TestTheKeyListNamesTheWidthKeys(t *testing.T) {
	if s := strings.Join(helpLines, "\n"); !strings.Contains(s, "< >") || !strings.Contains(s, "deck width") {
		t.Fatalf("key list:\n%s", s)
	}
}
