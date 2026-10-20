package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DavidGodefroid/lea/internal/launcher"
)

// A deck's projects directory maps to its container as the launcher names it: from the physical
// path, so a symlink to the directory names the same container, another directory another one.
func TestLockContainerNameIsTheLaunchers(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "lea.conf"), []byte("LEA_PREFIX=lea\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	env := map[string]string{"HOME": home}
	cfg, err := launcher.LoadConfig(func(k string) string { return env[k] }, root, root)
	if err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "projects")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	name := lockContainerName(cfg)
	want := cfg.ContainerName(real)
	if want != "lea-"+launcher.ID(real) {
		t.Fatalf("ContainerName=%q", want)
	}
	if got := name(real); got != want {
		t.Fatalf("name(%s)=%q, want %q", real, got, want)
	}
	if got := name(link); got != want {
		t.Fatalf("name(symlink)=%q, want %q", got, want)
	}
	if got := name(home); got == want {
		t.Fatalf("another directory names the same container %q", got)
	}
}
