package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/store"
)

// lockStubRun is a Lock's MCP server (stdio) whose run answers pending without a ticket (as
// lockkit's mcpkit.PendingOn does) and runs once called again with the approved ticket: it
// records the run in its log file (argv[2]).
const lockStubRun = `
import json, sys
ticket, log = sys.argv[1], sys.argv[2]
for line in sys.stdin:
    m = json.loads(line)
    mid, meth = m.get("id"), m.get("method")
    def send(r):
        sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": mid, "result": r}) + "\n"); sys.stdout.flush()
    if meth == "initialize":
        send({"protocolVersion": m["params"].get("protocolVersion", "2025-06-18"), "capabilities": {"tools": {}},
              "serverInfo": {"name": "lockstub", "version": "0"}})
    elif meth == "tools/list":
        send({"tools": [{"name": "run", "description": "Run a plan", "inputSchema": {"type": "object",
              "properties": {"ticket": {"type": "string"}}}}]})
    elif meth == "tools/call":
        if m["params"].get("arguments", {}).get("ticket") == ticket:
            open(log, "a").write("executed " + ticket + "\n")
            send({"content": [{"type": "text", "text": "Executed: 1 row."}], "structuredContent": {"status": "ok", "rows": 1}})
        else:
            sc = {"status": "pending", "ticket": ticket, "lock": "locksql", "instance": "crm-prod"}
            send({"content": [{"type": "text", "text": "Pending human approval: ticket " + ticket +
                  " on locksql. Do not retry. End your turn; you will be told when it is decided."}], "structuredContent": sc})
    elif mid is not None:
        send({})
`

// Phase 3 of the Lock Family hub, end to end with the real Claude Code (#161, #162, #163, #164):
// a lead's Lock call answers pending, lea's hook suspends it (suspended_warm); its keepalive
// elapses and lea stops that agent alone, keeping its session (suspended_cold), its child left
// as it is; the daemon stops, the ticket is approved on the hub meanwhile, and the restarted
// daemon reads the decision from its cursor and resumes the lead's session with it; the model
// calls the run again with the ticket and the Lock executes. Suspended once more, the lead is
// woken by its child's message. Set LEA_E2E_CLAUDE to a claude binary to run it.
func TestClaudePhase3SuspendResumeEndToEnd(t *testing.T) {
	e := newClaudeE2E(t)
	e.setLimited(false)
	lead, err := e.st.Register(store.NewAgent{Container: "lea-a", Name: "dev", Profile: "claude", Workdir: e.wd, Role: "lead"}, store.Caps{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	kid, err := e.st.Register(store.NewAgent{Container: "lea-a", Name: "kid", Profile: "claude", Workdir: e.wd, Role: "dev", Parent: lead.Key}, store.Caps{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(t.TempDir(), "lockstub.py")
	runs := filepath.Join(t.TempDir(), "runs.log")
	if err := os.WriteFile(stub, []byte(lockStubRun), 0o600); err != nil {
		t.Fatal(err)
	}
	mcp, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{
		"lockstub": map[string]any{"type": "stdio", "command": "python3", "args": []string{"-I", stub, testTicket, runs}}}})
	args := []string{"--mcp-config", string(mcp), "--strict-mcp-config", "--allowedTools=mcp__lockstub__run"}
	const tool = "mcp__lockstub__run"
	approved := "approved: call its run again with ticket=" + testTicket
	e.mu.Lock()
	// The fake model calls the run once, then, told of the approval, calls it again with the
	// ticket until the Lock ran it; else it answers with text.
	var called atomic.Bool
	e.answer = func(b string) (string, string) {
		if !strings.Contains(b, `"`+tool+`"`) {
			return "", "" // a request that does not offer the Lock (Claude's own side requests)
		}
		ran, _ := os.ReadFile(runs)
		switch {
		case called.CompareAndSwap(false, true):
			return tool, "{}"
		case strings.Contains(b, approved) && len(ran) == 0:
			return tool, `{"ticket":"` + testTicket + `"}`
		}
		return "", ""
	}
	e.mu.Unlock()
	fa := &e2eAgents{e: e, args: args}
	e.d.SetAgents(fa)
	e.d.SetKeepalive(2 * time.Second)

	// Suspended on the ticket by lea's hook.
	fa.kill = e.run(append([]string{"run the plan"}, args...)...)
	a := e.until("the suspension", 60, func(a store.Agent) bool { return a.State == store.StateSuspendedWarm && len(a.SessionID) == 36 })
	if ws, err := e.st.PendingHubWaits(a.Key); err != nil || len(ws) != 1 || ws[0].Ticket != testTicket {
		t.Fatalf("waits %+v %v", ws, err)
	}
	if err := e.st.SetState(kid.Key, "idle", "", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	ends := e.events(a.Key, "SessionEnd")

	// The keepalive elapses: that agent alone is stopped, its session kept.
	e.until("the soft stop", 30, func(b store.Agent) bool { return b.State == store.StateSuspendedCold })
	e.waitEvents(a.Key, "SessionEnd", ends+1) // the stopped Claude's SessionEnd leaves the agent cold
	if b := e.until("still cold", 1, func(store.Agent) bool { return true }); b.State != store.StateSuspendedCold || b.SessionID != a.SessionID {
		t.Fatalf("after the soft stop: %+v", b)
	}
	if k, _ := e.st.AgentByKey(kid.Key); k.State != "idle" {
		t.Fatalf("the child went from idle to %s with its lead's soft stop", k.State)
	}

	// The daemon stops; the hub approves meanwhile; the restarted daemon reads the decision from
	// its cursor and resumes the session with it, and the Lock runs.
	// An earlier decision (no agent waits on it) moves lea's cursor to 1 before the stop.
	const earlier = "01JB8Z5Q3W4X7Y9ABCDEFGHJKP"
	hub := &fakeHub{}
	sock := serveHub(t, hub)
	hub.decide(earlier, "crm-prod", "approved", "")
	e.d.SetHubFacade(SocketFacade{Path: sock})
	if err := e.d.syncHub(t.Context()); err != nil {
		t.Fatal(err)
	}
	if c, _ := e.st.HubCursor(); c != "1" {
		t.Fatalf("cursor %q before the stop", c)
	}
	e.mu.Lock()
	before := len(e.bodies)
	e.mu.Unlock()
	hub.mu.Lock()
	callsBefore := len(hub.calls)
	hub.mu.Unlock()
	e.restartDaemon(func() { hub.decide(testTicket, "crm-prod", "approved", "") })
	e.d.SetAgents(fa)
	e.d.SetKeepalive(2 * time.Second)
	e.d.SetHubFacade(SocketFacade{Path: sock})
	go e.d.RunHubSync(t.Context())
	for i := 0; ; i++ {
		if b, _ := os.ReadFile(runs); strings.Contains(string(b), "executed "+testTicket) {
			break
		}
		if i == 450 {
			e.until("the run of the approved ticket", 0, func(store.Agent) bool { return false })
		}
		time.Sleep(200 * time.Millisecond)
	}
	e.until("the resumed turn's end", 60, func(b store.Agent) bool { return b.State == "idle" && b.SessionID == a.SessionID })
	// The restarted daemon read from its cursor, not from the start.
	hub.mu.Lock()
	calls := strings.Join(hub.calls[callsBefore:], "\n")
	hub.mu.Unlock()
	if !strings.Contains(calls, "GET /v1/decided?since=1") || strings.Contains(calls, "GET /v1/decided?since=0") {
		t.Fatalf("the hub's decisions were not read from the cursor:\n%s", calls)
	}
	if c, _ := e.st.HubCursor(); c != "2" {
		t.Fatalf("cursor %q after the decision", c)
	}
	if ws, err := e.st.UndeliveredHubWaits(a.Key); err != nil || len(ws) != 0 {
		t.Fatalf("undelivered %+v %v", ws, err)
	}
	e.mu.Lock()
	sent := strings.Join(e.bodies[before:], "\n")
	e.mu.Unlock()
	if !strings.Contains(sent, approved) || !strings.Contains(sent, "run the plan") {
		t.Fatalf("the resumed session lacks the decision or its history:\n%.3000s", sent)
	}
	// Told once, by the resume prompt: no copy in its inbox, read or not, and no inbox notice.
	ms, err := e.st.Thread(a.Key, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if strings.Contains(m.Body, "approved") {
			t.Fatalf("the decision also went to the inbox: %+v", m)
		}
	}
	if strings.Contains(sent, "unread message(s)") {
		t.Fatalf("the resumed turn was also told of an unread message:\n%.3000s", sent)
	}

	// Suspended again and stopped, the lead is woken by its child's message.
	if err := e.d.suspend(a.Key); err != nil {
		t.Fatal(err)
	}
	ends = e.events(a.Key, "SessionEnd")
	e.until("the second soft stop", 30, func(b store.Agent) bool { return b.State == store.StateSuspendedCold })
	e.waitEvents(a.Key, "SessionEnd", ends+1)
	e.mu.Lock()
	before = len(e.bodies)
	e.mu.Unlock()
	if err := call(t, e.bus, "kid", "msg_send", map[string]any{"to": "dev", "body": "child done", "kind": "result"}, nil); err != nil {
		t.Fatal(err)
	}
	e.until("the lead's turn on its child's message", 60, func(b store.Agent) bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		return b.State == "idle" && b.SessionID == a.SessionID && strings.Contains(strings.Join(e.bodies[before:], "\n"), "unread message(s)")
	})
	e.mu.Lock()
	defer e.mu.Unlock()
	if sent := strings.Join(e.bodies[before:], "\n"); !regexp.MustCompile(`unread message\(s\) from [^.]*\bkid\b`).MatchString(sent) || !strings.Contains(sent, "run the plan") {
		t.Fatalf("the lead's resumed turn lacks its child's message:\n%.3000s", sent)
	}
}

// events is how many hook events of kind agent sent.
func (e *claudeE2E) events(agent, kind string) int {
	ps, _ := e.st.EventPayloads(agent, kind)
	return len(ps)
}

// waitEvents waits until agent sent n hook events of kind.
func (e *claudeE2E) waitEvents(agent, kind string, n int) {
	e.t.Helper()
	for i := 0; e.events(agent, kind) < n; i++ {
		if i == 150 {
			e.t.Fatalf("%d %s events of %s, want %d", e.events(agent, kind), kind, agent, n)
		}
		time.Sleep(200 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond) // the daemon handled it
}
