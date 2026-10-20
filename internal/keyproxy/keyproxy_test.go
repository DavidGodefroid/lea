package keyproxy

import (
	"bufio"
	"bytes"
	"crypto/x509"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/providers"
)

// fwdProxy is a forward proxy (absolute-form HTTP and CONNECT) that records what it carried.
type fwdProxy struct {
	*httptest.Server
	mu   sync.Mutex
	seen []string
}

func newFwdProxy(t *testing.T) *fwdProxy {
	p := &fwdProxy{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.seen = append(p.seen, r.Method+" "+r.RequestURI)
		p.mu.Unlock()
		if r.Method == http.MethodConnect {
			up, err := net.Dial("tcp", r.Host)
			if err != nil {
				http.Error(w, "dial", http.StatusBadGateway)
				return
			}
			conn, buf, err := http.NewResponseController(w).Hijack()
			if err != nil {
				up.Close()
				return
			}
			_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n")
			go func() { _, _ = io.Copy(up, buf); up.Close() }()
			_, _ = io.Copy(conn, up)
			conn.Close()
			return
		}
		if r.URL.Host == "" {
			http.Error(w, "not a proxy request", http.StatusBadRequest)
			return
		}
		r.RequestURI = ""
		res, err := (&http.Transport{Proxy: nil}).RoundTrip(r)
		if err != nil {
			http.Error(w, "upstream", http.StatusBadGateway)
			return
		}
		defer res.Body.Close()
		for k, v := range res.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(res.StatusCode)
		_, _ = io.Copy(w, res.Body)
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *fwdProxy) carried() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.seen...)
}

// echo is an upstream that answers with what it received.
type echo struct {
	*httptest.Server
	mu   sync.Mutex
	last *http.Request
	body []byte
}

func newEcho(t *testing.T) *echo {
	e := &echo{}
	e.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		e.mu.Lock()
		e.last, e.body = r.Clone(r.Context()), b
		e.mu.Unlock()
		w.Header().Set("Set-Cookie", "session=upstream")
		w.Header().Set("X-Upstream", "yes")
		_, _ = io.WriteString(w, "path="+r.URL.EscapedPath()+" query="+r.URL.RawQuery)
	}))
	t.Cleanup(e.Close)
	return e
}

func (e *echo) request() (*http.Request, []byte) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.last, e.body
}

func writeKey(t *testing.T, dir, name, key string) string {
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

type rig struct {
	fw    *httptest.Server
	proxy *fwdProxy
	up    *echo
	logs  *bytes.Buffer
	key   string
}

func newRig(t *testing.T) *rig {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "")
	r := &rig{proxy: newFwdProxy(t), up: newEcho(t), logs: &bytes.Buffer{}}
	r.key = writeKey(t, t.TempDir(), "vllm", "sk-secret-123")
	base, _ := url.Parse(r.up.URL + "/v1")
	via, _ := url.Parse(r.proxy.URL)
	f, err := New(Config{
		Endpoints: []Endpoint{{Name: "vllm", URL: base, KeyFile: r.key}, {Name: "ollama", URL: base}},
		Via:       via,
		Log:       log.New(&syncBuf{b: r.logs}, "", 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	r.fw = httptest.NewServer(f)
	t.Cleanup(r.fw.Close)
	return r
}

type syncBuf struct {
	mu sync.Mutex
	b  *bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

// raw sends a request with a raw request target (no client-side cleaning).
func raw(t *testing.T, addr, method, target string, hdr map[string]string) (int, http.Header, string) {
	t.Helper()
	c, err := net.Dial("tcp", strings.TrimPrefix(addr, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var b strings.Builder
	b.WriteString(method + " " + target + " HTTP/1.1\r\nHost: lea-proxy:4400\r\nConnection: close\r\n")
	for k, v := range hdr {
		b.WriteString(k + ": " + v + "\r\n")
	}
	b.WriteString("\r\n")
	if _, err := io.WriteString(c, b.String()); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	res, err := http.ReadResponse(bufioReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, string(body)
}

func TestRoutingAndKeyInjection(t *testing.T) {
	r := newRig(t)
	code, hdr, body := raw(t, r.fw.URL, "POST", "/vllm/responses?stream=true&a=b", map[string]string{
		"Authorization": "Bearer agent-token", "Proxy-Authorization": "Basic x", "X-Api-Key": "agent",
		"Api-Key": "agent", "Cookie": "c=1", "Forwarded": "for=1.2.3.4", "X-Forwarded-For": "1.2.3.4",
		"X-Forwarded-Host": "evil", "Content-Length": "0", "X-Keep": "kept",
	})
	if code != 200 || body != "path=/v1/responses query=stream=true&a=b" {
		t.Fatalf("got %d %q", code, body)
	}
	if hdr.Get("Set-Cookie") != "" || hdr.Get("X-Upstream") != "yes" {
		t.Errorf("response headers: %v", hdr)
	}
	in, _ := r.up.request()
	if got := in.Header.Values("Authorization"); len(got) != 1 || got[0] != "Bearer sk-secret-123" {
		t.Errorf("Authorization = %q", got)
	}
	for _, h := range []string{"Proxy-Authorization", "X-Api-Key", "Api-Key", "Cookie", "Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
		if v := in.Header.Get(h); v != "" {
			t.Errorf("%s reached the upstream: %q", h, v)
		}
	}
	if in.Header.Get("X-Keep") != "kept" {
		t.Error("an ordinary header was dropped")
	}
	if want := strings.TrimPrefix(r.up.URL, "http://"); in.Host != want {
		t.Errorf("Host = %q, want %q", in.Host, want)
	}
	// Without a key: the agent's credentials are still dropped and none is added.
	code, _, body = raw(t, r.fw.URL, "GET", "/ollama/models", map[string]string{"Authorization": "Bearer agent-token"})
	if code != 200 || body != "path=/v1/models query=" {
		t.Fatalf("ollama: %d %q", code, body)
	}
	if in, _ = r.up.request(); in.Header.Get("Authorization") != "" {
		t.Errorf("Authorization reached a keyless upstream: %q", in.Header.Get("Authorization"))
	}
	// /<name> alone and /<name>/ map to the base path.
	if _, _, body = raw(t, r.fw.URL, "GET", "/ollama", nil); body != "path=/v1 query=" {
		t.Errorf("/ollama -> %q", body)
	}
	if _, _, body = raw(t, r.fw.URL, "GET", "/ollama/", nil); body != "path=/v1/ query=" {
		t.Errorf("/ollama/ -> %q", body)
	}
	// Every upstream request went through the -via proxy, none through HTTP_PROXY.
	if got := r.proxy.carried(); len(got) != 4 || !strings.HasPrefix(got[0], "POST "+r.up.URL+"/v1/responses") {
		t.Errorf("proxy carried %q", got)
	}
	logs := r.logs.String()
	for _, secret := range []string{"sk-secret-123", "agent-token", "stream=true", "1.2.3.4"} {
		if strings.Contains(logs, secret) {
			t.Errorf("the log holds %q:\n%s", secret, logs)
		}
	}
	if !strings.Contains(logs, `vllm "POST" "/vllm/responses" 200`) {
		t.Errorf("log line missing:\n%s", logs)
	}
}

func TestKeyRotationAndMissingKey(t *testing.T) {
	r := newRig(t)
	if code, _, _ := raw(t, r.fw.URL, "GET", "/vllm/models", nil); code != 200 {
		t.Fatal(code)
	}
	writeKey(t, filepath.Dir(r.key), "vllm", "sk-rotated")
	if code, _, _ := raw(t, r.fw.URL, "GET", "/vllm/models", nil); code != 200 {
		t.Fatal(code)
	}
	if in, _ := r.up.request(); in.Header.Get("Authorization") != "Bearer sk-rotated" {
		t.Errorf("after rotation: %q", in.Header.Get("Authorization"))
	}
	n := len(r.proxy.carried())
	if err := os.Remove(r.key); err != nil {
		t.Fatal(err)
	}
	code, _, body := raw(t, r.fw.URL, "GET", "/vllm/models", nil)
	if code != http.StatusBadGateway || strings.Contains(body, r.key) || strings.Contains(body, "file") {
		t.Errorf("missing key: %d %q", code, body)
	}
	if len(r.proxy.carried()) != n {
		t.Error("a request without its key reached the upstream")
	}
	writeKey(t, filepath.Dir(r.key), "vllm", "two words")
	if code, _, _ := raw(t, r.fw.URL, "GET", "/vllm/models", nil); code != http.StatusBadGateway {
		t.Errorf("invalid key: %d", code)
	}
	if strings.Contains(r.logs.String(), "two words") {
		t.Error("the log holds the key")
	}
}

func TestRefusals(t *testing.T) {
	r := newRig(t)
	cases := []struct {
		method, target string
		want           int
	}{
		{"GET", "/nope/models", 404},
		{"GET", "/", 400},
		{"GET", "/vllm/../ollama/models", 400},
		{"GET", "/vllm/a/../../x", 400},
		{"GET", "/vllm/./models", 400},
		{"GET", "/vllm/..", 400},
		{"GET", "/vllm/%2e%2e/x", 400},
		{"GET", "/vllm/%2E./x", 400},
		{"GET", "/vllm/a%2fb", 400},
		{"GET", "/vllm/a%2Fb", 400},
		{"GET", "/vllm/a%5cb", 400},
		{"GET", "/vllm/a%5Cb", 400},
		{"GET", "/vllm/a%252e", 400},
		{"GET", "/vllm/a%00", 400},
		{"GET", "/vllm/a\\b", 400},
		{"GET", "/vllm//models", 400},
		{"GET", "//vllm/models", 400},
		{"GET", "/vllm/a%zz", 400},
		{"GET", "http://evil.test/vllm/models", 400},
		{"PUT", "/vllm/models", 405},
		{"DELETE", "/vllm/models", 405},
		{"OPTIONS", "/vllm/models", 405},
		{"HEAD", "/vllm/models", 405},
		{"CONNECT", "evil.test:443", 405},
	}
	for _, c := range cases {
		code, _, _ := raw(t, r.fw.URL, c.method, c.target, nil)
		if code != c.want {
			t.Errorf("%s %s: got %d, want %d", c.method, c.target, code, c.want)
		}
	}
	if code, _, _ := raw(t, r.fw.URL, "GET", "/vllm/models", map[string]string{"Connection": "Upgrade", "Upgrade": "websocket"}); code != 400 {
		t.Errorf("upgrade: %d", code)
	}
	if code, _, _ := raw(t, r.fw.URL, "POST", "/vllm/models", map[string]string{"Content-Length": "67108865"}); code != 413 {
		t.Errorf("oversized body: %d", code)
	}
	if got := r.proxy.carried(); len(got) != 0 {
		t.Errorf("refused requests reached the proxy: %q", got)
	}
	// An encoded ordinary character is forwarded as sent.
	if _, _, body := raw(t, r.fw.URL, "GET", "/ollama/a%20b", nil); body != "path=/v1/a%20b query=" {
		t.Errorf("encoded space: %q", body)
	}
}

func TestBodyCapStreaming(t *testing.T) {
	r := newRig(t)
	pr, pw := io.Pipe()
	go func() {
		chunk := bytes.Repeat([]byte("a"), 1<<20)
		for i := 0; i < 65; i++ {
			if _, err := pw.Write(chunk); err != nil {
				return
			}
		}
		pw.Close()
	}()
	req, _ := http.NewRequest("POST", r.fw.URL+"/ollama/responses", pr)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return // the forwarder may close the connection mid-upload
	}
	res.Body.Close()
	if res.StatusCode == 200 {
		if _, body := r.up.request(); len(body) > MaxBodyBytes {
			t.Errorf("upstream got %d bytes", len(body))
		}
	}
}

func TestTLSVerified(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "")
	p := newFwdProxy(t)
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer up.Close()
	base, _ := url.Parse(up.URL + "/v1")
	via, _ := url.Parse(p.URL)
	logs := &bytes.Buffer{}
	f, err := New(Config{Endpoints: []Endpoint{{Name: "tls", URL: base}}, Via: via, Log: log.New(&syncBuf{b: logs}, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	fw := httptest.NewServer(f)
	defer fw.Close()
	if code, _, _ := raw(t, fw.URL, "GET", "/tls/models", nil); code != http.StatusBadGateway {
		t.Errorf("self-signed upstream: %d", code)
	}
	if got := p.carried(); len(got) != 1 || !strings.HasPrefix(got[0], "CONNECT ") {
		t.Errorf("proxy carried %q", got)
	}
	if !strings.Contains(logs.String(), "upstream TLS error") {
		t.Errorf("log: %s", logs)
	}
	// With the test CA trusted, the same upstream works, still through the proxy.
	f, _ = New(Config{Endpoints: []Endpoint{{Name: "tls", URL: base}}, Via: via, RootCAs: certPool(up), Log: log.New(io.Discard, "", 0)})
	fw2 := httptest.NewServer(f)
	defer fw2.Close()
	if code, _, body := raw(t, fw2.URL, "GET", "/tls/models", nil); code != 200 || body != "ok" {
		t.Errorf("trusted upstream: %d %q", code, body)
	}
}

func TestViaUnreachable(t *testing.T) {
	up := newEcho(t)
	base, _ := url.Parse(up.URL + "/v1")
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	via, _ := url.Parse("http://" + addr)
	f, _ := New(Config{Endpoints: []Endpoint{{Name: "x", URL: base}}, Via: via, Log: log.New(io.Discard, "", 0)})
	fw := httptest.NewServer(f)
	defer fw.Close()
	if code, _, _ := raw(t, fw.URL, "GET", "/x/models", nil); code != http.StatusBadGateway {
		t.Errorf("no proxy: %d", code)
	}
	if in, _ := up.request(); in != nil {
		t.Error("the upstream was reached without the proxy")
	}
}

func TestNewRejectsBadVia(t *testing.T) {
	for _, v := range []string{"", "https://127.0.0.1:3128", "http://127.0.0.1", "http://u:p@127.0.0.1:3128", "socks5://127.0.0.1:1080", "http://127.0.0.1:3128/x"} {
		u, _ := url.Parse(v)
		if v == "" {
			u = nil
		}
		if _, err := New(Config{Via: u}); err == nil {
			t.Errorf("via %q accepted", v)
		}
	}
}

func TestParseEndpoints(t *testing.T) {
	hosts := providers.ParseHosts(strings.NewReader("10.0.0.5 11434\nvllm.lan 8000\n"))
	keyDir := "/etc/lea/keys"
	good := "# comment\n\nollama http://10.0.0.5:11434/v1\nvllm https://vllm.example.com/v1 /etc/lea/keys/vllm # key\nlan http://vllm.lan:8000/v1 /etc/lea/keys/lan\n"
	eps, err := ParseEndpoints(strings.NewReader(good), hosts, keyDir)
	if err != nil || len(eps) != 3 || eps[1].KeyFile != "/etc/lea/keys/vllm" || eps[0].URL.Host != "10.0.0.5:11434" {
		t.Fatalf("%v %+v", err, eps)
	}
	bad := map[string]string{
		"one field":                  "ollama\n",
		"four fields":                "a http://x.example.com/v1 /etc/lea/keys/a extra\n",
		"bad name":                   "Ollama http://x.example.com/v1\n",
		"long name":                  "abcdefghijklmnopq http://x.example.com/v1\n",
		"duplicate":                  "a https://x.example.com/v1\na https://y.example.com/v1\n",
		"key outside KeyDir":         "a https://x.example.com/v1 /etc/passwd\n",
		"key traversal":              "a https://x.example.com/v1 /etc/lea/keys/../passwd\n",
		"key dir itself":             "a https://x.example.com/v1 /etc/lea/keys\n",
		"key relative":               "a https://x.example.com/v1 keys/a\n",
		"key prefix trick":           "a https://x.example.com/v1 /etc/lea/keysx/a\n",
		"private not granted":        "a http://10.0.0.6:11434/v1\n",
		"granted host other port":    "a http://10.0.0.5:8080/v1\n",
		"localhost":                  "a http://localhost:11434/v1\n",
		"key over http, not granted": "a http://x.example.com/v1 /etc/lea/keys/a\n",
		"ftp":                        "a ftp://x.example.com/v1\n",
		"query":                      "a https://x.example.com/v1?x=1\n",
		"user info":                  "a https://u:p@x.example.com/v1\n",
		"ipv6":                       "a http://[::1]:80/v1\n",
	}
	for name, in := range bad {
		_, err := ParseEndpoints(strings.NewReader("# first\n"+in), hosts, keyDir)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if !strings.HasPrefix(err.Error(), "line ") {
			t.Errorf("%s: error without a line number: %v", name, err)
		}
	}
	_, err = ParseEndpoints(strings.NewReader("ok https://x.example.com/v1\nbad\n"), hosts, keyDir)
	if err == nil || !strings.HasPrefix(err.Error(), "line 2:") {
		t.Errorf("line number: %v", err)
	}
	var many strings.Builder
	for i := 0; i <= providers.MaxEndpoints; i++ {
		many.WriteString("e" + strings.Repeat("a", i%10) + string(rune('a'+i)) + " https://x.example.com/v1\n")
	}
	if _, err := ParseEndpoints(strings.NewReader(many.String()), hosts, keyDir); err == nil {
		t.Error("too many endpoints accepted")
	}
}

func TestCleanRawPath(t *testing.T) {
	for _, p := range []string{"/a", "/a/", "/a/b/c", "/a/b%20c", "/a/v1:generate", "/a/~x"} {
		if !cleanRawPath(p) {
			t.Errorf("%q refused", p)
		}
	}
}

func bufioReader(c net.Conn) *bufio.Reader { return bufio.NewReader(c) }

func certPool(s *httptest.Server) *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(s.Certificate())
	return pool
}
