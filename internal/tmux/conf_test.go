package tmux

import (
	"os"
	"path/filepath"
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

// The configuration's run-shell commands (a border drag's release, the window-resized hook) run
// the tmux lea runs, by its absolute path, never a "tmux" looked up in the shell's PATH (#275):
// on a host whose tmux is missing or too old lea runs its own static build, through LEA_TMUX_BIN.
func TestConfRunsLeasOwnTmuxNotThePATHs(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no tmux anywhere in PATH
	bin := "/opt/lea dir/build/tmux"
	c, err := ConfFor(bin)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(c, confBin) {
		t.Fatal("the tmux binary placeholder is left in the configuration")
	}
	var runs int
	for _, l := range strings.Split(c, "\n") {
		if strings.HasPrefix(l, "#") || !strings.Contains(l, "run-shell") {
			continue
		}
		// Every tmux a run-shell command starts is lea's, quoted: no bare "tmux" word.
		if regexp.MustCompile(`(^|[^/\w-])tmux\s`).MatchString(l) {
			t.Errorf("a run-shell runs the PATH's tmux: %s", l)
		}
		if strings.Contains(l, "-S ") {
			runs++
			if !regexp.MustCompile(`\\+"` + regexp.QuoteMeta(bin) + `\\+" -S`).MatchString(l) {
				t.Errorf("a run-shell does not run %s: %s", bin, l)
			}
		}
	}
	if runs != 2 {
		t.Errorf("%d run-shell lines start a tmux, want 2 (the drag's release, the resize hook)", runs)
	}
	for _, l := range []string{"MouseDragEnd1Border", "set-hook -g window-resized"} {
		if !strings.Contains(c, l) {
			t.Errorf("the configuration lacks %s", l)
		}
	}
	// Without LEA_TMUX_BIN and no tmux in PATH there is no tmux to write in: an error, never a
	// configuration that runs a bare "tmux".
	if _, err := ConfFor(""); err == nil {
		t.Error("a configuration was made without any tmux")
	}
	if err := WriteConf(t.TempDir()+"/tmux.conf", ""); err == nil {
		t.Error("WriteConf wrote a configuration without any tmux")
	}
}

// A name without a slash is looked up in PATH and written as its absolute path; a relative path
// is made absolute; a path the configuration's quoting cannot hold is refused.
func TestConfBin(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	for _, in := range []string{"", "tmux"} {
		if got, err := ConfBin(in); err != nil || got != filepath.Join(dir, "tmux") {
			t.Errorf("ConfBin(%q) = %q, %v; want %s", in, got, err, filepath.Join(dir, "tmux"))
		}
	}
	wd, _ := os.Getwd()
	if got, err := ConfBin("build/tmux"); err != nil || got != filepath.Join(wd, "build", "tmux") {
		t.Errorf("ConfBin(build/tmux) = %q, %v", got, err)
	}
	for _, bad := range []string{`/a"b/tmux`, "/a'b/tmux", `/a\b/tmux`, "/a$b/tmux", "/a`b/tmux", "/a#b/tmux", "/a\nb/tmux"} {
		if got, err := ConfBin(bad); err == nil {
			t.Errorf("ConfBin(%q) = %q, want an error", bad, got)
		}
	}
}
