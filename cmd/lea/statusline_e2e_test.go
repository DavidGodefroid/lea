package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
