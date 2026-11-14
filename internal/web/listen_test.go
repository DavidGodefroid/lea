package web

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeNet makes this host have lo (127.0.0.1), eth0 (192.0.2.10), docker0 (172.17.0.1),
// br-1a2b (172.18.0.1) and eth1 (198.51.100.7), and the container networks 172.17.0.0/16,
// 172.18.0.0/16 and 198.51.100.0/24; binding 192.0.2.10 binds the loopback.
func fakeNet(t *testing.T, subnetsErr error) {
	t.Helper()
	oldA, oldS, oldL := hostAddrs, containerSubnets, listenTCP
	t.Cleanup(func() { hostAddrs, containerSubnets, listenTCP = oldA, oldS, oldL })
	hostAddrs = func() ([]hostAddr, error) {
		return []hostAddr{{"lo", net.ParseIP("127.0.0.1")}, {"eth0", net.ParseIP("192.0.2.10")},
			{"docker0", net.ParseIP("172.17.0.1")}, {"br-1a2b", net.ParseIP("172.18.0.1")},
			{"eth1", net.ParseIP("198.51.100.7")}, {"eth0", net.ParseIP("2001:db8::10")}}, nil
	}
	containerSubnets = func() ([]*net.IPNet, error) {
		if subnetsErr != nil {
			return nil, subnetsErr
		}
		var out []*net.IPNet
		for _, c := range []string{"172.17.0.0/16", "172.18.0.0/16", "198.51.100.0/24"} {
			_, n, _ := net.ParseCIDR(c)
			out = append(out, n)
		}
		return out, nil
	}
	listenTCP = func(string) (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }
}

func remoteServer(t *testing.T, call Caller) *Server {
	t.Helper()
	if call == nil {
		call = SocketCaller("/nonexistent")
	}
	s, err := New(call, "lea-a")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestListenRefusesTheNetworkWithoutTLS(t *testing.T) {
	fakeNet(t, nil)
	dir := t.TempDir()
	for _, o := range []RemoteOptions{
		{Addr: "192.0.2.10:8443", Dir: dir},
		{Addr: "192.0.2.10:8443", Dir: dir, CertFile: "c.pem"},
		{Addr: "192.0.2.10:8443", Dir: dir, KeyFile: "k.pem"},
		{Addr: "192.0.2.10:8443", Dir: dir, SelfSigned: true, CertFile: "c.pem", KeyFile: "k.pem"},
	} {
		if l, _, err := remoteServer(t, nil).ListenRemote(o); err == nil {
			l.Close()
			t.Fatalf("%+v: started", o)
		} else if !strings.Contains(err.Error(), "TLS") && !strings.Contains(err.Error(), "go together") {
			t.Fatalf("%+v: err=%v", o, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "token")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a refused start made a token")
	}
}

func TestListenRefusesTheNetworkWithoutAPersistentToken(t *testing.T) {
	fakeNet(t, nil)
	if _, _, err := remoteServer(t, nil).ListenRemote(RemoteOptions{Addr: "192.0.2.10:8443", SelfSigned: true}); err == nil ||
		!strings.Contains(err.Error(), "persistent token") {
		t.Fatalf("no data dir: err=%v", err)
	}
	dir := t.TempDir()
	tok := filepath.Join(dir, "token")
	os.WriteFile(tok, []byte(strings.Repeat("a", 64)+"\n"), 0o644)
	if _, _, err := remoteServer(t, nil).ListenRemote(RemoteOptions{Addr: "192.0.2.10:8443", SelfSigned: true, Dir: dir}); err == nil ||
		!strings.Contains(err.Error(), "read by others") {
		t.Fatalf("token 0644: err=%v", err)
	}
	os.Remove(tok)
	os.Symlink("/etc/hostname", tok)
	if _, _, err := remoteServer(t, nil).ListenRemote(RemoteOptions{Addr: "192.0.2.10:8443", SelfSigned: true, Dir: dir}); err == nil {
		t.Fatal("token symlink accepted")
	}
	os.Remove(tok)
	os.WriteFile(tok, []byte("short\n"), 0o600)
	if _, _, err := remoteServer(t, nil).ListenRemote(RemoteOptions{Addr: "192.0.2.10:8443", SelfSigned: true, Dir: dir}); err == nil ||
		!strings.Contains(err.Error(), "no lea web token") {
		t.Fatalf("bad token: err=%v", err)
	}
}

// The listener is never bound where a container reaches it: no wildcard, no container bridge or
// container network, no address the host lacks.
func TestListenRefusesAddressesTheAgentsReach(t *testing.T) {
	fakeNet(t, nil)
	for addr, want := range map[string]string{
		"0.0.0.0:8443":        "every interface",
		"[::]:8443":           "every interface",
		"172.17.0.1:8443":     "docker0",
		"172.18.0.1:8443":     "br-1a2b",
		"198.51.100.7:8443":   "container network 198.51.100.0/24",
		"203.0.113.9:8443":    "not an address of this host",
		"224.0.0.1:8443":      "multicast",
		"127.0.0.1:8443":      "loopback address needs no --listen",
		"[::1]:8443":          "loopback address needs no --listen",
		"lea.example.com:443": "IP address",
		"192.0.2.10":          "ADDR:PORT",
		"192.0.2.10:0":        "port number",
	} {
		dir := t.TempDir()
		_, _, err := remoteServer(t, nil).ListenRemote(RemoteOptions{Addr: addr, SelfSigned: true, Dir: dir})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: err=%v, want %q", addr, err, want)
		}
		if _, err := os.Stat(filepath.Join(dir, "token")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s: a refused start made a token", addr)
		}
	}
	fakeNet(t, errors.New("cannot list the container networks"))
	if _, _, err := remoteServer(t, nil).ListenRemote(RemoteOptions{Addr: "192.0.2.10:8443", SelfSigned: true, Dir: t.TempDir()}); err == nil ||
		!strings.Contains(err.Error(), "container networks") {
		t.Fatalf("unknown container networks: err=%v (lea must fail closed)", err)
	}
}

func TestCheckListenIPOnTheRealHost(t *testing.T) {
	// The real seams: a wildcard and an address no host has are refused whatever the host.
	for _, ip := range []string{"0.0.0.0", "::", "192.0.2.254"} {
		if err := checkListenIP(net.ParseIP(ip)); err == nil {
			t.Fatalf("%s accepted", ip)
		}
	}
}

// remoteClient reaches the fake 192.0.2.10:port on the loopback listener l, trusting cert.
func remoteClient(t *testing.T, l net.Listener, certFile string) *http.Client {
	t.Helper()
	pemb, err := os.ReadFile(certFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pemb)
	addr := l.Addr().String()
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool},
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func TestTheNetworkSignsInWithThePersistentToken(t *testing.T) {
	fakeNet(t, nil)
	d := newFakeDaemon(t)
	dir := t.TempDir()
	s := remoteServer(t, SocketCaller(d.sock))
	l, r, err := s.ListenRemote(RemoteOptions{Addr: "192.0.2.10:8443", SelfSigned: true, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Serve(ctx, l)
	if r.URL != "https://192.0.2.10:8443/" || strings.Contains(r.URL, "token") {
		t.Fatalf("url %q", r.URL)
	}
	if len(r.Token) != 64 || r.TokenFile != filepath.Join(dir, "token") || len(r.Fingerprint) != 95 {
		t.Fatalf("remote=%+v", r)
	}
	for _, f := range []string{"token", "tls-cert.pem", "tls-key.pem"} {
		if fi, err := os.Stat(filepath.Join(dir, f)); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", f, fi.Mode(), err)
		}
	}
	c := remoteClient(t, l, r.CertFile)
	base := "https://192.0.2.10:8443"
	get := func(path string) (*http.Response, string) {
		t.Helper()
		resp, err := c.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(b)
	}
	resp, body := get("/")
	if resp.StatusCode != 200 || !strings.Contains(body, "Sign in to lea web") || resp.Header.Get("Strict-Transport-Security") == "" {
		t.Fatalf("sign-in page: %d %q", resp.StatusCode, resp.Header)
	}
	// The token in the address is refused, even the right one, and buys no cookie.
	resp, _ = get("/?token=" + r.Token)
	if resp.StatusCode != http.StatusBadRequest || len(resp.Cookies()) != 0 {
		t.Fatalf("token in the URL: %d %v", resp.StatusCode, resp.Cookies())
	}
	login := func(tok, origin string) (*http.Response, map[string]any) {
		t.Helper()
		b, _ := json.Marshal(map[string]string{"token": tok})
		req, _ := http.NewRequest("POST", base+"/api/login", strings.NewReader(string(b)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp, out
	}
	if resp, _ := login(r.Token, "http://192.0.2.10:8443"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("plain-http Origin: %d", resp.StatusCode)
	}
	if resp, _ := login(r.Token, "https://evil.example"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign Origin: %d", resp.StatusCode)
	}
	resp, _ = login(r.Token, base)
	var ses *http.Cookie
	for _, k := range resp.Cookies() {
		if k.Name == cookieName {
			ses = k
		}
	}
	if resp.StatusCode != 200 || ses == nil || !ses.Secure || !ses.HttpOnly || ses.SameSite != http.SameSiteStrictMode {
		t.Fatalf("sign-in: %d %+v", resp.StatusCode, ses)
	}
	api := func(k *http.Cookie, origin string) int {
		t.Helper()
		req, _ := http.NewRequest("POST", base+"/api/providers_list", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		if k != nil {
			req.AddCookie(k)
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := api(ses, base); code != 200 {
		t.Fatalf("API with the session: %d", code)
	}
	if code := api(nil, base); code != http.StatusUnauthorized {
		t.Fatalf("API without a session: %d", code)
	}
	if code := api(&http.Cookie{Name: cookieName, Value: s.session}, base); code != http.StatusUnauthorized {
		t.Fatalf("the loopback's session works on the network: %d", code)
	}
	resp, body = get("/")
	if !strings.Contains(body, "Sign in") {
		t.Fatal("no cookie, no board")
	}
	req, _ := http.NewRequest("GET", base+"/", nil)
	req.AddCookie(ses)
	resp, err = c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(b), "Sign in to lea web") {
		t.Fatal("signed in, still the sign-in page")
	}
	// A foreign Host (DNS rebinding) is refused.
	req, _ = http.NewRequest("GET", base+"/", nil)
	req.Host = "evil.example:8443"
	if resp, err := c.Do(req); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign Host: %v %v", resp, err)
	}

	// Wrong tokens slow the next tries, the right one's included.
	for i := 0; i < freeFailures; i++ {
		if resp, _ := login(strings.Repeat("0", 64), base); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("wrong token %d: %d", i, resp.StatusCode)
		}
	}
	if resp, _ := login(r.Token, base); resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("after %d wrong tokens: %d", freeFailures, resp.StatusCode)
	}
	s.remote.mu.Lock()
	s.remote.now = func() time.Time { return time.Now().Add(time.Hour) }
	s.remote.mu.Unlock()
	if resp, _ := login(r.Token, base); resp.StatusCode != 200 {
		t.Fatalf("after the wait: %d", resp.StatusCode)
	}

	// The next start keeps the token and the certificate; --new-token changes the token.
	s2 := remoteServer(t, nil)
	l2, r2, err := s2.ListenRemote(RemoteOptions{Addr: "192.0.2.10:8443", SelfSigned: true, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	l2.Close()
	if r2.Token != "" || s2.remote.token != r.Token || r2.Fingerprint != r.Fingerprint {
		t.Fatalf("second start: %+v", r2)
	}
	s3 := remoteServer(t, nil)
	l3, r3, err := s3.ListenRemote(RemoteOptions{Addr: "192.0.2.10:8443", SelfSigned: true, Dir: dir, NewToken: true})
	if err != nil {
		t.Fatal(err)
	}
	l3.Close()
	if len(r3.Token) != 64 || r3.Token == r.Token {
		t.Fatalf("--new-token: %+v", r3)
	}
}

func TestTheNetworkServesTheUsersCertificate(t *testing.T) {
	fakeNet(t, nil)
	dir := t.TempDir()
	// A certificate made the self-signed way, handed over as the user's own.
	if _, err := selfSigned(filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem"), net.ParseIP("192.0.2.10"), time.Now()); err != nil {
		t.Fatal(err)
	}
	s := remoteServer(t, nil)
	l, r, err := s.ListenRemote(RemoteOptions{Addr: "192.0.2.10:8443", CertFile: filepath.Join(dir, "c.pem"), KeyFile: filepath.Join(dir, "k.pem"), Dir: filepath.Join(dir, "web")})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if r.CertFile != filepath.Join(dir, "c.pem") || r.Fingerprint == "" {
		t.Fatalf("remote=%+v", r)
	}
	if _, err := os.Stat(filepath.Join(dir, "web", "tls-cert.pem")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a self-signed certificate was made beside the user's")
	}
}

func TestSelfSignedIsRenewedForAnotherAddressOrNearItsEnd(t *testing.T) {
	dir := t.TempDir()
	cf, kf := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	now := time.Now()
	fp := func(c tls.Certificate) string { return fingerprint(c.Certificate[0]) }
	a, err := selfSigned(cf, kf, net.ParseIP("192.0.2.10"), now)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := selfSigned(cf, kf, net.ParseIP("192.0.2.10"), now)
	if fp(a) != fp(b) {
		t.Fatal("not kept")
	}
	c, _ := selfSigned(cf, kf, net.ParseIP("192.0.2.11"), now)
	if fp(c) == fp(b) {
		t.Fatal("kept for another address")
	}
	d, _ := selfSigned(cf, kf, net.ParseIP("192.0.2.11"), now.Add(340*24*time.Hour))
	if fp(d) == fp(c) {
		t.Fatal("kept near its end")
	}
}
