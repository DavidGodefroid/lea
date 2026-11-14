// Package web is lea web (#414): the deck in a browser. A local HTTP server, bound to the
// loopback only, that serves embedded static assets and a thin JSON API forwarding an allowlist
// of the daemon's host methods to the host socket, plus a server-sent event stream of the lea's
// state. It acts as the user, as the deck does, through the host socket: no agent container can
// reach it (it listens on the host's loopback, which no container shares) or the host socket.
//
// Every request is checked: its Host header names this server exactly (DNS rebinding), an API
// request carries the session cookie (exchanged once for the one-time token of the URL lea web
// prints), and its Origin, when the browser sends one (always on a POST), is this server's own
// (CSRF). No CORS; a strict Content-Security-Policy.
package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/DavidGodefroid/lea/internal/client"
	"github.com/DavidGodefroid/lea/internal/wire"
)

//go:embed static
var staticFS embed.FS

const (
	cookieName = "lea_web"
	// maxBody caps an API request's body: a message is at most 64 KiB, escaped JSON a few times that.
	maxBody = 512 << 10
	csp     = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; " +
		"font-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
)

// Caller sends one request to the daemon's host socket (client.Call); a seam for the tests.
type Caller func(ctx context.Context, method string, params, result any) error

// SocketCaller calls the daemon on its host socket sock.
func SocketCaller(sock string) Caller {
	return func(ctx context.Context, method string, params, result any) error {
		var raw json.RawMessage
		if params != nil {
			b, err := json.Marshal(params)
			if err != nil {
				return err
			}
			raw = b
		}
		return client.Call(ctx, sock, wire.Request{Method: method, Params: raw}, result)
	}
}

// Server is one run of lea web for one lea (its container).
type Server struct {
	call      Caller
	container string
	// token is the one-time token of the printed URL, session the cookie it is exchanged for.
	token, session string
	mu             sync.Mutex
	tokenUsed      bool
	// hosts are the Host headers (and origins, with http://) this server answers: 127.0.0.1:port
	// and localhost:port, set by Listen.
	hosts map[string]bool
	port  int
	// retry is how long the event stream waits before it asks the daemon again after an error.
	retry func(ctx context.Context)
	// host is lea allow's and lea egress's part on the host (SetHost); nil without the launcher.
	host Host
}

// New prepares a server for container that talks to the daemon through call.
func New(call Caller, container string) (*Server, error) {
	tok, err := randomHex(32)
	if err != nil {
		return nil, err
	}
	ses, err := randomHex(32)
	if err != nil {
		return nil, err
	}
	return &Server{call: call, container: container, token: tok, session: ses, hosts: map[string]bool{}, retry: sleepRetry}, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Listen binds the server on 127.0.0.1:port (0: a free port). Only the loopback is offered: a
// remote browser reaches lea web through an SSH tunnel.
func (s *Server) Listen(port int) (net.Listener, error) {
	if port < 0 || port > 65535 {
		return nil, fmt.Errorf("invalid port %d", port)
	}
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	s.setPort(l.Addr().(*net.TCPAddr).Port)
	return l, nil
}

func (s *Server) setPort(port int) {
	s.port = port
	s.hosts = map[string]bool{"127.0.0.1:" + strconv.Itoa(port): true, "localhost:" + strconv.Itoa(port): true}
}

// URL is the address to open once: it carries the one-time token.
func (s *Server) URL() string {
	return "http://127.0.0.1:" + strconv.Itoa(s.port) + "/?token=" + s.token
}

// Serve serves l until ctx ends.
func (s *Server) Serve(ctx context.Context, l net.Listener) error {
	hs := &http.Server{Handler: s.Handler(), BaseContext: func(net.Listener) context.Context { return ctx }}
	go func() { <-ctx.Done(); hs.Close() }()
	err := hs.Serve(l)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Handler is the server's routes behind its checks.
func (s *Server) Handler() http.Handler {
	static, _ := fs.Sub(staticFS, "static")
	files := http.FileServer(http.FS(static))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("token") {
			s.exchange(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
	mux.Handle("GET /static/", http.StripPrefix("/static/", files))
	mux.HandleFunc("GET /api/events", s.api(s.events))
	mux.HandleFunc("POST /api/{method}", s.api(s.method))
	mux.HandleFunc("/api/", s.api(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "no such API")
	}))
	return s.guard(mux)
}

// guard sets the security headers and refuses a request for another host (DNS rebinding): the
// browser sends the name it resolved, which must be this server's loopback address.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		if !s.hosts[r.Host] {
			writeError(w, http.StatusForbidden, "foreign Host")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// api checks an API request: its origin (403 when foreign), then its session cookie (401).
func (s *Server) api(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !s.sameOrigin(r) {
			writeError(w, http.StatusForbidden, "foreign Origin")
			return
		}
		c, err := r.Cookie(cookieName)
		if err != nil || subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.session)) != 1 {
			writeError(w, http.StatusUnauthorized, "open the URL lea web printed")
			return
		}
		next(w, r)
	}
}

// sameOrigin: a browser sends Origin on every POST and on any cross-origin request, and
// Sec-Fetch-Site on all of them: either one naming another site refuses the request. A POST
// without Origin is refused too (no browser sends one; a script outside it has no cookie).
func (s *Server) sameOrigin(r *http.Request) bool {
	if o := r.Header.Get("Origin"); o != "" {
		host, ok := strings.CutPrefix(o, "http://")
		if !ok || !s.hosts[host] || host != r.Host {
			return false
		}
	} else if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin":
		return true
	}
	return false
}

// exchange trades the one-time token for the session cookie, once, and sends the browser to the
// page without the token in its address.
func (s *Server) exchange(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	s.mu.Lock()
	ok := !s.tokenUsed && subtle.ConstantTimeCompare([]byte(tok), []byte(s.token)) == 1
	if ok {
		s.tokenUsed = true
	}
	s.mu.Unlock()
	if !ok {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintln(w, "This link was already used or is not lea web's: start lea web again for a new one.")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: s.session, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// daemonError answers a daemon call's error: the daemon's own refusal (400), or no daemon (502).
func daemonError(w http.ResponseWriter, err error) {
	var re *client.RemoteError
	if errors.As(err, &re) {
		writeError(w, http.StatusBadRequest, re.Msg)
		return
	}
	writeError(w, http.StatusBadGateway, "the lea daemon does not answer: is lea running?")
}
