package deck

import (
	"hash/fnv"

	"github.com/charmbracelet/lipgloss"
)

// Theme tokens (Catppuccin Latte for light terminals, Mocha for dark ones).
type palette struct {
	Text, Muted, Border, Sel, Accent, Blocked, Done, Working, Idle, Unknown lipgloss.AdaptiveColor
}

func ac(light, dark string) lipgloss.AdaptiveColor {
	return lipgloss.AdaptiveColor{Light: light, Dark: dark}
}

var theme = palette{
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
}

var (
	plain  = lipgloss.NewStyle()
	muted  = lipgloss.NewStyle().Foreground(theme.Muted)
	border = lipgloss.NewStyle().Foreground(theme.Border)
	alert  = lipgloss.NewStyle().Foreground(theme.Blocked)
)

// agentColors are the 14 Catppuccin accents, light and dark.
var agentColors = []lipgloss.AdaptiveColor{
	ac("#dc8a78", "#f5e0dc"), ac("#dd7878", "#f2cdcd"), ac("#ea76cb", "#f5c2e7"), ac("#8839ef", "#cba6f7"),
	ac("#d20f39", "#f38ba8"), ac("#e64553", "#eba0ac"), ac("#fe640b", "#fab387"), ac("#df8e1d", "#f9e2af"),
	ac("#40a02b", "#a6e3a1"), ac("#179299", "#94e2d5"), ac("#04a5e5", "#89dceb"), ac("#209fb5", "#74c7ec"),
	ac("#1e66f5", "#89b4fa"), ac("#7287fd", "#b4befe"),
}

// AgentColor is an agent's stable colour: FNV-1a of its key (spec §4.1).
func AgentColor(key string) lipgloss.AdaptiveColor {
	h := fnv.New32a()
	h.Write([]byte(key))
	return agentColors[h.Sum32()%uint32(len(agentColors))]
}
