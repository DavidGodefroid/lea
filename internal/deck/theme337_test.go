package deck

import (
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/DavidGodefroid/lea/internal/settings"
)

// newModelWith is a deck that shows as, the first one selected.
func newModelWith(t *testing.T, as ...Agent) Model {
	t.Helper()
	m, _ := step(t, newModel(&fakeAPI{}), snapshotMsg{snap: Snapshot{Version: 7, Shown: as[0].Key, Agents: as}})
	return m
}

// themed restores the default theme after the test: the theme is the package's.
func themed(t *testing.T) {
	t.Helper()
	truecolour(t)
	t.Cleanup(func() { SetTheme(DefaultTheme) })
}

// Every theme but mono defines every palette token and the 14 accents, light and dark, so the 8
// folder colours exist in all of them, distinct within each; mono defines none. The settings
// screen offers the same themes, in the same order, the default first (#337).
func TestEveryThemeDefinesItsTokens(t *testing.T) {
	themed(t)
	if names := ThemeNames(); !slices.Equal(names, settings.Themes) || names[0] != DefaultTheme {
		t.Fatalf("deck themes %v, settings themes %v", names, settings.Themes)
	}
	if len(themes) < 5 || !slices.Contains(ThemeNames(), "high-contrast") || !slices.Contains(ThemeNames(), MonoTheme) {
		t.Fatalf("themes %v: want catppuccin and at least 4 others, high-contrast and mono among them", ThemeNames())
	}
	for _, th := range themes {
		p := th.palette
		tokens := []lipgloss.AdaptiveColor{p.Text, p.Muted, p.Border, p.Sel, p.Accent, p.Blocked, p.Done, p.Working, p.Idle, p.Unknown}
		tokens = append(tokens, th.accents[:]...)
		for i, c := range tokens {
			if th.mono != (c.Light == "" && c.Dark == "") || !th.mono && (c.Light == "" || c.Dark == "") {
				t.Errorf("%s: token %d is %+v", th.Name, i, c)
			}
		}
		if th.frame.Light == "" || th.frame.Dark == "" || th.inactive.Light == "" || th.inactive.Dark == "" {
			t.Errorf("%s: tmux frame %+v, inactive %+v", th.Name, th.frame, th.inactive)
		}
		SetTheme(th.Name)
		seen := map[lipgloss.AdaptiveColor]bool{}
		for _, fc := range folderColours {
			c, ok := folderColour(fc.name)
			if !ok {
				t.Fatalf("%s: no folder colour %s", th.Name, fc.name)
			}
			if !th.mono && seen[c] {
				t.Errorf("%s: folder colour %s repeats another", th.Name, fc.name)
			}
			seen[c] = true
		}
	}
}

// The default theme is Catppuccin's, byte for byte: the colours of the deck before #337.
func TestTheDefaultThemeIsCatppuccin(t *testing.T) {
	themed(t)
	SetTheme("nord")
	SetTheme(DefaultTheme)
	if theme.Blocked != ac("#d20f39", "#f38ba8") || theme.Sel != ac("#ccd0da", "#313244") || theme.Accent != ac("#1e66f5", "#89b4fa") ||
		agentColors[0] != ac("#dc8a78", "#f5e0dc") || agentColors[13] != ac("#7287fd", "#b4befe") || len(agentColors) != 14 {
		t.Fatalf("catppuccin changed: %+v %v", theme, agentColors)
	}
	if c, _ := folderColour("pink"); c != ac("#ea76cb", "#f5c2e7") {
		t.Fatalf("pink is %+v", c)
	}
}

// SetTheme changes what the deck draws: another theme's SGR, and back to the same bytes.
func TestSetThemeRepaintsTheDeck(t *testing.T) {
	themed(t)
	m := sized(newModelWith(t, withTask(ag("red", "idle", 1), false, true), ag("b", "idle", 1)), 34, 20)
	before := m.View()
	if !strings.Contains(before, sgrOf(theme.Blocked)) {
		t.Fatalf("no catppuccin red in %q", before)
	}
	SetTheme("gruvbox")
	after := m.View()
	if after == before || !strings.Contains(after, sgrOf(theme.Blocked)) || strings.Contains(after, sgrOf(ac("", "#f38ba8"))) {
		t.Fatalf("gruvbox did not repaint the deck:\n%q", after)
	}
	SetTheme(DefaultTheme)
	if m.View() != before {
		t.Fatal("back on catppuccin the deck draws other bytes")
	}
}

// mono has no colour: attributes only, the selection in reverse video.
func TestMonoHasNoColour(t *testing.T) {
	themed(t)
	SetTheme(MonoTheme)
	v := sized(newModelWith(t, withTask(ag("red", "idle", 1), false, true), ag("b", "idle", 1)), 34, 20).View()
	if strings.Contains(v, "38;2;") || strings.Contains(v, "48;2;") {
		t.Fatalf("mono draws colours: %q", v)
	}
	if !strings.Contains(v, "\x1b[7m") && !strings.Contains(v, ";7m") && !strings.Contains(v, ";7;") {
		t.Fatalf("mono's selection is not in reverse video: %q", v)
	}
}

// NO_COLOR gives mono unless LEA_THEME is set; an unknown LEA_THEME gives catppuccin, with a
// warning, never a crash.
func TestStartTheme(t *testing.T) {
	themed(t)
	for _, c := range []struct {
		value          string
		explicit, nc   bool
		want, warnPart string
	}{
		{"", false, false, DefaultTheme, ""},
		{"catppuccin", false, false, DefaultTheme, ""},
		{"", false, true, MonoTheme, ""},
		{"catppuccin", false, true, MonoTheme, ""},
		{"nord", true, true, "nord", ""},
		{"nord", true, false, "nord", ""},
		{"neon", true, false, DefaultTheme, "no theme neon"},
	} {
		warn := StartTheme(c.value, c.explicit, c.nc)
		if CurrentTheme() != c.want || (c.warnPart == "") != (warn == "") || !strings.Contains(warn, c.warnPart) {
			t.Errorf("StartTheme(%q, %v, %v): theme %s, warning %q; want %s, %q", c.value, c.explicit, c.nc, CurrentTheme(), warn, c.want, c.warnPart)
		}
	}
}

// themeSettings is the settings screen with the Theme row only.
func themeSettings() *fakeSettings {
	return &fakeSettings{list: []settings.Setting{{Key: themeKey, Group: "Deck", Name: "Theme", Kind: settings.Choice,
		Choices: settings.Themes, Value: DefaultTheme, Source: settings.SourceDefault, Apply: settings.ApplyNow, Live: true}}}
}

// , then enter on the Theme row cycles the themes: saved, and the deck repaints at once.
func TestTheThemeSettingAppliesLive(t *testing.T) {
	themed(t)
	st := themeSettings()
	var frames []string
	m := withSettings(t, &fakeAPI{}, st).WithThemeDrive(func(th Theme) error { frames = append(frames, th.Name); return nil })
	m, _ = step(t, m, key(","))
	m, _ = step(t, m, key("enter"))
	if CurrentTheme() != settings.Themes[1] || !slices.Equal(st.sets, []string{themeKey + "=" + settings.Themes[1]}) {
		t.Fatalf("theme %s, sets %v", CurrentTheme(), st.sets)
	}
	if !slices.Equal(frames, []string{settings.Themes[1]}) {
		t.Fatalf("tmux frame set for %v", frames)
	}
	if !strings.Contains(strings.Join(viewLines(m), "\n"), "Theme") {
		t.Fatal("no Theme row")
	}
}

// T opens the theme picker on the current theme, a swatch on each line; moving previews the
// theme (deck and tmux frame), esc puts the old one back, enter keeps and saves the one
// highlighted. A digit picks at once.
func TestThePickerPreviewsAndKeeps(t *testing.T) {
	themed(t)
	st := themeSettings()
	var frames []string
	m := withSettings(t, &fakeAPI{}, st).WithThemeDrive(func(th Theme) error { frames = append(frames, th.Name); return nil })
	m = typed(t, m, "T")
	if !m.themePicking() || m.menuSel != 0 {
		t.Fatalf("T: picking %v, on item %d", m.themePicking(), m.menuSel)
	}
	view := strings.Join(viewLines(m), "\n")
	for _, name := range ThemeNames() {
		if !strings.Contains(cells(view), name) {
			t.Fatalf("the picker does not list %s:\n%s", name, cells(view))
		}
	}
	if !strings.Contains(view, "■") {
		t.Fatal("no swatch in the picker")
	}
	m = typed(t, m, "j")
	if CurrentTheme() != ThemeNames()[1] {
		t.Fatalf("j previews %s", CurrentTheme())
	}
	m = typed(t, m, "j")
	m, _ = step(t, m, key("esc"))
	if CurrentTheme() != DefaultTheme || m.menuFor != "" || len(st.sets) != 0 {
		t.Fatalf("esc: theme %s, menu %q, sets %v", CurrentTheme(), m.menuFor, st.sets)
	}
	if want := []string{ThemeNames()[1], ThemeNames()[2], DefaultTheme}; !slices.Equal(frames, want) {
		t.Fatalf("tmux frames %v, want %v", frames, want)
	}
	m = typed(t, m, "T")
	m = typed(t, m, "j")
	m, _ = step(t, m, key("enter"))
	if CurrentTheme() != ThemeNames()[1] || m.menuFor != "" || !slices.Equal(st.sets, []string{themeKey + "=" + ThemeNames()[1]}) {
		t.Fatalf("enter: theme %s, menu %q, sets %v", CurrentTheme(), m.menuFor, st.sets)
	}
	m = typed(t, m, "T")
	if m.menuSel != 1 {
		t.Fatalf("the picker opens on item %d, want the current theme's 1", m.menuSel)
	}
	m = typed(t, m, "6")
	if CurrentTheme() != ThemeNames()[5] || m.menuFor != "" {
		t.Fatalf("6: theme %s, menu %q", CurrentTheme(), m.menuFor)
	}
}

// A click outside the picker closes it as esc does: the previewed theme goes back.
func TestAClickOutsideThePickerRestoresTheTheme(t *testing.T) {
	themed(t)
	m := withSettings(t, &fakeAPI{}, themeSettings())
	m = typed(t, m, "T")
	m = typed(t, m, "j")
	m, _ = step(t, m, click(tea.MouseButtonLeft, 1))
	if CurrentTheme() != DefaultTheme || m.menuFor != "" {
		t.Fatalf("theme %s, menu %q", CurrentTheme(), m.menuFor)
	}
}

// Without a settings store (inside lea) the picked theme applies to this deck only, and says so.
func TestThePickerWithoutSettings(t *testing.T) {
	themed(t)
	m := sized(newModelWith(t, ag("a", "idle", 1)), 34, 30)
	m = typed(t, m, "T")
	m = typed(t, m, "j")
	m, _ = step(t, m, key("enter"))
	if CurrentTheme() != ThemeNames()[1] || !strings.Contains(m.status, "not saved") {
		t.Fatalf("theme %s, status %q", CurrentTheme(), m.status)
	}
}

// T is in the key list and in the deck's own menu.
func TestTheThemeKeyIsListed(t *testing.T) {
	if !slices.ContainsFunc(helpLines, func(l string) bool { return strings.Contains(l, "T ") && strings.Contains(l, "theme") }) {
		t.Fatal("T is not in the key list")
	}
	if !slices.ContainsFunc(deckMenuItems("on"), func(it menuItem) bool { return it.key == "T" }) {
		t.Fatal("no Theme item in the deck's menu")
	}
}

// A saved layout's folder colours stay valid in every theme, and a folder's row takes the
// current theme's shade of its colour.
func TestFoldersKeepTheirColourAcrossThemes(t *testing.T) {
	themed(t)
	st := &memStore{l: Layout{Folders: []Folder{{ID: "f1", Name: "web", Color: "pink", Agents: []string{"lea-a/a"}}}}}
	m := withFolders(t, st, ag("a", "idle", 1), ag("b", "idle", 1))
	for _, name := range ThemeNames() {
		SetTheme(name)
		if current.mono {
			continue
		}
		pink, _ := folderColour("pink")
		if h := m.rows(m.innerWidth())[0]; !strings.Contains(h, sgrOf(pink)) {
			t.Errorf("%s: the folder's row is not pink: %q", name, h)
		}
	}
	if st.l.Folders[0].Color != "pink" {
		t.Fatalf("the layout changed: %+v", st.l)
	}
}
