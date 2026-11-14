package web

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	// maxSessions caps the browsers signed in at once on the network; the oldest goes first.
	maxSessions = 16
	// freeFailures is how many wrong tokens the sign-in takes before each next try waits.
	freeFailures = 5
	maxBackoff   = 60 * time.Second
)

// remoteAuth is the network's sign-in (#416): the persistent token, typed into the sign-in
// page, buys a session of its own; wrong tokens slow every next try down, whoever sends it.
type remoteAuth struct {
	token    string
	now      func() time.Time
	mu       sync.Mutex
	sessions map[string]time.Time // session → when it signed in
	failures int
	notUntil time.Time // no try is checked before it
}

// valid reports whether ses is a live session.
func (a *remoteAuth) valid(ses string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for s := range a.sessions {
		if subtle.ConstantTimeCompare([]byte(s), []byte(ses)) == 1 {
			return true
		}
	}
	return false
}

// signIn checks tok and returns a new session; wait > 0 when the try came too soon.
func (a *remoteAuth) signIn(tok string) (ses string, wait time.Duration, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	if now.Before(a.notUntil) {
		return "", a.notUntil.Sub(now), nil
	}
	if subtle.ConstantTimeCompare([]byte(tok), []byte(a.token)) != 1 {
		a.failures++
		if a.failures >= freeFailures {
			d := min(time.Second<<min(a.failures-freeFailures, 6), maxBackoff)
			a.notUntil = now.Add(d)
		}
		return "", 0, nil
	}
	a.failures, a.notUntil = 0, time.Time{}
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
	ses, wait, err := s.remote.signIn(p.Token)
	switch {
	case err != nil:
		writeError(w, http.StatusInternalServerError, "no session")
	case wait > 0:
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds()+0.999)))
		writeError(w, http.StatusTooManyRequests, "too many wrong tokens: wait a little")
	case ses == "":
		writeError(w, http.StatusUnauthorized, "wrong token")
	default:
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: ses, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}
