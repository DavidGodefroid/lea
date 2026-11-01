package daemon

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"github.com/DavidGodefroid/lockkit/ticket"
)

// Ctrl-O and one-click re-approval from the deck (lockhub#29, lockkit v0.5.0). A reveal shows the
// clear values of a pending ticket: the daemon makes an ephemeral X25519 key signed by the deck's
// facade key (which the Locks pin, written by lox up), the hub carries it to the Lock, the Lock
// seals the clear screen to it and signs the answer, and the daemon opens it under the Lock key
// lox up pinned. The ephemeral key does not outlive the call; the clear screen goes to the deck
// that asked, as the method's answer, and nowhere else: not lea.db, not a log, not the bus, not
// a snapshot. A renewal asks the Lock again for an expired ticket; the hub approves it at once
// when nothing changed (not for production), otherwise the renewal waits in the queue.
//
// Both are host-only deck methods (deckMethods): no agent can reach them.

const (
	// hubAskTimeout bounds a reveal or a renewal: the hub waits 30 s at most for the Lock.
	hubAskTimeout = 40 * time.Second
	// maxFacadeFile caps the facade key and the pinned lock keys files.
	maxFacadeFile = 1 << 20
	maxRevealBody = 512 << 10
)

// HubClear is a ticket's clear screen as the deck shows it: cleaned like the masked one (no
// control, bidi or zero-width character, each line capped), Truncated when lea had to cut it.
type HubClear struct {
	Header    string   `json:"header,omitempty"`
	Note      string   `json:"note,omitempty"`
	Body      []string `json:"body,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
}

// HubRenewed is the hub's answer to a renewal: the renewal's ticket, and whether the hub approved
// it at once (nothing changed since the approval).
type HubRenewed struct {
	Ticket       string `json:"ticket"`
	AutoApproved bool   `json:"auto_approved"`
}

// HubAskError is the hub's refusal of a reveal or a renewal: its HTTP status, its kind and, when
// the Lock refused (lock_refused), the Lock's answer code. Never the hub's or the Lock's free text.
type HubAskError struct {
	Status     int
	Kind, Code string
}

func (e *HubAskError) Error() string { return explainHub(e) }

var hubCode = regexp.MustCompile(`^[a-z][a-z_]{0,31}$`)

// lockAnswers are the human words of a Lock's answer codes (lockkit hubclient Code*).
var lockAnswers = map[string]string{
	"facade_refused":  "the lock does not pin the deck's facade key (run lox up)",
	"not_pending":     "the ticket is no longer pending at the lock: nothing to reveal",
	"no_clear_screen": "the lock kept no clear screen for this ticket",
	"not_renewable":   "the lock cannot ask again for this ticket (not expired there, or gone)",
	"recheck_failed":  "the lock could not check the plan again",
	"policy_changed":  "the policy changed: the agent must plan again",
	"rate_limited":    "the lock limits new tickets right now: try again later",
	"failed":          "the lock could not answer",
}

// hubKinds are the human words of the hub's error kinds on these routes.
var hubKinds = map[string]string{
	"timeout":          "the lock did not answer in time (is it running?)",
	"not_routed":       "this ticket is not routed to the deck: use lox approve",
	"foreign_key":      "the hub refused the deck's reveal key: it is for another ticket or lock",
	"expired":          "the ticket expired",
	"not_found":        "the hub no longer knows this ticket",
	"state":            "the ticket is not in a state that allows this",
	"conflict":         "the ticket changed meanwhile",
	"busy":             "the lock is busy with other requests: try again later",
	"too_many_pending": "the lock is busy with other requests: try again later",
	"forbidden":        "the hub refused: this facade may not do that",
	"unknown_peer":     "the hub cannot tell who asks on its socket",
	"bad_request":      "the hub refused the request",
}

// explainHub is the short human message of a hub refusal: codes only.
func explainHub(e *HubAskError) string {
	if e.Kind == "lock_refused" {
		if m, ok := lockAnswers[e.Code]; ok {
			return m
		}
		return "the lock refused"
	}
	if m, ok := hubKinds[e.Kind]; ok {
		return m
	}
	return fmt.Sprintf("the hub refused (HTTP %d)", e.Status)
}

// post sends one JSON request to the facade socket and decodes its answer; a refusal becomes a
// HubAskError.
func (f SocketFacade) post(ctx context.Context, path string, in, out any) error {
	if fi, err := os.Lstat(f.Path); err != nil || fi.Mode()&os.ModeSocket == 0 {
		return ErrNoHub
	}
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://lockhub"+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := f.client(hubAskTimeout).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, maxRevealBody+1))
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		e := &HubAskError{Status: res.StatusCode}
		var he struct {
			Error   json.RawMessage `json:"error"`
			Message string          `json:"message"`
		}
		if json.Unmarshal(b, &he) == nil {
			var kind string
			if json.Unmarshal(he.Error, &kind) == nil && hubCode.MatchString(kind) {
				e.Kind = kind
				if kind == "lock_refused" && hubCode.MatchString(he.Message) {
					e.Code = he.Message // the Lock's code (lockhub's fail): a code, never its text
				}
			}
		}
		return e
	}
	if len(b) > maxRevealBody {
		return errors.New("the hub's answer is too large")
	}
	if json.Unmarshal(b, out) != nil {
		return errors.New("the hub's answer is not valid") // never the decoder's words on the hub's bytes
	}
	return nil
}

func ticketPath(instance, id, verb string) string {
	return "/v1/tickets/" + url.PathEscape(instance) + "/" + url.PathEscape(id) + "/" + verb
}

// Reveal asks the Lock, through the hub, for the clear screen of a ticket sealed to key (a
// ticket.RevealKey of the deck facade), and returns the Lock's signed answer verbatim.
func (f SocketFacade) Reveal(ctx context.Context, instance, id string, key []byte) ([]byte, error) {
	var out struct {
		Answer json.RawMessage `json:"answer"`
	}
	err := f.post(ctx, ticketPath(instance, id, "reveal"), struct {
		Facade  string          `json:"facade"`
		Display string          `json:"display,omitempty"`
		Key     json.RawMessage `json:"key"`
	}{ticket.FacadeDeck, os.Getenv("USER"), key}, &out)
	return out.Answer, err
}

// Renew asks the Lock, through the hub, again for an expired ticket.
func (f SocketFacade) Renew(ctx context.Context, instance, id string) (HubRenewed, error) {
	var out HubRenewed
	err := f.post(ctx, ticketPath(instance, id, "renew"), struct {
		Facade  string `json:"facade"`
		Display string `json:"display,omitempty"`
	}{ticket.FacadeDeck, os.Getenv("USER")}, &out)
	return out, err
}

// SetHubKeys sets the directory of the host facades' keys lox up writes ($XDG_STATE_HOME/
// lockfamily/facades): the deck's key (deck.key) and the pinned lock keys (locks.json). "": no
// reveal.
func (d *Daemon) SetHubKeys(dir string) {
	d.mu.Lock()
	d.hubKeys = dir
	d.mu.Unlock()
}

// privateFile reads a file the user alone may change: opened once without following a link,
// then checked on the open file (a regular file owned by the user, mode without the bits in deny,
// not too large) and read from it, so that nothing can swap it between the check and the read.
// Its directory must be the user's and writable by no one else (no file swapped in by name).
func privateFile(path string, deny os.FileMode) ([]byte, error) {
	if err := privateDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("%s: not a regular file", path)
		}
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if err := ownedBy(path, fi); err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", path)
	}
	if fi.Mode().Perm()&deny != 0 {
		return nil, fmt.Errorf("%s: mode %v, want %v", path, fi.Mode().Perm(), fi.Mode().Perm()&^deny)
	}
	if fi.Size() > maxFacadeFile {
		return nil, fmt.Errorf("%s: too large", path)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxFacadeFile+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxFacadeFile {
		return nil, fmt.Errorf("%s: too large", path)
	}
	return b, nil
}

// privateDir checks that dir is a directory (not a link) of the user's, that no one else can write.
func privateDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s: not a directory", dir)
	}
	if err := ownedBy(dir, fi); err != nil {
		return err
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s: mode %v: others can write it", dir, fi.Mode().Perm())
	}
	return nil
}

func ownedBy(path string, fi os.FileInfo) error {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s: not owned by this user", path)
	}
	return nil
}

// loadFacadeKey reads a facade's Ed25519 key (PKCS#8 PEM) as lox up writes it, 0600: a key any
// other user could read is refused.
func loadFacadeKey(path string) (ed25519.PrivateKey, error) {
	b, err := privateFile(path, 0o077)
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(b)
	if blk == nil || blk.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("%s: not a PEM private key", path)
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s: not a PKCS#8 key", path)
	}
	priv, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s: not an Ed25519 key", path)
	}
	return priv, nil
}

// loadLockKeys reads the lock keys lox up pinned ({"locks": {"<instance>": {"public_key":
// "<base64>"}}}), by instance. A file another user could change is refused.
func loadLockKeys(path string) (map[string]ed25519.PublicKey, error) {
	b, err := privateFile(path, 0o022)
	if err != nil {
		return nil, err
	}
	var f struct {
		Locks map[string]struct {
			PublicKey string `json:"public_key"`
		} `json:"locks"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: not a lock keys file", path)
	}
	out := map[string]ed25519.PublicKey{}
	for name, l := range f.Locks {
		k, err := base64.StdEncoding.DecodeString(l.PublicKey)
		if err != nil || len(k) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("%s: instance %q: not a base64 Ed25519 key", path, name)
		}
		out[name] = ed25519.PublicKey(k)
	}
	return out, nil
}

// hubAsked finds the queued ticket of one of container's agents a deck asks about: pending
// (reveal) or expired (renew).
func (d *Daemon) hubAsked(params json.RawMessage, expired bool) (HubFacade, hubEntry, string, error) {
	var p struct{ Container, Instance, ID string }
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, hubEntry{}, "", err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, e := range d.hubQueue {
		if e.t.ID == p.ID && e.t.Instance == p.Instance && e.container == p.Container && p.Container != "" && e.t.Expired == expired {
			if d.hubFacade == nil {
				break
			}
			return d.hubFacade, e, d.hubKeys, nil
		}
	}
	return nil, hubEntry{}, "", errors.New("no such ticket in this deck")
}

// hubReveal shows the clear values of a pending ticket of one of container's agents to that
// container's deck. Nothing of them is kept: the answer goes to the deck, and the ephemeral key
// is dropped once the answer is open.
func (d *Daemon) hubReveal(ctx context.Context, params json.RawMessage) (HubClear, error) {
	f, e, dir, err := d.hubAsked(params, false)
	if err != nil {
		return HubClear{}, err
	}
	if dir == "" {
		return HubClear{}, errors.New("no facade keys: clear values cannot be shown here (run lox up)")
	}
	fk, err := loadFacadeKey(filepath.Join(dir, ticket.FacadeDeck+".key"))
	if err != nil {
		return HubClear{}, fmt.Errorf("the deck's facade key (run lox up): %w", err)
	}
	locks, err := loadLockKeys(filepath.Join(dir, "locks.json"))
	if err != nil {
		return HubClear{}, fmt.Errorf("the pinned lock keys (run lox up): %w", err)
	}
	lockPub := locks[e.t.Instance]
	if lockPub == nil || ticket.KeyID(lockPub) != e.t.KeyID {
		return HubClear{}, errors.New("this lock's key is not the one lox up pinned: clear values cannot be shown")
	}
	t := &ticket.Ticket{ID: e.t.ID, Source: ticket.Source{Lock: e.t.Lock, Instance: e.t.Instance, KeyID: e.t.KeyID}}
	rk, priv, err := ticket.NewRevealKey(fk, ticket.FacadeDeck, t, time.Now())
	if err != nil {
		return HubClear{}, errors.New("this ticket cannot be revealed: " + err.Error())
	}
	kb, err := json.Marshal(rk)
	if err != nil {
		return HubClear{}, err
	}
	ans, err := f.Reveal(ctx, e.t.Instance, e.t.ID, kb)
	if err != nil {
		return HubClear{}, fmt.Errorf("no clear values: %w", err)
	}
	// priv lives in this call only: once it returns, the answer cannot be opened again.
	scr, err := ticket.OpenReveal(lockPub, priv, rk, ans)
	if err != nil {
		return HubClear{}, errors.New("the answer does not verify under the pinned lock key: not shown")
	}
	return clearCard(scr), nil
}

// clearCard is a clear screen cleaned for the deck, like the masked one.
func clearCard(s ticket.Screen) HubClear {
	var cut bool
	line := func(s string, n int) string {
		l, c := hubLine(s, n)
		cut = cut || c
		return l
	}
	lines := func(ls []string, n int) []string {
		var out []string
		for i, l := range ls {
			if i == n {
				cut = true
				break
			}
			out = append(out, line(l, maxHubLine))
		}
		return out
	}
	c := HubClear{Header: line(s.Header, maxHubLine), Note: line(s.Note, 240), Body: lines(s.Body, maxHubLines),
		Warnings: lines(s.Warnings, 10)}
	c.Truncated = cut
	return c
}

// hubRenew asks the Lock again for an expired ticket of one of container's agents.
func (d *Daemon) hubRenew(ctx context.Context, params json.RawMessage) (HubRenewed, error) {
	f, e, _, err := d.hubAsked(params, true)
	if err != nil {
		return HubRenewed{}, err
	}
	r, err := f.Renew(ctx, e.t.Instance, e.t.ID)
	if err != nil {
		return HubRenewed{}, fmt.Errorf("not renewed: %w", err)
	}
	if !hubRef.MatchString(r.Ticket) {
		return HubRenewed{}, errors.New("not renewed: the hub named no ticket")
	}
	// The agent whose wait named the expired ticket waits on its renewal now: approved (at once,
	// or later from the queue), the decision reaches it (and resumes it if it is suspended), as
	// for the ticket its Lock call answered; it is told now which ticket that is. Only a verified
	// agent: a ticket shown on an agent by the requester's claim names no wait of lea's. The
	// renewal is for the same caller (lockkit renews a plan for the token that asked), so that
	// agent's run of it is the one the Lock takes. When several agents waited on the expired
	// ticket, the first one's wait names it (setHubQueue): only that agent waits on the renewal.
	if e.verified && e.agent != "" && !d.hubDisabled() {
		if err := d.st.AddHubWait(e.agent, r.Ticket, e.t.Lock, e.t.Instance, d.now()); err != nil {
			log.Printf("lea daemon: hub renewal %.40q of %.40q: no wait for its agent: %v", r.Ticket, e.t.ID, err)
		} else {
			d.post("user", e.agent, "result", hubRenewalText(e.t.ID, r.Ticket, e.t.Lock, e.t.Instance), 0)
			d.changed()
		}
	}
	// The expired ticket has a renewal now: the hub lists it no more.
	d.dropHub(ctx, func(x hubEntry) bool { return x.t.ID == e.t.ID && x.t.Instance == e.t.Instance && x.t.Expired })
	return r, nil
}

// hubRenewalText tells an agent that the deck asked its Lock again for its expired ticket old, as
// ticket renewal: the same planned run, whose decision comes as the renewal's. The ids, Lock and
// instance are the hub's, checked by hubRef and the store.
func hubRenewalText(old, renewal, lock, instance string) string {
	return fmt.Sprintf("[status: pending] the user asked %s (%s) again for your expired ticket %s, as ticket %s: the same run. "+
		"Its decision comes as ticket %s; once approved, run it with ticket=%s. If you planned that run again meanwhile, run only one of them.",
		lock, instance, old, renewal, renewal, renewal)
}
