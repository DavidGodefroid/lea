package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/daemon"
	"github.com/DavidGodefroid/lea/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func session(t *testing.T, bus, agent string) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	if _, err := New(bus, agent, "test").Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func text(r *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range r.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestToolsRoundTrip(t *testing.T) {
	dir, err := os.MkdirTemp("", "lea")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	st, err := store.Open(filepath.Join(dir, "lea.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	d := daemon.New(st, filepath.Join(dir, "run"), func() int64 { return time.Now().Unix() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus, err := d.AttachContainer(ctx, "lea-a")
	if err != nil {
		t.Fatal(err)
	}
	dev, rev := session(t, bus, "dev"), session(t, bus, "rev")

	tools, err := dev.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tl := range tools.Tools {
		names[tl.Name] = true
	}
	for _, n := range []string{"agents_list", "msg_send", "msg_inbox", "msg_wait", "msg_reply", "ask"} {
		if !names[n] {
			t.Errorf("missing tool %s", n)
		}
	}
	if _, err := rev.CallTool(ctx, &mcp.CallToolParams{Name: "agents_list", Arguments: map[string]any{}}); err != nil { // registers rev
		t.Fatal(err)
	}
	r, err := dev.CallTool(ctx, &mcp.CallToolParams{Name: "msg_send", Arguments: map[string]any{"to": "rev", "body": "review L88", "kind": "request"}})
	if err != nil || r.IsError {
		t.Fatalf("send: %v %s", err, text(r))
	}
	r, err = rev.CallTool(ctx, &mcp.CallToolParams{Name: "msg_wait", Arguments: map[string]any{"timeout_s": 2}})
	if err != nil || !strings.Contains(text(r), "review L88") {
		t.Fatalf("wait: %v %s", err, text(r))
	}
	r, err = dev.CallTool(ctx, &mcp.CallToolParams{Name: "msg_send", Arguments: map[string]any{"to": "ghost", "body": "x", "kind": "note"}})
	if err != nil || !r.IsError || !strings.Contains(text(r), "known:") {
		t.Fatalf("unknown recipient not reported: %s", text(r))
	}
}

func TestDaemonDownIsToolError(t *testing.T) {
	cs := session(t, "/nonexistent/bus.sock", "dev")
	r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "msg_inbox", Arguments: map[string]any{}})
	if err != nil || !r.IsError || !strings.Contains(text(r), "lea daemon") {
		t.Fatalf("err=%v r=%s", err, text(r))
	}
}

func newBus(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "lea")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	st, err := store.Open(filepath.Join(dir, "lea.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	d := daemon.New(st, filepath.Join(dir, "run"), func() int64 { return time.Now().Unix() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	bus, err := d.AttachContainer(ctx, "lea-a")
	if err != nil {
		t.Fatal(err)
	}
	return bus
}

func TestWaitTimeoutOutOfRangeReportsDaemonError(t *testing.T) {
	cs := session(t, newBus(t), "dev")
	r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "msg_wait", Arguments: map[string]any{"timeout_s": 0}})
	if err != nil || !r.IsError || !strings.Contains(text(r), "between 1 and 600") {
		t.Fatalf("err=%v r=%s", err, text(r))
	}
}

func TestCancelledWaitDoesNotConsumeMessage(t *testing.T) {
	bus := newBus(t)
	dev, rev := session(t, bus, "dev"), session(t, bus, "rev")
	ctx := context.Background()
	if _, err := rev.CallTool(ctx, &mcp.CallToolParams{Name: "agents_list", Arguments: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	wctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		rev.CallTool(wctx, &mcp.CallToolParams{Name: "msg_wait", Arguments: map[string]any{"timeout_s": 30}})
		close(done)
	}()
	time.Sleep(300 * time.Millisecond) // let the wait reach the daemon
	cancel()
	<-done
	time.Sleep(300 * time.Millisecond) // let the cancellation propagate
	if _, err := dev.CallTool(ctx, &mcp.CallToolParams{Name: "msg_send", Arguments: map[string]any{"to": "rev", "body": "late", "kind": "note"}}); err != nil {
		t.Fatal(err)
	}
	r, err := rev.CallTool(ctx, &mcp.CallToolParams{Name: "msg_inbox", Arguments: map[string]any{}})
	if err != nil || !strings.Contains(text(r), "late") {
		t.Fatalf("message lost: %v %s", err, text(r))
	}
}
func TestSpawnToolsAreListedAndRefusedToAgentsLeaDidNotStart(t *testing.T) {
	dir, err := os.MkdirTemp("", "lea")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	st, err := store.Open(filepath.Join(dir, "lea.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	d := daemon.New(st, filepath.Join(dir, "run"), func() int64 { return time.Now().Unix() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus, err := d.AttachContainer(ctx, "lea-a")
	if err != nil {
		t.Fatal(err)
	}
	dev := session(t, bus, "dev")
	tools, err := dev.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tl := range tools.Tools {
		names[tl.Name] = true
	}
	for _, n := range []string{"agents_spawn", "agents_stop", "agents_request_spawn"} {
		if !names[n] {
			t.Errorf("missing tool %s", n)
		}
	}
	r, err := dev.CallTool(ctx, &mcp.CallToolParams{Name: "agents_spawn", Arguments: map[string]any{"brief": "x"}})
	if err != nil || !r.IsError || !strings.Contains(text(r), "only running agents lea started") {
		t.Fatalf("spawn by an unknown agent: err=%v result=%s", err, text(r))
	}
	r, err = dev.CallTool(ctx, &mcp.CallToolParams{Name: "agents_request_spawn", Arguments: map[string]any{"brief": "x", "reason": "y"}})
	if err != nil || !r.IsError || !strings.Contains(text(r), "agents_request_spawn: only running agents lea started") {
		t.Fatalf("request by an unknown agent: err=%v result=%s", err, text(r))
	}
}

// TestAskRoundTrip is ask end to end over MCP and the bus socket: dev asks rev, rev reads the
// question with msg_wait and answers with msg_reply, dev's ask returns the answer.
func TestAskRoundTrip(t *testing.T) {
	bus := newBus(t)
	dev, rev := session(t, bus, "dev"), session(t, bus, "rev")
	ctx := context.Background()
	if _, err := rev.CallTool(ctx, &mcp.CallToolParams{Name: "agents_list", Arguments: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	got := make(chan *mcp.CallToolResult, 1)
	go func() {
		r, err := dev.CallTool(ctx, &mcp.CallToolParams{Name: "ask", Arguments: map[string]any{"to": "rev", "question": "which DB?", "timeout_s": 20}})
		if err != nil {
			r = &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}
		}
		got <- r
	}()
	r, err := rev.CallTool(ctx, &mcp.CallToolParams{Name: "msg_wait", Arguments: map[string]any{"timeout_s": 10}})
	if err != nil || r.IsError {
		t.Fatalf("wait: %v %s", err, text(r))
	}
	var qs []struct {
		ID   int64
		Kind string
		Text string
	}
	if err := json.Unmarshal([]byte(text(r)), &qs); err != nil || len(qs) != 1 || qs[0].Kind != "question" || !strings.Contains(qs[0].Text, "which DB?") {
		t.Fatalf("question: %v %s", err, text(r))
	}
	if r, err := rev.CallTool(ctx, &mcp.CallToolParams{Name: "msg_reply", Arguments: map[string]any{"id": qs[0].ID, "status": "ok", "body": "postgres"}}); err != nil || r.IsError {
		t.Fatalf("reply: %v %s", err, text(r))
	}
	select {
	case r := <-got:
		if r.IsError || !strings.Contains(text(r), `"answered":true`) || !strings.Contains(text(r), "[status: ok] postgres") {
			t.Fatalf("ask: %s", text(r))
		}
	case <-time.After(15 * time.Second):
		t.Fatal("ask did not return")
	}
}

// TestCancelledAskFreesTheAsker: an ask whose client goes away ends, so the asker can ask again.
func TestCancelledAskFreesTheAsker(t *testing.T) {
	bus := newBus(t)
	dev, rev := session(t, bus, "dev"), session(t, bus, "rev")
	ctx := context.Background()
	if _, err := rev.CallTool(ctx, &mcp.CallToolParams{Name: "agents_list", Arguments: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	actx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		dev.CallTool(actx, &mcp.CallToolParams{Name: "ask", Arguments: map[string]any{"to": "rev", "question": "q1", "timeout_s": 30}})
		close(done)
	}()
	time.Sleep(300 * time.Millisecond) // let the ask reach the daemon
	cancel()
	<-done
	time.Sleep(300 * time.Millisecond) // let the cancellation propagate
	r, err := dev.CallTool(ctx, &mcp.CallToolParams{Name: "ask", Arguments: map[string]any{"to": "rev", "question": "q2", "timeout_s": 1}})
	if err != nil || r.IsError || !strings.Contains(text(r), "no answer") {
		t.Fatalf("second ask: %v %s", err, text(r))
	}
}
