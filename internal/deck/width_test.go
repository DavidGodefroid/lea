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
	m := newModel(&fakeAPI{}).WithSettings(st, "").WithWidthDrive(rec.apply, func() int { return 200 })
	m.restoreWidth()
	if !reflect.DeepEqual(rec.applied, []int{48}) {
		t.Fatalf("applied %v", rec.applied)
	}
	// No store (inside lea): nothing is applied, nothing panics.
	newModel(&fakeAPI{}).WithWidthDrive(rec.apply, nil).restoreWidth()
	if len(rec.applied) != 1 {
		t.Fatalf("applied %v", rec.applied)
	}
}

// The key list names < and >.
func TestTheKeyListNamesTheWidthKeys(t *testing.T) {
	if s := strings.Join(helpLines, "\n"); !strings.Contains(s, "< >") || !strings.Contains(s, "deck width") {
		t.Fatalf("key list:\n%s", s)
	}
}
