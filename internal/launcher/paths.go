package launcher

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DataDir is <data>/lea: state/ (mounted into lea item by item) and host/ (never mounted, except
// host/run/<container>, the bus directory each container gets at /run/lea/bus).
func (c *Config) DataDir() string {
	return nonEmpty(c.Get("XDG_DATA_HOME"), c.Get("HOME")+"/.local/share") + "/lea"
}
func (c *Config) StateDir() string { return c.DataDir() + "/state" }
func (c *Config) HostDir() string  { return c.DataDir() + "/host" }

// ContainerName is lea_container_name: <prefix>-<id>.
func (c *Config) ContainerName(projects string) string {
	return c.Get("LEA_PREFIX") + "-" + ID(projects)
}

// InternalNetwork is lea_internal_network: the container's own --internal network.
func (c *Config) InternalNetwork(id string) string { return c.Get("LEA_PREFIX") + "-internal-" + id }

// ID is lea_id: the first 10 hex digits of the SHA-256 of the path.
func ID(path string) string {
	s := sha256.Sum256([]byte(path))
	return hex.EncodeToString(s[:])[:10]
}

// Slug is Claude Code's project slug (lea_slug): every character other than [A-Za-z0-9]
// becomes '-'. One '-' per character, as sed does in a UTF-8 locale.
func Slug(path string) string {
	var b strings.Builder
	for _, r := range path {
		if r < 0x80 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// SafePath is lea_safe_path: p must be base or below it, without "." or ".." components, and no
// component below base (p included) may be a symbolic link. Missing components are fine.
func SafePath(base, p string) error {
	if p == base {
		return nil
	}
	if !strings.HasPrefix(p, base+"/") {
		return fmt.Errorf("internal: %s is not below %s", p, base)
	}
	if strings.Contains("/"+p+"/", "/../") || strings.Contains("/"+p+"/", "/./") {
		return fmt.Errorf("refusing to use %s: it contains '.' or '..'", p)
	}
	cur := base
	for _, part := range strings.Split(strings.TrimPrefix(p, base+"/"), "/") {
		cur += "/" + part
		if isSymlink(cur) {
			return fmt.Errorf("refusing to use %s: %s is a symbolic link (remove it, then retry)", p, cur)
		}
	}
	return nil
}

// Mkdirs is lea_mkdirs: mkdir -p of each directory after SafePath.
func Mkdirs(base string, dirs ...string) error {
	for _, d := range dirs {
		if err := SafePath(base, d); err != nil {
			return err
		}
		if err := os.MkdirAll(d, 0o777); err != nil {
			return err
		}
	}
	return nil
}

// WriteFile is lea_write_file: data goes to a temporary file next to p (mode 0600), then
// replaces p.
func WriteFile(base, p string, data []byte) error {
	if err := SafePath(base, p); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), filepath.Base(p)+".*")
	if err != nil {
		return fmt.Errorf("cannot create a temporary file next to %s", p)
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr == nil && cerr == nil {
		if err := os.Rename(f.Name(), p); err == nil {
			return nil
		}
	}
	os.Remove(f.Name())
	return fmt.Errorf("cannot write %s", p)
}

func isDir(p string) bool     { fi, err := os.Stat(p); return err == nil && fi.IsDir() }
func isRegular(p string) bool { fi, err := os.Stat(p); return err == nil && fi.Mode().IsRegular() }
func exists(p string) bool    { _, err := os.Stat(p); return err == nil }
func isSymlink(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}
func isFifo(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode()&os.ModeNamedPipe != 0
}

// parentText is the shell's ${p%/*}: p without its last component, textually. filepath.Dir
// would clean "a//lea" to "a/lea", which is then not below DataDir() for SafePath.
func parentText(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[:i]
	}
	return p
}

// nonEmpty is the shell's ${v:-def}.
func nonEmpty(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
