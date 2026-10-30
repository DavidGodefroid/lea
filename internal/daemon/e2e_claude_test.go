package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
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

// claudeE2E is the real Claude Code (LEA_E2E_CLAUDE) with lea's hooks (image/claude-settings.json,
// this build of lea) on a fake Messages API, which answers 429 rate_limit_error while limited,
// then a short answer; and lea's daemon with the agent's bus.
type claudeE2E struct {
	t       *testing.T
	st      *store.Store
	d       *Daemon
	api     string
	mu      sync.Mutex
	limited bool
	bodies  []string
	claude  string
	wd      string
	env     []string
	sf      string
	out     strings.Builder
	// toolUse, when set, is the tool the fake model calls first: a request that offers it and
	// carries no tool result yet is answered with a call to it.
	toolUse string
	// answer, when set, decides instead: the tool to call (and its JSON input) for a request
	// body, "" for the text answer.
	answer func(body string) (tool, input string)
	// dir, runDir and stop are the daemon's: restartDaemon ends it and starts another on the
	// same store and run directory.
	dir, runDir, bus string
	// ctx is the running daemon's lifetime, which stop ends.
	ctx  context.Context
	stop context.CancelFunc
}

func newClaudeE2E(t *testing.T) *claudeE2E {
	claude := os.Getenv("LEA_E2E_CLAUDE")
	if claude == "" {
		t.Skip("LEA_E2E_CLAUDE is not set")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3 for the pseudo-terminal")
	}
	e := &claudeE2E{t: t, limited: true, claude: claude}
	dir := shortDir(t)
	lea := filepath.Join(dir, "lea")
	if out, err := exec.Command("go", "build", "-o", lea, "../../cmd/lea").CombinedOutput(); err != nil {
		t.Fatalf("build lea: %v\n%s", err, out)
	}

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		e.mu.Lock()
		lim := e.limited
		if strings.HasPrefix(r.URL.Path, "/v1/messages") && !strings.Contains(r.URL.Path, "count_tokens") {
			e.bodies = append(e.bodies, fmt.Sprintf("%v %s", lim, b))
		}
		e.mu.Unlock()
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
		e.mu.Lock()
		tool, input := e.toolUse, "{}"
		answer := e.answer
		e.mu.Unlock()
		if answer != nil {
			tool, input = answer(string(b))
		} else if strings.Contains(string(b), `"tool_result"`) {
			tool = ""
		}
		if tool != "" && strings.Contains(string(b), `"`+tool+`"`) {
			in, _ := json.Marshal(input)
			for _, ev := range []string{
				`{"type":"message_start","message":{"id":"msg_t","type":"message","role":"assistant","model":"claude-e2e","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_e2e1","name":"` + tool + `","input":{}}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":` + string(in) + `}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":2}}`,
				`{"type":"message_stop"}`,
			} {
				var ev2 struct{ Type string }
				_ = json.Unmarshal([]byte(ev), &ev2)
				io.WriteString(w, "event: "+ev2.Type+"\ndata: "+ev+"\n\n")
			}
			return
		}
		for _, ev := range []string{
			`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-e2e","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"resumed ok"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`,
			`{"type":"message_stop"}`,
		} {
			var ev2 struct{ Type string }
			_ = json.Unmarshal([]byte(ev), &ev2)
			io.WriteString(w, "event: "+ev2.Type+"\ndata: "+ev+"\n\n")
		}
	}))
	t.Cleanup(api.Close)

	st, err := store.Open(filepath.Join(dir, "lea.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e.st = st
	e.dir, e.runDir = dir, filepath.Join(dir, "run")
	bus := e.startDaemon()

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
	e.sf = filepath.Join(dir, "settings.json")
	os.WriteFile(e.sf, sb, 0o600)
	e.wd = filepath.Join(dir, "wd")
	cfg := filepath.Join(dir, "cfg")
	os.Mkdir(e.wd, 0o755)
	os.Mkdir(cfg, 0o700)
	const key = "sk-ant-api03-e2e-not-a-real-key-0000"
	seed, _ := json.Marshal(map[string]any{"hasCompletedOnboarding": true, "theme": "dark",
		"customApiKeyResponses": map[string]any{"approved": []string{key[len(key)-20:]}, "rejected": []string{}},
		"projects":              map[string]any{e.wd: map[string]any{"hasTrustDialogAccepted": true, "hasCompletedProjectOnboarding": true}}})
	os.WriteFile(filepath.Join(cfg, ".claude.json"), seed, 0o600)
	e.env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "CLAUDE_CONFIG_DIR=" + cfg, "TERM=xterm-256color",
		"ANTHROPIC_BASE_URL=" + api.URL, "ANTHROPIC_API_KEY=" + key, "DISABLE_TELEMETRY=1", "DISABLE_AUTOUPDATER=1",
		"LEA_BUS_SOCK=" + bus, "LEA_AGENT=dev"}
	return e
}

// startDaemon starts lea's daemon on the store, serving its API and the container lea-a's bus;
// the bus socket's path.
func (e *claudeE2E) startDaemon() string {
	e.t.Helper()
	d := New(e.st, e.runDir, func() int64 { return time.Now().Unix() })
	ctx, cancel := context.WithCancel(context.Background())
	e.t.Cleanup(cancel)
	l, err := Listen(filepath.Join(e.dir, "d.sock"))
	if err != nil {
		e.t.Fatal(err)
	}
	go d.Serve(ctx, l, Origin{})
	bus, err := d.AttachContainer(ctx, "lea-a")
	if err != nil {
		e.t.Fatal(err)
	}
	e.d, e.api, e.ctx, e.stop, e.bus = d, filepath.Join(e.dir, "d.sock"), ctx, cancel, bus
	return bus
}

// restartDaemon ends the daemon (its timers and queues go with it) and starts another on the
// same store, as a lea daemon that stops and starts again; down runs in between.
func (e *claudeE2E) restartDaemon(down func()) {
	e.t.Helper()
	e.stop()
	for i := 0; ; i++ {
		c, err := net.DialTimeout("unix", e.api, 100*time.Millisecond)
		if err != nil {
			break
		}
		c.Close()
		if i == 50 {
			e.t.Fatal("the daemon did not stop")
		}
		time.Sleep(100 * time.Millisecond)
	}
	for i := 0; ; i++ { // the bus listener closes with it
		c, err := net.DialTimeout("unix", e.bus, 100*time.Millisecond)
		if err != nil {
			break
		}
		c.Close()
		if i == 50 {
			e.t.Fatal("the bus did not close")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if down != nil {
		down()
	}
	e.startDaemon()
}

// run starts claude in a pseudo-terminal with args; kill ends it.
func (e *claudeE2E) run(args ...string) (kill func()) {
	argv := append([]string{"-I", "-c", claudePty, "90", e.claude, "--settings", e.sf}, args...)
	cmd := exec.Command("python3", argv...)
	cmd.Dir = e.wd
	cmd.Env = e.env
	cmd.Stdout, cmd.Stderr = &e.out, &e.out
	if err := cmd.Start(); err != nil {
		e.t.Fatal(err)
	}
	kill = func() { cmd.Process.Kill(); cmd.Wait() }
	e.t.Cleanup(kill)
	return kill
}

func (e *claudeE2E) setLimited(on bool) {
	e.mu.Lock()
	e.limited = on
	e.mu.Unlock()
}

func (e *claudeE2E) until(what string, secs int, ok func(store.Agent) bool) store.Agent {
	e.t.Helper()
	for end := time.Now().Add(time.Duration(secs) * time.Second); time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
		if a, err := e.st.AgentByKey("lea-a/dev"); err == nil && ok(a) {
			return a
		}
	}
	a, _ := e.st.AgentByKey("lea-a/dev")
	e.mu.Lock()
	defer e.mu.Unlock()
	var evs []string
	for _, k := range []string{"SessionStart", "SessionEnd", "StopFailure", "Stop", "UserPromptSubmit", "hit", "resume"} {
		ps, _ := e.st.EventPayloads("lea-a/dev", k)
		evs = append(evs, fmt.Sprintf("%s=%q", k, ps))
	}
	e.t.Fatalf("%s: not in %d s; agent=%+v requests=%d events=%v\n%s", what, secs, a, len(e.bodies), evs, e.out.String())
	return a
}

// resumedWithLeasText checks the first request after the limit lifted carries lea's fixed text.
func (e *claudeE2E) resumedWithLeasText() {
	e.t.Helper()
	if ps, _ := e.st.EventPayloads("lea-a/dev", "resume"); len(ps) == 0 {
		e.t.Fatal("no resume event")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	var resumed string
	for _, b := range e.bodies {
		if strings.HasPrefix(b, "false ") {
			resumed = b
			break
		}
	}
	if !strings.Contains(resumed, hook.ResumeText) {
		e.t.Fatalf("the resumed request lacks lea's text:\n%.2000s", resumed)
	}
}

// The real Claude Code, with lea's hooks, hits a usage limit (the API answers 429
// rate_limit_error) and stops: lea marks the agent limited. Once the limit lifts and the resume
// time comes, lea's LimitWait job wakes the model with lea's fixed text, typing nothing into the
// terminal; the turn succeeds and the limit is cleared. Set LEA_E2E_CLAUDE to a claude binary to
// run it (the image has one: /usr/local/bin/claude).
func TestClaudeResumesAfterAUsageLimit(t *testing.T) {
	defer func(m, b, j int64) { minResumeWait, retryBase, resumeJitter = m, b, j }(minResumeWait, retryBase, resumeJitter)
	minResumeWait, retryBase, resumeJitter = 1, 3, 0
	e := newClaudeE2E(t)
	e.run("say hi")
	e.until("the usage limit", 45, func(a store.Agent) bool { return a.LimitUntil > 0 })
	// The daemon records the limit (StopFailure) just before it sets the agent idle.
	e.until("a limited agent idle", 10, func(a store.Agent) bool { return a.State == "idle" })
	e.setLimited(false)
	e.until("the resume", 45, func(a store.Agent) bool { return a.LimitUntil == 0 && a.LimitTries == 0 && a.State == "idle" })
	e.resumedWithLeasText()
}

// A container restart while a resume is pending (#83): Claude ends with the container, lea marks
// the agent stopped then starts it again with --resume, as lea stop and the next container do.
// The resumed session keeps the limit, its LimitResume job (SessionStart) waits for the recorded
// time, and wakes the model with lea's fixed text.
func TestClaudeResumesAfterAContainerRestart(t *testing.T) {
	defer func(m, b, j int64) { minResumeWait, retryBase, resumeJitter = m, b, j }(minResumeWait, retryBase, resumeJitter)
	minResumeWait, retryBase, resumeJitter = 1, 20, 0
	e := newClaudeE2E(t)
	kill := e.run("say hi")
	a := e.until("the usage limit", 45, func(a store.Agent) bool { return a.LimitUntil > 0 && len(a.SessionID) == 36 })
	// lea stop: the agent is marked stopped, then its container (and Claude, which may still run
	// its SessionEnd hook) ends; the next container marks it unknown and starts it again.
	if err := e.st.SetState(a.Key, "stopped", "", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	kill()
	time.Sleep(3 * time.Second)
	if err := e.st.SetState(a.Key, "unknown", "", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if b := e.until("the limit kept", 1, func(store.Agent) bool { return true }); b.LimitUntil != a.LimitUntil {
		t.Fatalf("the restart lost the limit: %+v", b)
	}
	e.setLimited(false)
	e.run("--resume", a.SessionID)
	e.until("the resumed session", 45, func(b store.Agent) bool { return b.State == "idle" && b.LimitUntil > 0 })
	e.until("the resume", 60, func(b store.Agent) bool { return b.LimitUntil == 0 && b.LimitTries == 0 && b.State == "idle" })
	e.resumedWithLeasText()
}

// e2eAgents stands for the agent manager in the suspension e2e (#161): SoftStop ends the real
// Claude, ResumeSuspended starts it again on its session with the prompt, as the manager does in
// the agent's pane.
type e2eAgents struct {
	fakeAgents
	e    *claudeE2E
	kill func()
	// args are claude's arguments after the resume prompt (the Lock's MCP server).
	args []string
}

func (f *e2eAgents) SoftStop(ctx context.Context, key string) error {
	if a, err := f.e.st.AgentByKey(key); err != nil || a.State != store.StateSuspendedWarm {
		return fmt.Errorf("%s: not warm", key)
	}
	if err := f.e.st.SetState(key, store.StateSuspendedCold, "", time.Now().Unix()); err != nil {
		return err
	}
	f.kill()
	return nil
}

func (f *e2eAgents) EndSuspended(ctx context.Context, key string) (bool, error) {
	return f.e.st.EndSuspended(key, time.Now().Unix())
}
func (f *e2eAgents) ResumeSuspended(ctx context.Context, key, prompt string) error {
	a, err := f.e.st.AgentByKey(key)
	if err != nil || a.State != store.StateSuspendedCold {
		return fmt.Errorf("%s: not cold", key)
	}
	if err := f.e.st.SetState(key, "unknown", "", time.Now().Unix()); err != nil {
		return err
	}
	f.kill = f.e.run(append([]string{"--resume", a.SessionID, prompt}, f.args...)...)
	return nil
}

// A suspended agent (#161) whose keepalive ends is stopped, its session kept; a message to it
// then resumes the real Claude on that session with lea's unread notice as its next turn, and the
// turn runs to its end: a suspended lead still hears its children.
func TestClaudeSuspendedAgentIsResumedByAMessage(t *testing.T) {
	e := newClaudeE2E(t)
	e.setLimited(false)
	fa := &e2eAgents{e: e}
	fa.kill = e.run("say hi")
	e.d.SetAgents(fa)
	a := e.until("the first turn", 45, func(a store.Agent) bool {
		ps, _ := e.st.EventPayloads(a.Key, "Stop")
		return a.State == "idle" && len(a.SessionID) == 36 && len(ps) > 0
	})
	e.d.SetKeepalive(2 * time.Second)
	if err := e.d.suspend(a.Key); err != nil {
		t.Fatal(err)
	}
	e.until("the soft stop", 30, func(b store.Agent) bool { return b.State == store.StateSuspendedCold })
	time.Sleep(2 * time.Second) // the stopped Claude's SessionEnd leaves the agent cold
	if b := e.until("still cold", 1, func(store.Agent) bool { return true }); b.State != store.StateSuspendedCold || b.SessionID != a.SessionID {
		t.Fatalf("after the soft stop: %+v", b)
	}
	e.mu.Lock()
	before := len(e.bodies)
	e.mu.Unlock()
	if err := call(t, e.api, "", "msg_send", map[string]any{"to": "dev", "body": "child done", "kind": "result"}, nil); err != nil {
		t.Fatal(err)
	}
	e.until("the resumed turn", 60, func(b store.Agent) bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		return b.State == "idle" && len(e.bodies) > before
	})
	e.mu.Lock()
	defer e.mu.Unlock()
	if !strings.Contains(strings.Join(e.bodies[before:], "\n"), "unread message(s) from user") {
		t.Fatalf("the resumed turn lacks the unread notice:\n%.2000s", strings.Join(e.bodies[before:], "\n"))
	}
}

// A real Claude suspended on a ticket and stopped after its keepalive resumes when the ticket is
// approved, with the decision in the first turn of its resumed session (#163).
func TestClaudeSuspendedAgentIsResumedByADecision(t *testing.T) {
	e := newClaudeE2E(t)
	e.setLimited(false)
	fa := &e2eAgents{e: e}
	fa.kill = e.run("say hi")
	e.d.SetAgents(fa)
	a := e.until("the first turn", 45, func(a store.Agent) bool {
		ps, _ := e.st.EventPayloads(a.Key, "Stop")
		return a.State == "idle" && len(a.SessionID) == 36 && len(ps) > 0
	})
	if err := e.st.AddHubWait(a.Key, testTicket, "locksql", "crm-prod", e.d.now()); err != nil {
		t.Fatal(err)
	}
	e.d.SetKeepalive(2 * time.Second)
	if err := e.d.suspend(a.Key); err != nil {
		t.Fatal(err)
	}
	e.until("the soft stop", 30, func(b store.Agent) bool { return b.State == store.StateSuspendedCold })
	time.Sleep(2 * time.Second) // the stopped Claude's SessionEnd leaves the agent cold
	e.mu.Lock()
	before := len(e.bodies)
	e.mu.Unlock()
	if _, err := e.st.DecideHubWaits(testTicket, "crm-prod", store.DecisionApproved, "", e.d.now()); err != nil {
		t.Fatal(err)
	}
	e.d.wakeSuspended(a.Key)
	e.until("the resumed turn", 60, func(b store.Agent) bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		return b.State == "idle" && len(e.bodies) > before
	})
	e.mu.Lock()
	defer e.mu.Unlock()
	if !strings.Contains(strings.Join(e.bodies[before:], "\n"), "approved: call its run again with ticket="+testTicket) {
		t.Fatalf("the resumed turn lacks the decision:\n%.2000s", strings.Join(e.bodies[before:], "\n"))
	}
}

// lockStub is a Lock's MCP server (stdio) whose run answers pending, as lockkit's mcpkit.PendingOn
// does: the fixed text and the structured content.
const lockStub = `
import json, sys
for line in sys.stdin:
    m = json.loads(line)
    mid, meth = m.get("id"), m.get("method")
    def send(r):
        sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": mid, "result": r}) + "\n"); sys.stdout.flush()
    if meth == "initialize":
        send({"protocolVersion": m["params"].get("protocolVersion", "2025-06-18"), "capabilities": {"tools": {}},
              "serverInfo": {"name": "lockstub", "version": "0"}})
    elif meth == "tools/list":
        send({"tools": [{"name": "run", "description": "Run a plan", "inputSchema": {"type": "object", "properties": {}}}]})
    elif meth == "tools/call":
        sc = {"status": "pending", "ticket": sys.argv[1], "lock": "locksql", "instance": "crm-prod"}
        send({"content": [{"type": "text", "text": "Pending human approval: ticket " + sys.argv[1] +
              " on locksql. Do not retry. End your turn; you will be told when it is decided."}], "structuredContent": sc})
    elif mid is not None:
        send({})
`

// A real Claude whose Lock call answers pending is suspended by lea's hook: the wait is recorded,
// the agent is suspended_warm, and the turn ends on the tool result, the model not asked again
// (#160).
func TestClaudePendingLockCallSuspendsTheAgent(t *testing.T) {
	e := newClaudeE2E(t)
	e.setLimited(false)
	if _, err := e.st.Register(store.NewAgent{Container: "lea-a", Name: "dev", Profile: "claude", Workdir: e.wd, Role: "dev"}, store.Caps{}, 1); err != nil {
		t.Fatal(err)
	}
	e.d.SetKeepalive(time.Hour)
	// The hub's facade: lea suspends an agent only on a hub it can read (LEA_HUB=1's default).
	e.d.SetHubFacade(SocketFacade{Path: serveHub(t, &fakeHub{})})
	stub := filepath.Join(t.TempDir(), "lockstub.py")
	os.WriteFile(stub, []byte(lockStub), 0o600)
	mcp, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{
		"lockstub": map[string]any{"type": "stdio", "command": "python3", "args": []string{"-I", stub, testTicket}}}})
	e.mu.Lock()
	e.toolUse = "mcp__lockstub__run"
	e.mu.Unlock()
	e.run("run the plan", "--mcp-config", string(mcp), "--strict-mcp-config", "--allowedTools=mcp__lockstub__run")
	a := e.until("the suspension", 60, func(a store.Agent) bool { return a.State == store.StateSuspendedWarm })
	ws, err := e.st.PendingHubWaits(a.Key)
	if err != nil || len(ws) != 1 || ws[0].Ticket != testTicket || ws[0].Lock != "locksql" || ws[0].Instance != "crm-prod" {
		t.Fatalf("waits %+v %v", ws, err)
	}
	time.Sleep(3 * time.Second)
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, b := range e.bodies {
		if strings.Contains(b, `"tool_result"`) {
			t.Fatalf("the model was asked again after the pending result:\n%.2000s", b)
		}
	}
	if b := e.until("still suspended", 1, func(store.Agent) bool { return true }); b.State != store.StateSuspendedWarm {
		t.Fatalf("state %s after the turn", b.State)
	}
}
