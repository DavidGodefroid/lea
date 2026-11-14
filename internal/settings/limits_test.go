package settings

import (
	"os"
	"strings"
	"testing"
)

// Each live limit (#420) is a setting of the Limits group that applies now and names its limit;
// Set writes it to the user file, Unset removes it: the environment, else the default, applies.
func TestLiveLimitsAreSettings(t *testing.T) {
	w := newWorld(t, "")
	w.env["LEA_MAX_RUNNING"] = "4"
	s := find(t, list(t, w), "LEA_MAX_RUNNING")
	if s.Group != "Limits" || s.Limit != "max_running" || !s.Live || s.Apply != ApplyNow || s.Default != "3" || s.Value != "4" || s.Source != SourceEnvironment {
		t.Fatalf("setting=%+v", s)
	}
	if err := s.Check("x"); err == nil {
		t.Fatal("x accepted")
	}
	if err := w.store().Set("LEA_MAX_RUNNING", "101"); err == nil {
		t.Fatal("101 accepted")
	}
	w.userConf(t, "LEA_SOUND='0'\n")
	if err := w.store().Set("LEA_MAX_RUNNING", "2"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(w.user)
	if string(b) != "LEA_SOUND='0'\nLEA_MAX_RUNNING='2'\n" {
		t.Fatalf("file=%q", b)
	}
	if s := find(t, list(t, w), "LEA_MAX_RUNNING"); s.Value != "2" || s.Source != SourceFile {
		t.Fatalf("after Set: %+v", s)
	}
	if err := w.store().Unset("LEA_MAX_RUNNING"); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(w.user)
	if strings.Contains(string(b), "LEA_MAX_RUNNING") || !strings.Contains(string(b), "LEA_SOUND='0'") {
		t.Fatalf("after Unset: file=%q", b)
	}
	if s := find(t, list(t, w), "LEA_MAX_RUNNING"); s.Value != "4" || s.Source != SourceEnvironment {
		t.Fatalf("after Unset: %+v", s)
	}
	delete(w.env, "LEA_MAX_RUNNING")
	if s := find(t, list(t, w), "LEA_MAX_RUNNING"); s.Value != "3" || s.Source != SourceDefault {
		t.Fatalf("no value: %+v", s)
	}
	if err := w.store().Unset("LEA_NOPE"); err == nil {
		t.Fatal("unknown key unset")
	}
	// Without a user file, Unset has nothing to do.
	os.Remove(w.user)
	if err := w.store().Unset("LEA_MAX_RUNNING"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(w.user); !os.IsNotExist(err) {
		t.Fatal("Unset made a file")
	}
}
