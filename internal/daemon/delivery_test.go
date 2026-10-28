package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/DavidGodefroid/lea/internal/grants"
	"github.com/DavidGodefroid/lea/internal/wire"
)

// brokenConn hands the daemon one request, then fails every write: the client went away
// between the daemon's work and its answer. Reads after the request block until the test ends,
// so the daemon does not notice the client leaving before it answers.
type brokenConn struct {
	net.Conn
	in   io.Reader
	hold chan struct{}
}

func (c *brokenConn) Read(b []byte) (int, error) {
	if n, err := c.in.Read(b); n > 0 || err != io.EOF {
		return n, err
	}
	<-c.hold
	return 0, io.EOF
}
func (c *brokenConn) Write(b []byte) (int, error) { return 0, errors.New("broken pipe") }
func (c *brokenConn) Close() error                { return nil }

func handleBroken(t *testing.T, e env, agent, method string) {
	t.Helper()
	req, _ := json.Marshal(wire.Request{Method: method, Agent: agent})
	c := &brokenConn{in: bytes.NewReader(append(req, '\n')), hold: make(chan struct{})}
	t.Cleanup(func() { close(c.hold) })
	e.d.handle(e.ctx, c, Origin{Container: "lea-a"})
}

func userSends(t *testing.T, e env, to, body string) {
	t.Helper()
	if err := call(t, e.api, "", "msg_send", map[string]any{"to": to, "body": body, "kind": "note"}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestADeliveryThatCannotBeWrittenGoesBackToTheInbox(t *testing.T) {
	for _, method := range []string{"msg_inbox", "stop_check"} {
		e := start(t)
		call(t, e.bus, "dev", "agents_list", nil, nil) // registers lea-a/dev
		userSends(t, e, "dev", "keep me")
		handleBroken(t, e, "dev", method)
		var ms []delivered
		if err := call(t, e.bus, "dev", "msg_inbox", nil, &ms); err != nil || len(ms) != 1 || !strings.Contains(ms[0].Text, "keep me") {
			t.Fatalf("%s: the message was lost: %+v err=%v", method, ms, err)
		}
	}
}

func TestStopChecksAreCountedOneAtATime(t *testing.T) {
	e := start(t)
	call(t, e.bus, "dev", "agents_list", nil, nil)
	// Enough messages for every concurrent check to find some (8 per delivery) if the round cap
	// were read and written by several checks at once.
	for i := 0; i < 64; i++ {
		if i%10 == 0 {
			*e.clock += 61 // the user's rate limit: 10 a minute
		}
		userSends(t, e, "dev", fmt.Sprintf("m%d", i))
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	blocks := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var r struct{ Block bool }
			if err := call(t, e.bus, "dev", "stop_check", nil, &r); err != nil {
				t.Error(err)
				return
			}
			if r.Block {
				mu.Lock()
				blocks++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if blocks != maxStopBlocks {
		t.Fatalf("%d blocked stops, want exactly %d", blocks, maxStopBlocks)
	}
}

func TestMessageEventsNotifyTheDeck(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	call(t, e.bus, "dev", "agents_list", nil, nil)
	changes := func() int { fa.mu.Lock(); defer fa.mu.Unlock(); return fa.changed }
	userSends(t, e, "dev", "hello")
	if n := changes(); n != 1 {
		t.Fatalf("after a send: changed=%d", n)
	}
	call(t, e.bus, "dev", "msg_inbox", nil, nil)
	if n := changes(); n != 2 {
		t.Fatalf("after a read: changed=%d", n)
	}
	call(t, e.bus, "dev", "msg_inbox", nil, nil) // nothing left to take
	if n := changes(); n != 2 {
		t.Fatalf("after an empty read: changed=%d", n)
	}
}

func TestContextUnreadCountsEveryMessage(t *testing.T) {
	e := start(t)
	call(t, e.bus, "dev", "agents_list", nil, nil)
	for i := 0; i < 10; i++ {
		userSends(t, e, "dev", fmt.Sprintf("m%d", i))
	}
	var r struct {
		Count int
		Text  string
	}
	if err := call(t, e.bus, "dev", "context_unread", nil, &r); err != nil || r.Count != 10 || !strings.Contains(r.Text, "10 unread") {
		t.Fatalf("r=%+v err=%v", r, err)
	}
}

// TestGrantCallerRoutesThroughLeaProxy: every pinned call of a grant goes out through the proxy
// lea set up for the container (its network is internal), never one the agent names.
func TestGrantCallerRoutesThroughLeaProxy(t *testing.T) {
	e := start(t)
	call(t, e.bus, "dev", "agents_list", nil, nil) // registers lea-a/dev
	c, err := e.d.grantCaller(Origin{Container: "lea-a"}, "lea-a/dev", "")
	if err != nil || c.Proxy != grants.LeaProxy {
		t.Fatalf("caller proxy %q, err %v", c.Proxy, err)
	}
}
