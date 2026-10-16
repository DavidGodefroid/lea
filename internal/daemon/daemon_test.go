package daemon

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/DavidGodefroid/lea/internal/client"
	"github.com/DavidGodefroid/lea/internal/store"
	"github.com/DavidGodefroid/lea/internal/wire"
)

// shortDir keeps socket paths under the 104-byte limit (t.TempDir paths are long).
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "lea")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

type env struct {
	d     *Daemon
	api   string
	bus   string
	ctx   context.Context
	clock *int64
}

func start(t *testing.T) env {
	t.Helper()
	dir := shortDir(t)
	st, err := store.Open(filepath.Join(dir, "lea.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	clock := int64(1000)
	d := New(st, filepath.Join(dir, "run"), func() int64 { return clock })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	api := filepath.Join(dir, "d.sock")
	l, err := Listen(api)
	if err != nil {
		t.Fatal(err)
	}
	go d.Serve(ctx, l, Origin{})
	bus, err := d.AttachContainer(ctx, "lea-a")
	if err != nil {
		t.Fatal(err)
	}
	return env{d: d, api: api, bus: bus, ctx: ctx, clock: &clock}
}

func call(t *testing.T, sock, agent, method string, params, result any) error {
	t.Helper()
	p, _ := json.Marshal(params)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return client.Call(ctx, sock, wire.Request{Method: method, Agent: agent, Params: p}, result)
}

type delivered struct {
	ID     int64  `json:"id"`
	From   string `json:"from"`
	Kind   string `json:"kind"`
	Thread int64  `json:"thread"`
	Text   string `json:"text"`
}

func TestSendInboxReply(t *testing.T) {
	e := start(t)
	// two agents in the same container register by calling agents_list
	var as []map[string]any
	if err := call(t, e.bus, "dev", "agents_list", nil, &as); err != nil {
		t.Fatal(err)
	}
	if err := call(t, e.bus, "rev", "agents_list", nil, &as); err != nil {
		t.Fatal(err)
	}
	var sent struct{ ID, Thread int64 }
	if err := call(t, e.bus, "dev", "msg_send", map[string]any{"to": "rev", "body": "review L88", "kind": "request"}, &sent); err != nil {
		t.Fatal(err)
	}
	var in []delivered
	if err := call(t, e.bus, "rev", "msg_inbox", nil, &in); err != nil || len(in) != 1 {
		t.Fatalf("inbox=%v err=%v", in, err)
	}
	if in[0].From != "lea-a/dev" || !strings.Contains(in[0].Text, "untrusted") {
		t.Fatalf("delivered=%+v", in[0])
	}
	if err := call(t, e.bus, "rev", "msg_reply", map[string]any{"id": sent.ID, "status": "ok", "body": "looks good"}, &sent); err != nil {
		t.Fatal(err)
	}
	if err := call(t, e.bus, "dev", "msg_inbox", nil, &in); err != nil || len(in) != 1 || in[0].Kind != "result" {
		t.Fatalf("reply=%v err=%v", in, err)
	}
	if !strings.Contains(in[0].Text, "[status: ok] looks good") {
		t.Fatalf("text=%q", in[0].Text)
	}
}

func TestReplyOnlyToOwnMessages(t *testing.T) {
	e := start(t)
	var sent struct{ ID, Thread int64 }
	call(t, e.bus, "c", "agents_list", nil, nil)
	if err := call(t, e.bus, "a", "msg_send", map[string]any{"to": "c", "body": "x", "kind": "note"}, &sent); err != nil {
		t.Fatal(err)
	}
	err := call(t, e.bus, "b", "msg_reply", map[string]any{"id": sent.ID, "status": "ok", "body": "y"}, nil)
	if err == nil || !strings.Contains(err.Error(), "not addressed to you") {
		t.Fatalf("err=%v", err)
	}
}

func TestUnknownRecipientListsAgents(t *testing.T) {
	e := start(t)
	call(t, e.bus, "dev", "agents_list", nil, nil)
	err := call(t, e.bus, "dev", "msg_send", map[string]any{"to": "ghost", "body": "x", "kind": "note"}, nil)
	if err == nil || !strings.Contains(err.Error(), "known: lea-a/dev") {
		t.Fatalf("err=%v", err)
	}
}

func TestUserSendsAndWaitWakes(t *testing.T) {
	e := start(t)
	call(t, e.bus, "dev", "agents_list", nil, nil)
	got := make(chan []delivered, 1)
	go func() {
		var in []delivered
		call(t, e.bus, "dev", "msg_wait", map[string]any{"timeout_s": 5}, &in)
		got <- in
	}()
	time.Sleep(100 * time.Millisecond)
	if err := call(t, e.api, "", "msg_send", map[string]any{"to": "dev", "body": "hello", "kind": "note"}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case in := <-got:
		if len(in) != 1 || in[0].From != "user" {
			t.Fatalf("in=%v", in)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("msg_wait did not wake")
	}
}

func TestWaitTimesOutEmpty(t *testing.T) {
	e := start(t)
	var in []delivered
	if err := call(t, e.bus, "dev", "msg_wait", map[string]any{"timeout_s": 1}, &in); err != nil || len(in) != 0 {
		t.Fatalf("in=%v err=%v", in, err)
	}
}

func TestContainerCannotAttach(t *testing.T) {
	e := start(t)
	err := call(t, e.bus, "dev", "container_attach", map[string]any{"name": "lea-x"}, nil)
	if err == nil || !strings.Contains(err.Error(), "host only") {
		t.Fatalf("err=%v", err)
	}
}

func TestBadAgentName(t *testing.T) {
	e := start(t)
	if err := call(t, e.bus, "", "agents_list", nil, nil); err == nil {
		t.Fatal("empty agent name accepted")
	}
	if err := call(t, e.bus, "a/b", "agents_list", nil, nil); err == nil {
		t.Fatal("slash in agent name accepted")
	}
}

// An oversized line leaves the stream mid-line: the daemon must answer with an error, close
// that connection without parsing the remainder, and keep serving other connections.
func TestOversizedLineClosesConnection(t *testing.T) {
	e := start(t)
	c, err := net.Dial("unix", e.bus)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	// Oversized line, then (after its newline) a valid request that must never be served.
	valid := `{"method":"agents_list","agent":"dev"}` + "\n"
	big := strings.Repeat("a", wire.MaxLine+10) + "\n" + valid
	go c.Write([]byte(big))
	var resp wire.Response
	if err := wire.Read(wire.NewReader(c), &resp); err != nil {
		t.Fatalf("no error response: %v", err)
	}
	if !strings.Contains(resp.Error, "bad request") {
		t.Fatalf("resp=%+v", resp)
	}
	// Connection is closed: no second response, EOF (or reset) instead.
	var extra wire.Response
	if err := wire.Read(wire.NewReader(c), &extra); err == nil {
		t.Fatalf("daemon kept serving the connection: %+v", extra)
	}
	// The daemon still serves other connections.
	if err := call(t, e.bus, "dev", "agents_list", nil, nil); err != nil {
		t.Fatalf("daemon stopped serving: %v", err)
	}
}

func TestListenPathTooLong(t *testing.T) {
	p := "/tmp/" + strings.Repeat("x", 120) + "/bus.sock"
	_, err := Listen(p)
	if err == nil || !strings.Contains(err.Error(), "limit is 104") || !strings.Contains(err.Error(), p) {
		t.Fatalf("err=%v", err)
	}
}

func TestListenStaleAndLive(t *testing.T) {
	p := filepath.Join(shortDir(t), "s.sock")
	if err := os.WriteFile(p, nil, 0o600); err != nil { // stale file, nobody listening
		t.Fatal(err)
	}
	l, err := Listen(p)
	if err != nil {
		t.Fatalf("stale socket not replaced: %v", err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	if _, err := Listen(p); err == nil || !strings.Contains(err.Error(), "already answers") {
		t.Fatalf("second daemon accepted: %v", err)
	}
}

func TestAttachRefusesSymlink(t *testing.T) {
	e := start(t)
	run := filepath.Dir(filepath.Dir(e.bus))
	if err := os.Symlink(shortDir(t), filepath.Join(run, "lea-evil")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.AttachContainer(e.ctx, "lea-evil"); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("err=%v", err)
	}
}

func TestWaitDisconnectDoesNotLoseMessage(t *testing.T) {
	e := start(t)
	call(t, e.bus, "dev", "agents_list", nil, nil)
	c, err := net.Dial("unix", e.bus)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := json.Marshal(map[string]any{"timeout_s": 30})
	if err := wire.Write(c, wire.Request{Method: "msg_wait", Agent: "dev", Params: p}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // the daemon is now blocked in msg_wait
	c.Close()
	time.Sleep(200 * time.Millisecond)
	if err := call(t, e.api, "", "msg_send", map[string]any{"to": "dev", "body": "kept", "kind": "note"}, nil); err != nil {
		t.Fatal(err)
	}
	var in []delivered
	if err := call(t, e.bus, "dev", "msg_inbox", nil, &in); err != nil || len(in) != 1 {
		t.Fatalf("message lost: in=%v err=%v", in, err)
	}
}

func TestConcurrentInboxNeverDuplicates(t *testing.T) {
	e := start(t)
	call(t, e.bus, "dev", "agents_list", nil, nil)
	const n = 6
	for i := 0; i < n; i++ {
		if err := call(t, e.api, "", "msg_send", map[string]any{"to": "dev", "body": "m", "kind": "note"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	seen := map[int64]int{}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var in []delivered
			if err := call(t, e.bus, "dev", "msg_inbox", nil, &in); err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			for _, m := range in {
				seen[m.ID]++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(seen) != n {
		t.Fatalf("delivered %d distinct, want %d", len(seen), n)
	}
	for id, c := range seen {
		if c != 1 {
			t.Fatalf("message %d delivered %d times", id, c)
		}
	}
}

func TestConcurrentSendsRespectRateLimit(t *testing.T) {
	e := start(t)
	call(t, e.bus, "dev", "agents_list", nil, nil)
	var ok int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if call(t, e.bus, "dev", "msg_send", map[string]any{"to": "user", "body": "x", "kind": "note"}, nil) == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ok != 10 {
		t.Fatalf("accepted %d sends, limit is 10", ok)
	}
}

func TestAttachRefusesSymlinkedRunDir(t *testing.T) {
	dir := shortDir(t)
	st, err := store.Open(filepath.Join(dir, "lea.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	target := filepath.Join(dir, "real")
	os.Mkdir(target, 0o700)
	run := filepath.Join(dir, "run")
	if err := os.Symlink(target, run); err != nil {
		t.Fatal(err)
	}
	d := New(st, run, func() int64 { return 1 })
	if _, err := d.AttachContainer(context.Background(), "lea-a"); err == nil || !strings.Contains(err.Error(), "symlink") || !strings.Contains(err.Error(), run) {
		t.Fatalf("err=%v", err)
	}
}

// A batch whose encoded response would exceed the wire limit must not be marked read and lost:
// msg_inbox caps each batch by encoded size and the rest stays unread for the next call.
func TestLargeBatchesDeliverEverything(t *testing.T) {
	e := start(t)
	call(t, e.bus, "dev", "agents_list", nil, nil)
	if err := call(t, e.api, "", "msg_send", map[string]any{"to": "dev", "body": "hello", "kind": "note"}, nil); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := call(t, e.bus, "peer", "msg_send", map[string]any{"to": "dev", "body": strings.Repeat("<", 64<<10), "kind": "note"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[int64]bool{}
	for i := 0; i < 6; i++ {
		var got []delivered
		if err := call(t, e.bus, "dev", "msg_inbox", nil, &got); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if len(got) == 0 {
			break
		}
		for _, m := range got {
			seen[m.ID] = true
		}
	}
	if len(seen) != 4 {
		t.Fatalf("delivered %d of 4 messages", len(seen))
	}
}

// The launcher binds a container's bus socket with one short container_attach request: the
// socket must outlive that request (it lives as long as the daemon).
func TestContainerAttachOverTheAPIOutlivesTheRequest(t *testing.T) {
	e := start(t)
	var r struct{ Socket string }
	if err := call(t, e.api, "", "container_attach", map[string]string{"name": "lea-b"}, &r); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(r.Socket); err != nil {
		t.Fatalf("bus socket gone after the attach request: %v", err)
	}
	var as []agentView
	if err := call(t, r.Socket, "claude-1", "agents_list", nil, &as); err != nil {
		t.Fatalf("bus socket does not answer after the attach request: %v", err)
	}
}

func TestCapToolTruncatesOnARuneBoundary(t *testing.T) {
	got := capTool(strings.Repeat("é", 100))
	if len(got) > maxToolName || !utf8.ValidString(got) {
		t.Fatalf("len %d valid %v", len(got), utf8.ValidString(got))
	}
	if capTool("a\xffb") != "a?b" || capTool("Bash") != "Bash" {
		t.Fatal("capTool")
	}
}
