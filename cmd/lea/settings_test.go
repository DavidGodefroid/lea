package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DavidGodefroid/lea/internal/settings"
)

// The deck's settings screen works on the host only: inside a lea container (where an agent could
// run lea deck) there is no store, whatever the files say.
func TestDeckSettingsAreHostOnly(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "config"), 0o755)
	os.WriteFile(filepath.Join(root, "config", "lea.conf"), nil, 0o644)
	exe := filepath.Join(root, "build", "lea")
	none := func(string) string { return "" }
	host := func(p string) bool { return p != "/run/lea/ready" && fileExists(p) } // these tests may run inside lea
	st, off := deckSettings(func(k string) string { return map[string]string{"LEA_BUS_SOCK": "/run/lea/bus/bus.sock"}[k] },
		func(string) bool { return false }, exe, root)
	if st != nil || !strings.Contains(off, "host") {
		t.Fatalf("inside lea: %v %q", st, off)
	}
	if st, off = deckSettings(none, func(p string) bool { return p == "/run/lea/ready" }, exe, root); st != nil || off == "" {
		t.Fatalf("ready marker: %v %q", st, off)
	}
	if st, off = deckSettings(none, host, filepath.Join(t.TempDir(), "build", "lea"), root); st != nil || !strings.Contains(off, "config/lea.conf") {
		t.Fatalf("no installation: %v %q", st, off)
	}
	st, off = deckSettings(none, host, exe, root)
	if s, ok := st.(settings.Store); !ok || off != "" || s.Root != root || s.Cwd != root {
		t.Fatalf("host: %#v %q", st, off)
	}
}
