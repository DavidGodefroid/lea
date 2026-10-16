package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if rc := run([]string{"version"}, strings.NewReader(""), &out, &errb); rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errb.String())
	}
	if got := strings.TrimSpace(out.String()); got != "dev" {
		t.Fatalf("version = %q, want dev", got)
	}
}

func TestUnknownWordGoesToTheLauncher(t *testing.T) {
	var out, errb bytes.Buffer
	t.Setenv("LEA_BUS_SOCK", "/run/lea/bus/bus.sock") // the launcher refuses at once, touching nothing
	if rc := run([]string{"nope"}, strings.NewReader(""), &out, &errb); rc != 2 || !strings.Contains(errb.String(), "the launcher runs on the host") {
		t.Fatalf("rc=%d stderr=%q", rc, errb.String())
	}
}

func TestDeckUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if rc := run([]string{"deck"}, strings.NewReader(""), &out, &errb); rc != 2 || !strings.Contains(errb.String(), "usage: lea deck") {
		t.Fatalf("rc=%d stderr=%q", rc, errb.String())
	}
}
