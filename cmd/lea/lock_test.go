package main

import (
	"crypto/sha256"
	"encoding/hex"
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

// The deck's locksql console is on when lox up runs a locksql lock for the deck's projects
// directory and the user's configuration does not set LEA_LOCKSQL; LEA_LOCKSQL=0 keeps it off.
func TestLockSpecEnabledWithUpJSONOnly(t *testing.T) {
	home, cfgHome, proj := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("LEA_CONFIG", "")
	h := sha256.Sum256([]byte(proj))
	project := "lockfamily-" + hex.EncodeToString(h[:4])
	d := filepath.Join(proj, ".lockfamily")
	if err := os.Mkdir(d, 0o755); err != nil {
		t.Fatal(err)
	}
	up := `{"version":1,"project":"` + project + `","workspace":"` + proj + `","locks":{"crm":{"type":"locksql","socket_volume":"` + project + `_crm-run"}}}`
	if err := os.WriteFile(filepath.Join(d, "up.json"), []byte(up), 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := lockSpec(filepath.Join(root, "build", "lea"))("locksql")
	if err != nil {
		t.Fatal(err)
	}
	if spec.EnabledFor == nil || !spec.EnabledFor(proj) || spec.EnabledFor(t.TempDir()) {
		t.Fatal("locksql must be on for the projects directory with a lock, off for another")
	}
	if err := os.MkdirAll(filepath.Join(cfgHome, "lea"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgHome, "lea", "lea.conf"), []byte("LEA_LOCKSQL=0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec, err = lockSpec(filepath.Join(root, "build", "lea"))("locksql")
	if err != nil {
		t.Fatal(err)
	}
	if spec.EnabledFor(proj) {
		t.Fatal("LEA_LOCKSQL=0 must keep it off")
	}
}
