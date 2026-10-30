package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sid = "0f8e2c1a-1b2c-4d5e-8f90-123456789abc"

func write(t *testing.T, f, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, f string) string {
	t.Helper()
	b, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRepairCutsAfterTheLastValidLine(t *testing.T) {
	const good = `{"type":"user","n":1}` + "\n" + `{"type":"assistant","n":2}` + "\n"
	for _, c := range []struct{ name, in, want string }{
		{"cut last line", good + `{"type":"user","mess`, good},
		{"cut last line ending in a newline", good + `{"type":"user","mess` + "\n", good},
		{"several invalid lines at the end", good + "{\"a\":\n" + `{"b"` + "\n" + `xx`, good},
		{"valid last line without its newline", good + `{"type":"user","n":3}`, good + `{"type":"user","n":3}` + "\n"},
		{"only a cut line", `{"type":"us`, ""},
		{"blank lines at the end", good + "\n\n", good},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := filepath.Join(t.TempDir(), sid+".jsonl")
			write(t, f, c.in)
			r, err := Repair(f)
			if err != nil || r == nil {
				t.Fatalf("r=%v err=%v", r, err)
			}
			if got := read(t, f); got != c.want {
				t.Fatalf("repaired to %q, want %q", got, c.want)
			}
			if got := read(t, r.Backup); got != c.in {
				t.Fatalf("backup %q, want the file as it was %q", got, c.in)
			}
			if !strings.HasPrefix(r.Backup, f+BackupSuffix) || strings.HasSuffix(r.Backup, ".jsonl") {
				t.Fatalf("backup name %s", r.Backup)
			}
			if st, _ := os.Stat(r.Backup); st.Mode().Perm() != 0o600 {
				t.Fatalf("backup mode %v", st.Mode())
			}
			// Dropped counts the bytes cut; a valid last line only gains its newline.
			if want := int64(len(c.in) - len(c.want)); r.File != f || r.Dropped != max(want, 0) {
				t.Fatalf("r=%+v", r)
			}
		})
	}
}

func TestRepairLeavesASoundTranscriptAlone(t *testing.T) {
	d := t.TempDir()
	for _, in := range []string{"", `{"a":1}` + "\n", `{"a":1}` + "\n" + "not json\n" + `{"b":2}` + "\n"} {
		f := filepath.Join(d, sid+".jsonl")
		write(t, f, in)
		if r, err := Repair(f); r != nil || err != nil {
			t.Fatalf("%q: r=%v err=%v", in, r, err)
		}
		if got := read(t, f); got != in {
			t.Fatalf("changed to %q", got)
		}
	}
	// Invalid lines before the last valid one are history: they stay.
	if es, _ := os.ReadDir(d); len(es) != 1 {
		t.Fatalf("a backup was written: %v", es)
	}
}

func TestRepairRefusesALink(t *testing.T) {
	d := t.TempDir()
	target := filepath.Join(d, "elsewhere")
	write(t, target, `{"a":`)
	f := filepath.Join(d, sid+".jsonl")
	if err := os.Symlink(target, f); err != nil {
		t.Fatal(err)
	}
	if _, err := Repair(f); err == nil {
		t.Fatal("a link was repaired")
	}
	if got := read(t, target); got != `{"a":` {
		t.Fatalf("the link's target changed: %q", got)
	}
}

func TestRepairSessionRepairsEachProjectsTranscript(t *testing.T) {
	cfg := t.TempDir()
	a := filepath.Join(cfg, "projects", "-p-a", sid+".jsonl")
	b := filepath.Join(cfg, "projects", "-p-b", sid+".jsonl")
	other := filepath.Join(cfg, "projects", "-p-a", "11111111-2222-4333-8444-555555555555.jsonl")
	write(t, a, `{"a":1}`+"\n"+`{"cut`)
	write(t, b, `{"b":1}`+"\n")
	write(t, other, `{"cut`)
	rs, err := RepairSession(cfg, sid)
	if err != nil || len(rs) != 1 || rs[0].File != a {
		t.Fatalf("rs=%+v err=%v", rs, err)
	}
	if read(t, a) != `{"a":1}`+"\n" || read(t, b) != `{"b":1}`+"\n" || read(t, other) != `{"cut` {
		t.Fatal("wrong files repaired")
	}
	// Not a session id: nothing is looked for (no pattern, no path).
	for _, id := range []string{"", "*", "../x", sid + "/../" + sid, strings.ToUpper(sid)} {
		if rs, err := RepairSession(cfg, id); rs != nil || err != nil {
			t.Fatalf("%q: rs=%v err=%v", id, rs, err)
		}
	}
}
