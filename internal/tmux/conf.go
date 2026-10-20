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

// DeckWidth is the deck pane's width, its border included (spec §4.1: 32 columns inside).
const DeckWidth = 34

var conf = perKey(strings.ReplaceAll(confTemplate, "@DECK_WIDTH@", strconv.Itoa(DeckWidth)))

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
