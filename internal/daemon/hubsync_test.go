package daemon

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/DavidGodefroid/lea/internal/store"
)

const ticketA = "01JB0000000000000000000001"

// fakeHub is the hub's facade socket as lockhub serves it (internal/api/facade.go of
// hub/approve): GET /v1/decided?since=<seq> with the signed decisions verbatim, and GET
// /v1/tickets/{instance}/{id} with a ticket's state.
type fakeHub struct {
	mu     sync.Mutex
	items  []fakeDecision
	states map[string]string // instance/id → state
	calls  []string
	fail   bool
}

type fakeDecision struct{ ticket, instance, decision, reason string }

func (h *fakeHub) decide(ticket, instance, decision, reason string) {
	h.mu.Lock()
	h.items = append(h.items, fakeDecision{ticket, instance, decision, reason})
	h.mu.Unlock()
	h.setState(instance, ticket, decision)
}

func (h *fakeHub) setState(instance, id, st string) {
	h.mu.Lock()
	if h.states == nil {
		h.states = map[string]string{}
	}
	h.states[instance+"/"+id] = st
	h.mu.Unlock()
}

func (h *fakeHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, r.Method+" "+r.URL.RequestURI())
	if h.fail {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if strings.HasPrefix(r.URL.Path, "/v1/tickets/") {
		st, ok := h.states[strings.TrimPrefix(r.URL.Path, "/v1/tickets/")]
		if !ok {
			st = "pending"
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "x", "instance": "y", "risk": "write", "state": st, "updated": "2026-10-29T22:00:00Z"})
		return
	}
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	type item struct {
		Seq      int64          `json:"seq"`
		Ticket   string         `json:"ticket"`
		Decision map[string]any `json:"decision"`
	}
	out := struct {
		Decisions []item `json:"decisions"`
		Cursor    int64  `json:"cursor"`
	}{Decisions: []item{}, Cursor: since}
	for i, d := range h.items {
		seq := int64(i + 1)
		if seq <= since {
			continue
		}
		out.Decisions = append(out.Decisions, item{Seq: seq, Ticket: d.ticket, Decision: map[string]any{
			"v": 1, "ticket": d.ticket, "instance": d.instance, "key_id": "ed25519:k", "plan_hash": "sha256:p",
			"decision": d.decision, "reason": d.reason, "approver": map[string]string{"id": "uid:1000", "display": "me"},
			"facade": "tty", "at": "2026-10-29T22:00:00Z", "nonce": "n", "hub_key_id": "ed25519:h", "sig": "s"}})
		out.Cursor = seq
	}
	json.NewEncoder(w).Encode(out)
}

// serveHub serves h on a unix socket, as the hub's host facade.
func serveHub(t *testing.T, h *fakeHub) string {
	t.Helper()
	sock := filepath.Join(shortDir(t), "facade.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return sock
}

func inbox(t *testing.T, e env, agent string) []delivered {
	t.Helper()
	var ms []delivered
	if err := call(t, e.bus, agent, "msg_inbox", nil, &ms); err != nil {
		t.Fatal(err)
	}
	return ms
}

// A decision taken while the daemon was down reaches the waiting agent once the daemon is back:
// lea reads the hub's decisions from its recorded cursor (#164). Read again, it is not told twice.
func TestHubDecisionReachesTheAgentAfterARestart(t *testing.T) {
	e := start(t)
	dev := spawned(t, e, "dev-1", "dev", "")
	if err := e.d.st.AddHubWait(dev, ticketA, "locksql", "crm-prod", 1); err != nil {
		t.Fatal(err)
	}
	hub := &fakeHub{}
	sock := serveHub(t, hub)
	e.d.SetHubFacade(SocketFacade{Path: sock})
	if err := e.d.syncHub(e.ctx); err != nil {
		t.Fatal(err)
	}
	if ms := inbox(t, e, "dev-1"); len(ms) != 0 {
		t.Fatalf("told before a decision: %+v", ms)
	}
	// The daemon goes down; the human approves meanwhile; a new daemon starts on the same lea.db.
	e.d.SetHubFacade(nil)
	hub.decide(ticketA, "crm-prod", "approved", "")
	d2 := New(e.d.st, filepath.Join(shortDir(t), "run"), e.d.now)
	d2.SetHubFacade(SocketFacade{Path: sock})
	if err := d2.syncHub(e.ctx); err != nil {
		t.Fatal(err)
	}
	ms := inbox(t, e, "dev-1")
	if len(ms) != 1 || !strings.Contains(ms[0].Text, "[status: ok] ticket "+ticketA+" on locksql (crm-prod) approved: call its run again with ticket="+ticketA) {
		t.Fatalf("inbox %+v", ms)
	}
	if c, _ := e.d.st.HubCursor(); c != "1" {
		t.Fatalf("cursor %q", c)
	}
	if err := d2.syncHub(e.ctx); err != nil {
		t.Fatal(err)
	}
	if ms := inbox(t, e, "dev-1"); len(ms) != 0 {
		t.Fatalf("told twice: %+v", ms)
	}
	hub.mu.Lock()
	last := hub.calls[len(hub.calls)-1]
	hub.mu.Unlock()
	if !strings.Contains(strings.Join(hub.calls, "\n"), "GET /v1/decided?since=1") || !strings.HasPrefix(last, "GET /v1/") {
		t.Fatalf("not read from the cursor: %q", last)
	}
}

// A hub that fails leaves the cursor where it was; a denied ticket says why, on one line; a
// decision on a ticket no agent waits on, or an invalid one, is passed; no socket is no hub.
func TestHubSyncFailuresAndDenials(t *testing.T) {
	e := start(t)
	dev := spawned(t, e, "dev-1", "dev", "")
	e.d.st.AddHubWait(dev, ticketA, "lockfile", "s3-prod", 1)
	e.d.SetHubFacade(SocketFacade{Path: filepath.Join(shortDir(t), "none.sock")})
	if err := e.d.syncHub(e.ctx); err != nil {
		t.Fatalf("no socket: %v", err)
	}
	hub := &fakeHub{fail: true}
	e.d.SetHubFacade(SocketFacade{Path: serveHub(t, hub)})
	if err := e.d.syncHub(e.ctx); err == nil {
		t.Fatal("a failing hub is no error")
	}
	if c, _ := e.d.st.HubCursor(); c != "" {
		t.Fatalf("cursor moved: %q", c)
	}
	hub.mu.Lock()
	hub.fail = false
	hub.mu.Unlock()
	hub.decide("01JB0000000000000000000009", "s3-prod", "approved", "")
	hub.decide(ticketA, "s3-prod", "maybe", "")
	hub.decide(ticketA, "other", "approved", "")
	hub.decide(ticketA, "s3-prod", "denied", "not on\nprod\x1b[31m")
	if err := e.d.syncHub(e.ctx); err != nil {
		t.Fatal(err)
	}
	ms := inbox(t, e, "dev-1")
	if len(ms) != 1 || !strings.Contains(ms[0].Text, "[status: fail] ticket "+ticketA+" on lockfile (s3-prod) denied: not on prod [31m. Do not retry it as is.") {
		t.Fatalf("inbox %+v", ms)
	}
	if c, _ := e.d.st.HubCursor(); c != "4" {
		t.Fatalf("cursor %q", c)
	}
}

// Withdrawn and expired are no decisions: lea asks the state of the tickets agents wait on.
func TestHubWithdrawnTicketReachesTheAgent(t *testing.T) {
	e := start(t)
	dev := spawned(t, e, "dev-1", "dev", "")
	e.d.st.AddHubWait(dev, ticketA, "locksql", "crm-prod", 1)
	hub := &fakeHub{}
	e.d.SetHubFacade(SocketFacade{Path: serveHub(t, hub)})
	e.d.syncHub(e.ctx)
	if ms := inbox(t, e, "dev-1"); len(ms) != 0 {
		t.Fatalf("told while pending: %+v", ms)
	}
	hub.setState("crm-prod", ticketA, "withdrawn")
	if err := e.d.syncHub(e.ctx); err != nil {
		t.Fatal(err)
	}
	if ms := inbox(t, e, "dev-1"); len(ms) != 1 || !strings.Contains(ms[0].Text, "withdrawn by the Lock") {
		t.Fatalf("inbox %+v", ms)
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if !strings.Contains(strings.Join(hub.calls, "\n"), "GET /v1/tickets/crm-prod/"+ticketA) {
		t.Fatalf("calls %q", hub.calls)
	}
}

// A hub whose sequence went back under lea's cursor (its database made again) answers that cursor
// and nothing after it; the decision still reaches the agent through the ticket's state, as one
// the stream does not hold.
func TestHubCursorBackAndDecisionOutOfTheStream(t *testing.T) {
	e := start(t)
	dev := spawned(t, e, "dev-1", "dev", "")
	e.d.st.AddHubWait(dev, ticketA, "locksql", "crm-prod", 1)
	e.d.st.SetHubCursor("100")
	hub := &fakeHub{}
	hub.decide("01JB0000000000000000000007", "crm-prod", "denied", "")
	hub.decide("01JB0000000000000000000008", "crm-prod", "denied", "")
	hub.decide(ticketA, "crm-prod", "approved", "")
	e.d.SetHubFacade(SocketFacade{Path: serveHub(t, hub)})
	if err := e.d.syncHub(e.ctx); err != nil {
		t.Fatal(err)
	}
	if ms := inbox(t, e, "dev-1"); len(ms) != 1 || !strings.Contains(ms[0].Text, "approved") {
		t.Fatalf("cursor back: inbox %+v", ms)
	}
	const tB = "01JB000000000000000000000B"
	e.d.st.AddHubWait(dev, tB, "locksql", "crm-prod", 2)
	hub.setState("crm-prod", tB, "denied") // decided, but not in the stream
	if err := e.d.syncHub(e.ctx); err != nil {
		t.Fatal(err)
	}
	if ms := inbox(t, e, "dev-1"); len(ms) != 1 || !strings.Contains(ms[0].Text, tB+" on locksql (crm-prod) denied: no reason given") {
		t.Fatalf("out of the stream: inbox %+v", ms)
	}
}

// A decision recorded but not told (a crash between the two) is told at the next sync.
func TestHubDecisionRecordedButNotToldIsTold(t *testing.T) {
	e := start(t)
	dev := spawned(t, e, "dev-1", "dev", "")
	e.d.st.AddHubWait(dev, ticketA, "lockmq", "rabbit", 1)
	if _, err := e.d.st.DecideHubWaits(ticketA, "rabbit", store.DecisionExpired, "", 2); err != nil {
		t.Fatal(err)
	}
	e.d.SetHubFacade(stubFacade{})
	if err := e.d.syncHub(e.ctx); err != nil {
		t.Fatal(err)
	}
	if ms := inbox(t, e, "dev-1"); len(ms) != 1 || !strings.Contains(ms[0].Text, "expired before a decision") {
		t.Fatalf("inbox %+v", ms)
	}
}

type stubFacade struct{}

func (stubFacade) Decided(ctx context.Context, since int64) ([]HubDecided, int64, error) {
	return nil, since, nil
}

func (stubFacade) TicketState(ctx context.Context, instance, ticket string) (string, error) {
	return "pending", nil
}
