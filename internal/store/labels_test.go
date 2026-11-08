package store

import (
	"strings"
	"testing"
)

// Labels are unique among the live agents of a container (#356): an automatic label that clashes
// gets " 2", " 3"…, cut to fit; a done agent's label is free again.
func TestSetLabelUniqueSuffixesAClash(t *testing.T) {
	s := open(t)
	for _, n := range []string{"claude-1", "claude-2", "claude-3", "claude-4"} {
		if _, err := s.Register(NewAgent{Container: "lea-a", Name: n, Profile: "claude", Workdir: "/p", Role: "dev"}, Caps{}, 1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Register(NewAgent{Container: "lea-b", Name: "claude-1", Profile: "claude", Workdir: "/p", Role: "dev"}, Caps{}, 1); err != nil {
		t.Fatal(err)
	}
	set := func(key, label string, max int, onlyIfEmpty bool) string {
		t.Helper()
		got, err := s.SetLabelUnique(key, label, max, onlyIfEmpty)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := set("lea-a/claude-1", "Control Tower", 24, true); got != "Control Tower" {
		t.Fatalf("first %q", got)
	}
	if got := set("lea-a/claude-2", "Control Tower", 24, true); got != "Control Tower 2" {
		t.Fatalf("second %q", got)
	}
	if got := set("lea-a/claude-3", "control tower", 24, true); got != "control tower 3" {
		t.Fatalf("third %q: labels clash whatever their case", got)
	}
	// Another container's labels do not count.
	if got := set("lea-b/claude-1", "Control Tower", 24, true); got != "Control Tower" {
		t.Fatalf("other container %q", got)
	}
	// Setting its own label again is no clash.
	if got := set("lea-a/claude-1", "Control Tower", 24, false); got != "Control Tower" {
		t.Fatalf("again %q", got)
	}
	// onlyIfEmpty keeps a label already set (the user's).
	if got := set("lea-a/claude-1", "other", 24, true); got != "Control Tower" {
		t.Fatalf("kept %q", got)
	}
	if a, _ := s.AgentByKey("lea-a/claude-1"); a.Label != "Control Tower" {
		t.Fatalf("label %q", a.Label)
	}
	// The suffix fits in max: the label is cut, not the suffix.
	long := strings.Repeat("a", 24)
	set("lea-a/claude-4", long, 24, false)
	if err := s.SetState("lea-a/claude-2", "done", "", 2); err != nil {
		t.Fatal(err)
	}
	if got := set("lea-a/claude-2", long, 24, false); got != strings.Repeat("a", 22)+" 2" {
		t.Fatalf("cut %q", got)
	}
	// A done agent's label is free again.
	if got := set("lea-a/claude-3", "Control Tower 2", 24, false); got != "Control Tower 2" {
		t.Fatalf("free %q", got)
	}
	if _, err := s.SetLabelUnique("lea-a/nobody", "x", 24, false); err != ErrNotFound {
		t.Fatalf("unknown agent: %v", err)
	}
}

// LabelHolder names the live agent of a container that holds a label, whatever its case.
func TestLabelHolder(t *testing.T) {
	s := open(t)
	for _, n := range []string{"dev-1", "dev-2"} {
		if _, err := s.Register(NewAgent{Container: "lea-a", Name: n, Profile: "claude", Workdir: "/p", Role: "dev"}, Caps{}, 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetLabel("lea-a/dev-1", "auth work"); err != nil {
		t.Fatal(err)
	}
	if n, err := s.LabelHolder("lea-a/dev-2", "Auth Work"); err != nil || n != "dev-1" {
		t.Fatalf("holder %q err=%v", n, err)
	}
	if n, err := s.LabelHolder("lea-a/dev-1", "auth work"); err != nil || n != "" {
		t.Fatalf("own label %q err=%v", n, err)
	}
	if err := s.SetState("lea-a/dev-1", "done", "", 2); err != nil {
		t.Fatal(err)
	}
	if n, err := s.LabelHolder("lea-a/dev-2", "auth work"); err != nil || n != "" {
		t.Fatalf("done holder %q err=%v", n, err)
	}
}
