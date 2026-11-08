package settings

import (
	"strings"
	"testing"
)

// LEA_THEME (#337) is a live Choice of the deck group: catppuccin unless set, one of the built-in
// themes, saved in the user file; an environment value counts as set.
func TestTheThemeSetting(t *testing.T) {
	w := newWorld(t, "")
	s := find(t, list(t, w), "LEA_THEME")
	if s.Kind != Choice || s.Group != "Deck" || !s.Live || s.Apply != ApplyNow || s.Value != "catppuccin" || s.Source != SourceDefault {
		t.Fatalf("LEA_THEME = %+v", s)
	}
	st := w.store()
	for _, bad := range []string{"", "neon", "Nord"} {
		if err := st.Set("LEA_THEME", bad); err == nil {
			t.Errorf("LEA_THEME=%q accepted", bad)
		}
	}
	if err := st.Set("LEA_THEME", "nord"); err != nil {
		t.Fatal(err)
	}
	if s := find(t, list(t, w), "LEA_THEME"); s.Value != "nord" || s.Source != SourceFile {
		t.Fatalf("after Set: %+v", s)
	}
	w2 := newWorld(t, "")
	w2.env["LEA_THEME"] = "mono"
	if s := find(t, list(t, w2), "LEA_THEME"); s.Value != "mono" || s.Source != SourceEnvironment || !strings.Contains(strings.Join(s.Choices, " "), "high-contrast") {
		t.Fatalf("from the environment: %+v", s)
	}
}
