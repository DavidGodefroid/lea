package web

import (
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	// maxSessions caps the browsers signed in at once on the network; the oldest goes first.
	maxSessions = 16
	// sessionLife is how long a sign-in lasts: the browser signs in again after it.
	sessionLife = 7 * 24 * time.Hour
	// freeFailures is how many wrong tokens one source may send before each next try waits.
	freeFailures = 5
	maxBackoff   = 60 * time.Second
	// failureMemory is how long a source's wrong tokens count; maxSources caps the sources
	// remembered (the oldest goes first).
	failureMemory = 10 * time.Minute
	maxSources    = 1024
)

// remoteAuth is the network's sign-in (#416): the persistent token, typed into the sign-in
// page, buys a session of its own for sessionLife; a source (an IP address, an IPv6 /64) that
// sends wrong tokens waits longer and longer before its next try is checked. A source that
// waits locks out only itself, and the browsers signed in keep their sessions.
type remoteAuth struct {
	token    string
	now      func() time.Time
	mu       sync.Mutex
	sessions map[string]time.Time // session → when it signed in
	sources  map[string]*failures
}

type failures struct {
	n        int
	last     time.Time
	notUntil time.Time // no try from the source is checked before it
}

// source is the client's IP address, its /64 for IPv6 (one host holds a whole /64).
func source(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return host
	}
	if ip.To4() == nil {
		return ip.Mask(net.CIDRMask(64, 128)).String()
	}
	return ip.String()
}

// valid reports whether ses is a live session; an expired one is forgotten.
func (a *remoteAuth) valid(ses string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	for s, at := range a.sessions {
		if now.Sub(at) >= sessionLife {
			delete(a.sessions, s)
			continue
		}
		if subtle.ConstantTimeCompare([]byte(s), []byte(ses)) == 1 {
			return true
		}
	}
	return false
}

// signOut forgets session ses.
func (a *remoteAuth) signOut(ses string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, ses)
}

// signIn checks tok from src and returns a new session; wait > 0 when src tried too soon.
func (a *remoteAuth) signIn(src, tok string) (ses string, wait time.Duration, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	f := a.sources[src]
	if f != nil && now.Sub(f.last) >= failureMemory {
		delete(a.sources, src)
		f = nil
	}
	if f != nil && now.Before(f.notUntil) {
		return "", f.notUntil.Sub(now), nil
	}
	if subtle.ConstantTimeCompare([]byte(tok), []byte(a.token)) != 1 {
		if f == nil {
			a.forgetOldestSource()
			f = &failures{}
			a.sources[src] = f
		}
		f.n++
		f.last = now
		if f.n >= freeFailures {
			f.notUntil = now.Add(min(time.Second<<min(f.n-freeFailures, 6), maxBackoff))
		}
		return "", 0, nil
	}
	delete(a.sources, src)
	ses, err = randomHex(32)
	if err != nil {
		return "", 0, err
	}
	if len(a.sessions) >= maxSessions {
		oldest, at := "", now
		for s, t := range a.sessions {
			if !t.After(at) {
				oldest, at = s, t
			}
		}
		delete(a.sessions, oldest)
	}
	a.sessions[ses] = now
	return ses, 0, nil
}

// forgetOldestSource makes room for one more source.
func (a *remoteAuth) forgetOldestSource() {
	if len(a.sources) < maxSources {
		return
	}
	oldest, at := "", a.now()
	for s, f := range a.sources {
		if !f.last.After(at) {
			oldest, at = s, f.last
		}
	}
	delete(a.sources, oldest)
}

// login serves POST /api/login on the network: {"token": …} for the session cookie. Its origin
// is checked as every API call's; the token travels in the body only.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.remote == nil {
		writeError(w, http.StatusNotFound, "open the URL lea web printed")
		return
	}
	if !s.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "foreign Origin")
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "send application/json")
		return
	}
	var p struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	ses, wait, err := s.remote.signIn(source(r), p.Token)
	switch {
	case err != nil:
		writeError(w, http.StatusInternalServerError, "no session")
	case wait > 0:
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds()+0.999)))
		writeError(w, http.StatusTooManyRequests, "too many wrong tokens: wait a little")
	case ses == "":
		writeError(w, http.StatusUnauthorized, "wrong token")
	default:
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: ses, Path: "/", MaxAge: int(sessionLife.Seconds()),
			HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

// logout serves POST /api/logout on the network: the session ends and its cookie goes.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if s.remote == nil {
		writeError(w, http.StatusNotFound, "no sign-out on the loopback: stop lea web")
		return
	}
	if c, err := r.Cookie(cookieName); err == nil {
		s.remote.signOut(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
