package launcher

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

// noFollow opens a file without following a symbolic link in its last component.
const noFollow = unix.O_NOFOLLOW

// claudeLayered are the ~/.claude entries each lea container gets its own writable copy of
// (#111): /plugin install, /plugin marketplace add, the marketplaces' auto-update and
// /permissions write there. The copy is made on the host from the host's entry, into the
// container's private ~/.claude; nothing is ever written back to the host. What an agent must
// not undo (lea's hooks) is in Claude Code's managed settings, which outrank these.
var claudeLayered = []string{"settings.json", "plugins"}

// layerRecord is what lea remembers of one layered entry: the fingerprints of the host's entry
// and of the copy when lea last made the copy.
type layerRecord struct {
	Host string `json:"host"`
	Copy string `json:"copy"`
}

// layerRecordPath is where lea keeps the records of a container's copies: next to its private
// ~/.claude, never inside it (the agents do not see it).
func layerRecordPath(priv string) string { return priv + ".layer.json" }

// fingerprint names the content of a layered entry: a settings file by its bytes, a plugins
// directory by its top-level JSON files (installed_plugins.json, known_marketplaces.json...),
// which change with every install, removal and marketplace update. "absent" when there is no real
// entry (a symbolic link included).
func fingerprint(p, entry string) string {
	fi, err := os.Lstat(p)
	if err != nil {
		return "absent"
	}
	h := sha256.New()
	switch {
	case entry == "plugins" && fi.IsDir():
		ents, _ := os.ReadDir(p)
		var names []string
		for _, e := range ents {
			if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".json") {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, n := range names {
			b, _ := os.ReadFile(p + "/" + n)
			h.Write([]byte(n + "\x00"))
			s := sha256.Sum256(b)
			h.Write(s[:])
		}
		return "dir:" + hex.EncodeToString(h.Sum(nil))
	case entry != "plugins" && fi.Mode().IsRegular():
		b, _ := os.ReadFile(p)
		h.Write(b)
		return "file:" + hex.EncodeToString(h.Sum(nil))
	}
	return "other:" + fi.Mode().Type().String()
}

// claudeLayer brings the container's copies of the layered entries up to date before it starts.
// The copies are kept across restarts: an entry is copied from the host when the container has no
// copy yet, or when the host's entry changed since the last copy while the container's did not.
// When both changed, the container keeps its own and lea says how to take the host's (remove the
// copy, restart lea). src is the host's ~/.claude, priv the container's private one, sbase the
// directory no symbolic link may lie below.
func (l *Launcher) claudeLayer(src, priv, sbase string) error {
	recPath := layerRecordPath(priv)
	if err := SafePath(sbase, recPath); err != nil {
		return err
	}
	recs := map[string]layerRecord{}
	if b, err := os.ReadFile(recPath); err == nil {
		_ = json.Unmarshal(b, &recs)
	}
	for _, e := range claudeLayered {
		h, c := src+"/"+e, priv+"/"+e
		if isSymlink(h) {
			l.logf("skipped %s: it is a symbolic link (lea copies only real entries of ~/.claude)", h)
		}
		host, cur := fingerprint(h, e), fingerprint(c, e)
		r, ok := recs[e]
		switch {
		case ok && host == r.Host && cur != "absent":
			continue // the host's did not change: the container's copy stays as it is
		case ok && cur != r.Copy && cur != "absent":
			// Both changed: the container's copy is its own (an install, a rule); kept.
			l.logf("note: %s changed on the host, and this projects directory's copy changed too: lea keeps the copy; "+
				"to take the host's, remove %s and restart lea (lea stop, then lea)", h, c)
			recs[e] = layerRecord{Host: host, Copy: r.Copy}
			continue
		}
		if err := l.seed(e, h, c, priv, sbase); err != nil {
			return err
		}
		if ok && cur != "absent" {
			l.logf("took the host's new %s into this projects directory's copy", h)
		}
		recs[e] = layerRecord{Host: host, Copy: fingerprint(c, e)}
	}
	b, _ := json.MarshalIndent(recs, "", "  ")
	return WriteFile(sbase, recPath, append(b, '\n'))
}

// seed replaces the container's copy c of the host's entry h. The copy is made aside (next to
// priv, out of the agents' sight) and renamed into place; whatever the agents left at c, a
// symbolic link included, is removed, never followed. A plugins copy keeps the container's own
// plugin data (plugins/data, #90, or the plugins-data directory of an older lea), and never takes
// the host's.
func (l *Launcher) seed(entry, h, c, priv, sbase string) error {
	if err := SafePath(sbase, priv); err != nil {
		return err
	}
	if isSymlink(c) {
		if err := os.Remove(c); err != nil {
			return die("%v", err)
		}
	}
	if entry != "plugins" {
		if fi, err := os.Lstat(h); err != nil || !fi.Mode().IsRegular() {
			if err := os.RemoveAll(c); err != nil {
				return die("%v", err)
			}
			return nil
		}
		b, err := readRegular(h)
		if err != nil {
			return die("cannot read %s: %v", h, err)
		}
		if err := os.RemoveAll(c); err != nil {
			return die("%v", err)
		}
		if err := WriteFile(sbase, c, b); err != nil {
			return die("%v", err)
		}
		return nil
	}
	tmp := priv + ".plugins.new"
	if err := SafePath(sbase, tmp); err != nil {
		return err
	}
	if err := os.RemoveAll(tmp); err != nil {
		return die("%v", err)
	}
	if fi, err := os.Lstat(h); err == nil && fi.IsDir() {
		if err := copyTree(h, tmp, "data"); err != nil {
			os.RemoveAll(tmp)
			return die("cannot copy %s: %v", h, err)
		}
	} else if err := os.Mkdir(tmp, 0o755); err != nil {
		return die("%v", err)
	}
	// The container's plugin data moves into the new copy: its own (a real directory only), else
	// an older lea's plugins-data.
	for _, d := range []string{c + "/data", priv + "/plugins-data"} {
		if fi, err := os.Lstat(d); err == nil && fi.IsDir() {
			if err := os.Rename(d, tmp+"/data"); err != nil {
				os.RemoveAll(tmp)
				return die("cannot keep the plugin data %s: %v", d, err)
			}
			break
		}
	}
	if err := os.RemoveAll(c); err != nil {
		os.RemoveAll(tmp)
		return die("%v", err)
	}
	if err := os.Rename(tmp, c); err != nil {
		os.RemoveAll(tmp)
		return die("cannot put the copy of %s in place: %v", h, err)
	}
	return nil
}

// readRegular reads p, refusing anything but a regular file (no symbolic link is followed).
func readRegular(p string) ([]byte, error) {
	f, err := os.OpenFile(p, os.O_RDONLY|noFollow, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	return io.ReadAll(f)
}

// copyTree copies the directory src to dst (which must not exist): directories, regular files
// with their permission bits, symbolic links as links (never followed: they resolve in the
// container, which sees only what lea mounts). Other files are skipped, and so is the top-level
// entry named skip.
func copyTree(src, dst, skip string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if rel == skip {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		t := dst
		if rel != "." {
			t = filepath.Join(dst, rel)
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		switch m := fi.Mode(); {
		case m.IsDir():
			if err := os.Mkdir(t, 0o700); err != nil {
				return err
			}
			return os.Chmod(t, m.Perm()|0o700)
		case m&fs.ModeSymlink != 0:
			to, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(to, t)
		case m.IsRegular():
			return copyFile(p, t, m.Perm())
		}
		return nil
	})
}

// copyFile copies the regular file src to the new file dst with mode perm.
func copyFile(src, dst string, perm fs.FileMode) error {
	in, err := os.OpenFile(src, os.O_RDONLY|noFollow, 0)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL|noFollow, perm|0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// HostPermissionsPath is where the container reads the host's permission rules (#111): the root
// entrypoint merges them into the managed settings, the only source of permission rules Claude
// Code honours in lea (allowManagedPermissionRulesOnly). Read-only, outside ~/.claude.
const HostPermissionsPath = "/etc/lea/claude-permissions.json"

// permissionModes are the default modes lea takes from the host's settings: never
// bypassPermissions, which the managed settings disable.
var permissionModes = map[string]bool{"default": true, "acceptEdits": true, "plan": true, "dontAsk": true, "auto": true}

// hostPermissions is the permissions of the host's ~/.claude/settings.json (src) as lea passes
// them to its container: the allow, ask and deny rules and the additional directories (strings
// only, at most 1000 of each, 1024 bytes each) and a default mode lea accepts. The host's file is
// the user's, which no agent writes; the container's copy of it (claudeLayer) is the agents' and
// never a source of rules. "{}" when the host has none, or a file lea cannot read.
func hostPermissions(src string) []byte {
	out := map[string]any{}
	b, err := readRegular(src + "/settings.json")
	if err != nil {
		return []byte("{}\n")
	}
	var s struct {
		Permissions map[string]json.RawMessage `json:"permissions"`
	}
	if json.Unmarshal(b, &s) != nil {
		return []byte("{}\n")
	}
	for _, k := range []string{"allow", "ask", "deny", "additionalDirectories"} {
		var raw []any
		if json.Unmarshal(s.Permissions[k], &raw) != nil {
			continue
		}
		var rules []string
		for _, r := range raw {
			if v, ok := r.(string); ok && v != "" && len(v) <= 1024 && len(rules) < 1000 {
				rules = append(rules, v)
			}
		}
		if len(rules) > 0 {
			out[k] = rules
		}
	}
	var mode string
	if json.Unmarshal(s.Permissions["defaultMode"], &mode) == nil && permissionModes[mode] {
		out["defaultMode"] = mode
	}
	j, _ := json.MarshalIndent(out, "", "  ")
	return append(j, '\n')
}

// hostPermissionsFile writes hostPermissions, with lea config claude's settings on top
// (claudeHanded, #334), next to the container's private ~/.claude (priv), out of the agents'
// sight, and returns its mount: read-only at HostPermissionsPath.
func (l *Launcher) hostPermissionsFile(src, priv, sbase string) ([]string, error) {
	p := priv + ".permissions.json"
	if err := noComma(p); err != nil {
		return nil, err
	}
	c, err := readClaudeConfig(l.claudeConfigPath())
	if err != nil {
		return nil, die("%v", err)
	}
	if err := WriteFile(sbase, p, claudeHanded(hostPermissions(src), c)); err != nil {
		return nil, die("%v", err)
	}
	// Readable by the container's root under every runtime and engine (a user namespace, a VM).
	if err := os.Chmod(p, 0o644); err != nil {
		return nil, die("%v", err)
	}
	return []string{"--mount", "type=bind,src=" + p + ",dst=" + HostPermissionsPath + ",readonly"}, nil
}
