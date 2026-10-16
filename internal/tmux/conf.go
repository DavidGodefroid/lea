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

var conf = strings.ReplaceAll(confTemplate, "@DECK_WIDTH@", strconv.Itoa(DeckWidth))

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
