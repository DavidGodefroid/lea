// Package keyproxy is lea-keyproxy, the HTTP forwarder of the proxy container that lets the agents
// reach the OpenAI-compatible endpoints of the providers file without ever seeing their API keys
// (issue #21).
//
// An agent sends its request to http://lea-proxy:4400/<name>/<rest>. The forwarder drops every
// credential the agent sent (Authorization, X-Api-Key, cookies ...), adds the endpoint's key as
// "Authorization: Bearer <key>" when it has one, and forwards the request to the endpoint's base
// URL joined with <rest>, always through the proxy's squid (-via): the egress rules of the agents
// (private ranges refused unless the internal-hosts file grants them) apply to every upstream.
//
// The key is read from its file on every request, so a rotated key needs no restart; it is never
// logged, never echoed in an error, and nothing of the request but its endpoint name, method and
// path is logged.
package keyproxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/DavidGodefroid/lea/internal/providers"
)

const (
	// MaxBodyBytes caps a request body.
	MaxBodyBytes = 64 << 20
	// MaxHeaderBytes caps the request and response headers.
	MaxHeaderBytes = 64 << 10
)

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,15}$`)

// Endpoint is one line of the endpoints file.
type Endpoint struct {
	Name string
	// URL is the upstream's base URL, checked by providers.CheckEndpoint.
	URL *url.URL
	// KeyFile is the key file's path inside the proxy container, "" for no key.
	KeyFile string
}

// ParseEndpoints reads the endpoints file the launcher renders:
//
//	<name> <base-url> [<key file>]
//
// '#' starts a comment. Every endpoint passes providers.CheckEndpoint against hosts, and a key
// file is a clean path under keyDir. The error names the line, never a key.
func ParseEndpoints(r io.Reader, hosts providers.Hosts, keyDir string) ([]Endpoint, error) {
	var out []Endpoint
	seen := map[string]bool{}
	keyDir = strings.TrimSuffix(keyDir, "/")
	sc := bufio.NewScanner(r)
	n := 0
	for sc.Scan() {
		n++
		line, _, _ := strings.Cut(sc.Text(), "#")
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if len(f) < 2 || len(f) > 3 {
			return nil, fmt.Errorf("line %d: expected '<name> <base-url> [<key file>]'", n)
		}
		ep := Endpoint{Name: f[0]}
		if !nameRe.MatchString(ep.Name) {
			return nil, fmt.Errorf("line %d: invalid name: lowercase letters, digits and '-', starting with a letter, at most 16", n)
		}
		if seen[ep.Name] {
			return nil, fmt.Errorf("line %d: %s is listed twice", n, ep.Name)
		}
		seen[ep.Name] = true
		u, err := url.Parse(f[1])
		if err != nil {
			return nil, fmt.Errorf("line %d: %s: invalid base URL", n, ep.Name)
		}
		ep.URL = u
		if len(f) == 3 {
			k := f[2]
			if keyDir == "" || path.Clean(k) != k || !strings.HasPrefix(k, keyDir+"/") || strings.ContainsAny(k, "\\,") {
				return nil, fmt.Errorf("line %d: %s: the key file must be a clean path under %s", n, ep.Name, keyDir)
			}
			ep.KeyFile = k
		}
		if err := providers.CheckEndpoint(&providers.Endpoint{URL: u, KeyFile: ep.KeyFile}, hosts); err != nil {
			return nil, fmt.Errorf("line %d: %s: %w", n, ep.Name, err)
		}
		out = append(out, ep)
		if len(out) > providers.MaxEndpoints {
			return nil, fmt.Errorf("line %d: at most %d endpoints", n, providers.MaxEndpoints)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("line %d: %w", n+1, err)
	}
	return out, nil
}

// Config is what New builds a forwarder from.
type Config struct {
	Endpoints []Endpoint
	// Via is the forward proxy (squid) every upstream request goes through: http://host:port.
	Via *url.URL
	// Log receives one line per request; nil logs to stderr.
	Log *log.Logger
	// RootCAs overrides the system roots (tests); nil uses the system's.
	RootCAs *x509.CertPool
	// Refused reports whether a connection that arrived on the forwarder's local address must be
	// refused: one on the proxy's egress network (the host and that network's other containers),
	// never an agents' internal network (EgressAddrs). nil refuses none (tests).
	Refused func(local net.IP) bool
}

type keyCtx struct{}

// Forwarder is the http.Handler of lea-keyproxy.
type Forwarder struct {
	refused func(net.IP) bool
	eps     map[string]Endpoint
	proxy   *httputil.ReverseProxy
	log     *log.Logger
}

// New builds the forwarder. Its transport only ever dials cfg.Via: the environment's proxy
// settings are ignored.
func New(cfg Config) (*Forwarder, error) {
	v := cfg.Via
	if v == nil || v.Scheme != "http" || v.Host == "" || v.Hostname() == "" || v.Port() == "" || v.User != nil ||
		(v.Path != "" && v.Path != "/") || v.RawQuery != "" || v.Fragment != "" {
		return nil, errors.New("the -via proxy must be http://<host>:<port>")
	}
	via := *v
	viaAddr := net.JoinHostPort(via.Hostname(), via.Port())
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	tlsConf := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: cfg.RootCAs}
	tr := &http.Transport{
		// Fixed: never http.ProxyFromEnvironment.
		Proxy: http.ProxyURL(&via),
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			// Defence in depth: the only connection the forwarder opens is to its proxy.
			if addr != viaAddr {
				return nil, errDirectDial
			}
			return dialer.DialContext(ctx, network, addr)
		},
		TLSClientConfig:        tlsConf,
		ForceAttemptHTTP2:      true,
		TLSHandshakeTimeout:    10 * time.Second,
		IdleConnTimeout:        90 * time.Second,
		ExpectContinueTimeout:  1 * time.Second,
		MaxResponseHeaderBytes: MaxHeaderBytes,
	}
	f := &Forwarder{eps: map[string]Endpoint{}, log: cfg.Log, refused: cfg.Refused}
	if f.log == nil {
		f.log = log.New(os.Stderr, "lea-keyproxy: ", log.LstdFlags)
	}
	for _, ep := range cfg.Endpoints {
		if ep.URL == nil || !nameRe.MatchString(ep.Name) {
			return nil, errors.New("invalid endpoint")
		}
		f.eps[ep.Name] = ep
	}
	f.proxy = &httputil.ReverseProxy{
		Rewrite:        f.rewrite,
		Transport:      tr,
		FlushInterval:  -1,
		ErrorLog:       log.New(io.Discard, "", 0),
		ModifyResponse: modifyResponse,
		ErrorHandler:   f.errorHandler,
	}
	return f, nil
}

var errDirectDial = errors.New("direct dial refused")

// Server is the http.Server lea-keyproxy listens with: no write timeout (long streams).
func Server(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    MaxHeaderBytes,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
}

// strip are the inbound headers that never reach an upstream: the agent's own credentials and
// anything that tells the upstream about the agent's side.
var strip = []string{"Authorization", "Proxy-Authorization", "X-Api-Key", "Api-Key", "Cookie", "Cookie2",
	"Forwarded", "Via", "Proxy-Connection", "Connection", "Upgrade", "Keep-Alive", "Te", "Trailer", "Transfer-Encoding"}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController flush the stream (FlushInterval -1).
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (f *Forwarder) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	start := time.Now()
	w := &statusWriter{ResponseWriter: rw}
	name, logPath := "-", "-"
	defer func() {
		st := w.status
		if st == 0 {
			st = http.StatusOK
		}
		f.log.Printf("%s %s %s %d %s", name, safeLog(r.Method, 10), logPath, st, time.Since(start).Round(time.Millisecond))
	}()
	if f.refused != nil {
		la, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
		ta, ok := la.(*net.TCPAddr)
		if !ok || f.refused(ta.IP) {
			// Only the agents' internal networks reach the endpoints.
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}
	raw, _, _ := strings.Cut(r.RequestURI, "?")
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !cleanRawPath(raw) {
		http.Error(w, "bad request path", http.StatusBadRequest)
		return
	}
	logPath = safeLog(raw, 200)
	if r.Header.Get("Upgrade") != "" || r.ContentLength > MaxBodyBytes {
		if r.ContentLength > MaxBodyBytes {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "protocol upgrades are not forwarded", http.StatusBadRequest)
		}
		return
	}
	n, rest, _ := strings.Cut(strings.TrimPrefix(raw, "/"), "/")
	ep, ok := f.eps[n]
	if !ok {
		http.Error(w, "unknown endpoint", http.StatusNotFound)
		return
	}
	name = n
	key := ""
	if ep.KeyFile != "" {
		k, err := providers.ReadKey(ep.KeyFile)
		if err != nil {
			f.log.Printf("%s: the key file cannot be used", name)
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}
		key = k
	}
	target := *ep.URL
	joined := strings.TrimSuffix(ep.URL.EscapedPath(), "/")
	if rest != "" || strings.HasSuffix(raw, "/") {
		joined += "/" + rest
	}
	p, err := url.PathUnescape(joined)
	if err != nil {
		http.Error(w, "bad request path", http.StatusBadRequest)
		return
	}
	target.Path, target.RawPath = p, joined
	target.RawQuery = r.URL.RawQuery
	target.ForceQuery = false
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
	}
	ctx := context.WithValue(r.Context(), keyCtx{}, route{target: &target, key: key})
	f.proxy.ServeHTTP(w, r.WithContext(ctx))
}

type route struct {
	target *url.URL
	key    string
}

func (f *Forwarder) rewrite(pr *httputil.ProxyRequest) {
	rt, _ := pr.In.Context().Value(keyCtx{}).(route)
	out := pr.Out
	h := out.Header
	for _, k := range strip {
		h.Del(k)
	}
	for k := range h {
		if strings.HasPrefix(http.CanonicalHeaderKey(k), "X-Forwarded-") {
			delete(h, k)
		}
	}
	out.URL = rt.target
	out.Host = rt.target.Host
	if rt.key != "" {
		h.Set("Authorization", "Bearer "+rt.key)
	}
}

func modifyResponse(res *http.Response) error {
	if res.StatusCode == http.StatusSwitchingProtocols {
		return errUpgrade
	}
	res.Header.Del("Set-Cookie")
	res.Header.Del("Set-Cookie2")
	return nil
}

var errUpgrade = errors.New("upgrade refused")

func (f *Forwarder) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	class := "upstream error"
	var mbe *http.MaxBytesError
	var ne net.Error
	switch {
	case errors.As(err, &mbe):
		class = "request body too large"
	case errors.Is(err, context.Canceled):
		class = "client gone"
	case errors.Is(err, errUpgrade):
		class = "upstream switched protocols"
	case errors.Is(err, errDirectDial):
		class = "direct dial refused"
	case errors.As(err, &ne) && ne.Timeout():
		class = "upstream timeout"
	case isTLS(err):
		class = "upstream TLS error"
	}
	f.log.Printf("%s", class)
	if mbe != nil {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	http.Error(w, "bad gateway", http.StatusBadGateway)
}

func isTLS(err error) bool {
	var ce *tls.CertificateVerificationError
	var re tls.RecordHeaderError
	var ae tls.AlertError
	return errors.As(err, &ce) || errors.As(err, &re) || errors.As(err, &ae)
}

// cleanRawPath accepts an origin-form path /<segment>/... without empty, '.' or '..' segments,
// backslashes, control characters, or percent-encodings of '.', '/', '\', '%' or control
// characters (so no upstream decoding can turn it into another path).
func cleanRawPath(p string) bool {
	if !strings.HasPrefix(p, "/") || len(p) < 2 {
		return false
	}
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c <= ' ' || c >= 0x7f || c == '\\':
			return false
		case c == '%':
			if i+2 >= len(p) || !isHex(p[i+1]) || !isHex(p[i+2]) {
				return false
			}
			b := unhex(p[i+1])<<4 | unhex(p[i+2])
			if b == '.' || b == '/' || b == '\\' || b == '%' || b < ' ' || b >= 0x7f {
				return false
			}
			i += 2
		}
	}
	for i, s := range strings.Split(p[1:], "/") {
		if s == "." || s == ".." {
			return false
		}
		// An empty segment ("//") anywhere but a trailing slash.
		if s == "" && i != strings.Count(p[1:], "/") {
			return false
		}
	}
	return !strings.Contains(p, "//")
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case c <= '9':
		return c - '0'
	case c <= 'F':
		return c - 'A' + 10
	}
	return c - 'a' + 10
}

func safeLog(s string, max int) string {
	if len(s) > max {
		s = s[:max] + "..."
	}
	return fmt.Sprintf("%q", s)
}
