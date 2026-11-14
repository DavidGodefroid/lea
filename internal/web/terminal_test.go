package web

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// sseEvent is one event of a server-sent event stream.
type sseEvent struct{ name, data string }

// readEvents reads the events of resp into a channel until it ends.
func readEvents(resp *http.Response) <-chan sseEvent {
	ch := make(chan sseEvent, 16)
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		var ev sseEvent
		for sc.Scan() {
			l := sc.Text()
			switch {
			case strings.HasPrefix(l, "event: "):
				ev.name = strings.TrimPrefix(l, "event: ")
			case strings.HasPrefix(l, "data: "):
				ev.data = strings.TrimPrefix(l, "data: ")
			case l == "" && ev.name != "":
				ch <- ev
				ev = sseEvent{}
			}
		}
	}()
	return ch
}

func next(t *testing.T, ch <-chan sseEvent) sseEvent {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("the stream ended")
		}
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("no event")
	}
	return sseEvent{}
}

func (ts *testServer) openTerminal(t *testing.T, ctx context.Context, key string, c *http.Cookie) *http.Response {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.url+"/api/terminal?key="+key, nil)
	if c != nil {
		req.AddCookie(c)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestTerminalStreamsTheAgentsScreenReadOnly(t *testing.T) {
	ts := newTestServer(t)
	ts.s.captureEvery = 20 * time.Millisecond
	ts.d.mu.Lock()
	ts.d.answers["agent_capture"] = map[string]any{"lines": []string{"\x1b[32mok\x1b[0m 12 tests"}, "width": 80, "height": 24, "cursor_x": 0, "cursor_y": 1}
	ts.d.mu.Unlock()
	c := ts.login(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp := ts.openTerminal(t, ctx, "lea-a/dev-1", c)
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("terminal: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	ch := readEvents(resp)
	ev := next(t, ch)
	var sc Screen
	if ev.name != "screen" || json.Unmarshal([]byte(ev.data), &sc) != nil || sc.Lines[0] != "\x1b[32mok\x1b[0m 12 tests" || sc.Width != 80 {
		t.Fatalf("first event %+v", ev)
	}
	if p := ts.d.last("agent_capture"); p["key"] != "lea-a/dev-1" || len(p) != 1 {
		t.Fatalf("agent_capture params %v", p)
	}
	// A change is sent; the same screen is not sent again.
	ts.d.mu.Lock()
	ts.d.answers["agent_capture"] = map[string]any{"lines": []string{"done"}, "width": 80, "height": 24}
	ts.d.mu.Unlock()
	ev = next(t, ch)
	if ev.name != "screen" || !strings.Contains(ev.data, `"done"`) {
		t.Fatalf("second event %+v", ev)
	}
	// The pane gone: an error, once.
	ts.d.mu.Lock()
	ts.d.answers["agent_capture"] = map[string]any{"error": "agent dev-1 has no terminal"}
	ts.d.mu.Unlock()
	ev = next(t, ch)
	if ev.name != "terminal-error" || !strings.Contains(ev.data, "has no terminal") {
		t.Fatalf("error event %+v", ev)
	}
	for _, m := range ts.d.methods() {
		if m != "agent_capture" {
			t.Fatalf("the terminal stream called %s", m)
		}
	}
}

func TestTerminalRefusals(t *testing.T) {
	ts := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if resp := ts.openTerminal(t, ctx, "lea-a/dev-1", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no cookie: %d", resp.StatusCode)
	}
	c := ts.login(t)
	for _, k := range []string{"lea-b/dev-1", "lea-a/", "lea-a/x/y", "", "shell:lea-a/sh-1"} {
		resp := ts.openTerminal(t, ctx, k, c)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%q: %d", k, resp.StatusCode)
		}
	}
	req, _ := http.NewRequest("GET", ts.url+"/api/terminal?key=lea-a/dev-1", nil)
	req.AddCookie(c)
	req.Header.Set("Origin", "http://evil.example")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin: %v %v", resp, err)
	}
	if code, _ := ts.post(t, "agent_capture", map[string]any{"key": "lea-a/dev-1"}, c, nil); code != http.StatusNotFound {
		t.Fatalf("agent_capture as a method: %d", code)
	}
	// At most maxTerminals at once.
	var open []*http.Response
	for i := 0; i < maxTerminals; i++ {
		resp := ts.openTerminal(t, ctx, "lea-a/dev-1", c)
		if resp.StatusCode != 200 {
			t.Fatalf("terminal %d: %d", i, resp.StatusCode)
		}
		open = append(open, resp)
	}
	resp := ts.openTerminal(t, ctx, "lea-a/dev-1", c)
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("one too many: %d", resp.StatusCode)
	}
	for _, r := range open {
		r.Body.Close()
	}
}

func TestTerminalCapsTheScreen(t *testing.T) {
	ts := newTestServer(t)
	lines := make([]string, maxScreenLines+20)
	for i := range lines {
		lines[i] = "x"
	}
	lines[len(lines)-1] = strings.Repeat("é", maxLineBytes)
	ts.d.answers["agent_capture"] = map[string]any{"lines": lines}
	sc, err := ts.s.capture(context.Background(), "lea-a/dev-1")
	if err != nil {
		t.Fatal(err)
	}
	last := sc.Lines[len(sc.Lines)-1]
	if len(sc.Lines) != maxScreenLines || len(last) > maxLineBytes || !strings.HasPrefix(last, "é") || strings.ContainsRune(last, '�') {
		t.Fatalf("%d lines, last of %d bytes", len(sc.Lines), len(last))
	}
}
