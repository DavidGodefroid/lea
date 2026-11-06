package launcher

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// treeHash is a digest of everything below root (names, modes, contents, link targets).
func treeHash(t *testing.T, root string) string {
	t.Helper()
	h := sha256.New()
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			h.Write([]byte("err " + p))
			return nil
		}
		fi, _ := d.Info()
		h.Write([]byte(p + " " + fi.Mode().String() + "\n"))
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			to, _ := os.Readlink(p)
			h.Write([]byte("-> " + to))
		case fi.Mode().IsRegular():
			b, _ := os.ReadFile(p)
			h.Write(b)
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))
}

// layerWorld is a host ~/.claude with settings and plugins (a marketplace, a cached plugin, the
// host's own plugin data, a symbolic link), and the private ~/.claude of one projects dir.
func layerWorld(t *testing.T) (w *world, src, priv, st string) {
	t.Helper()
	w = newWorld(t, repoRoot(t))
	src, st = w.home+"/.claude", w.data+"/lea/state"
	priv = st + "/claude/" + ID(w.proj)
	for _, d := range []string{"plugins/cache/mk/p1/1.0.0/commands", "plugins/marketplaces/mk", "plugins/data/p1-mk"} {
		os.MkdirAll(src+"/"+d, 0o755)
	}
	os.WriteFile(src+"/settings.json", []byte(`{"permissions":{"allow":["Bash(ls)"]}}`), 0o644)
	os.WriteFile(src+"/plugins/installed_plugins.json", []byte(`{"version":2,"plugins":{}}`), 0o644)
	os.WriteFile(src+"/plugins/known_marketplaces.json", []byte(`{"mk":{}}`), 0o644)
	os.WriteFile(src+"/plugins/cache/mk/p1/1.0.0/commands/ok.md", []byte("Say ok."), 0o644)
	os.WriteFile(src+"/plugins/cache/mk/p1/1.0.0/run.sh", []byte("#!/bin/sh\n"), 0o755)
	os.WriteFile(src+"/plugins/data/p1-mk/token", []byte("host secret"), 0o600)
	os.Symlink("cache/mk", src+"/plugins/current")
	return w, src, priv, st
}

func mounts(t *testing.T, w *world, st string) {
	t.Helper()
	if _, err := w.launcher().claudeMounts(w.home, w.proj, st); err != nil {
		t.Fatal(err)
	}
}

// TestClaudeLayerCopiesAndNeverWritesTheHost: each container gets its own writable copy of
// settings.json and plugins (#111), and the host's are byte-identical after every start, whatever
// the container did to its copies.
func TestClaudeLayerCopiesAndNeverWritesTheHost(t *testing.T) {
	w, src, priv, st := layerWorld(t)
	// An older lea's plugin data (#90) and placeholders.
	os.MkdirAll(priv+"/plugins-data/p1-mk", 0o755)
	os.WriteFile(priv+"/plugins-data/p1-mk/state", []byte("agent data"), 0o600)
	os.MkdirAll(priv+"/plugins", 0o755)
	os.WriteFile(priv+"/settings.json", nil, 0o644)
	// The host's entries lea copies (it makes the transcripts dir projects/<slug> only).
	host := func() string { return treeHash(t, src+"/plugins") + fingerprint(src+"/settings.json", "settings.json") }
	before := host()
	mounts(t, w, st)
	if b, _ := os.ReadFile(priv + "/settings.json"); string(b) != `{"permissions":{"allow":["Bash(ls)"]}}` {
		t.Errorf("settings copy %q", b)
	}
	if b, _ := os.ReadFile(priv + "/plugins/cache/mk/p1/1.0.0/commands/ok.md"); string(b) != "Say ok." {
		t.Errorf("plugin copy %q", b)
	}
	if fi, err := os.Stat(priv + "/plugins/cache/mk/p1/1.0.0/run.sh"); err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("an executable stays executable: %v %v", fi, err)
	}
	if to, err := os.Readlink(priv + "/plugins/current"); err != nil || to != "cache/mk" {
		t.Errorf("a link is copied as a link: %q %v", to, err)
	}
	// The host's plugin data never; the container's own (an older lea's plugins-data) kept.
	if exists(priv + "/plugins/data/p1-mk/token") {
		t.Error("the host's plugin data was copied")
	}
	if b, _ := os.ReadFile(priv + "/plugins/data/p1-mk/state"); string(b) != "agent data" || exists(priv+"/plugins-data") {
		t.Errorf("the container's plugin data: %q", b)
	}
	// The container installs a plugin and adds a permission rule: they stay across restarts.
	os.WriteFile(priv+"/settings.json", []byte(`{"permissions":{"allow":["Bash(make *)"]}}`), 0o644)
	os.WriteFile(priv+"/plugins/installed_plugins.json", []byte(`{"version":2,"plugins":{"p2@mk":[]}}`), 0o644)
	mounts(t, w, st)
	if b, _ := os.ReadFile(priv + "/settings.json"); !strings.Contains(string(b), "make") {
		t.Errorf("the container's rule was lost: %q", b)
	}
	if b, _ := os.ReadFile(priv + "/plugins/installed_plugins.json"); !strings.Contains(string(b), "p2@mk") {
		t.Errorf("the container's install was lost: %q", b)
	}
	if after := host(); after != before {
		t.Fatal("the host's ~/.claude changed")
	}
}

// TestClaudeLayerFollowsTheHost: a host change reaches a copy the container did not change, never
// one it changed (lea says how to take the host's); a removed copy is made again.
func TestClaudeLayerFollowsTheHost(t *testing.T) {
	w, src, priv, st := layerWorld(t)
	mounts(t, w, st)
	os.MkdirAll(priv+"/plugins/data/p1-mk", 0o755)
	os.WriteFile(priv+"/plugins/data/p1-mk/state", []byte("agent data"), 0o600)
	// The host installs a plugin: the untouched copy takes it, keeping the container's data.
	os.WriteFile(src+"/plugins/installed_plugins.json", []byte(`{"version":2,"plugins":{"p3@mk":[]}}`), 0o644)
	os.WriteFile(src+"/plugins/cache/mk/p3", []byte("p3"), 0o644)
	mounts(t, w, st)
	if !exists(priv+"/plugins/cache/mk/p3") || !strings.Contains(w.errb.String(), "took the host's new "+src+"/plugins") {
		t.Errorf("the host's install did not reach the copy: %q", w.errb.String())
	}
	if b, _ := os.ReadFile(priv + "/plugins/data/p1-mk/state"); string(b) != "agent data" {
		t.Errorf("the container's plugin data was lost: %q", b)
	}
	// Both change: the container's copy is kept, once noted.
	os.WriteFile(priv+"/settings.json", []byte(`{"agent":1}`), 0o644)
	os.WriteFile(src+"/settings.json", []byte(`{"host":2}`), 0o644)
	w.errb.Reset()
	mounts(t, w, st)
	if b, _ := os.ReadFile(priv + "/settings.json"); string(b) != `{"agent":1}` {
		t.Errorf("the container's settings were replaced: %q", b)
	}
	if !strings.Contains(w.errb.String(), "remove "+priv+"/settings.json and restart lea") {
		t.Errorf("no note: %q", w.errb.String())
	}
	w.errb.Reset()
	mounts(t, w, st)
	if w.errb.String() != "" {
		t.Errorf("noted twice: %q", w.errb.String())
	}
	// The user removes the copy: the host's comes back.
	os.Remove(priv + "/settings.json")
	mounts(t, w, st)
	if b, _ := os.ReadFile(priv + "/settings.json"); string(b) != `{"host":2}` {
		t.Errorf("a removed copy: %q", b)
	}
	// No settings on the host: none in the container either, until Claude Code writes one.
	os.Remove(src + "/settings.json")
	mounts(t, w, st)
	if exists(priv + "/settings.json") {
		t.Error("a settings copy without the host's")
	}
}

// TestClaudeLayerNeverWritesThroughALink: what an agent plants in its ~/.claude (a link from
// settings.json or plugins to a host file) is removed, never followed, when lea copies the host's
// entries again; a symlinked host entry is not copied.
func TestClaudeLayerNeverWritesThroughALink(t *testing.T) {
	w, src, priv, st := layerWorld(t)
	mounts(t, w, st)
	victim, vdir := w.dir+"/victim", w.dir+"/victim-dir"
	os.WriteFile(victim, []byte("host file"), 0o600)
	os.MkdirAll(vdir, 0o755)
	os.WriteFile(vdir+"/keep", []byte("k"), 0o600)
	os.Remove(priv + "/settings.json")
	os.Symlink(victim, priv+"/settings.json")
	os.RemoveAll(priv + "/plugins")
	os.Symlink(vdir, priv+"/plugins")
	// Removed links are copies the container changed; the host changes too: lea keeps them...
	os.WriteFile(src+"/settings.json", []byte(`{"host":3}`), 0o644)
	mounts(t, w, st)
	// ...and the user takes the host's by removing them.
	os.Remove(priv + "/settings.json")
	os.Remove(priv + "/plugins")
	mounts(t, w, st)
	if b, _ := os.ReadFile(victim); string(b) != "host file" {
		t.Errorf("written through a link: %q", b)
	}
	if ents, _ := os.ReadDir(vdir); len(ents) != 1 {
		t.Errorf("written through a link: %v", ents)
	}
	// A planted link, and lea's record of the copies lost: lea copies again, removing the link.
	w2, src2, priv2, st2 := layerWorld(t)
	mounts(t, w2, st2)
	os.Remove(priv2 + "/settings.json")
	os.Symlink(victim, priv2+"/settings.json")
	os.Remove(layerRecordPath(priv2)) // a lost record: lea copies again
	os.WriteFile(src2+"/settings.json", []byte(`{"host":4}`), 0o644)
	mounts(t, w2, st2)
	if b, _ := os.ReadFile(victim); string(b) != "host file" || isSymlink(priv2+"/settings.json") {
		t.Errorf("written through a link: %q", b)
	}
	if b, _ := os.ReadFile(priv2 + "/settings.json"); string(b) != `{"host":4}` {
		t.Errorf("copy %q", b)
	}
	// A symlinked host entry is skipped, with a word.
	w3, src3, priv3, st3 := layerWorld(t)
	os.Rename(src3+"/settings.json", w3.dir+"/dotfiles-settings.json")
	os.Symlink(w3.dir+"/dotfiles-settings.json", src3+"/settings.json")
	mounts(t, w3, st3)
	if exists(priv3+"/settings.json") || !strings.Contains(w3.errb.String(), "skipped "+src3+"/settings.json") {
		t.Errorf("a symlinked host settings.json: %q", w3.errb.String())
	}
}
