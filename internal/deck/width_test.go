package deck

import (
	"reflect"
	"strings"
	"testing"

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
	m.restoreWidth()
	if !reflect.DeepEqual(rec.applied, []int{48}) {
		t.Fatalf("applied %v", rec.applied)
	}
	// No store (inside lea): nothing is applied, nothing panics.
	newModel(&fakeAPI{}).WithWidthDrive(rec.apply, nil, nil).restoreWidth()
	if len(rec.applied) != 1 {
		t.Fatalf("applied %v", rec.applied)
	}
}

// A border drag stores the width in the tmux server's option; the deck, seeing its pane resized,
// saves it as LEA_DECK_WIDTH, and a deck started again restores it. Transient widths (the
// repaint nudge, a terminal resize before the hook) and a window that is not the lea deck window
// save nothing.
func TestADraggedWidthIsSavedAndRestored(t *testing.T) {
	withWidth := func(st *fakeSettings, value string) *fakeSettings {
		st.list = append(st.list, settings.Setting{Key: deckWidthKey, Group: "Deck", Name: "Deck width", Help: "the deck column's width",
			Kind: settings.Text, Value: value, Source: settings.SourceDefault, Apply: settings.ApplyNow, Live: true})
		return st
	}
	st := withWidth(someSettings(), "34")
	m := withSettings(t, &fakeAPI{}, st)
	m.windowWidth = func() int { return 200 }
	// The drag ends: the binding stored 49 in the option, the pane takes 49.
	m.readWidth = func() (int, bool) { return 49, true }
	m, cmd := step(t, m, tea.WindowSizeMsg{Width: 49, Height: 30})
	if cmd == nil {
		t.Fatal("a resize scheduled no read of the width")
	}
	m, _ = step(t, m, cmd())
	if len(st.sets) != 1 || st.sets[0] != "LEA_DECK_WIDTH=49" {
		t.Fatalf("sets %q, want LEA_DECK_WIDTH=49", st.sets)
	}
	// A transient width (the option unchanged at 34... here at 49, the saved one): nothing saved.
	m, cmd = step(t, m, tea.WindowSizeMsg{Width: 50, Height: 30})
	m, _ = step(t, m, cmd())
	if len(st.sets) != 1 {
		t.Fatalf("a transient width was saved: sets %q", st.sets)
	}
	// A window that is not the lea deck window: nothing saved either.
	m.readWidth = func() (int, bool) { return 52, false }
	m, cmd = step(t, m, tea.WindowSizeMsg{Width: 52, Height: 30})
	m, _ = step(t, m, cmd())
	if len(st.sets) != 1 {
		t.Fatalf("a width outside the deck window was saved: sets %q", st.sets)
	}
	// A deck started again restores the dragged width from the file.
	rec := &widthRec{}
	m2 := newModel(&fakeAPI{}).WithSettings(st, "").WithWidthDrive(rec.apply, func() int { return 200 }, nil)
	m2.restoreWidth()
	if !reflect.DeepEqual(rec.applied, []int{49}) {
		t.Fatalf("after a restart: applied %v, want [49]", rec.applied)
	}
	// A height-only change reads nothing.
	m3 := withSettings(t, &fakeAPI{}, st)
	m3.readWidth = func() (int, bool) { return 52, true }
	_, cmd = step(t, m3, tea.WindowSizeMsg{Width: 34, Height: 25})
	if cmd != nil {
		t.Fatal("a height-only change read the width")
	}
}

// The key list names < and >.
func TestTheKeyListNamesTheWidthKeys(t *testing.T) {
	if s := strings.Join(helpLines, "\n"); !strings.Contains(s, "< >") || !strings.Contains(s, "deck width") {
		t.Fatalf("key list:\n%s", s)
	}
}
