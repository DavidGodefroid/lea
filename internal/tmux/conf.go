package tmux

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

//go:embed lea.tmux.conf
var confTemplate string

// DeckWidth is the deck pane's default width, its border included (spec §4.1: 32 columns
// inside). The width the user chooses (the deck's < and >, LEA_DECK_WIDTH, a border drag) lives
// in the server's DeckWidthOption option instead.
const DeckWidth = 34

const (
	// DeckWidthMin and DeckWidthMax bound the deck's width; the maximum is also capped at half
	// the window.
	DeckWidthMin = 24
	DeckWidthMax = 60
	// DeckWidthOption is the tmux server option holding the chosen deck width: the deck sets it,
	// the border drag stores it, the conf's window-resized hook and the daemon's layout repair
	// follow it.
	DeckWidthOption = "@lea_deck_width"
)

// ClampDeckWidth clamps w to the deck width's bounds: DeckWidthMin at least, DeckWidthMax or
// half the window at most, whichever is smaller (window <= 0: no window is known).
func ClampDeckWidth(w, window int) int {
	hi := DeckWidthMax
	if window > 0 && window/2 < hi {
		hi = window / 2
	}
	return min(max(w, DeckWidthMin), hi)
}

var conf = perKey(strings.NewReplacer(
	"@DECK_WIDTH@", strconv.Itoa(DeckWidth),
	"@DECK_MIN@", strconv.Itoa(DeckWidthMin),
	"@DECK_MAX@", strconv.Itoa(DeckWidthMax),
	"@DECK_CLAMP@", clampFormat(),
).Replace(confTemplate))

// clampFormat is the tmux format of the dragged border's width clamped to the deck width's
// bounds: DeckWidthMin at least, DeckWidthMax or half the window at most. The comparisons are
// arithmetic ones (e|<:): tmux's < and > compare strings.
func clampFormat() string {
	max := fmt.Sprintf(`#{?#{e|<:#{e|/:#{window_width},2},%[1]d},#{e|/:#{window_width},2},%[1]d}`, DeckWidthMax)
	return fmt.Sprintf(`#{?#{e|<:#{pane_width},%[1]d},%[1]d,#{?#{e|>:#{pane_width},%[2]s},%[2]s,#{pane_width}}}`, DeckWidthMin, max)
}

// typedKeys are the keys typed into a prompt, quoted for the configuration: printable ASCII but q
// (bound on its own), and Space, Enter, BSpace, Tab.
func typedKeys() []string {
	keys := []string{"Space", "Enter", "BSpace", "Tab"}
	for c := byte('!'); c <= '~'; c++ {
		switch c {
		case 'q':
		case '\'':
			keys = append(keys, `"'"`)
		default:
			keys = append(keys, "'"+string(c)+"'")
		}
	}
	return keys
}

// perKey repeats each line of c that holds @KEY@ once for each typed key.
func perKey(c string) string {
	var b strings.Builder
	for _, line := range strings.SplitAfter(c, "\n") {
		if !strings.Contains(line, "@KEY@") {
			b.WriteString(line)
			continue
		}
		for _, k := range typedKeys() {
			b.WriteString(strings.ReplaceAll(line, "@KEY@", k))
		}
	}
	return b.String()
}

// Conf is lea's tmux configuration.
func Conf() string { return conf }

// WriteConf writes Conf to path, mode 0600, through a temporary file; a symlink at path is refused.
func WriteConf(path string) error {
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink, refused", path)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmux.conf.*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(conf); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
