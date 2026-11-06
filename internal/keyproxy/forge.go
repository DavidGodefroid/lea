package keyproxy

// The forge forwarder (issue #103): the GitHub token of a lea lives in the proxy container, never in
// an agent's. gh sends every request to a unix socket of the agent container (gh's
// http_unix_socket), which a relay of lea's carries to this port of the proxy as plain HTTP, Host
// api.github.com and the like; git reaches github.com at http://lea-proxy:4401/ (the image's
// url.insteadOf). The forwarder drops every credential the agent sent, adds the token for the
// GitHub hosts that take it, and sends the request over HTTPS through squid (or the LockHTTP gate),
// as the endpoints forwarder does: the egress rules apply. gh follows redirects itself, and the
// redirected requests come through the socket too (Host: the redirect's host, such as
// *.blob.core.windows.net for job logs): the forwarder sends them to that host, without the token,
// which only ever goes to GitHub's own hosts.

import (
	"context"
	"encoding/base64"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/DavidGodefroid/lea/internal/providers"
)

const (
	// ForgePort is the forge forwarder's port in the proxy container.
	ForgePort = 4401
	// GitHubTokenFile is the token's file name in the forge key directory.
	GitHubTokenFile = "github.com"
	// MaxForgeBodyBytes caps a request body: a git push sends its pack (GitHub refuses pushes over
	// 2 GiB).
	MaxForgeBodyBytes = 2 << 30
)

// tokenHosts are the GitHub hosts that get the token: the REST and GraphQL API, release
// uploads, and git over HTTPS.
var tokenHosts = map[string]bool{"api.github.com": true, "uploads.github.com": true, "github.com": true}

// ProxyName is the proxy's name on a lea's internal network (git's url.insteadOf names it).
const ProxyName = "lea-proxy"

// validHostName matches a DNS name: dot-separated labels of letters, digits and inner hyphens.
var validHostName = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// forgeHost is the upstream of a request whose Host is host, "" to refuse it (why says why).
//   - The forwarder's own address (lea-proxy, or an address of the proxy's for which self is
//     true, with no port or ForgePort): git through url.insteadOf, upstream github.com.
//   - Any other host is its own upstream (with its port, unless 443): gh sends every request
//     through the socket, the redirects it follows included (job logs and artifacts at
//     *.blob.core.windows.net, release assets at objects.githubusercontent.com), and so do GHES
//     hosts. Only tokenHosts get the token; the egress rules apply to every host.
func forgeHost(host string, self func(net.IP) bool) (up, why string) {
	h, port := host, ""
	if hh, pp, err := net.SplitHostPort(host); err == nil {
		h, port = hh, pp
	}
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	if h == "" {
		return "", "a request without a Host"
	}
	if port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return "", "a bad port"
		}
	}
	ip := net.ParseIP(h)
	if h == ProxyName || (ip != nil && self != nil && self(ip)) {
		if port != "" && port != strconv.Itoa(ForgePort) {
			return "", "another port of the proxy's"
		}
		return "github.com", ""
	}
	if ip == nil && !validHostName.MatchString(h) {
		return "", "a bad host name"
	}
	if ip != nil && ip.To4() == nil {
		h = "[" + h + "]"
	}
	if port != "" && port != "443" {
		return h + ":" + port, ""
	}
	return h, ""
}

// localIP reports whether ip is an address of this host's (the proxy container's: one per
// internal network it is on, read at every call, as lea connects it to new ones).
func localIP(ip net.IP) bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
			return true
		}
	}
	return false
}

// forgeMethods are the methods the forge forwarder carries.
var forgeMethods = map[string]bool{http.MethodGet: true, http.MethodHead: true, http.MethodPost: true,
	http.MethodPut: true, http.MethodPatch: true, http.MethodDelete: true}

// minting reports whether a call on the API would hand a credential, or a copy of what the token
// reaches, to someone outside lea: an SSH, GPG or signing key on the account, a deploy key, a
// webhook, an OAuth authorization. Reads are fine. Best effort: the token still acts as its owner
// on everything else it may (GraphQL included).
func minting(method string, segs []string) bool {
	if method == http.MethodGet || method == http.MethodHead {
		return false
	}
	at := func(i int, s string) bool { return len(segs) > i && segs[i] == s }
	switch {
	case at(0, "user") && (at(1, "keys") || at(1, "gpg_keys") || at(1, "ssh_signing_keys")):
		return true
	case at(0, "authorizations"), at(0, "applications"):
		return true
	case at(0, "repos") && len(segs) > 3 && (segs[3] == "keys" || segs[3] == "hooks"):
		return true
	case at(0, "orgs") && at(2, "hooks"):
		return true
	}
	return false
}

// forgePath checks an origin-form path: printable ASCII without a backslash, valid escapes, and no
// "." or ".." segment, even escaped. It returns the path's segments, decoded and lower-cased.
func forgePath(p string) ([]string, bool) {
	if !strings.HasPrefix(p, "/") {
		return nil, false
	}
	for i := 0; i < len(p); i++ {
		if c := p[i]; c <= ' ' || c >= 0x7f || c == '\\' {
			return nil, false
		}
	}
	var segs []string
	for _, s := range strings.Split(p[1:], "/") {
		d, err := url.PathUnescape(s)
		if err != nil || d == "." || d == ".." || strings.ContainsAny(d, "/\\") {
			return nil, false
		}
		segs = append(segs, strings.ToLower(d))
	}
	return segs, true
}

// Forge is the forge forwarder's handler. keyDir holds the token (GitHubTokenFile), read at every
// request: lea github login replaces it without a restart. upstream maps a host (forgeHost) to its
// base URL; nil is https://<host> (tests point it elsewhere).
func (f *Forwarder) Forge(keyDir string, upstream func(host string) *url.URL) http.Handler {
	if upstream == nil {
		upstream = func(h string) *url.URL { return &url.URL{Scheme: "https", Host: h} }
	}
	return &forge{f: f, keyDir: keyDir, upstream: upstream, self: localIP}
}

type forge struct {
	f        *Forwarder
	keyDir   string
	upstream func(string) *url.URL
	self     func(net.IP) bool // the proxy's own addresses (localIP)
}

func (g *forge) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	start := time.Now()
	w := &statusWriter{ResponseWriter: rw}
	host, logPath := "-", "-"
	defer func() {
		st := w.status
		if st == 0 {
			st = http.StatusOK
		}
		g.f.log.Printf("forge %s %s %s %d %s", host, safeLog(r.Method, 10), logPath, st, time.Since(start).Round(time.Millisecond))
	}()
	source, ok := g.f.admit(w, r)
	if !ok {
		return
	}
	if !forgeMethods[r.Method] {
		w.Header().Set("Allow", "GET, HEAD, POST, PUT, PATCH, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	raw, _, _ := strings.Cut(r.RequestURI, "?")
	segs, ok := forgePath(raw)
	if !ok {
		http.Error(w, "bad request path", http.StatusBadRequest)
		return
	}
	logPath = safeLog(raw, 200)
	h, why := forgeHost(r.Host, g.self)
	if h == "" {
		http.Error(w, "lea's forge forwarder refuses "+why+": it carries gh's requests (any host, the token for GitHub's only) "+
			"and git's to github.com at "+ProxyName+":"+strconv.Itoa(ForgePort), http.StatusBadRequest)
		return
	}
	host = h
	if r.Header.Get("Upgrade") != "" {
		http.Error(w, "protocol upgrades are not forwarded", http.StatusBadRequest)
		return
	}
	if r.ContentLength > MaxForgeBodyBytes {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	if h == "api.github.com" && minting(r.Method, segs) {
		http.Error(w, "lea refuses this GitHub call: it would give a key, a deploy key or a webhook to someone outside lea "+
			"(the token stays in lea's proxy). Make it yourself, on the host.", http.StatusForbidden)
		return
	}
	auth := ""
	if tokenHosts[h] {
		tok, err := providers.ReadKey(filepath.Join(g.keyDir, GitHubTokenFile))
		switch {
		case err == nil && h == "github.com":
			// git over HTTPS takes Basic; any user name goes with a token.
			auth = "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+tok))
		case err == nil:
			auth = "Bearer " + tok
		default:
			// No token: the request goes out anonymous (public repositories still clone).
			g.f.log.Printf("forge: no GitHub token (lea github login)")
		}
	}
	base := g.upstream(h)
	if base == nil {
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	target := *base
	target.Path, target.RawPath = strings.TrimSuffix(base.Path, "/")+mustUnescape(raw), strings.TrimSuffix(base.EscapedPath(), "/")+raw
	target.RawQuery, target.ForceQuery = r.URL.RawQuery, false
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, MaxForgeBodyBytes)
	}
	ctx := context.WithValue(r.Context(), keyCtx{}, route{target: &target, auth: auth, source: source})
	g.f.proxy.ServeHTTP(w, r.WithContext(ctx))
}

// mustUnescape is p unescaped; forgePath checked that it unescapes.
func mustUnescape(p string) string {
	u, err := url.PathUnescape(p)
	if err != nil {
		return p
	}
	return u
}
