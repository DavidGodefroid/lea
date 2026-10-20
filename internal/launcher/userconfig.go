package launcher

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteUserConfig is lea_user_config_set: it sets KEY='VALUE' in the user configuration at path
// and keeps every other line. Every line that sets KEY for parseLine goes (indented, or after
// "export "). The file is private and replaced at once; a symbolic link is refused, and so is a
// value holding a new line (one line per key).
func WriteUserConfig(path, key, value string) error {
	if !keyRe.MatchString(key) {
		return fmt.Errorf("%q is not a configuration key", key)
	}
	if strings.ContainsAny(value, "\n\r\x00") {
		return fmt.Errorf("%s: a value is one line", key)
	}
	old, err := readUserConfig(path)
	if err != nil {
		return err
	}
	var b strings.Builder
	for _, l := range strings.SplitAfter(old, "\n") {
		if l != "" && !SetsKey(l, key) {
			b.WriteString(l)
			if !strings.HasSuffix(l, "\n") {
				b.WriteString("\n")
			}
		}
	}
	b.WriteString(key + "='" + strings.ReplaceAll(value, "'", `'\''`) + "'\n")
	return ReplaceUserConfig(path, []byte(b.String()))
}

// SetsKey reports whether line assigns key, as parseLine reads it: after blanks and an optional
// "export ".
func SetsKey(line, key string) bool {
	s := strings.TrimPrefix(strings.TrimLeft(line, " \t"), "export ")
	return strings.HasPrefix(s, key+"=")
}

// readUserConfig is the user configuration at path, "" when there is none; a symbolic link is
// refused.
func readUserConfig(path string) (string, error) {
	if isSymlink(path) {
		return "", fmt.Errorf("refusing to write %s: it is a symbolic link", path)
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	return string(b), err
}

// ReplaceUserConfig writes data as the user configuration at path, at once (a temporary file
// renamed over it) and private; a symbolic link is refused.
func ReplaceUserConfig(path string, data []byte) error {
	if isSymlink(path) {
		return fmt.Errorf("refusing to write %s: it is a symbolic link", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	t, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".")
	if err != nil {
		return fmt.Errorf("cannot create a temporary file next to %s: %v", path, err)
	}
	_, err = t.Write(data)
	if cerr := t.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(t.Name(), 0o600)
	}
	if err == nil {
		err = os.Rename(t.Name(), path)
	}
	if err != nil {
		os.Remove(t.Name())
	}
	return err
}
