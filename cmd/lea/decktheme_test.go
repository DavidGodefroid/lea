package main

import (
	"testing"

	"github.com/DavidGodefroid/lea/internal/settings"
)

// themeStore is a settings store holding LEA_THEME only.
type themeStore struct{ s settings.Setting }

func (f themeStore) List() ([]settings.Setting, error) { return []settings.Setting{f.s}, nil }
func (f themeStore) Set(string, string) error          { return nil }

// A deck reads LEA_THEME from the files (fresh: a theme picked in another deck), set unless it is
// lea's default; inside lea, without a store, from the environment (#337).
func TestDeckTheme(t *testing.T) {
	env := func(v string) func(string) string { return func(string) string { return v } }
	for _, c := range []struct {
		st       themeStore
		env      string
		want     string
		explicit bool
	}{
		{themeStore{settings.Setting{Key: "LEA_THEME", Value: "catppuccin", Source: settings.SourceDefault}}, "nord", "catppuccin", false},
		{themeStore{settings.Setting{Key: "LEA_THEME", Value: "nord", Source: settings.SourceFile}}, "", "nord", true},
		{themeStore{settings.Setting{Key: "LEA_THEME", Value: "mono", Source: settings.SourceEnvironment}}, "mono", "mono", true},
	} {
		if v, ex := deckTheme(c.st, env(c.env)); v != c.want || ex != c.explicit {
			t.Errorf("%+v: %q %v, want %q %v", c.st.s, v, ex, c.want, c.explicit)
		}
	}
	if v, ex := deckTheme(nil, env("gruvbox")); v != "gruvbox" || !ex {
		t.Errorf("inside lea: %q %v", v, ex)
	}
	if v, ex := deckTheme(nil, env("")); v != "" || ex {
		t.Errorf("inside lea, unset: %q %v", v, ex)
	}
}
