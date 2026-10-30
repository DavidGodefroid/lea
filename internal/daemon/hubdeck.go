package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/DavidGodefroid/lockkit/ticket"

	"github.com/DavidGodefroid/lea/internal/agents"
	"github.com/DavidGodefroid/lea/internal/store"
	"github.com/DavidGodefroid/lea/internal/textsafe"
)

// The deck's facade (#158): lea reads the hub's pending tickets from the facade socket's queue
// (a long poll), shows each one on the agent it is for, and answers it there with the user's
// approval or denial (facade "deck"). A ticket is for the agent whose own wait names it (hub_waits,
// verified), else for the agent its requester claims (the requester is the agent's own words:
// unverified). A ticket no agent of lea claims shows nowhere: `lock approve` answers it. Each
// Lock's counter (#153) follows the queue.

const (
	// hubQueueWait is the long poll of GET /v1/queue (the hub caps it at 60 s); hubQueueRetry the
	// pause after a failure or while no hub runs.
	hubQueueWait  = 30
	hubQueueRetry = 10 * time.Second
	maxHubQueue   = 200
	maxHubLines   = 40
	// maxHubLine is the longest line of a Lock's screen lea passes whole; maxRetype the longest
	// name the hub takes typed.
	maxHubLine = 2000
	maxRetype  = 128
)

// HubTicket is one pending ticket of the hub's queue: the fields of the facade's item and of the
// Lock's signed ticket (lockkit's ticket.Ticket) the deck uses, as the hub sent them.
type HubTicket struct {
	ID, Instance, Lock, Risk string
	Expires                  int64
	Production               bool
	Retype                   string
	Header, Note             string
	Body, Warnings           []string
	// Agent and Session are the ticket's requester: what the agent told its Lock.
	Agent, Session string
	// KeyID names the Lock's key that signed the ticket: a reveal opens only under the one lock
	// up pinned for the instance (hubreveal.go).
	KeyID string
	// Expired: from the queue's expired list (the deck may ask the Lock again, never decide it).
	// Renews: this ticket asks again for an expired one.
	Expired bool
	Renews  *HubRenewal
}

// HubRenewal is a renewal ticket's link to the expired ticket it asks again for (lockkit's
// ticket.Renewal): its id, why it expired, whether the Lock compared the plan's inputs, and what
// changed since.
type HubRenewal struct {
	Of, Reason string
	Checked    bool
	Changes    []HubChange
}

// HubChange is one input of a renewal's plan and how it changed (changed, new, gone).
type HubChange struct{ Input, Change string }

// HubVerdict is the user's answer to a ticket from the deck.
type HubVerdict struct {
	Approved       bool
	Reason, Retype string
}

// hubEntry is a queued ticket lea shows: the agent it is for and that agent's container.
type hubEntry struct {
	t                HubTicket
	agent, container string
	verified         bool
}

var hubRef = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func (f SocketFacade) client(timeout time.Duration) *http.Client {
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", f.Path)
	}}, Timeout: timeout}
}

func (f SocketFacade) Queue(ctx context.Context, since uint64, wait int) ([]HubTicket, uint64, error) {
	if fi, err := os.Lstat(f.Path); err != nil || fi.Mode()&os.ModeSocket == 0 {
		return nil, 0, ErrNoHub
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://lockhub/v1/queue?since=%d&wait=%d", since, wait), nil)
	if err != nil {
		return nil, 0, err
	}
	res, err := f.client(time.Duration(wait)*time.Second + hubSyncTimeout).Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("lockhub: GET /v1/queue: %s", res.Status)
	}
	var out struct {
		Tickets []queueItem `json:"tickets"`
		Version uint64      `json:"version"`
		// Expired are the tickets that expired in the last 24 hours with no renewal yet; Facades
		// the ones that may ask the Lock again for each (lockhub#29).
		Expired []queueItem `json:"expired"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxHubBody)).Decode(&out); err != nil {
		return nil, 0, fmt.Errorf("lockhub: GET /v1/queue: %w", err)
	}
	var ts []HubTicket
	for _, it := range out.Tickets {
		if len(ts) == maxHubQueue {
			break
		}
		if t, ok := it.ticket(); ok {
			ts = append(ts, t)
		}
	}
	for _, it := range out.Expired {
		if len(ts) == maxHubQueue {
			break
		}
		if !slices.Contains(it.Facades, "deck") {
			continue // the deck may not ask again for it: lock approve, or nothing
		}
		if t, ok := it.ticket(); ok {
			t.Expired = true
			ts = append(ts, t)
		}
	}
	return ts, out.Version, nil
}

// queueItem is one item of GET /v1/queue (lockhub's api.QueueItem).
type queueItem struct {
	ID       string          `json:"id"`
	Instance string          `json:"instance"`
	Risk     string          `json:"risk"`
	Expires  *time.Time      `json:"expires"`
	Facades  []string        `json:"facades"`
	Ticket   json.RawMessage `json:"ticket"`
}

// ticket reads the fields of the item's signed ticket the deck uses; false: not a ticket lea can
// name (`lock approve` answers it).
func (it queueItem) ticket() (HubTicket, bool) {
	var tk struct {
		ID     string `json:"id"`
		Source struct {
			Lock     string `json:"lock"`
			Instance string `json:"instance"`
			KeyID    string `json:"key_id"`
		} `json:"source"`
		Requester struct {
			Agent   string `json:"agent"`
			Session string `json:"session"`
		} `json:"requester"`
		Flags struct {
			Production bool   `json:"production"`
			Retype     string `json:"retype"`
		} `json:"flags"`
		Screen struct {
			Header   string   `json:"header"`
			Note     string   `json:"note"`
			Body     []string `json:"body"`
			Warnings []string `json:"warnings"`
		} `json:"screen"`
		Renews *struct {
			Of      string `json:"of"`
			Reason  string `json:"reason"`
			Checked bool   `json:"checked"`
			Changes []struct {
				Input  string `json:"input"`
				Change string `json:"change"`
			} `json:"changes"`
		} `json:"renews"`
	}
	if json.Unmarshal(it.Ticket, &tk) != nil || tk.ID != it.ID || tk.Source.Instance != it.Instance ||
		!hubRef.MatchString(it.ID) || !hubRef.MatchString(it.Instance) || !lockName.MatchString(tk.Source.Lock) {
		return HubTicket{}, false
	}
	t := HubTicket{ID: it.ID, Instance: it.Instance, Lock: tk.Source.Lock, KeyID: tk.Source.KeyID, Risk: it.Risk,
		Production: tk.Flags.Production, Retype: tk.Flags.Retype, Header: tk.Screen.Header, Note: tk.Screen.Note,
		Body: tk.Screen.Body, Warnings: tk.Screen.Warnings, Agent: tk.Requester.Agent, Session: tk.Requester.Session}
	if it.Expires != nil {
		t.Expires = it.Expires.Unix()
	}
	if r := tk.Renews; r != nil {
		if !hubRef.MatchString(r.Of) {
			return HubTicket{}, false
		}
		t.Renews = &HubRenewal{Of: r.Of, Reason: r.Reason, Checked: r.Checked}
		for i, c := range r.Changes {
			if i == ticket.MaxChanges {
				t.Renews.Checked = false // more than a Lock writes: say it could not compare
				break
			}
			t.Renews.Changes = append(t.Renews.Changes, HubChange{Input: c.Input, Change: c.Change})
		}
	}
	return t, true
}

// Errors of POST …/decide, as the deck tells them.
var (
	ErrHubDecided   = errors.New("this ticket was decided already")
	ErrHubRetype    = errors.New("the typed name does not match: approving this production ticket needs it typed exactly")
	ErrHubNotRouted = errors.New("this ticket is not routed to the deck: answer it with lock approve")
)

func (f SocketFacade) Decide(ctx context.Context, instance, id string, v HubVerdict) error {
	if fi, err := os.Lstat(f.Path); err != nil || fi.Mode()&os.ModeSocket == 0 {
		return ErrNoHub
	}
	body, _ := json.Marshal(struct {
		Approved bool   `json:"approved"`
		Reason   string `json:"reason,omitempty"`
		Retype   string `json:"retype,omitempty"`
		Facade   string `json:"facade"`
		Display  string `json:"display,omitempty"`
	}{v.Approved, v.Reason, v.Retype, "deck", os.Getenv("USER")})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://lockhub/v1/tickets/"+url.PathEscape(instance)+"/"+url.PathEscape(id)+"/decide", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := f.client(hubSyncTimeout).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	io.Copy(io.Discard, io.LimitReader(res.Body, maxHubBody))
	switch res.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusConflict:
		return ErrHubDecided
	case http.StatusUnprocessableEntity:
		return ErrHubRetype
	case http.StatusForbidden:
		return ErrHubNotRouted
	}
	return fmt.Errorf("lockhub: decide: %s", res.Status)
}

// RunHubQueue follows the hub's queue until ctx ends: a long poll, and a pause after a failure.
func (d *Daemon) RunHubQueue(ctx context.Context) {
	var since uint64
	for ctx.Err() == nil {
		v, err := d.pollHubQueue(ctx, since)
		if err == nil {
			if v == since { // the hub answered at once with nothing new: no busy loop
				select {
				case <-time.After(time.Second):
				case <-ctx.Done():
				}
			}
			since = v
			continue
		}
		if !errors.Is(err, ErrNoHub) && ctx.Err() == nil {
			log.Printf("lea daemon: hub queue: %v", err)
		}
		since = 0
		select {
		case <-time.After(hubQueueRetry):
		case <-ctx.Done():
		}
	}
}

// pollHubQueue reads the queue once (waiting for a version after since) and shows it. A failure
// shows no ticket: the deck cannot answer them.
func (d *Daemon) pollHubQueue(ctx context.Context, since uint64) (uint64, error) {
	d.mu.Lock()
	f := d.hubFacade
	d.mu.Unlock()
	if f == nil {
		return 0, ErrNoHub
	}
	ts, v, err := f.Queue(ctx, since, hubQueueWait)
	if err != nil {
		d.setHubQueue(ctx, nil, false)
		return 0, err
	}
	d.setHubQueue(ctx, ts, true)
	return v, nil
}

// setHubQueue maps each ticket to the agent it is for and sets each Lock's counter from them.
func (d *Daemon) setHubQueue(ctx context.Context, ts []HubTicket, up bool) {
	var entries []hubEntry
	if len(ts) > 0 {
		all, err := d.st.Agents()
		var waits, every []store.HubWait
		if err == nil {
			waits, err = d.st.AllPendingHubWaits()
		}
		if err == nil {
			every, err = d.st.AllHubWaits()
		}
		if err != nil { // no agent to show them on: none shows
			log.Printf("lea daemon: hub queue: %v", err)
			d.showHub(ctx, nil, "")
			return
		}
		byKey := map[string]store.Agent{}
		for _, a := range all {
			byKey[a.Key] = a
		}
		waiting := map[string]string{}
		for _, w := range waits {
			if _, ok := waiting[w.Instance+"/"+w.Ticket]; !ok {
				waiting[w.Instance+"/"+w.Ticket] = w.Agent
			}
		}
		// An expired ticket, or the renewal of one, is for the agent that waited on the expired
		// ticket: its wait has ended (expired), so any of its waits names it.
		waited := map[string]string{}
		for _, w := range every {
			if _, ok := waited[w.Instance+"/"+w.Ticket]; !ok {
				waited[w.Instance+"/"+w.Ticket] = w.Agent
			}
		}
		for _, t := range ts {
			e := hubEntry{t: t}
			k, ok := waiting[t.Instance+"/"+t.ID]
			switch {
			case !ok && t.Expired:
				k, ok = waited[t.Instance+"/"+t.ID]
			case !ok && t.Renews != nil:
				// renews.of is the Lock's signed word, which lea does not check itself: the hub
				// verifies the ticket and links the renewal to a ticket of the same instance.
				k, ok = waited[t.Instance+"/"+t.Renews.Of]
			}
			if ok && byKey[k].Key != "" {
				e.agent, e.verified = k, true
			} else {
				e.agent = claimedAgent(all, t)
			}
			if e.agent == "" {
				continue
			}
			e.container = byKey[e.agent].Container
			entries = append(entries, e)
		}
	}
	console := ""
	if up {
		console = "up"
	}
	d.showHub(ctx, entries, console)
}

// showHub records the queued tickets lea shows and sets each Lock's counter from them.
func (d *Daemon) showHub(ctx context.Context, entries []hubEntry, console string) {
	d.showHubWith(ctx, func([]hubEntry) []hubEntry { return entries }, console)
}

// dropHub removes the queued tickets gone matches from the queue as it is then, not from an
// older copy (a poll may have set a newer one meanwhile), and sets the counters from it.
func (d *Daemon) dropHub(ctx context.Context, gone func(hubEntry) bool) {
	d.showHubWith(ctx, func(cur []hubEntry) []hubEntry {
		var rest []hubEntry
		for _, e := range cur {
			if !gone(e) {
				rest = append(rest, e)
			}
		}
		return rest
	}, "up")
}

// showHubWith sets the queue to next(current), under one lock, and each Lock's counter from it.
func (d *Daemon) showHubWith(ctx context.Context, next func(cur []hubEntry) []hubEntry, console string) {
	d.mu.Lock()
	entries := next(d.hubQueue)
	d.hubQueue = entries
	known := d.hubLocks
	d.hubLocks = map[string]bool{}
	counts := map[string]map[string]int{}
	for _, e := range entries {
		if e.t.Lock == "lockhttp" || e.t.Expired {
			continue // the gate's status file counts LockHTTP's requests; an expired ticket waits on no one
		}
		if counts[e.t.Lock] == nil {
			counts[e.t.Lock] = map[string]int{}
		}
		counts[e.t.Lock][e.container]++
		d.hubLocks[e.t.Lock] = true
	}
	for l := range known {
		d.hubLocks[l] = true
	}
	locks := d.hubLocks
	d.mu.Unlock()
	for l := range locks {
		d.SetLockStatus(ctx, l, LockStatus{Console: console, PendingByContainer: counts[l]})
	}
	d.changed()
}

// claimedAgent is the agent a ticket's requester claims: the live one with that session, else the
// only live agent of that name; "" when none (or more than one) is. The requester is what an
// agent told its Lock, any agent could claim another: the deck shows such a ticket "unverified",
// and the Lock's own screen says what it would do.
func claimedAgent(all []store.Agent, t HubTicket) string {
	live := func(a store.Agent) bool { return a.State != "done" && a.State != "stopped" }
	if t.Session != "" {
		for _, a := range all {
			if a.SessionID == t.Session && live(a) {
				return a.Key
			}
		}
	}
	found := ""
	for _, a := range all {
		if t.Agent == "" || a.Name != t.Agent || !live(a) {
			continue
		}
		if found != "" {
			return ""
		}
		found = a.Key
	}
	return found
}

// addHubCards puts each queued ticket on its agent's row, oldest first.
func (d *Daemon) addHubCards(snap *agents.Snapshot) {
	d.mu.Lock()
	entries := d.hubQueue
	d.mu.Unlock()
	if len(entries) == 0 {
		return
	}
	for i := range snap.Agents {
		for _, e := range entries {
			if e.agent == snap.Agents[i].Key {
				snap.Agents[i].Hub = append(snap.Agents[i].Hub, hubCard(e))
			}
		}
	}
}

func hubCard(e hubEntry) agents.HubCard {
	t := e.t
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
	claimed, _ := hubLine(t.Agent, 64)
	note, _ := hubLine(t.Note, 240) // the agent's words: cut, they say nothing of what runs
	c := agents.HubCard{ID: t.ID, Instance: t.Instance, Lock: t.Lock, Risk: line(t.Risk, 16),
		Production: t.Production, Retype: line(t.Retype, maxRetype), Verified: e.verified,
		Header: line(t.Header, maxHubLine), Note: note, Body: lines(t.Body, maxHubLines),
		Warnings: lines(t.Warnings, 10), Expires: t.Expires, Claimed: claimed, Truncated: cut, Expired: t.Expired}
	if r := t.Renews; r != nil {
		c.Renews = &agents.HubRenewal{Of: r.Of, Reason: line(r.Reason, 32), Checked: r.Checked}
		for _, ch := range r.Changes {
			c.Renews.Changes = append(c.Renews.Changes, agents.HubChange{Input: line(ch.Input, 200), Change: line(ch.Change, 16)})
		}
		c.Truncated = cut
	}
	return c
}

// hubLine is a line of the Lock's screen on one line, without control, bidi or zero-width
// characters, at most n runes; cut reports that it was longer.
func hubLine(s string, n int) (line string, cut bool) {
	s = strings.Map(func(r rune) rune {
		if textsafe.Unsafe(r) || r == utf8.RuneError {
			return ' '
		}
		return r
	}, s)
	if utf8.RuneCountInString(s) > n {
		return string([]rune(s)[:n-1]) + "…", true
	}
	return s, false
}

// hubDecide answers a queued ticket of one of container's agents from that container's deck.
func (d *Daemon) hubDecide(ctx context.Context, params json.RawMessage) error {
	var p struct {
		Container, Instance, ID string
		Approved                bool
		Reason, Retype          string
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return err
	}
	d.mu.Lock()
	f := d.hubFacade
	found := false
	for _, e := range d.hubQueue {
		if e.t.ID == p.ID && e.t.Instance == p.Instance && e.container == p.Container && p.Container != "" && !e.t.Expired {
			found = true
		}
	}
	d.mu.Unlock()
	if f == nil || !found {
		return errors.New("no such ticket in this deck")
	}
	if len(p.Retype) > maxRetype || strings.ContainsFunc(p.Retype, textsafe.Unsafe) {
		return errors.New("the typed name is too long or holds control characters")
	}
	reason, _ := hubLine(p.Reason, 400)
	if err := f.Decide(ctx, p.Instance, p.ID, HubVerdict{Approved: p.Approved, Reason: reason, Retype: p.Retype}); err != nil {
		return err
	}
	d.dropHub(ctx, func(e hubEntry) bool { return e.t.ID == p.ID && e.t.Instance == p.Instance && !e.t.Expired })
	return nil
}
