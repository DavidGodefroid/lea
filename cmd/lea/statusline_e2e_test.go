package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DavidGodefroid/lea/internal/daemon"
	"github.com/DavidGodefroid/lea/internal/store"
)

// TestStatuslineBinary builds lea and runs `lea statusline` as Claude Code does: the status-line
// JSON on stdin, the line on stdout, exit 0 whatever the input.
func TestStatuslineBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	bin := filepath.Join(t.TempDir(), "lea")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	in := `{"model":{"display_name":"Opus 5.5"},"workspace":{"current_dir":"/p/shop"},
	  "context_window":{"used_percentage":9},
	  "rate_limits":{"five_hour":{"used_percentage":31,"resets_at":1790000000},"seven_day":{"used_percentage":12}}}`
	for _, c := range []struct{ in, want string }{
		{in, "Opus 5.5 · ctx ▓░░░░░░░ 9% · 5h ▓▓░░░░ 31% · 7d ▓░░░░░ 12%"},
		{`{"model":{"display_name":"Opus 5.5"},"context_window":{"used_percentage":9}}`, "Opus 5.5 · ctx ▓░░░░░░░ 9%"},
		{"not json", "lea"},
	} {
		cmd := exec.Command(bin, "statusline")
		// No agent identity: nothing is posted, the line is still printed.
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "NO_COLOR=1"}
		cmd.Stdin = strings.NewReader(c.in)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if got := strings.TrimSpace(string(out)); got != c.want {
			t.Errorf("got %q want %q", got, c.want)
		}
	}
	cmd := exec.Command(bin, "statusline")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	cmd.Stdin = strings.NewReader(in)
	if out, err := cmd.Output(); err != nil || !strings.Contains(string(out), "\x1b[32m▓▓\x1b[2m░░░░\x1b[0m 31%") {
		t.Fatalf("colour line=%q err=%v", out, err)
	}
}

// TestStatuslineBinaryChainsTheUserLine (#374): with a status line script in the agent's
// ~/.claude/settings.json, the built `lea statusline` prints the script's output while the daemon
// still gets the agent's context use; with the script gone, lea's own line is back.
func TestStatuslineBinaryChainsTheUserLine(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	bin := filepath.Join(t.TempDir(), "lea")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	dir, _ := os.MkdirTemp("", "lea")
	t.Cleanup(func() { os.RemoveAll(dir) })
	st, err := store.Open(filepath.Join(dir, "lea.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	d := daemon.New(st, filepath.Join(dir, "run"), func() int64 { return 1 })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	bus, err := d.AttachContainer(ctx, "lea-a")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	c := home + "/.claude"
	os.MkdirAll(c, 0o700)
	os.WriteFile(c+"/statusline.sh", []byte("#!/bin/sh\nprintf 'mine: %s\\n' \"$(cat)\"\n"), 0o755)
	settings := c + "/settings.json"
	in := `{"model":{"display_name":"Opus 5.5"},"context_window":{"used_percentage":%d}}`
	line := func(pct int) string {
		cmd := exec.Command(bin, "statusline")
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "NO_COLOR=1", "HOME=" + home, "LEA_BUS_SOCK=" + bus, "LEA_AGENT=dev"}
		cmd.Stdin = strings.NewReader(fmt.Sprintf(in, pct))
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	ctxPct := func() int { u, _ := st.Usages("lea-a"); return u["lea-a/dev"].CtxPct }

	os.WriteFile(settings, []byte(`{"statusLine":{"type":"command","command":"~/.claude/statusline.sh"}}`), 0o600)
	if got, want := line(12), "mine: "+fmt.Sprintf(in, 12); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if p := ctxPct(); p != 12 {
		t.Fatalf("ctx_pct=%d with the user line", p)
	}
	// A failing script: lea's line, still reported.
	os.WriteFile(settings, []byte(`{"statusLine":{"type":"command","command":"exit 1"}}`), 0o600)
	if got := line(20); got != "Opus 5.5 · ctx ▓▓░░░░░░ 20%" || ctxPct() != 20 {
		t.Fatalf("failing script: %q, ctx_pct %d", got, ctxPct())
	}
	os.WriteFile(settings, []byte(`{}`), 0o600)
	if got := line(30); got != "Opus 5.5 · ctx ▓▓░░░░░░ 30%" || ctxPct() != 30 {
		t.Fatalf("no user line: %q, ctx_pct %d", got, ctxPct())
	}
}
