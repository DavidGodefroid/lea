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
		"bind-key -n MouseDragEnd1Border set -su " + DragOption + " \\; if-shell -F '#{@lea_deck}' 'run-shell",
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
			// The edge scroll's loop names lea's tmux once, as $t, and runs every tmux as "$t".
			if strings.Contains(l, "lea-drag-scroll=") {
				if !strings.Contains(l, `t=\"`+bin+`\";`) || strings.Count(l, " -S ") != strings.Count(l, `\"\$t\" -S `) {
					t.Errorf("the edge scroll does not run every tmux as %s: %s", bin, l)
				}
				continue
			}
			if !regexp.MustCompile(`\\+"` + regexp.QuoteMeta(bin) + `\\+" -S`).MatchString(l) {
				t.Errorf("a run-shell does not run %s: %s", bin, l)
			}
		}
	}
	if runs != 3 {
		t.Errorf("%d run-shell lines start a tmux, want 3 (the drag's release, the resize hook, the edge scroll)", runs)
	}
	for _, l := range []string{"MouseDragEnd1Border", "set-hook -g window-resized", "command-alias[100]"} {
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

// A drag in the history held at a pane's edge scrolls on (#280): every way a drag starts in the
// history (the root table's copy mode, the copy tables' selection) runs lea-drag-scroll, an alias
// quoted for tmux's double quotes; every release ends it, and the root table's still forwards the
// release (-M) to the program under the pointer.
func TestConfDragsScrollAtTheEdges(t *testing.T) {
	c := Conf()
	var alias string
	lines := map[string]string{}
	for _, l := range strings.Split(c, "\n") {
		if v, ok := strings.CutPrefix(l, `set -s command-alias[100] "lea-drag-scroll=`); ok {
			alias = v
		}
		switch f := strings.Fields(l); {
		case len(f) > 3 && f[0] == "bind-key" && f[1] == "-n":
			lines["-n "+f[2]] = l
		case len(f) > 3 && f[0] == "bind-key" && f[1] == "-T":
			lines[f[2]+" "+f[3]] = l
		}
	}
	if !strings.HasSuffix(alias, `"`) {
		t.Fatalf("no lea-drag-scroll alias, or not on one line: %q", alias)
	}
	alias = strings.TrimSuffix(alias, `"`)
	// Inside tmux's double quotes every $ and " is escaped: tmux would expand $i and end the
	// string.
	if regexp.MustCompile(`(^|[^\\])(\\\\)*[$"]`).MatchString(alias) {
		t.Errorf("an unescaped $ or \" in the alias: %s", alias)
	}
	unq := strings.NewReplacer(`\\`, `\`, `\"`, `"`, `\$`, `$`).Replace(alias)
	for _, want := range []string{
		"set -sF " + DragCountOption + " '#{e|+:0#{" + DragCountOption + "},1}' ; set -sF " + DragOption + " '#{" + DragCountOption + "}' ; run-shell -b '",
		`k="#{` + DragOption + `}"`,
		// It stops on its own: out of the history, its drag ended or replaced, the history's end
		// reached that way (a second on: a release lost on the bar above the pane), ten minutes.
		`"##{pane_in_mode} ##{copy_cursor_y} ##{pane_height} ##{` + DragOption + `} ##{scroll_position} ##{history_size}"`,
		`[ "$1" = 1 ] && [ "$4" = "$k" ] || exit 0`, `[ $i -lt 12000 ]`,
		`if [ "$2" = 0 ]; then c=cursor-up; f=$(($5 >= $6)); elif [ "$2" = $(($3 - 1)) ]; then c=cursor-down; f=$(($5 == 0));`,
		`if [ $f = 1 ]; then e=$((e + 1)); [ $e -lt 20 ] || exit 0; continue; fi; e=0;`,
		` -X -t "$p" -N $m $c; done; exit 0'`,
	} {
		if !strings.Contains(unq, want) {
			t.Errorf("the edge scroll lacks %q:\n%s", want, unq)
		}
	}
	for key, want := range map[string]string{
		"-n MouseDrag1Pane":              `'copy-mode -H -M ; lea-drag-scroll'"`,
		"copy-mode MouseDrag1Pane":       `begin-selection \; lea-drag-scroll`,
		"copy-mode-vi MouseDrag1Pane":    `begin-selection \; lea-drag-scroll`,
		"-n MouseDragEnd1Pane":           `bind-key -n MouseDragEnd1Pane set -su ` + DragOption + ` \; `,
		"-n MouseDragEnd1Border":         `bind-key -n MouseDragEnd1Border set -su ` + DragOption + ` \; `,
		"copy-mode MouseDragEnd1Pane":    `MouseDragEnd1Pane set -su ` + DragOption + ` \; `,
		"copy-mode-vi MouseDragEnd1Pane": `MouseDragEnd1Pane set -su ` + DragOption + ` \; `,
	} {
		if !strings.Contains(lines[key], want) {
			t.Errorf("%s = %q, want %q in it", key, lines[key], want)
		}
	}
	if l := lines["-n MouseDragEnd1Pane"]; !strings.HasSuffix(l, " -M") {
		t.Errorf("the root table's release no longer reaches the program under the pointer: %q", l)
	}
}
