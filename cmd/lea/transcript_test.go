package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// lea repair-transcript (lea-session's step before a resume, #219) repairs the session's transcript
// under CLAUDE_CONFIG_DIR, else ~/.claude, says what it did, and never fails the resume.
func TestRepairTranscriptCommand(t *testing.T) {
	const id = "0f8e2c1a-1b2c-4d5e-8f90-123456789abc"
	for _, viaHome := range []bool{false, true} {
		d := t.TempDir()
		cfg := d
		env := map[string]string{"CLAUDE_CONFIG_DIR": d}
		if viaHome {
			cfg = filepath.Join(d, ".claude")
			env = map[string]string{"HOME": d}
		}
		f := filepath.Join(cfg, "projects", "-p", id+".jsonl")
		if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(f, []byte(`{"a":1}`+"\n"+`{"cut`), 0o600)
		var errb bytes.Buffer
		if rc := runRepairTranscript([]string{id}, &errb, func(k string) string { return env[k] }); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
		if b, _ := os.ReadFile(f); string(b) != `{"a":1}`+"\n" || !strings.Contains(errb.String(), "5 bytes dropped") {
			t.Fatalf("home=%v: %q, %s", viaHome, b, errb.String())
		}
	}
	// A transcript it cannot repair is reported; the resume goes on.
	d := t.TempDir()
	f := filepath.Join(d, "projects", "-p", id+".jsonl")
	os.MkdirAll(f, 0o700) // a directory, not a file
	var errb bytes.Buffer
	if rc := runRepairTranscript([]string{id}, &errb, func(k string) string { return map[string]string{"CLAUDE_CONFIG_DIR": d}[k] }); rc != 0 ||
		!strings.Contains(errb.String(), "repair the transcript") {
		t.Fatalf("rc=%d %s", rc, errb.String())
	}
	if rc := runRepairTranscript(nil, &errb, os.Getenv); rc != 2 {
		t.Fatalf("no id: rc=%d", rc)
	}
}
