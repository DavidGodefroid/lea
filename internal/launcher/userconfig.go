package launcher

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteUserConfig is lea_user_config_set: it sets KEY='VALUE' in the user configuration at path
// and keeps every other line. The file is private and replaced at once; a symbolic link is
// refused, and so is a value holding a new line (one line per key).
func WriteUserConfig(path, key, value string) error {
	if !keyRe.MatchString(key) {
		return fmt.Errorf("%q is not a configuration key", key)
	}
	if strings.ContainsAny(value, "\n\r\x00") {
		return fmt.Errorf("%s: a value is one line", key)
	}
	if st, err := os.Lstat(path); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to write %s: it is a symbolic link", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var b strings.Builder
	if old, err := os.ReadFile(path); err == nil {
		for _, l := range strings.SplitAfter(string(old), "\n") {
			if l != "" && !strings.HasPrefix(l, key+"=") {
				b.WriteString(l)
				if !strings.HasSuffix(l, "\n") {
					b.WriteString("\n")
				}
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	b.WriteString(key + "='" + strings.ReplaceAll(value, "'", `'\''`) + "'\n")
	t, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".")
	if err != nil {
		return fmt.Errorf("cannot create a temporary file next to %s: %v", path, err)
	}
	_, err = t.WriteString(b.String())
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
