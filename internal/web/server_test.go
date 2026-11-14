package web

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/wire"
)

// fakeDaemon answers the host socket's requests: answers maps a method to its result (or, with
// an "error" key, the daemon's refusal); agents_watch reports version and waits for bump when
// asked for that version; deck_web_attach is held until its client leaves.
type fakeDaemon struct {
	sock     string
	mu       sync.Mutex
	calls    []wire.Request
	answers  map[string]any
	version  int64
	bump     chan struct{}
	attached int
}

func newFakeDaemon(t *testing.T) *fakeDaemon {
	t.Helper()
	dir, err := os.MkdirTemp("", "leaweb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	f := &fakeDaemon{sock: filepath.Join(dir, "d.sock"), answers: map[string]any{}, version: 1, bump: make(chan struct{}, 8)}
	l, err := net.Listen("unix", f.sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeDaemon) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	var req wire.Request
	if err := wire.Read(r, &req); err != nil {
		return
	}
	f.mu.Lock()
	f.calls = append(f.calls, req)
	ans, v := f.answers[req.Method], f.version
	f.mu.Unlock()
	switch req.Method {
	case "deck_web_attach":
		f.mu.Lock()
		f.attached++
		f.mu.Unlock()
		io.Copy(io.Discard, r) // until the client closes
		f.mu.Lock()
		f.attached--
		f.mu.Unlock()
		return
	case "agents_watch":
		var p struct{ Since int64 }
		json.Unmarshal(req.Params, &p)
		if p.Since == v {
			select {
			case <-f.bump:
			case <-time.After(2 * time.Second):
			}
			f.mu.Lock()
			v = f.version
			f.mu.Unlock()
		}
		ans = map[string]any{"version": v, "agents": []map[string]any{{"key": "lea-a/dev-1", "name": "dev-1", "state": "idle"}}}
	}
	if m, ok := ans.(map[string]any); ok {
		if e, ok := m["error"].(string); ok {
			wire.Write(c, wire.Response{Error: e})
			return
		}
	}
	if ans == nil && req.Method != "board_list" && strings.HasSuffix(req.Method, "_list") {
		ans = []any{}
	}
	if ans == nil {
		ans = struct{}{}
	}
	b, _ := json.Marshal(ans)
	wire.Write(c, wire.Response{Result: b})
}

func (f *fakeDaemon) methods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []string{}
	for _, c := range f.calls {
		out = append(out, c.Method)
	}
	return out
}

func (f *fakeDaemon) last(method string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i].Method == method {
			var m map[string]any
			json.Unmarshal(f.calls[i].Params, &m)
			return m
		}
	}
	return nil
}

// nth is the parameters of the n-th call (from 0) of method, nil when there is none yet.
func (f *fakeDaemon) nth(method string, n int) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c.Method == method {
			if n == 0 {
				var m map[string]any
				json.Unmarshal(c.Params, &m)
				return m
			}
			n--
		}
	}
	return nil
}

func (f *fakeDaemon) attachedNow() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attached
}

// testServer is a Server for lea-a on a fake daemon, served by httptest on the loopback.
type testServer struct {
	s    *Server
	d    *fakeDaemon
	url  string
	host string
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	d := newFakeDaemon(t)
	s, err := New(SocketCaller(d.sock), "lea-a")
	if err != nil {
		t.Fatal(err)
	}
	s.retry = func(ctx context.Context) {
		select {
		case <-ctx.Done():
		case <-time.After(20 * time.Millisecond):
		}
	}
	l, err := s.Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewUnstartedServer(s.Handler())
	hs.Listener.Close()
	hs.Listener = l
	hs.Start()
	t.Cleanup(hs.Close)
	host := strings.TrimPrefix(hs.URL, "http://")
	return &testServer{s: s, d: d, url: hs.URL, host: host}
}

func noRedirect() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// login exchanges the token and returns the session cookie.
func (ts *testServer) login(t *testing.T) *http.Cookie {
	t.Helper()
	resp, err := noRedirect().Get(ts.s.URL())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Fatalf("exchange: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	for _, c := range resp.Cookies() {
		if c.Name == cookieName {
			return c
		}
	}
	t.Fatal("no session cookie")
	return nil
}

// post sends a JSON POST as the page does: same Origin, the cookie when given.
func (ts *testServer) post(t *testing.T, method string, body any, c *http.Cookie, edit func(*http.Request)) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", ts.url+"/api/"+method, strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://"+ts.host)
	if c != nil {
		req.AddCookie(c)
	}
	if edit != nil {
		edit(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestListenBindsTheLoopbackOnly(t *testing.T) {
	s, _ := New(SocketCaller("/nonexistent"), "lea-a")
	l, err := s.Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	a := l.Addr().(*net.TCPAddr)
	if !a.IP.Equal(net.IPv4(127, 0, 0, 1)) || a.Port == 0 {
		t.Fatalf("listens on %s", a)
	}
	if !strings.HasPrefix(s.URL(), "http://127.0.0.1:") || len(s.token) != 64 {
		t.Fatalf("url %q, token of %d characters", s.URL(), len(s.token))
	}
	if _, err := s.Listen(70000); err == nil {
		t.Fatal("port 70000 accepted")
	}
}

func TestEveryAPIAnswers401WithoutTheCookie(t *testing.T) {
	ts := newTestServer(t)
	bad := &http.Cookie{Name: cookieName, Value: strings.Repeat("0", 64)}
	for _, c := range []*http.Cookie{nil, bad} {
		for _, m := range []string{"agent_stop", "msg_send", "nope"} {
			if code, _ := ts.post(t, m, map[string]any{"key": "lea-a/dev-1"}, c, nil); code != http.StatusUnauthorized {
				t.Fatalf("%s with cookie %v: %d", m, c, code)
			}
		}
		req, _ := http.NewRequest("GET", ts.url+"/api/events", nil)
		if c != nil {
			req.AddCookie(c)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("events: %d", resp.StatusCode)
		}
	}
	if ms := ts.d.methods(); len(ms) != 0 {
		t.Fatalf("the daemon was called: %v", ms)
	}
}

func TestTheTokenIsExchangedOnceForAStrictHttpOnlyCookie(t *testing.T) {
	ts := newTestServer(t)
	resp, err := noRedirect().Get(ts.url + "/?token=" + strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || len(resp.Cookies()) != 0 {
		t.Fatalf("a wrong token: %d %v", resp.StatusCode, resp.Cookies())
	}
	c := ts.login(t)
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || len(c.Value) != 64 || c.Value == ts.s.token {
		t.Fatalf("cookie %+v", c)
	}
	resp, err = noRedirect().Get(ts.s.URL())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the token worked twice: %d", resp.StatusCode)
	}
	if code, _ := ts.post(t, "providers_list", map[string]any{}, c, nil); code != http.StatusOK {
		t.Fatalf("with the cookie: %d", code)
	}
}

func TestForeignHostOrOriginIsRefusedEvenWithTheCookie(t *testing.T) {
	ts := newTestServer(t)
	c := ts.login(t)
	port := ts.host[strings.LastIndex(ts.host, ":"):]
	cases := map[string]func(*http.Request){
		"rebinding Host":     func(r *http.Request) { r.Host = "evil.example" + port },
		"other port Host":    func(r *http.Request) { r.Host = "127.0.0.1:1" },
		"foreign Origin":     func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") },
		"https Origin":       func(r *http.Request) { r.Header.Set("Origin", "https://"+ts.host) },
		"null Origin":        func(r *http.Request) { r.Header.Set("Origin", "null") },
		"no Origin":          func(r *http.Request) { r.Header.Del("Origin") },
		"cross-site fetch":   func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
		"same-site fetch":    func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") },
		"localhost vs 127.0": func(r *http.Request) { r.Header.Set("Origin", "http://localhost"+port) },
	}
	for name, edit := range cases {
		if code, _ := ts.post(t, "providers_list", map[string]any{}, c, edit); code != http.StatusForbidden {
			t.Errorf("%s: %d", name, code)
		}
	}
	// localhost is this server too, when the browser uses it for both.
	code, _ := ts.post(t, "providers_list", map[string]any{}, c, func(r *http.Request) {
		r.Host = "localhost" + port
		r.Header.Set("Origin", "http://localhost"+port)
	})
	if code != http.StatusOK {
		t.Fatalf("localhost: %d", code)
	}
	// The page itself checks its Host too.
	req, _ := http.NewRequest("GET", ts.url+"/", nil)
	req.Host = "evil.example" + port
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("page on a foreign Host: %d", resp.StatusCode)
	}
	for _, m := range ts.d.methods() {
		if m != "providers_list" {
			t.Fatalf("the daemon got %s", m)
		}
	}
	if n := len(ts.d.methods()); n != 1 {
		t.Fatalf("the daemon got %d calls, want only the allowed one", n)
	}
}

func TestPagesCarryAStrictCSP(t *testing.T) {
	ts := newTestServer(t)
	for _, p := range []string{"/", "/static/app.js", "/static/app.css"} {
		resp, err := http.Get(ts.url + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d", p, resp.StatusCode)
		}
		csp := resp.Header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "script-src 'self'") ||
			strings.Contains(csp, "unsafe") || resp.Header.Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("%s: CSP %q", p, csp)
		}
	}
}

func TestOnlyAllowlistedMethodsReachTheDaemon(t *testing.T) {
	ts := newTestServer(t)
	c := ts.login(t)
	for _, m := range []string{"agents_watch", "container_attach", "allow_set", "grant_revoke", "agents_spawn", "deck_web_attach", "request_list", "grant_request_list", "question_drop", "msg_inbox", "sound"} {
		if code, _ := ts.post(t, m, map[string]any{"key": "lea-a/dev-1"}, c, nil); code != http.StatusNotFound {
			t.Fatalf("%s: %d", m, code)
		}
	}
	// An allowed method naming another lea's agent, or a bad kind, never reaches the socket either.
	refused := []struct {
		m    string
		body map[string]any
	}{
		{"agent_stop", map[string]any{"key": "lea-b/dev-1"}},
		{"agent_stop", map[string]any{"key": "lea-a/"}},
		{"agent_label", map[string]any{"key": "lea-a/dev-1"}},
		{"agent_pause", map[string]any{"key": "lea-a/dev-1", "paused": "yes"}},
		{"msg_send", map[string]any{"to": "lea-a/dev-1", "body": "hi", "kind": "question"}},
		{"msg_send", map[string]any{"to": "lea-b/dev-1", "body": "hi", "kind": "note"}},
		{"approval_decide", map[string]any{"id": 1, "verdict": "maybe"}},
	}
	for _, r := range refused {
		if code, _ := ts.post(t, r.m, r.body, c, nil); code != http.StatusBadRequest {
			t.Fatalf("%s %v: %d", r.m, r.body, code)
		}
	}
	if ms := ts.d.methods(); len(ms) != 0 {
		t.Fatalf("the daemon was called: %v", ms)
	}
	// A wrong content type is refused (a cross-site form cannot send JSON).
	code, _ := ts.post(t, "agent_stop", map[string]any{"key": "lea-a/dev-1"}, c, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") })
	if code != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain: %d", code)
	}
}

func TestAllowedMethodsAreForwardedWithTheirOwnFieldsOnly(t *testing.T) {
	ts := newTestServer(t)
	c := ts.login(t)
	if code, _ := ts.post(t, "agent_hold", map[string]any{"key": "lea-a/dev-1", "held": true, "container": "x"}, c, nil); code != 200 {
		t.Fatalf("agent_hold: %d", code)
	}
	if p := ts.d.last("agent_hold"); len(p) != 2 || p["agent"] != "lea-a/dev-1" || p["held"] != true {
		t.Fatalf("agent_hold params %v", p)
	}
	ts.post(t, "msg_send", map[string]any{"to": "lea-a/claude-1", "body": "ship it", "kind": "request", "reply_to": 4}, c, nil)
	if p := ts.d.last("msg_send"); len(p) != 3 || p["to"] != "lea-a/claude-1" || p["kind"] != "request" {
		t.Fatalf("msg_send params %v", p)
	}
	ts.d.mu.Lock()
	ts.d.answers["agent_new"] = map[string]any{"key": "lea-a/dev-2", "name": "dev-2"}
	ts.d.mu.Unlock()
	code, out := ts.post(t, "agent_new", map[string]any{"profile": "claude", "role": "dev", "repo": "lea", "brief": "do it", "label": "web", "container": "lea-b"}, c, nil)
	if code != 200 {
		t.Fatalf("agent_new: %d %v", code, out)
	}
	if p := ts.d.last("agent_new"); p["container"] != "lea-a" || p["profile"] != "claude" || p["brief"] != "do it" || p["tower"] != false || p["focus"] != nil {
		t.Fatalf("agent_new params %v", p)
	}
	if p := ts.d.last("agent_label"); p["key"] != "lea-a/dev-2" || p["label"] != "web" {
		t.Fatalf("agent_label params %v", p)
	}
	// The daemon's refusal comes back as a 400 with its text.
	ts.d.mu.Lock()
	ts.d.answers["agent_stop"] = map[string]any{"error": "no agent lea-a/dev-9"}
	ts.d.mu.Unlock()
	if code, out := ts.post(t, "agent_stop", map[string]any{"key": "lea-a/dev-9"}, c, nil); code != 400 || out["error"] != "no agent lea-a/dev-9" {
		t.Fatalf("refusal: %d %v", code, out)
	}
}

func TestApprovalDecideAnswersOnlyThisLeasPermissions(t *testing.T) {
	ts := newTestServer(t)
	c := ts.login(t)
	ts.d.mu.Lock()
	ts.d.answers["approval_list"] = []map[string]any{{"id": 3, "agent": "lea-a/dev-1"}, {"id": 4, "agent": "lea-b/dev-1"}}
	ts.d.mu.Unlock()
	if code, _ := ts.post(t, "approval_decide", map[string]any{"id": 4, "verdict": "allow"}, c, nil); code != 400 {
		t.Fatalf("another lea's: %d", code)
	}
	if code, _ := ts.post(t, "approval_decide", map[string]any{"id": 3, "verdict": "always"}, c, nil); code != 200 {
		t.Fatalf("ours: %d", code)
	}
	if p := ts.d.last("approval_decide"); p["id"] != float64(3) || p["verdict"] != "always" {
		t.Fatalf("approval_decide params %v", p)
	}
}

// readEvent reads one server-sent event: its name and data.
func readEvent(t *testing.T, r *bufio.Reader) (string, string) {
	t.Helper()
	name, data := "", ""
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("stream ended: %v", err)
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case line == "" && name != "":
			return name, data
		case strings.HasPrefix(line, "event: "):
			name = line[7:]
		case strings.HasPrefix(line, "data: "):
			data = line[6:]
		}
	}
}

func TestTheEventStreamPushesEachChangeAndAttachesAsADeck(t *testing.T) {
	ts := newTestServer(t)
	c := ts.login(t)
	ts.d.mu.Lock()
	ts.d.answers["board_list"] = map[string]any{"items": []map[string]any{{"agent": "dev-1", "status": "testing", "next": "CI"}}}
	ts.d.answers["approval_list"] = []map[string]any{{"id": 7, "agent": "lea-a/dev-1", "tool": "Bash"}, {"id": 8, "agent": "lea-b/x", "tool": "Bash"}}
	ts.d.answers["question_list"] = []map[string]any{{"id": 2, "agent": "lea-b/claude-1"}}
	ts.d.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.url+"/api/events", nil)
	req.AddCookie(c)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("events: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	r := bufio.NewReader(resp.Body)
	name, data := readEvent(t, r)
	var st State
	if err := json.Unmarshal([]byte(data), &st); err != nil || name != "state" {
		t.Fatalf("first event %s %s: %v", name, data, err)
	}
	if st.Version != 1 || st.Container != "lea-a" || len(st.Tasks) != 1 || len(st.Approvals) != 1 || len(st.Questions) != 0 {
		t.Fatalf("state %+v", st)
	}
	if p := ts.d.nth("agents_watch", 0); p["container"] != "lea-a" || p["since"] != float64(-1) {
		t.Fatalf("first watch %v", p)
	}
	for i := 0; i < 100 && ts.d.attachedNow() != 1; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if n := ts.d.attachedNow(); n != 1 {
		t.Fatalf("%d web decks attached, want 1", n)
	}
	if p := ts.d.last("deck_web_attach"); p["container"] != "lea-a" {
		t.Fatalf("attach %v", p)
	}
	// A change: the next state comes at once.
	began := time.Now()
	ts.d.mu.Lock()
	ts.d.version = 2
	ts.d.mu.Unlock()
	ts.d.bump <- struct{}{}
	_, data = readEvent(t, r)
	json.Unmarshal([]byte(data), &st)
	if st.Version != 2 || time.Since(began) > time.Second {
		t.Fatalf("version %d after %s", st.Version, time.Since(began))
	}
	if p := ts.d.nth("agents_watch", 1); p["since"] != float64(1) {
		t.Fatalf("second watch %v", p)
	}
	// The browser leaves: the deck detaches.
	cancel()
	resp.Body.Close()
	for i := 0; i < 300 && ts.d.attachedNow() != 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if n := ts.d.attachedNow(); n != 0 {
		t.Fatalf("%d web decks still attached after the browser left", n)
	}
}

func TestTheEventStreamSaysWhenTheDaemonIsDown(t *testing.T) {
	d := newFakeDaemon(t)
	s, _ := New(SocketCaller(d.sock+".missing"), "lea-a")
	s.retry = func(ctx context.Context) { time.Sleep(10 * time.Millisecond) }
	s.setPort(80)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest("GET", "/api/events", nil).WithContext(ctx)
	req.Host = "127.0.0.1:80"
	req.AddCookie(&http.Cookie{Name: cookieName, Value: s.session})
	pr, pw := io.Pipe()
	w := &streamRecorder{ResponseRecorder: httptest.NewRecorder(), w: pw}
	go func() { s.Handler().ServeHTTP(w, req); pw.Close() }()
	name, data := readEvent(t, bufio.NewReader(pr))
	cancel()
	go io.Copy(io.Discard, pr)
	if name != "lea-error" || !strings.Contains(data, "does not answer") {
		t.Fatalf("event %s %s", name, data)
	}
}

// streamRecorder is a ResponseRecorder whose body goes to w as it is written.
type streamRecorder struct {
	*httptest.ResponseRecorder
	w io.Writer
}

func (s *streamRecorder) Write(b []byte) (int, error) { return s.w.Write(b) }
func (s *streamRecorder) Flush()                      {}
