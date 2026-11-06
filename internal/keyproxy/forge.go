package keyproxy

// The forge forwarder (issue #103): the GitHub token of a lea lives in the proxy container, never in
// an agent's. gh sends every request to a unix socket of the agent container (gh's
// http_unix_socket), which a relay of lea's carries to this port of the proxy as plain HTTP, Host
// api.github.com and the like; git reaches github.com at http://lea-proxy:4401/ (the image's
// url.insteadOf). The forwarder drops every credential the agent sent, adds the token for the
// GitHub hosts that take it, and sends the request over HTTPS through squid (or the LockHTTP gate),
// as the endpoints forwarder does: the egress rules apply, and the token never leaves for another
// host (redirects go back to the client, which follows them without it).

import (
	"context"
	"encoding/base64"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
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

// forgeHost is the upstream of a request whose Host is host: a GitHub host as is (gh, through the
// socket), anything else (lea-proxy:4401, the proxy's address: git through url.insteadOf) is
// github.com. "" refuses it.
func forgeHost(host string) string {
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	}
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	switch {
	case tokenHosts[h], h == "codeload.github.com":
		return h
	case strings.HasSuffix(h, ".githubusercontent.com") && h != ".githubusercontent.com":
		return h
	case strings.HasSuffix(h, "github.com"), strings.HasSuffix(h, "githubusercontent.com"):
		// Another name of GitHub's (gist.github.com, a look-alike): not one lea sends anywhere.
		return ""
	}
	return "github.com"
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
// request: lea github login replaces it without a restart. upstream maps a GitHub host to its
// base URL; nil is https://<host> (tests point it elsewhere).
func (f *Forwarder) Forge(keyDir string, upstream func(host string) *url.URL) http.Handler {
	if upstream == nil {
		upstream = func(h string) *url.URL { return &url.URL{Scheme: "https", Host: h} }
	}
	return &forge{f: f, keyDir: keyDir, upstream: upstream}
}

type forge struct {
	f        *Forwarder
	keyDir   string
	upstream func(string) *url.URL
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
	h := forgeHost(r.Host)
	if h == "" {
		http.Error(w, "lea's forge forwarder serves github.com only", http.StatusForbidden)
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
