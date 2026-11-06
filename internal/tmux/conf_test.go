package tmux

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The deck's width is the server's @lea_deck_width option, not a number baked into the
// configuration: the window-resized hook resizes with it, the border drag (deck window only)
// stores it clamped, and only the default, set while nothing chose a width, is a constant.
func TestConfReadsTheDeckWidthFromTheOption(t *testing.T) {
	c := Conf()
	if strings.Contains(c, "@DECK_WIDTH@") || strings.Contains(c, "@DECK_CLAMP@") {
		t.Fatal("a placeholder is left in the configuration")
	}
	if !strings.Contains(c, `resize-pane -t \\\"#{session_name}:.0\\\" -x \\\"#{e|+:#{`+DeckWidthOption+`},0}\\\"`) {
		t.Error("the window-resized hook does not resize the deck to the @lea_deck_width option (arithmetic)")
	}
	if m := regexp.MustCompile(`resize-pane [^;]*-x [0-9]`).FindString(c); m != "" {
		t.Errorf("a fixed width is baked into a resize-pane: %q", m)
	}
	for _, want := range []string{
		"bind-key -n MouseDrag1Border if-shell -F '#{@lea_deck}' 'resize-pane -M'",
		"bind-key -n MouseDragEnd1Border if-shell -F '#{@lea_deck}' 'run-shell",
		"set -s @lea_deck_width " + strconv.Itoa(DeckWidth),
	} {
		if !strings.Contains(c, want) {
			t.Errorf("the configuration lacks %q", want)
		}
	}
	// The drag binding and the stored width are clamped to the bounds, with the arithmetic
	// comparisons (tmux's < and > compare strings).
	if strings.Contains(c, "#{<:") || strings.Contains(c, "#{>:") {
		t.Error("a string comparison compares widths")
	}
	if n := strings.Count(c, "#{e|<:"); n != 6 {
		t.Errorf("%d arithmetic comparisons, want 6 (the bounds, twice)", n)
	}
}

func TestClampDeckWidth(t *testing.T) {
	for _, c := range []struct{ w, window, want int }{
		{34, 0, 34},   // no window known: the default is within the static bounds
		{10, 0, 24},   // the minimum
		{100, 0, 60},  // the maximum
		{24, 200, 24}, // within: unchanged
		{60, 200, 60},
		{100, 100, 50}, // half the window caps the maximum
		{10, 40, 20},   // a window narrower than twice the minimum narrows the bound
	} {
		if got := ClampDeckWidth(c.w, c.window); got != c.want {
			t.Errorf("ClampDeckWidth(%d, %d) = %d, want %d", c.w, c.window, got, c.want)
		}
	}
}

// The history's per-key line is repeated for each typed key, in both copy tables: q is bound on
// its own, nothing binds Any, and no placeholder is left in the configuration.
func TestConfRepeatsTheHistoryLineForEachTypedKey(t *testing.T) {
	c := Conf()
	if strings.Contains(c, "@KEY@") || strings.Contains(c, "@DECK_WIDTH@") {
		t.Fatal("a placeholder is left in the configuration")
	}
	for _, table := range []string{"copy-mode", "copy-mode-vi"} {
		prefix := "bind-key -T " + table + " -- "
		var tpl string
		for _, l := range strings.SplitAfter(confTemplate, "\n") {
			if strings.HasPrefix(l, prefix+"@KEY@ ") {
				tpl = l
			}
		}
		if tpl == "" {
			t.Fatalf("%s has no per-key line", table)
		}
		for _, k := range []string{"'a'", `"'"`, "'~'", "'#'", "Enter", "Space", "BSpace", "Tab"} {
			if line := strings.ReplaceAll(tpl, "@KEY@", k); !strings.Contains(c, line) {
				t.Errorf("missing %q", line)
			}
		}
		if n := strings.Count(c, prefix); n != 97 {
			t.Errorf("%s: %d typed keys bound, want 97", table, n)
		}
		if strings.Contains(c, prefix+"'q'") || strings.Contains(c, "bind-key -T "+table+" Any") {
			t.Errorf("%s binds q twice or Any", table)
		}
	}
}
