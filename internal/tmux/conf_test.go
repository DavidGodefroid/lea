package tmux

import (
	"strings"
	"testing"
)

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
