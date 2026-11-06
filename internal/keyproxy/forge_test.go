package keyproxy

import (
	"bytes"
	"encoding/base64"
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
)

// forgeRig is a forge forwarder whose GitHub hosts all lead to one TLS echo, through a forward proxy.
type forgeRig struct {
	fw     *httptest.Server
	proxy  *fwdProxy
	keyDir string
	mu     sync.Mutex
	seen   []*http.Request
	bodies []string
	logs   *syncBuf
	hosts  []string // the hosts the upstream function was asked for
}

func newForgeRig(t *testing.T) *forgeRig {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "")
	r := &forgeRig{proxy: newFwdProxy(t), keyDir: t.TempDir(), logs: &syncBuf{b: &bytes.Buffer{}}}
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		b, _ := io.ReadAll(q.Body)
		r.mu.Lock()
		r.seen = append(r.seen, q.Clone(q.Context()))
		r.bodies = append(r.bodies, string(b))
		r.mu.Unlock()
		w.Header().Set("Set-Cookie", "user_session=x")
		w.Header().Set("Location", "https://objects.githubusercontent.com/x")
		_, _ = io.WriteString(w, q.Method+" "+q.URL.RequestURI())
	}))
	t.Cleanup(up.Close)
	via, _ := url.Parse(r.proxy.URL)
	f, err := New(Config{Via: via, RootCAs: certPool(up), Log: log.New(r.logs, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	upURL, _ := url.Parse(up.URL)
	g := f.Forge(r.keyDir, func(h string) *url.URL {
		r.mu.Lock()
		r.hosts = append(r.hosts, h)
		r.mu.Unlock()
		u := *upURL
		return &u
	}).(*forge)
	g.self = testSelf
	r.fw = httptest.NewServer(g)
	t.Cleanup(r.fw.Close)
	return r
}

func (r *forgeRig) last(t *testing.T) (*http.Request, string, string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.seen) == 0 {
		t.Fatal("nothing reached the upstream")
	}
	return r.seen[len(r.seen)-1], r.bodies[len(r.bodies)-1], r.hosts[len(r.hosts)-1]
}

func (r *forgeRig) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.seen)
}

// send is a raw request with Host host to the forge forwarder.
func (r *forgeRig) send(t *testing.T, method, host, target string, hdr map[string]string) (int, http.Header, string) {
	t.Helper()
	h := map[string]string{"Host": host}
	for k, v := range hdr {
		h[k] = v
	}
	return rawHost(t, r.fw.URL, method, target, h)
}

// rawHost is raw with the Host header of hdr.
func rawHost(t *testing.T, addr, method, target string, hdr map[string]string) (int, http.Header, string) {
	t.Helper()
	c, err := net.Dial("tcp", strings.TrimPrefix(addr, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var b strings.Builder
	b.WriteString(method + " " + target + " HTTP/1.1\r\nConnection: close\r\n")
	for k, v := range hdr {
		b.WriteString(k + ": " + v + "\r\n")
	}
	b.WriteString("\r\n")
	if _, err := io.WriteString(c, b.String()); err != nil {
		t.Fatal(err)
	}
	res, err := http.ReadResponse(bufioReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, string(body)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

const testToken = "ghp_testTokenNotASecret"

// testSelf is the proxy's address in the tests.
func testSelf(ip net.IP) bool { return ip.Equal(net.ParseIP("172.18.0.5")) }

func TestForgeInjectsTheTokenForGitHubOnly(t *testing.T) {
	r := newForgeRig(t)
	writeKey(t, r.keyDir, GitHubTokenFile, testToken)
	agent := map[string]string{"Authorization": "token lea-proxy-injects-the-token", "Cookie": "c=1",
		"Proxy-Authorization": "Basic eA==", SourceHeader: "lea-evil", "X-Forwarded-For": "1.2.3.4", "Content-Length": "0"}

	// gh, through the socket: Host api.github.com.
	code, hdr, body := r.send(t, "GET", "api.github.com", "/user?per_page=1", agent)
	if code != 200 || body != "GET /user?per_page=1" {
		t.Fatalf("api: %d %q", code, body)
	}
	req, _, host := r.last(t)
	if host != "api.github.com" || req.Header.Get("Authorization") != "Bearer "+testToken {
		t.Errorf("api: host %q, Authorization %q", host, req.Header.Get("Authorization"))
	}
	for _, k := range []string{"Cookie", "Proxy-Authorization", SourceHeader, "X-Forwarded-For"} {
		if req.Header.Get(k) != "" {
			t.Errorf("api: the agent's %s reached the upstream", k)
		}
	}
	if hdr.Get("Set-Cookie") != "" || hdr.Get("Location") == "" {
		t.Errorf("api: response headers %v", hdr)
	}

	// git, through url.insteadOf: Host lea-proxy:4401, upstream github.com, Basic.
	code, _, _ = r.send(t, "POST", "lea-proxy:4401", "/o/r.git/git-upload-pack", map[string]string{"Content-Length": "0"})
	if code != 200 {
		t.Fatalf("git: %d", code)
	}
	req, _, host = r.last(t)
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+testToken))
	if host != "github.com" || req.Header.Get("Authorization") != want || req.URL.Path != "/o/r.git/git-upload-pack" {
		t.Errorf("git: host %q, Authorization %q, path %q", host, req.Header.Get("Authorization"), req.URL.Path)
	}
	// git at the proxy's address (a pinned push): github.com too.
	r.send(t, "GET", "172.18.0.5:4401", "/o/r.git/info/refs?service=git-upload-pack", nil)
	if req, _, host = r.last(t); host != "github.com" || req.Header.Get("Authorization") != want {
		t.Errorf("git at the proxy's address: host %q, Authorization %q", host, req.Header.Get("Authorization"))
	}
	for _, m := range []string{"PUT", "PATCH", "DELETE", "HEAD"} {
		if code, _, _ := r.send(t, m, "api.github.com", "/repos/o/r/pulls/1/merge", nil); code != 200 {
			t.Errorf("%s: %d", m, code)
		}
	}

	// Release assets and raw files: forwarded, never with the token.
	for _, h := range []string{"objects.githubusercontent.com", "codeload.github.com", "raw.githubusercontent.com"} {
		if code, _, _ := r.send(t, "GET", h, "/x", map[string]string{"Authorization": "Bearer " + testToken}); code != 200 {
			t.Fatalf("%s: %d", h, code)
		}
		req, _, host := r.last(t)
		if host != h || req.Header.Get("Authorization") != "" {
			t.Errorf("%s: host %q, Authorization %q", h, host, req.Header.Get("Authorization"))
		}
	}
	// A redirect gh follows (job logs, artifacts: GitHub sends them to Azure blob storage), and
	// other forges' hosts: to their own host, never github.com, never with the token.
	sas := "/actions-results/abc/logs/job.txt?rsct=text%2Fplain&se=2026-11-06T10%3A00%3A00Z&sig=SASsig"
	for _, c := range []struct{ host, up string }{
		{"productionresultssa1.blob.core.windows.net", "productionresultssa1.blob.core.windows.net"},
		{"x.blob.core.windows.net:443", "x.blob.core.windows.net"},
		{"ghes.example.com", "ghes.example.com"},
		{"ghes.example.com:8443", "ghes.example.com:8443"},
		{"acme.ghe.com", "acme.ghe.com"},
		{"api.githubcopilot.com", "api.githubcopilot.com"},
		{"gist.github.com", "gist.github.com"},
	} {
		code, _, body := r.send(t, "GET", c.host, sas, map[string]string{"Authorization": "token lea-proxy-adds-the-token"})
		if code != 200 || body != "GET "+sas {
			t.Fatalf("%s: %d %q", c.host, code, body)
		}
		req, _, host := r.last(t)
		if host != c.up || req.Header.Get("Authorization") != "" || req.URL.RawQuery != strings.SplitN(sas, "?", 2)[1] {
			t.Errorf("%s: upstream %q, Authorization %q, query %q", c.host, host, req.Header.Get("Authorization"), req.URL.RawQuery)
		}
	}
	if strings.Contains(r.logs.String(), testToken) || strings.Contains(r.logs.String(), "per_page") || strings.Contains(r.logs.String(), "SASsig") {
		t.Errorf("the log holds the token or a query: %s", r.logs.String())
	}
}

func TestForgeWithoutATokenGoesOutAnonymous(t *testing.T) {
	r := newForgeRig(t)
	if code, _, _ := r.send(t, "GET", "api.github.com", "/rate_limit", map[string]string{"Authorization": "token placeholder"}); code != 200 {
		t.Fatalf("%d", code)
	}
	if req, _, _ := r.last(t); req.Header.Get("Authorization") != "" {
		t.Errorf("Authorization %q", req.Header.Get("Authorization"))
	}
	// Rotated in place: the next request reads it.
	writeKey(t, r.keyDir, GitHubTokenFile, testToken)
	r.send(t, "GET", "api.github.com", "/rate_limit", nil)
	if req, _, _ := r.last(t); req.Header.Get("Authorization") != "Bearer "+testToken {
		t.Errorf("after login: Authorization %q", req.Header.Get("Authorization"))
	}
	if err := os.Remove(filepath.Join(r.keyDir, GitHubTokenFile)); err != nil {
		t.Fatal(err)
	}
	r.send(t, "GET", "api.github.com", "/rate_limit", nil)
	if req, _, _ := r.last(t); req.Header.Get("Authorization") != "" {
		t.Errorf("after logout: Authorization %q", req.Header.Get("Authorization"))
	}
}

func TestForgeRefusals(t *testing.T) {
	r := newForgeRig(t)
	writeKey(t, r.keyDir, GitHubTokenFile, testToken)
	for _, c := range []struct {
		name, method, host, target string
		code                       int
	}{
		{"CONNECT", "CONNECT", "api.github.com", "api.github.com:443", http.StatusMethodNotAllowed},
		{"TRACE", "TRACE", "api.github.com", "/user", http.StatusMethodNotAllowed},
		{"absolute form", "GET", "evil.example", "http://evil.example/x", http.StatusBadRequest},
		{"dot segment", "GET", "api.github.com", "/repos/../user", http.StatusBadRequest},
		{"escaped dot segment", "GET", "api.github.com", "/repos/%2e%2e/user", http.StatusBadRequest},
		{"escaped slash", "GET", "api.github.com", "/repos/a%2fb", http.StatusBadRequest},
		{"bad escape", "GET", "api.github.com", "/x%zz", http.StatusBadRequest},
		{"backslash", "GET", "api.github.com", "/x\\y", http.StatusBadRequest},
		{"another port of the proxy's", "GET", "lea-proxy:3128", "/x", http.StatusBadRequest},
		{"another port of the proxy's address", "GET", "172.18.0.5:4400", "/x", http.StatusBadRequest},
		{"a bad host name", "GET", "a_b.example", "/x", http.StatusBadRequest},
		{"a bad port", "GET", "api.github.com:x", "/x", http.StatusBadRequest},
		{"new SSH key", "POST", "api.github.com", "/user/keys", http.StatusForbidden},
		{"new GPG key", "POST", "api.github.com", "/user/gpg_keys", http.StatusForbidden},
		{"new signing key", "POST", "api.github.com", "/user/ssh_signing_keys", http.StatusForbidden},
		{"deploy key", "POST", "api.github.com", "/repos/o/r/keys", http.StatusForbidden},
		{"deploy key, case", "POST", "api.github.com", "/Repos/o/r/Keys", http.StatusForbidden},
		{"repo webhook", "POST", "api.github.com", "/repos/o/r/hooks", http.StatusForbidden},
		{"repo webhook edit", "PATCH", "api.github.com", "/repos/o/r/hooks/1/config", http.StatusForbidden},
		{"org webhook", "POST", "api.github.com", "/orgs/o/hooks", http.StatusForbidden},
		{"authorization", "POST", "api.github.com", "/authorizations", http.StatusForbidden},
		{"application token", "PATCH", "api.github.com", "/applications/x/token", http.StatusForbidden},
	} {
		if code, _, _ := r.send(t, c.method, c.host, c.target, nil); code != c.code {
			t.Errorf("%s: %d, want %d", c.name, code, c.code)
		}
	}
	if n := r.count(); n != 0 {
		t.Errorf("%d refused requests reached the upstream", n)
	}
	// Reads of the same paths are fine.
	for _, p := range []string{"/user/keys", "/repos/o/r/hooks"} {
		if code, _, _ := r.send(t, "GET", "api.github.com", p, nil); code != 200 {
			t.Errorf("GET %s: %d", p, code)
		}
	}
	if code, _, _ := r.send(t, "GET", "api.github.com", "/x", map[string]string{"Content-Length": "3000000000"}); code != http.StatusRequestEntityTooLarge {
		t.Errorf("huge body: %d", code)
	}
}

func TestForgeHost(t *testing.T) {
	for in, want := range map[string]string{
		"api.github.com": "api.github.com", "API.GitHub.com.": "api.github.com", "api.github.com:443": "api.github.com",
		"uploads.github.com": "uploads.github.com", "github.com": "github.com", "codeload.github.com": "codeload.github.com",
		"objects.githubusercontent.com": "objects.githubusercontent.com",
		// The forwarder's own address: git, upstream github.com.
		"lea-proxy": "github.com", "lea-proxy:4401": "github.com", "LEA-PROXY.:4401": "github.com",
		"172.18.0.5": "github.com", "172.18.0.5:4401": "github.com",
		// Anything else is its own upstream, not github.com.
		"x.blob.core.windows.net": "x.blob.core.windows.net", "productionresultssa12.blob.core.windows.net:443": "productionresultssa12.blob.core.windows.net",
		"ghes.example.com:8443": "ghes.example.com:8443", "acme.ghe.com": "acme.ghe.com", "api.githubcopilot.com": "api.githubcopilot.com",
		"notgithub.com": "notgithub.com", "10.0.0.9:4401": "10.0.0.9:4401", "[fd00::1]:443": "[fd00::1]",
		// Refused.
		"": "", "lea-proxy:3128": "", "172.18.0.5:80": "", "a_b.example": "", "x..example": "", "api.github.com:0": "",
		"api.github.com:065": "", "api.github.com:99999": "", "evil.example/x": "", "u@evil.example": "",
	} {
		if got, _ := forgeHost(in, testSelf); got != want {
			t.Errorf("forgeHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLocalIP(t *testing.T) {
	if !localIP(net.ParseIP("127.0.0.1")) {
		t.Error("127.0.0.1 is not local")
	}
	if localIP(net.ParseIP("192.0.2.77")) {
		t.Error("192.0.2.77 (TEST-NET-1) is local")
	}
}

func TestForgeRefusesTheEgressNetwork(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "")
	via, _ := url.Parse("http://127.0.0.1:1")
	f, err := New(Config{Via: via, Log: log.New(io.Discard, "", 0), Refused: func(net.IP) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	fw := httptest.NewServer(f.Forge(t.TempDir(), nil))
	defer fw.Close()
	if code, _, _ := rawHost(t, fw.URL, "GET", "/user", map[string]string{"Host": "api.github.com"}); code != http.StatusForbidden {
		t.Errorf("%d", code)
	}
}
