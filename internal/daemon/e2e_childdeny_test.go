package daemon

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/store"
)

// childMCPStub is an MCP server (stdio) with one tool, named after argv[1].
const childMCPStub = `
import json, sys
for line in sys.stdin:
    m = json.loads(line)
    mid, meth = m.get("id"), m.get("method")
    def send(r):
        sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": mid, "result": r}) + "\n"); sys.stdout.flush()
    if meth == "initialize":
        send({"protocolVersion": m["params"].get("protocolVersion", "2025-06-18"), "capabilities": {"tools": {}},
              "serverInfo": {"name": "stub", "version": "0"}})
    elif meth == "tools/list":
        send({"tools": [{"name": sys.argv[1], "description": "probe", "inputSchema": {"type": "object"}}]})
    elif mid is not None:
        send({})
`

// Lean children (#423), end to end with the real Claude Code started by the image's lea-session:
// with LEA_CHILD_PLUGINS=foo and LEA_CHILD_MCP=gmail in a child's environment (what the daemon
// puts on a lead's child's docker exec), the child's model sees neither foo's skill nor gmail's
// tool, and still sees lea's MCP server and a Lock's client; the lead, started without them,
// sees all. Set LEA_E2E_CLAUDE to a claude binary to run it.
func TestClaudeChildDenyListLeavesOutHostPluginsAndMCPServers(t *testing.T) {
	e := newClaudeE2E(t)
	e.setLimited(false)
	lead, err := e.st.Register(store.NewAgent{Container: "lea-a", Name: "dev", Profile: "claude", Workdir: e.wd, Role: "lead"}, store.Caps{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Register(store.NewAgent{Container: "lea-a", Name: "kid", Profile: "claude", Workdir: e.wd, Role: "dev", Parent: lead.Key}, store.Caps{}, 1); err != nil {
		t.Fatal(err)
	}
	var cfg string
	for _, v := range e.env {
		if d, ok := strings.CutPrefix(v, "CLAUDE_CONFIG_DIR="); ok {
			cfg = d
		}
	}

	// The host's plugin foo, installed from a local marketplace (Claude Code itself writes
	// enabledPlugins and installed_plugins.json).
	mkt := filepath.Join(t.TempDir(), "mkt")
	files := map[string]string{
		".claude-plugin/marketplace.json": `{"name":"mkt","owner":{"name":"t"},"plugins":[{"name":"foo","source":"./foo","description":"probe"}]}`,
		"foo/.claude-plugin/plugin.json":  `{"name":"foo","version":"1.0.0","description":"probe"}`,
		"foo/skills/fooskill/SKILL.md":    "---\nname: fooskill\ndescription: FOOSKILLPROBE a probe skill\n---\nbody\n",
	}
	for p, s := range files {
		f := filepath.Join(mkt, p)
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"plugin", "marketplace", "add", mkt}, {"plugin", "install", "foo@mkt"}} {
		c := exec.Command(e.claude, args...)
		c.Dir, c.Env = e.wd, e.env
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("claude %v: %v\n%s", args, err, out)
		}
	}
	// The host's MCP servers: gmail (denied), locksql (a Lock's client, always kept), browser (kept).
	stub := filepath.Join(t.TempDir(), "stub.py")
	if err := os.WriteFile(stub, []byte(childMCPStub), 0o600); err != nil {
		t.Fatal(err)
	}
	cj := filepath.Join(cfg, ".claude.json")
	b, err := os.ReadFile(cj)
	if err != nil {
		t.Fatal(err)
	}
	var seed map[string]any
	if err := json.Unmarshal(b, &seed); err != nil {
		t.Fatal(err)
	}
	servers := map[string]any{}
	for name, tool := range map[string]string{"gmail": "GMAILPROBE", "locksql": "LOCKSQLPROBE", "browser": "BROWSERPROBE"} {
		servers[name] = map[string]any{"type": "stdio", "command": "python3", "args": []string{"-I", stub, tool}}
	}
	seed["mcpServers"] = servers
	b, _ = json.Marshal(seed)
	if err := os.WriteFile(cj, b, 0o600); err != nil {
		t.Fatal(err)
	}

	// sees runs lea-session as lea-agent does (agent, extra environment) and gives the probes the
	// model's requests offer. Claude Code connects the MCP servers while the session starts, and
	// its first request may go out before they answered: until a request offers every probe of
	// ready, the fake model calls Read (a missing file), which brings another request; once one
	// does, it calls Read once more a second later, so a slower server shows too, then answers.
	session, err := filepath.Abs("../../image/lea-session")
	if err != nil {
		t.Fatal(err)
	}
	isTurn := func(body string) bool { return strings.Contains(body, `"hi"`) && strings.Contains(body, `"Bash"`) }
	probes := []string{"FOOSKILLPROBE", "GMAILPROBE", "LOCKSQLPROBE", "BROWSERPROBE"}
	read := `{"file_path":"` + filepath.Join(e.wd, "no-such-file") + `"}`
	sees := func(agent string, ready []string, env ...string) map[string]bool {
		t.Helper()
		var settled, answered atomic.Bool
		e.mu.Lock()
		e.bodies = nil
		e.answer = func(body string) (string, string) {
			if !isTurn(body) {
				return "", ""
			}
			for _, p := range ready {
				if !strings.Contains(body, p) {
					time.Sleep(300 * time.Millisecond)
					return "Read", read
				}
			}
			if settled.CompareAndSwap(false, true) {
				time.Sleep(time.Second)
				return "Read", read
			}
			answered.Store(true)
			return "", ""
		}
		e.mu.Unlock()
		defer func() { e.mu.Lock(); e.answer = nil; e.mu.Unlock() }()
		c := exec.Command("python3", "-I", "-c", claudePty, "90", "sh", session, "hi")
		c.Dir = e.wd
		c.Env = append(append(append([]string{}, e.env...), "LEA_AGENT="+agent, "LEA_BIN="+e.lea, "LEA_CLAUDE_SETTINGS="+e.sf,
			"LEA_CLAUDE_PLUGIN="+filepath.Join(e.wd, "no-plugin"), "ENABLE_TOOL_SEARCH=false"), env...)
		var out strings.Builder
		c.Stdout, c.Stderr = &out, &out
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { syscall.Kill(-c.Process.Pid, syscall.SIGKILL); c.Wait() }()
		for end := time.Now().Add(90 * time.Second); ; time.Sleep(200 * time.Millisecond) {
			e.mu.Lock()
			got := map[string]bool{}
			turns := 0
			for _, body := range e.bodies {
				if !isTurn(body) {
					continue // Claude's own side requests
				}
				turns++
				for _, p := range probes {
					got[p] = got[p] || strings.Contains(body, p)
				}
			}
			e.mu.Unlock()
			if answered.Load() {
				return got
			}
			if time.Now().After(end) {
				t.Fatalf("lea-session for %s: no settled turn in 90 s (%d requests, probes %v)\n%s", agent, turns, got, out.String())
			}
		}
	}
	if got := sees("dev", []string{"BROWSERPROBE", "LOCKSQLPROBE", "GMAILPROBE"}); !got["FOOSKILLPROBE"] || !got["GMAILPROBE"] || !got["LOCKSQLPROBE"] || !got["BROWSERPROBE"] {
		t.Fatalf("the lead does not see the host's plugin and servers: %v", got)
	}
	got := sees("kid", []string{"BROWSERPROBE", "LOCKSQLPROBE"}, "LEA_CHILD_PLUGINS=foo", "LEA_CHILD_MCP=gmail,locksql")
	if got["FOOSKILLPROBE"] || got["GMAILPROBE"] {
		t.Fatalf("the child sees a denied plugin or server: %v", got)
	}
	if !got["LOCKSQLPROBE"] || !got["BROWSERPROBE"] {
		t.Fatalf("the child lost a server it keeps: %v", got)
	}
	// The host's copy is untouched: foo stays enabled for every other agent.
	if s, _ := os.ReadFile(filepath.Join(cfg, "settings.json")); !strings.Contains(string(s), `"foo@mkt": true`) {
		t.Fatalf("the host's settings changed: %s", s)
	}
}
