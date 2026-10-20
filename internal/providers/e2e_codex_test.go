package providers_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/keyproxy"
	"github.com/DavidGodefroid/lea/internal/providers"
)

// ptyRun runs argv in a pseudo-terminal for secs seconds (Codex's TUI needs one), then kills it.
const ptyRun = `
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

// The real Codex CLI, started with the arguments lea gives an agent on an endpoint provider,
// reaches the endpoint through lea-keyproxy, which adds the key: Codex never has it. Set
// LEA_E2E_CODEX to a codex binary to run it (the image has one: /usr/bin/codex).
func TestCodexReachesAnEndpointThroughTheForwarder(t *testing.T) {
	codex := os.Getenv("LEA_E2E_CODEX")
	if codex == "" {
		t.Skip("LEA_E2E_CODEX is not set")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3 for the pseudo-terminal")
	}
	type seen struct{ path, auth, model, agentKey string }
	var mu sync.Mutex
	var got []seen
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		mu.Lock()
		got = append(got, seen{r.URL.Path, r.Header.Get("Authorization"), body.Model, r.Header.Get("X-Api-Key")})
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range []string{
			`{"type":"response.created","response":{"id":"resp_1"}}`,
			`{"type":"response.output_item.done","item":{"type":"message","role":"assistant","id":"m1","content":[{"type":"output_text","text":"hello from the endpoint"}]}}`,
			`{"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
		} {
			var e struct{ Type string }
			_ = json.Unmarshal([]byte(ev), &e)
			io.WriteString(w, "event: "+e.Type+"\ndata: "+ev+"\n\n")
		}
	}))
	defer up.Close()
	// The forward proxy squid would be: plain HTTP, absolute-form requests.
	var via []string
	fwd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		via = append(via, r.URL.Host)
		mu.Unlock()
		r.RequestURI = ""
		res, err := http.DefaultTransport.RoundTrip(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer res.Body.Close()
		for k, v := range res.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(res.StatusCode)
		io.Copy(w, res.Body)
	}))
	defer fwd.Close()

	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	if err := os.WriteFile(key, []byte("sk-e2e-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	upURL, _ := url.Parse(up.URL + "/v1")
	viaURL, _ := url.Parse(fwd.URL)
	f, err := keyproxy.New(keyproxy.Config{Endpoints: []keyproxy.Endpoint{{Name: "llm", URL: upURL, KeyFile: key}}, Via: viaURL})
	if err != nil {
		t.Fatal(err)
	}
	kp := httptest.NewServer(f)
	defer kp.Close()

	p := providers.Provider{Name: "llm", CLI: providers.Codex, Endpoint: &providers.Endpoint{URL: upURL, KeyFile: key}}
	wd := filepath.Join(dir, "projects")
	os.Mkdir(wd, 0o755)
	// The test reaches the forwarder on its own address, not lea-proxy:4400: the user's -c
	// comes after lea's and wins.
	argv, err := p.Argv("dev-1", wd, "qwen3-coder", "", []string{"-c", `model_providers.lea-llm.base_url="` + kp.URL + `/llm"`}, "say hi")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("python3", append([]string{"-I", "-c", ptyRun, "20", codex}, argv...)...)
	cmd.Dir = wd
	home := filepath.Join(dir, "home")
	os.Mkdir(home, 0o700)
	// No lea bus here: the MCP server and notify of the agent reach nothing.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "CODEX_HOME=" + filepath.Join(home, ".codex"),
		"TERM=xterm-256color", "LEA_BUS_SOCK=" + filepath.Join(dir, "no-bus.sock"), "NO_PROXY=*"}
	os.Mkdir(filepath.Join(home, ".codex"), 0o700)
	done := make(chan []byte, 1)
	go func() { out, _ := cmd.CombinedOutput(); done <- out }()
	deadline := time.After(25 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n > 0 {
			break
		}
		select {
		case out := <-done:
			t.Fatalf("codex ended without reaching the endpoint:\n%s", out)
		case <-deadline:
			t.Fatal("codex did not reach the endpoint in 25 s")
		case <-time.After(200 * time.Millisecond):
		}
	}
	if cmd.Process != nil {
		cmd.Process.Kill()
	}
	mu.Lock()
	defer mu.Unlock()
	g := got[0]
	if g.path != "/v1/responses" || g.auth != "Bearer sk-e2e-secret" || g.model != "qwen3-coder" || g.agentKey != "" {
		t.Fatalf("the endpoint saw %+v", g)
	}
	if len(via) == 0 || !strings.HasPrefix(up.URL, "http://"+via[0]) {
		t.Fatalf("the request did not go through the forward proxy: %v", via)
	}
}
