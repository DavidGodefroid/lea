package acp

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func buildFake(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fakeagent")
	if out, err := exec.Command("go", "build", "-o", bin, "testdata/fakeagent.go").CombinedOutput(); err != nil {
		t.Fatalf("build fake agent: %v %s", err, out)
	}
	return bin
}

func TestProbeAgainstFakeAgent(t *testing.T) {
	bin := buildFake(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var log bytes.Buffer
	stop, err := Probe(ctx, []string{bin}, t.TempDir(), "Reply with the word pong", &log)
	if err != nil || stop != "end_turn" {
		t.Fatalf("stop=%q err=%v log=%s", stop, err, log.String())
	}
	if !strings.Contains(log.String(), "pong") {
		t.Fatalf("update not logged: %s", log.String())
	}
}

func TestProbeAnswersAgentRequests(t *testing.T) {
	t.Setenv("FAKE_MODE", "requests")
	bin := buildFake(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var log bytes.Buffer
	stop, err := Probe(ctx, []string{bin}, t.TempDir(), "x", &log)
	if err != nil || stop != "end_turn" {
		t.Fatalf("stop=%q err=%v log=%s", stop, err, log.String())
	}
	l := log.String()
	for _, want := range []string{`fake: permission reply`, `"id":"perm-1"`, `cancelled`, "non-JSON line", "unsupported request fs/read_text_file"} {
		if !strings.Contains(l, want) {
			t.Fatalf("log lacks %q: %s", want, l)
		}
	}
}

func TestProbeMissingStopReason(t *testing.T) {
	t.Setenv("FAKE_MODE", "noreason")
	bin := buildFake(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var log bytes.Buffer
	if stop, err := Probe(ctx, []string{bin}, t.TempDir(), "x", &log); err == nil {
		t.Fatalf("want error, got stop=%q", stop)
	}
}

func TestProbeRelativeCwdIsAbsolute(t *testing.T) {
	bin := buildFake(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var log bytes.Buffer
	if _, err := Probe(ctx, []string{bin}, ".", "x", &log); err != nil {
		t.Fatal(err)
	}
}
