package tmux

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
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

const (
	// BorderPressOption is the server option set while the left button went down on a border of
	// the deck window: only such a press arms the border drag's resize and its release's stored
	// width (#341). A press in a pane and every root release clear it.
	BorderPressOption = "@lea_border_press"
)

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

// confBin is the placeholder of the tmux binary the configuration's run-shell commands run.
const confBin = "@TMUX_BIN@"

// ConfBin is the absolute path of the tmux binary bin names, as the configuration runs it: bin
// itself when it holds a slash, else (bin, or "tmux" when empty) looked up in PATH. The
// configuration's run-shell commands (a border drag's release, the window-resized hook) run it,
// so that they run the tmux lea runs (LEA_TMUX_BIN: possibly lea's own static build) and not
// whatever tmux the shell's PATH holds, or none. A path the configuration cannot quote (a quote,
// a backslash, $, `, # or a control character) is refused.
func ConfBin(bin string) (string, error) {
	if bin == "" {
		bin = "tmux"
	}
	if !strings.Contains(bin, "/") {
		p, err := exec.LookPath(bin)
		if err != nil {
			return "", fmt.Errorf("tmux for the configuration: %w", err)
		}
		bin = p
	}
	abs, err := filepath.Abs(bin)
	if err != nil {
		return "", err
	}
	for _, r := range abs {
		if r < 0x20 || r == 0x7f || strings.ContainsRune("'\"\\$`#", r) {
			return "", fmt.Errorf("tmux path %q holds a character lea's tmux configuration cannot quote", abs)
		}
	}
	return abs, nil
}

// ConfFor is lea's tmux configuration for the tmux binary bin (see ConfBin).
func ConfFor(bin string) (string, error) {
	abs, err := ConfBin(bin)
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(conf, confBin, abs), nil
}

// Conf is lea's tmux configuration for the tmux in PATH ("tmux" itself when there is none): the
// tests' configuration. lea writes ConfFor its own tmux (WriteConf).
func Conf() string {
	c, err := ConfFor("")
	if err != nil {
		return strings.ReplaceAll(conf, confBin, "tmux")
	}
	return c
}

// WriteConf writes ConfFor(bin) to path, mode 0600, through a temporary file; a symlink at path
// is refused.
func WriteConf(path, bin string) error {
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink, refused", path)
	}
	conf, err := ConfFor(bin)
	if err != nil {
		return err
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
