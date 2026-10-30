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
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/DavidGodefroid/lea/internal/store"
	"github.com/DavidGodefroid/lea/internal/textsafe"
)

// Decision sync (#164): the Lock Family hub decides tickets while agents wait on them (hub_waits).
// lea reads the hub's decided tickets through the read-only endpoint of the hub's host facade
// socket, from the cursor it recorded, so that a decision taken while the daemon was down (a
// restart, a crash) still reaches the agent that waits. Each decision is recorded once on the
// waits (store.DecideHubWaits), told to each waiting agent once, and the cursor moves past it only
// then. A withdrawn or expired ticket is no decision: lea asks the hub for the state of the
// tickets agents still wait on.

const (
	// hubSyncEvery is how often lea asks the hub for new decisions.
	hubSyncEvery = 10 * time.Second
	// hubSyncTimeout bounds one request to the facade socket; maxHubBody one answer.
	hubSyncTimeout = 15 * time.Second
	maxHubBody     = 1 << 20
	maxHubItems    = 500
)

// HubDecided is one decision of GET /v1/decided: its ticket and the fields of the hub's signed
// decision (lockkit's ticket.Decision) lea uses. The socket is the user's own, on the host: lea
// does not check the hub's signature (the Lock does, before it acts).
type HubDecided struct {
	Seq      int64
	Ticket   string
	Instance string
	Decision string // approved or denied
	Reason   string
}

// HubFacade is the hub's host facade: the decisions after a cursor (0: from the start), one
// ticket's state (pending, approved, denied, withdrawn, expired, consumed), and for the deck the
// queue of pending tickets and the user's answer to one.
type HubFacade interface {
	Decided(ctx context.Context, since int64) (items []HubDecided, cursor int64, err error)
	TicketState(ctx context.Context, instance, ticket string) (string, error)
	// Queue is the pending tickets, once the queue's version is past since (or wait seconds
	// passed), and that version; Decide answers one from the deck (hubdeck.go, #158).
	Queue(ctx context.Context, since uint64, wait int) ([]HubTicket, uint64, error)
	Decide(ctx context.Context, instance, id string, v HubVerdict) error
}

// ErrNoHub is a facade socket that is not there: no hub runs (yet); nothing to read.
var ErrNoHub = errors.New("no lockhub facade socket")

// SocketFacade is the hub's host facade socket (HTTP/JSON over a unix socket).
type SocketFacade struct{ Path string }

func (f SocketFacade) get(ctx context.Context, path string, out any) error {
	if fi, err := os.Lstat(f.Path); err != nil || fi.Mode()&os.ModeSocket == 0 {
		return ErrNoHub
	}
	c := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", f.Path)
	}}, Timeout: hubSyncTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://lockhub"+path, nil)
	if err != nil {
		return err
	}
	res, err := c.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("lockhub: GET %s: %s", strings.SplitN(path, "?", 2)[0], res.Status)
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxHubBody)).Decode(out); err != nil {
		return fmt.Errorf("lockhub: GET %s: %w", strings.SplitN(path, "?", 2)[0], err)
	}
	return nil
}

// Ping is nil when the facade socket is there and something accepts a connection on it.
func (f SocketFacade) Ping(ctx context.Context) error {
	if fi, err := os.Lstat(f.Path); err != nil || fi.Mode()&os.ModeSocket == 0 {
		return ErrNoHub
	}
	var dl net.Dialer
	c, err := dl.DialContext(ctx, "unix", f.Path)
	if err != nil {
		return err
	}
	return c.Close()
}

func (f SocketFacade) Decided(ctx context.Context, since int64) ([]HubDecided, int64, error) {
	var out struct {
		Decisions []struct {
			Seq      int64           `json:"seq"`
			Ticket   string          `json:"ticket"`
			Decision json.RawMessage `json:"decision"`
		} `json:"decisions"`
		Cursor int64 `json:"cursor"`
	}
	if err := f.get(ctx, "/v1/decided?since="+strconv.FormatInt(since, 10), &out); err != nil {
		return nil, 0, err
	}
	var items []HubDecided
	for i, d := range out.Decisions {
		if i == maxHubItems {
			break
		}
		var dec struct {
			Ticket   string `json:"ticket"`
			Instance string `json:"instance"`
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
		}
		if json.Unmarshal(d.Decision, &dec) != nil || dec.Ticket != d.Ticket {
			continue // not a decision on that ticket: passed
		}
		items = append(items, HubDecided{Seq: d.Seq, Ticket: d.Ticket, Instance: dec.Instance, Decision: dec.Decision, Reason: dec.Reason})
	}
	cursor := out.Cursor
	if len(out.Decisions) > maxHubItems {
		cursor = out.Decisions[maxHubItems-1].Seq // the rest at the next sync
	}
	return items, cursor, nil
}

func (f SocketFacade) TicketState(ctx context.Context, instance, ticket string) (string, error) {
	var out struct {
		State string `json:"state"`
	}
	err := f.get(ctx, "/v1/tickets/"+url.PathEscape(instance)+"/"+url.PathEscape(ticket), &out)
	return out.State, err
}

// SetHubFacade sets where lea reads the hub's decisions (nil: no hub).
func (d *Daemon) SetHubFacade(f HubFacade) {
	d.mu.Lock()
	d.hubFacade = f
	d.mu.Unlock()
}

// RunHubSync reads the hub's decisions at start, then every hubSyncEvery, until ctx ends.
func (d *Daemon) RunHubSync(ctx context.Context) {
	t := time.NewTicker(hubSyncEvery)
	defer t.Stop()
	for {
		if err := d.syncHub(ctx); err != nil && ctx.Err() == nil {
			log.Printf("lea daemon: hub decisions: %v", err)
		}
		select {
		case <-t.C:
		case <-ctx.Done():
			return
		}
	}
}

// syncHub reads the decisions since the recorded cursor, records them on the waits, tells each
// waiting agent, and moves the cursor. A decision the store cannot take (an invalid item) is
// logged and passed; the cursor moves only once every item was handled, so a failure reads
// them again (recording and delivery are once only).
func (d *Daemon) syncHub(ctx context.Context) error {
	d.mu.Lock()
	f := d.hubFacade
	d.mu.Unlock()
	if f == nil {
		return nil
	}
	d.hubSyncMu.Lock()
	defer d.hubSyncMu.Unlock()
	c, err := d.st.HubCursor()
	if err != nil {
		return err
	}
	since, _ := strconv.ParseInt(c, 10, 64)
	items, cursor, err := f.Decided(ctx, max(since, 0))
	d.setHubDown(err != nil)
	if errors.Is(err, ErrNoHub) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, it := range items {
		if it.Decision != store.DecisionApproved && it.Decision != store.DecisionDenied {
			continue
		}
		if _, err := d.st.DecideHubWaits(it.Ticket, it.Instance, it.Decision, it.Reason, d.now()); err != nil {
			log.Printf("lea daemon: hub decision on %.40q: %v", it.Ticket, err)
		}
	}
	// Withdrawn and expired are no decisions: the ticket's state says so. An approval or a denial
	// the stream does not hold (a hub whose sequence went back under lea's cursor answers that
	// cursor and nothing after it) shows there too, without its reason.
	pending, err := d.st.AllPendingHubWaits()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, w := range pending {
		if seen[w.Instance+"/"+w.Ticket] || ctx.Err() != nil {
			continue
		}
		seen[w.Instance+"/"+w.Ticket] = true
		st, err := f.TicketState(ctx, w.Instance, w.Ticket)
		if err != nil {
			continue // asked again at the next sync
		}
		if st == store.DecisionWithdrawn || st == store.DecisionExpired || st == store.DecisionApproved || st == store.DecisionDenied {
			if _, err := d.st.DecideHubWaits(w.Ticket, w.Instance, st, "", d.now()); err != nil {
				log.Printf("lea daemon: hub ticket %s %s: %v", w.Ticket, st, err)
			}
		}
	}
	// Every decided wait not told yet, these and any a crash left between the record and the
	// message.
	if err := d.deliverHubDecisions(); err != nil {
		return err
	}
	if err := d.st.PurgeHubWaits(d.now()); err != nil {
		log.Printf("lea daemon: purge hub waits: %v", err)
	}
	if cursor != since {
		return d.st.SetHubCursor(strconv.FormatInt(cursor, 10))
	}
	return nil
}

// deliverHubDecisions tells each agent the decisions on its waits it has not had yet, once.
func (d *Daemon) deliverHubDecisions() error {
	ws, err := d.st.UndeliveredAllHubWaits()
	if err != nil {
		return err
	}
	woken := map[string]bool{}
	for _, w := range ws {
		// A cold-suspended agent reads no inbox: its wait stays undelivered, lea resumes it, and
		// the resume prompt tells it the decision (#163).
		if a, err := d.st.AgentByKey(w.Agent); err == nil && a.State == store.StateSuspendedCold {
			if !woken[w.Agent] {
				woken[w.Agent] = true
				d.wakeSuspended(w.Agent)
			}
			continue
		}
		if err := d.st.MarkHubWaitDelivered(w.Agent, w.Ticket, d.now()); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue // delivered meanwhile
			}
			return err
		}
		d.post("user", w.Agent, "result", hubDecisionText(w), 0)
		d.changed()
	}
	return nil
}

// hubDecisionText is what an agent is told of the decision on its wait. The ticket, Lock and
// instance were checked when the wait was recorded; the reason is the hub's, at most 500 bytes.
func hubDecisionText(w store.HubWait) string {
	switch w.Decision {
	case store.DecisionApproved:
		return fmt.Sprintf("[status: ok] ticket %s on %s (%s) approved: call its run again with ticket=%s.", w.Ticket, w.Lock, w.Instance, w.Ticket)
	case store.DecisionDenied:
		return fmt.Sprintf("[status: fail] ticket %s on %s (%s) denied: %s. Do not retry it as is.", w.Ticket, w.Lock, w.Instance, orNone(w.Reason))
	case store.DecisionAbandoned:
		return fmt.Sprintf("[status: fail] lea no longer waits on ticket %s on %s (%s): %s. Its decision will not reach you through lea: ask the user before calling that Lock again.", w.Ticket, w.Lock, w.Instance, orNone(w.Reason))
	case store.DecisionExpired:
		return fmt.Sprintf("[status: fail] ticket %s on %s (%s) expired before a decision: plan it again if it is still needed.", w.Ticket, w.Lock, w.Instance)
	default:
		return fmt.Sprintf("[status: fail] ticket %s on %s (%s) withdrawn by the Lock: %s.", w.Ticket, w.Lock, w.Instance, orNone(w.Reason))
	}
}

// orNone is the hub's reason on one line, without control, bidi or zero-width characters.
func orNone(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || textsafe.Unsafe(r) {
			return ' '
		}
		return r
	}, s)
	if strings.TrimSpace(s) == "" {
		return "no reason given"
	}
	return s
}
