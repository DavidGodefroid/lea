package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/hook"
	"github.com/DavidGodefroid/lea/internal/store"
)

// claudePty runs argv in a pseudo-terminal for secs seconds (Claude Code's TUI needs one), then
// kills it.
const claudePty = `
import os, pty, sys, time, select, signal, struct, fcntl, termios
secs = float(sys.argv[1]); argv = sys.argv[2:]
pid, fd = pty.fork()
if pid == 0:
    os.execvp(argv[0], argv)
fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack('HHHH', 45, 160, 0, 0))
end = time.time() + secs
while time.time() < end:
    r, _, _ = select.select([fd], [], [], 0.2)
    if r:
        try:
            d = os.read(fd, 65536)
        except OSError:
            break
        if not d:
            break
        sys.stdout.buffer.write(d)
        if b'\x1b[6n' in d:
            os.write(fd, b'\x1b[1;1R')
os.kill(pid, signal.SIGKILL)
`

// The real Claude Code, with lea's hooks (image/claude-settings.json), hits a usage limit (the
// API answers 429 rate_limit_error) and stops: lea marks the agent limited. Once the limit lifts
// and the resume time comes, lea's LimitWait job wakes the model with lea's fixed text, typing
// nothing into the terminal; the turn succeeds and the limit is cleared. Set LEA_E2E_CLAUDE to a
// claude binary to run it (the image has one: /usr/local/bin/claude).
func TestClaudeResumesAfterAUsageLimit(t *testing.T) {
	claude := os.Getenv("LEA_E2E_CLAUDE")
	if claude == "" {
		t.Skip("LEA_E2E_CLAUDE is not set")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3 for the pseudo-terminal")
	}
	defer func(m, b, j int64) { minResumeWait, retryBase, resumeJitter = m, b, j }(minResumeWait, retryBase, resumeJitter)
	minResumeWait, retryBase, resumeJitter = 1, 3, 0

	dir := shortDir(t)
	lea := filepath.Join(dir, "lea")
	if out, err := exec.Command("go", "build", "-o", lea, "../../cmd/lea").CombinedOutput(); err != nil {
		t.Fatalf("build lea: %v\n%s", err, out)
	}

	// The Messages API: 429 while limited, then a short answer.
	var mu sync.Mutex
	limited := true
	var bodies []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		lim := limited
		if strings.HasPrefix(r.URL.Path, "/v1/messages") && !strings.Contains(r.URL.Path, "count_tokens") {
			bodies = append(bodies, fmt.Sprintf("%v %s", lim, b))
		}
		mu.Unlock()
		if lim {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Should-Retry", "false")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"usage limit reached"}}`)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/v1/messages") || strings.Contains(r.URL.Path, "count_tokens") {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"input_tokens":1}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range []string{
			`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-e2e","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"resumed ok"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`,
			`{"type":"message_stop"}`,
		} {
			var e struct{ Type string }
			_ = json.Unmarshal([]byte(ev), &e)
			io.WriteString(w, "event: "+e.Type+"\ndata: "+ev+"\n\n")
		}
	}))
	defer api.Close()

	// lea's daemon, and the agent's bus.
	st, err := store.Open(filepath.Join(dir, "lea.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	d := New(st, filepath.Join(dir, "run"), func() int64 { return time.Now().Unix() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l, err := Listen(filepath.Join(dir, "d.sock"))
	if err != nil {
		t.Fatal(err)
	}
	go d.Serve(ctx, l, Origin{})
	bus, err := d.AttachContainer(ctx, "lea-a")
	if err != nil {
		t.Fatal(err)
	}

	// lea's own Claude settings, with this build of lea and without the telemetry.
	raw, err := os.ReadFile("../../image/claude-settings.json")
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal([]byte(strings.ReplaceAll(string(raw), "/usr/local/bin/lea", lea)), &settings); err != nil {
		t.Fatal(err)
	}
	delete(settings, "env")
	sb, _ := json.Marshal(settings)
	sf := filepath.Join(dir, "settings.json")
	os.WriteFile(sf, sb, 0o600)
	wd, cfg := filepath.Join(dir, "wd"), filepath.Join(dir, "cfg")
	os.Mkdir(wd, 0o755)
	os.Mkdir(cfg, 0o700)
	const key = "sk-ant-api03-e2e-not-a-real-key-0000"
	seed, _ := json.Marshal(map[string]any{"hasCompletedOnboarding": true, "theme": "dark",
		"customApiKeyResponses": map[string]any{"approved": []string{key[len(key)-20:]}, "rejected": []string{}},
		"projects":              map[string]any{wd: map[string]any{"hasTrustDialogAccepted": true, "hasCompletedProjectOnboarding": true}}})
	os.WriteFile(filepath.Join(cfg, ".claude.json"), seed, 0o600)

	cmd := exec.Command("python3", "-I", "-c", claudePty, "90", claude, "--settings", sf, "say hi")
	cmd.Dir = wd
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "CLAUDE_CONFIG_DIR=" + cfg, "TERM=xterm-256color",
		"ANTHROPIC_BASE_URL=" + api.URL, "ANTHROPIC_API_KEY=" + key, "DISABLE_TELEMETRY=1", "DISABLE_AUTOUPDATER=1",
		"LEA_BUS_SOCK=" + bus, "LEA_AGENT=dev"}
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()

	until := func(what string, secs int, ok func(store.Agent) bool) store.Agent {
		t.Helper()
		for end := time.Now().Add(time.Duration(secs) * time.Second); time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
			if a, err := st.AgentByKey("lea-a/dev"); err == nil && ok(a) {
				return a
			}
		}
		a, _ := st.AgentByKey("lea-a/dev")
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("%s: not in %d s; agent=%+v requests=%d\n%s", what, secs, a, len(bodies), out.String())
		return a
	}
	a := until("the usage limit", 45, func(a store.Agent) bool { return a.LimitUntil > 0 })
	if a.State != "idle" {
		t.Fatalf("a limited agent should be idle: %+v", a)
	}
	mu.Lock()
	limited = false
	mu.Unlock()
	until("the resume", 45, func(a store.Agent) bool { return a.LimitUntil == 0 && a.LimitTries == 0 && a.State == "idle" })
	if ps, _ := st.EventPayloads("lea-a/dev", "resume"); len(ps) == 0 {
		t.Fatal("no resume event")
	}
	mu.Lock()
	defer mu.Unlock()
	var resumed string
	for _, b := range bodies {
		if strings.HasPrefix(b, "false ") {
			resumed = b
			break
		}
	}
	if !strings.Contains(resumed, hook.ResumeText) {
		t.Fatalf("the resumed request lacks lea's text:\n%.2000s", resumed)
	}
}
