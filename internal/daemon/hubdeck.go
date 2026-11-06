package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/DavidGodefroid/lea/internal/store"
)

// The hub's queue (#158, #300): lea reads the hub's pending tickets from the facade socket's queue
// (a long poll) only to know which agent each one waits for. It shows no ticket and answers none:
// `lox run`, in a terminal of its own outside lea, decides them. A ticket is for the agent whose
// own wait names it (hub_waits, verified), else for the agent its requester claims (the requester
// is the agent's own words), for which lea records a claimed wait so that the decision reaches it
// (#261). Each Lock's counter (#153) follows the queue: the deck says how many requests of its
// container wait for lox run.

const (
	// hubQueueWait is the long poll of GET /v1/queue (the hub caps it at 60 s); hubQueueRetry the
	// pause after a failure or while no hub runs.
	hubQueueWait  = 30
	hubQueueRetry = 10 * time.Second
	maxHubQueue   = 200
)

// HubTicket is one pending ticket of the hub's queue: the fields of the facade's item and of the
// Lock's signed ticket (lockkit's ticket.Ticket) lea uses to tell which agent waits on it, as
// the hub sent them. lea keeps nothing of the Lock's screen: lox run shows it.
type HubTicket struct {
	ID, Instance, Lock string
	Created, Expires   int64
	// Agent and Session are the ticket's requester: what the agent told its Lock.
	Agent, Session string
	// Expired: from the queue's expired list (it waits on no one). Renews: this ticket asks again
	// for an expired one.
	Expired bool
	Renews  *HubRenewal
}

// HubRenewal is a renewal ticket's link to the expired ticket it asks again for (lockkit's
// ticket.Renewal): its id.
type HubRenewal struct{ Of string }

// hubEntry is a queued ticket: the agent it is for ("" when none), that agent's container, and
// whether the agent's own wait names it (verified) or its requester claims the agent.
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
		// Expired are the tickets that expired in the last 24 hours with no renewal yet: a
		// renewal of one is for the agent that waited on it.
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
	Created  *time.Time      `json:"created"`
	Expires  *time.Time      `json:"expires"`
	Ticket   json.RawMessage `json:"ticket"`
}

// ticket reads the fields of the item's signed ticket lea uses; false: not a ticket lea can name.
func (it queueItem) ticket() (HubTicket, bool) {
	var tk struct {
		ID     string `json:"id"`
		Source struct {
			Lock     string `json:"lock"`
			Instance string `json:"instance"`
		} `json:"source"`
		Requester struct {
			Agent   string `json:"agent"`
			Session string `json:"session"`
		} `json:"requester"`
		Renews *struct {
			Of string `json:"of"`
		} `json:"renews"`
	}
	if json.Unmarshal(it.Ticket, &tk) != nil || tk.ID != it.ID || tk.Source.Instance != it.Instance ||
		!hubRef.MatchString(it.ID) || !hubRef.MatchString(it.Instance) || !lockName.MatchString(tk.Source.Lock) {
		return HubTicket{}, false
	}
	t := HubTicket{ID: it.ID, Instance: it.Instance, Lock: tk.Source.Lock, Agent: tk.Requester.Agent, Session: tk.Requester.Session}
	if it.Expires != nil {
		t.Expires = it.Expires.Unix()
	}
	if it.Created != nil {
		t.Created = it.Created.Unix()
	}
	if r := tk.Renews; r != nil {
		if !hubRef.MatchString(r.Of) {
			return HubTicket{}, false
		}
		t.Renews = &HubRenewal{Of: r.Of}
	}
	return t, true
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
		var abandoned map[string]bool
		if err == nil {
			waits, err = d.st.AllPendingHubWaits()
		}
		if err == nil {
			every, err = d.st.AllHubWaits()
		}
		if err == nil {
			abandoned, err = d.st.AbandonedHubTickets()
		}
		if err != nil { // no agent to tell them from: none shows
			log.Printf("lea daemon: hub queue: %v", err)
			d.showHub(ctx, nil, "")
			return
		}
		byKey := map[string]store.Agent{}
		for _, a := range all {
			byKey[a.Key] = a
		}
		// A wait lea claimed itself (#261) verifies nothing: its ticket is still the requester's
		// word.
		waiting := map[string]string{}
		for _, w := range waits {
			if w.Claimed {
				continue
			}
			if _, ok := waiting[w.Instance+"/"+w.Ticket]; !ok {
				waiting[w.Instance+"/"+w.Ticket] = w.Agent
			}
		}
		// An expired ticket, or the renewal of one, is for the agent that waited on the expired
		// ticket: its wait has ended (expired), so any of its waits names it, but one lea
		// abandoned (the hub off): lea told that agent it stopped waiting on the ticket.
		waited := map[string]string{}
		for _, w := range every {
			if w.Decision == store.DecisionAbandoned || w.Claimed {
				continue
			}
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
			switch {
			case ok && byKey[k].Key != "":
				e.agent, e.verified = k, true
			case abandoned[t.Instance+"/"+t.ID] || (t.Renews != nil && abandoned[t.Instance+"/"+t.Renews.Of]):
				// lea stopped waiting on it, or on the ticket it renews (its agent forgotten, the
				// hub off): not a ticket of a live agent that bears the same name.
			default:
				e.agent = claimedAgent(all, t)
				d.claimHubWait(e.agent, t)
			}
			if e.agent != "" {
				e.container = byKey[e.agent].Container
			}
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

// showHubWith sets the queue to next(current), under one lock, and each Lock's counter from it.
func (d *Daemon) showHubWith(ctx context.Context, next func(cur []hubEntry) []hubEntry, console string) {
	d.mu.Lock()
	entries := next(d.hubQueue)
	d.hubQueue = entries
	known := d.hubLocks
	d.hubLocks = map[string]bool{}
	counts := map[string]map[string]int{}
	for _, e := range entries {
		if e.t.Lock == "lockhttp" || e.t.Expired || e.container == "" {
			// the gate's status file counts LockHTTP's requests; an expired ticket waits on no
			// one; a ticket for no agent waits in no container
			continue
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
// agent told its Lock, any agent could claim another: lea records such a wait as claimed,
// and lox run shows the Lock's own screen, which says what it would do.
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

// claimHubWait records the wait of agent on the pending ticket its requester claims it for
// (#261): an agent that ran its Lock through the shell, or whose Lock call lea did not see
// answer pending, registered none, and would never hear the decision. With the wait, the
// decision reaches it as any wait's does: in its inbox, which wakes it when its turn is over (its
// rewake watch), or in the resume of a cold-suspended agent. The claim is the requester's word:
// the card stays unverified, and the Lock still runs the ticket for its requester's token alone.
// An expired ticket waits on no one; LockHTTP's request waits in the gate, not in a turn.
func (d *Daemon) claimHubWait(agent string, t HubTicket) {
	if agent == "" || t.Expired || t.Lock == "lockhttp" {
		return
	}
	if err := d.st.AddClaimedHubWait(agent, t.ID, t.Lock, t.Instance, d.now()); err != nil {
		log.Printf("lea daemon: claim hub ticket %.40q for %s: %v", t.ID, agent, err)
	}
}
