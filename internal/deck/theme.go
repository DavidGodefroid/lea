package deck

import (
	"fmt"
	"hash/fnv"
	"os/exec"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Theme tokens, each a light and a dark shade: lipgloss picks one by the terminal's background.
type palette struct {
	Text, Muted, Border, Sel, Accent, Blocked, Done, Working, Idle, Unknown lipgloss.AdaptiveColor
}

func ac(light, dark string) lipgloss.AdaptiveColor {
	return lipgloss.AdaptiveColor{Light: light, Dark: dark}
}

// Theme is one of the deck's colour schemes (#337). accents are the agents' colours, in the order
// of Catppuccin's 14 accents (rosewater, flamingo, pink, mauve, red, maroon, peach, yellow, green,
// teal, sky, sapphire, blue, lavender): a folder's colour is one of them by name (folderColours),
// so every theme has the 8 folder colours. frame and inactive are the tmux pane borders and the
// background of the panes without the keyboard ("default": the terminal's own). mono themes
// have no colour at all: the deck marks with attributes instead (bold, faint, reverse).
type Theme struct {
	Name string
	palette
	accents         [14]lipgloss.AdaptiveColor
	frame, inactive lipgloss.AdaptiveColor
	mono            bool
}

// DefaultTheme is the theme without LEA_THEME; MonoTheme the one NO_COLOR asks for.
const (
	DefaultTheme = "catppuccin"
	MonoTheme    = "mono"
)

// themes are the built-in themes, in the order the picker (T) and the settings screen show them.
// Catppuccin (Latte for light terminals, Mocha for dark ones) is the default; its tmux frame stays
// Mocha's, as lea.tmux.conf sets it.
var themes = []Theme{
	{Name: "catppuccin", palette: palette{
		Text:    ac("#4c4f69", "#cdd6f4"),
		Muted:   ac("#6c6f85", "#7f849c"),
		Border:  ac("#9ca0b0", "#585b70"),
		Sel:     ac("#ccd0da", "#313244"),
		Accent:  ac("#1e66f5", "#89b4fa"),
		Blocked: ac("#d20f39", "#f38ba8"),
		Done:    ac("#40a02b", "#a6e3a1"),
		Working: ac("#fe640b", "#fab387"),
		Idle:    ac("#7287fd", "#b4befe"),
		Unknown: ac("#9ca0b0", "#6c7086"),
	}, accents: [14]lipgloss.AdaptiveColor{
		ac("#dc8a78", "#f5e0dc"), ac("#dd7878", "#f2cdcd"), ac("#ea76cb", "#f5c2e7"), ac("#8839ef", "#cba6f7"),
		ac("#d20f39", "#f38ba8"), ac("#e64553", "#eba0ac"), ac("#fe640b", "#fab387"), ac("#df8e1d", "#f9e2af"),
		ac("#40a02b", "#a6e3a1"), ac("#179299", "#94e2d5"), ac("#04a5e5", "#89dceb"), ac("#209fb5", "#74c7ec"),
		ac("#1e66f5", "#89b4fa"), ac("#7287fd", "#b4befe"),
	}, frame: ac("#585b70", "#585b70"), inactive: ac("#181825", "#181825")},
	{Name: "gruvbox", palette: palette{
		Text:    ac("#3c3836", "#ebdbb2"),
		Muted:   ac("#7c6f64", "#a89984"),
		Border:  ac("#a89984", "#665c54"),
		Sel:     ac("#ebdbb2", "#3c3836"),
		Accent:  ac("#076678", "#83a598"),
		Blocked: ac("#9d0006", "#fb4934"),
		Done:    ac("#79740e", "#b8bb26"),
		Working: ac("#af3a03", "#fe8019"),
		Idle:    ac("#8f3f71", "#d3869b"),
		Unknown: ac("#928374", "#7c6f64"),
	}, accents: [14]lipgloss.AdaptiveColor{
		ac("#d65d0e", "#e78a4e"), ac("#cc241d", "#ea6962"), ac("#b16286", "#d3869b"), ac("#8f3f71", "#b16286"),
		ac("#9d0006", "#fb4934"), ac("#cc241d", "#cc241d"), ac("#af3a03", "#fe8019"), ac("#b57614", "#fabd2f"),
		ac("#79740e", "#b8bb26"), ac("#427b58", "#8ec07c"), ac("#689d6a", "#a9b665"), ac("#458588", "#7daea3"),
		ac("#076678", "#83a598"), ac("#5f6ea8", "#a3a6d6"),
	}, frame: ac("#a89984", "#665c54"), inactive: ac("#f2e5bc", "#1d2021")},
	{Name: "nord", palette: palette{
		Text:    ac("#2e3440", "#d8dee9"),
		Muted:   ac("#4c566a", "#7b88a1"),
		Border:  ac("#a3abb9", "#4c566a"),
		Sel:     ac("#d8dee9", "#3b4252"),
		Accent:  ac("#5e81ac", "#88c0d0"),
		Blocked: ac("#a5404a", "#bf616a"),
		Done:    ac("#5a7a3e", "#a3be8c"),
		Working: ac("#b0583a", "#d08770"),
		Idle:    ac("#8a5f86", "#b48ead"),
		Unknown: ac("#7b88a1", "#616e88"),
	}, accents: [14]lipgloss.AdaptiveColor{
		ac("#a8705a", "#e3b5a0"), ac("#a55a62", "#d99aa0"), ac("#9c5f93", "#c9a0dc"), ac("#8a5f86", "#b48ead"),
		ac("#a5404a", "#bf616a"), ac("#b0505a", "#d08890"), ac("#b0583a", "#d08770"), ac("#9a7a2a", "#ebcb8b"),
		ac("#5a7a3e", "#a3be8c"), ac("#4f8a87", "#8fbcbb"), ac("#3f8aa0", "#88c0d0"), ac("#4a7a9a", "#81a1c1"),
		ac("#5e81ac", "#5e81ac"), ac("#6a6fb0", "#a9b1e0"),
	}, frame: ac("#a3abb9", "#4c566a"), inactive: ac("#e5e9f0", "#272c36")},
	{Name: "tokyo-night", palette: palette{
		Text:    ac("#3760bf", "#c0caf5"),
		Muted:   ac("#6172b0", "#737aa2"),
		Border:  ac("#a1a6c5", "#3b4261"),
		Sel:     ac("#c4c8da", "#292e42"),
		Accent:  ac("#2e7de9", "#7aa2f7"),
		Blocked: ac("#f52a65", "#f7768e"),
		Done:    ac("#587539", "#9ece6a"),
		Working: ac("#b15c00", "#ff9e64"),
		Idle:    ac("#7847bd", "#9d7cd8"),
		Unknown: ac("#848cb5", "#565f89"),
	}, accents: [14]lipgloss.AdaptiveColor{
		ac("#c64343", "#db4b4b"), ac("#d20065", "#ff007c"), ac("#b15dbf", "#f0a0e0"), ac("#9854f1", "#bb9af7"),
		ac("#f52a65", "#f7768e"), ac("#a83a5a", "#e0909f"), ac("#b15c00", "#ff9e64"), ac("#8c6c3e", "#e0af68"),
		ac("#587539", "#9ece6a"), ac("#118c74", "#1abc9c"), ac("#007197", "#7dcfff"), ac("#188092", "#2ac3de"),
		ac("#2e7de9", "#7aa2f7"), ac("#7847bd", "#b4b9f8"),
	}, frame: ac("#a1a6c5", "#3b4261"), inactive: ac("#d0d5e3", "#16161e")},
	{Name: "solarized", palette: palette{
		Text:    ac("#586e75", "#93a1a1"),
		Muted:   ac("#839496", "#657b83"),
		Border:  ac("#93a1a1", "#586e75"),
		Sel:     ac("#eee8d5", "#073642"),
		Accent:  ac("#268bd2", "#268bd2"),
		Blocked: ac("#dc322f", "#dc322f"),
		Done:    ac("#859900", "#859900"),
		Working: ac("#cb4b16", "#cb4b16"),
		Idle:    ac("#6c71c4", "#6c71c4"),
		Unknown: ac("#93a1a1", "#586e75"),
	}, accents: [14]lipgloss.AdaptiveColor{
		ac("#b58960", "#d9b38c"), ac("#c9584f", "#e07f78"), ac("#d33682", "#d33682"), ac("#6c71c4", "#6c71c4"),
		ac("#dc322f", "#dc322f"), ac("#b0413e", "#e0605d"), ac("#cb4b16", "#cb4b16"), ac("#b58900", "#b58900"),
		ac("#859900", "#859900"), ac("#2aa198", "#2aa198"), ac("#3a9fc0", "#5fb8d8"), ac("#1f78b4", "#4aa3df"),
		ac("#268bd2", "#268bd2"), ac("#7d80d1", "#9ea1e0"),
	}, frame: ac("#93a1a1", "#586e75"), inactive: ac("#eee8d5", "#073642")},
	{Name: "high-contrast", palette: palette{
		Text:    ac("#000000", "#ffffff"),
		Muted:   ac("#303030", "#d0d0d0"),
		Border:  ac("#000000", "#ffffff"),
		Sel:     ac("#ffd700", "#0000af"),
		Accent:  ac("#0000d7", "#00ffff"),
		Blocked: ac("#d70000", "#ff5f5f"),
		Done:    ac("#005f00", "#00ff00"),
		Working: ac("#af5f00", "#ffff00"),
		Idle:    ac("#8700af", "#ff87ff"),
		Unknown: ac("#5f5f5f", "#bcbcbc"),
	}, accents: [14]lipgloss.AdaptiveColor{
		ac("#875f00", "#ffd7af"), ac("#af0000", "#ff8787"), ac("#af0087", "#ff87d7"), ac("#5f00af", "#d7afff"),
		ac("#d70000", "#ff5f5f"), ac("#870000", "#ffafaf"), ac("#af5f00", "#ffaf5f"), ac("#875f00", "#ffff00"),
		ac("#005f00", "#00ff00"), ac("#005f5f", "#00ffd7"), ac("#005f87", "#87d7ff"), ac("#005faf", "#5fd7ff"),
		ac("#0000d7", "#5f87ff"), ac("#5f5faf", "#afafff"),
	}, frame: ac("#000000", "#ffffff"), inactive: ac("#e4e4e4", "#121212")},
	{Name: "mono", mono: true, frame: ac("default", "default"), inactive: ac("default", "default")},
}

// ThemeNames are the built-in themes' names, in the picker's order.
func ThemeNames() []string {
	names := make([]string, len(themes))
	for i, t := range themes {
		names[i] = t.Name
	}
	return names
}

// findTheme is the built-in theme named name; ok is false for a name the deck does not know.
func findTheme(name string) (Theme, bool) {
	for _, t := range themes {
		if t.Name == name {
			return t, true
		}
	}
	return Theme{}, false
}

// current is the theme the deck draws with; theme and agentColors are its tokens, plain, muted,
// border and alert its styles. Every view reads them when it draws: SetTheme repaints the next
// frame. Only the deck's Update (one goroutine) sets them.
var (
	current     Theme
	theme       palette
	agentColors []lipgloss.AdaptiveColor
	plain       lipgloss.Style
	muted       lipgloss.Style
	border      lipgloss.Style
	alert       lipgloss.Style
)

func init() { SetTheme(DefaultTheme) }

// SetTheme makes name the current theme. An unknown name sets the default and returns false.
func SetTheme(name string) bool {
	t, ok := findTheme(name)
	if !ok {
		t, _ = findTheme(DefaultTheme)
	}
	current, theme, agentColors = t, t.palette, t.accents[:]
	plain = lipgloss.NewStyle()
	muted = lipgloss.NewStyle().Foreground(theme.Muted)
	border = lipgloss.NewStyle().Foreground(theme.Border)
	alert = lipgloss.NewStyle().Foreground(theme.Blocked)
	if t.mono {
		muted, alert = muted.Faint(true), alert.Bold(true)
	}
	return ok
}

// CurrentTheme is the current theme's name.
func CurrentTheme() string { return current.Name }

// StartTheme sets the theme a deck process starts with: value is LEA_THEME, explicit says it is
// set (the user's file or the environment, not lea's default). NO_COLOR asks for mono unless
// LEA_THEME is set; an unknown name gives the default, and the returned warning says so.
func StartTheme(value string, explicit, noColor bool) string {
	switch {
	case !explicit && noColor:
		SetTheme(MonoTheme)
	case value == "":
		SetTheme(DefaultTheme)
	case !SetTheme(value):
		startWarning = "LEA_THEME: no theme " + value + ", " + DefaultTheme + " instead"
		return startWarning
	}
	startWarning = ""
	return ""
}

// startWarning is what StartTheme last warned of: the deck's first status.
var startWarning string

// selected is st on the selected line, item or button: on the theme's selection background, in
// reverse video for a theme without colours.
func selected(st lipgloss.Style) lipgloss.Style {
	if current.mono {
		return st.Reverse(true)
	}
	return st.Background(theme.Sel)
}

// menuBg is st on a menu's background, which a theme without colours does not have: its menu
// shows the highlighted item only, in reverse video.
func menuBg(st lipgloss.Style) lipgloss.Style {
	if current.mono {
		return st
	}
	return st.Background(theme.Sel)
}

// AgentColor is an agent's stable colour: FNV-1a of its key (spec §4.1).
func AgentColor(key string) lipgloss.AdaptiveColor {
	h := fnv.New32a()
	h.Write([]byte(key))
	return agentColors[h.Sum32()%uint32(len(agentColors))]
}

// themeKey is the settings key of the deck's theme.
const themeKey = "LEA_THEME"

// themeItems are the built-in themes (T), numbered.
func themeItems() []menuItem {
	var items []menuItem
	for i, name := range ThemeNames() {
		k := ""
		if i < 9 {
			k = string(rune('1' + i))
		}
		items = append(items, menuItem{name, k, themeTarget + name})
	}
	return items
}

// WithThemeDrive lets the deck colour the tmux side of its theme: the pane borders and the
// background of the panes without the keyboard.
func (m Model) WithThemeDrive(apply func(Theme) error) Model {
	m.applyFrame = apply
	return m
}

// themePicking reports whether the open menu is the theme picker.
func (m Model) themePicking() bool { return m.menuFor == deckMenu && m.menuPicker }

// openThemePicker opens the theme picker (T) on the current theme.
func (m *Model) openThemePicker() {
	m.openMenu(deckMenu)
	m.menuPicker, m.themeWas = true, CurrentTheme()
	for i, name := range ThemeNames() {
		if name == m.themeWas {
			m.menuSel = i
		}
	}
}

// closeMenu closes the open menu; the theme picker puts back the theme it opened on.
func (m *Model) closeMenu() {
	if m.themePicking() && m.themeWas != "" {
		m.setTheme(m.themeWas)
	}
	m.menuFor, m.menuPicker, m.themeWas, m.questions = "", false, "", nil
}

// previewTheme draws the deck in the theme the picker highlights.
func (m *Model) previewTheme() {
	if !m.themePicking() {
		return
	}
	if items := themeItems(); m.menuSel >= 0 && m.menuSel < len(items) {
		m.setTheme(strings.TrimPrefix(items[m.menuSel].target, themeTarget))
	}
}

// keepTheme makes name the deck's theme and saves it as LEA_THEME, for every deck started later.
func (m Model) keepTheme(name string) Model {
	m.setTheme(name)
	switch {
	case m.settings == nil:
		m.status = "theme " + name + ", not saved"
		if m.settingsOff != "" {
			m.status += ": " + m.settingsOff
		}
	default:
		if err := m.settings.Set(themeKey, name); err != nil {
			m.status = "theme: " + oneLine(err.Error())
			return m
		}
		m.status = "theme " + name
	}
	return m
}

// setTheme sets the current theme and colours the tmux side to match.
func (m *Model) setTheme(name string) {
	SetTheme(name)
	if m.applyFrame != nil {
		if err := m.applyFrame(current); err != nil {
			m.status = "theme: " + oneLine(err.Error())
		}
	}
}

// frameColours are the tmux pane borders' colour and the inactive panes' background of t, the
// shade of the terminal's background.
func frameColours(t Theme) (frame, inactive string) {
	if lipgloss.HasDarkBackground() {
		return t.frame.Dark, t.inactive.Dark
	}
	return t.frame.Light, t.inactive.Light
}

// themeSwatch is a few cells in t's colours (its accent and three state colours), on the menu's
// background.
func themeSwatch(t Theme, ascii bool) string {
	cell := "■"
	if ascii {
		cell = "#"
	}
	sts := []lipgloss.Style{lipgloss.NewStyle().Bold(true), lipgloss.NewStyle().Faint(true),
		lipgloss.NewStyle().Reverse(true), lipgloss.NewStyle()}
	if !t.mono {
		for i, c := range []lipgloss.AdaptiveColor{t.Accent, t.Blocked, t.Working, t.Done} {
			sts[i] = lipgloss.NewStyle().Foreground(c)
		}
	}
	var b strings.Builder
	for _, st := range sts {
		b.WriteString(menuBg(st).Render(cell))
	}
	return b.String()
}

// tmuxThemeDrive colours the tmux server the deck runs in (the TMUX environment) through the tmux
// binary LEA_TMUX_BIN names ("tmux" otherwise): the options lea.tmux.conf sets in Catppuccin's
// colours. nil outside tmux.
func tmuxThemeDrive(getenv func(string) string) func(Theme) error {
	if getenv("TMUX") == "" {
		return nil
	}
	bin := getenv("LEA_TMUX_BIN")
	if bin == "" {
		bin = "tmux"
	}
	return func(t Theme) error {
		frame, inactive := frameColours(t)
		out, err := exec.Command(bin, "set-option", "-g", "pane-border-style", "fg="+frame, ";",
			"set-option", "-g", "pane-active-border-style", "fg="+frame, ";",
			"set-option", "-g", "window-style", "bg="+inactive).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
		}
		return nil
	}
}
