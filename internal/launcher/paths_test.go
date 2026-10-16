package launcher

import (
	"os"
	"strings"
	"testing"
)

func TestSafePathRefusesSymlinksAndDots(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(base+"/a", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), base+"/a/link"); err != nil {
		t.Fatal(err)
	}
	if err := SafePath(base, base+"/a/missing/x"); err != nil {
		t.Fatalf("missing components are fine: %v", err)
	}
	for p, want := range map[string]string{
		base + "/a/link/x": "is a symbolic link",
		base + "/a/../b":   "contains '.' or '..'",
		"/elsewhere/x":     "is not below",
	} {
		if err := SafePath(base, p); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("SafePath(%s) = %v, want %q", p, err, want)
		}
	}
	if err := Mkdirs(base, base+"/a/link/new"); err == nil {
		t.Fatal("Mkdirs through a symlink must fail")
	}
	if err := WriteFile(base, base+"/a/link/f", []byte("x")); err == nil {
		t.Fatal("WriteFile through a symlink must fail")
	}
}

func TestWriteFileReplacesAtomically(t *testing.T) {
	base := t.TempDir()
	if err := WriteFile(base, base+"/f", []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(base, base+"/f", []byte("two")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(base + "/f")
	entries, _ := os.ReadDir(base)
	if string(b) != "two" || len(entries) != 1 {
		t.Fatalf("content %q, %d entries (temporary file left?)", b, len(entries))
	}
}
