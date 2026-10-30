package daemon

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/agents"
	"github.com/DavidGodefroid/lea/internal/deck"
)

const ticketB, ticketC = "01JB0000000000000000000002", "01JB0000000000000000000003"

// queued is one ticket of GET /v1/queue as lockhub serves it: its signed lockkit ticket verbatim.
func queued(id, instance, lock, agent, session string, production bool, retype string) map[string]any {
	return map[string]any{"id": id, "instance": instance, "risk": "write", "created": "2026-10-29T22:00:00Z",
		"facades": []string{"deck", "tty"},
		"ticket": map[string]any{"v": 1, "id": id, "risk": "write",
			"source":    map[string]any{"lock": lock, "instance": instance, "key_id": "ed25519:k"},
			"requester": map[string]any{"agent": agent, "session": session},
			"flags":     map[string]any{"production": production, "retype": retype},
			"screen": map[string]any{"header": "db / " + instance + " · write", "note": "fix\x1b[31m the row",
				"body": []string{"UPDATE t SET a=1 WHERE id=7"}, "warnings": []string{"1 row"}}}}
}

func hubEnv(t *testing.T, h *fakeHub) (env, string, string) {
	t.Helper()
	e := start(t)
	dev := spawned(t, e, "dev-1", "dev", "")
	dev2 := spawned(t, e, "dev-2", "dev", "")
	if err := e.d.st.SetState(dev2, "working", "s-2", 2); err != nil {
		t.Fatal(err)
	}
	e.d.SetHubFacade(SocketFacade{Path: serveHub(t, h)})
	return e, dev, dev2
}

func cardsOf(e env, keys ...string) agents.Snapshot {
	var snap agents.Snapshot
	for _, k := range keys {
		snap.Agents = append(snap.Agents, agents.Row{Key: k})
	}
	e.d.addHubCards(&snap)
	return snap
}

func lockView(e env, lock string) *agents.LockView {
	for _, v := range e.d.lockViews("lea-a") {
		if v.Lock == lock {
			return &v
		}
	}
	return nil
}

// The hub's pending tickets show on the agents that wait on them: one the agent's own wait names
// (verified), one whose requester claims the agent's session (unverified); a ticket no agent of
// lea claims shows nowhere. The Lock's counter follows the queue.
func TestHubQueueShowsEachTicketOnItsAgent(t *testing.T) {
	h := &fakeHub{queue: []map[string]any{
		queued(ticketA, "crm", "locksql", "someone", "", true, "crm"),
		queued(ticketB, "rabbit", "lockmq", "dev-x", "s-2", false, ""),
		queued(ticketC, "crm", "locksql", "ghost", "s-9", false, ""),
	}}
	e, dev, dev2 := hubEnv(t, h)
	e.d.st.AddHubWait(dev, ticketA, "locksql", "crm", 1)
	if _, err := e.d.pollHubQueue(e.ctx, 0); err != nil {
		t.Fatal(err)
	}
	snap := cardsOf(e, dev, dev2)
	a, b := snap.Agents[0].Hub, snap.Agents[1].Hub
	if len(a) != 1 || a[0].ID != ticketA || a[0].Lock != "locksql" || a[0].Instance != "crm" || !a[0].Verified ||
		!a[0].Production || a[0].Retype != "crm" || a[0].Header != "db / crm · write" || len(a[0].Body) != 1 {
		t.Fatalf("dev-1's cards %+v", a)
	}
	if strings.ContainsRune(a[0].Note, '\x1b') {
		t.Fatalf("note not cleaned: %q", a[0].Note)
	}
	if len(b) != 1 || b[0].ID != ticketB || b[0].Verified {
		t.Fatalf("dev-2's cards %+v", b)
	}
	if v := lockView(e, "locksql"); v == nil || v.Pending != 1 || v.Console != "up" {
		t.Fatalf("locksql %+v", v)
	}
	if v := lockView(e, "lockmq"); v == nil || v.Pending != 1 {
		t.Fatalf("lockmq %+v", v)
	}
	h.mu.Lock()
	h.queue = nil
	h.mu.Unlock()
	if _, err := e.d.pollHubQueue(e.ctx, 0); err != nil {
		t.Fatal(err)
	}
	if snap := cardsOf(e, dev); len(snap.Agents[0].Hub) != 0 {
		t.Fatalf("cards after the queue emptied %+v", snap.Agents[0].Hub)
	}
	if v := lockView(e, "locksql"); v == nil || v.Pending != 0 {
		t.Fatalf("locksql after %+v", v)
	}
}

// The deck answers a ticket of one of its agents through the facade (facade "deck", the typed
// name for production); a ticket of another container's agent, or none, is refused, and the
// hub's refusals come back as one line.
func TestHubDecideFromTheDeck(t *testing.T) {
	h := &fakeHub{queue: []map[string]any{queued(ticketA, "crm", "locksql", "dev-1", "", true, "crm")}}
	e, dev, _ := hubEnv(t, h)
	e.d.SetAgents(&fakeAgents{})
	e.d.st.AddHubWait(dev, ticketA, "locksql", "crm", 1)
	if _, err := e.d.pollHubQueue(e.ctx, 0); err != nil {
		t.Fatal(err)
	}
	decide := func(container, id string, approved bool, retype string) error {
		return call(t, e.api, "", "hub_decide", map[string]any{"container": container, "instance": "crm", "id": id,
			"approved": approved, "retype": retype}, nil)
	}
	if err := decide("lea-b", ticketA, true, "crm"); err == nil {
		t.Fatal("another container's deck decided the ticket")
	}
	if err := decide("lea-a", ticketB, true, ""); err == nil {
		t.Fatal("a ticket not in the queue was decided")
	}
	h.mu.Lock()
	h.decideStatus = 422
	h.mu.Unlock()
	if err := decide("lea-a", ticketA, true, "crn"); err == nil || !strings.Contains(err.Error(), "typed") {
		t.Fatalf("retype refusal: %v", err)
	}
	h.mu.Lock()
	h.decideStatus = 0
	h.mu.Unlock()
	if err := decide("lea-a", ticketA, true, "crm"); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	got := h.decided
	h.mu.Unlock()
	if len(got) != 2 || !strings.HasPrefix(got[1], "crm/"+ticketA+"/decide ") || !strings.Contains(got[1], `"approved":true`) ||
		!strings.Contains(got[1], `"facade":"deck"`) || !strings.Contains(got[1], `"retype":"crm"`) {
		t.Fatalf("decided %q", got)
	}
	if snap := cardsOf(e, dev); len(snap.Agents[0].Hub) != 0 {
		t.Fatalf("the decided ticket still shows: %+v", snap.Agents[0].Hub)
	}
	if !deckMethods["hub_decide"] {
		t.Fatal("hub_decide is not host-only")
	}
}

// End to end (#158): a ticket in the hub's queue reaches the deck's snapshot on the agent that
// waits on it, the deck's client approves it through the daemon to the facade socket, and the
// card and the Lock's count go once the queue no longer holds it.
func TestHubTicketApprovedFromTheDeck(t *testing.T) {
	h := &fakeHub{queue: []map[string]any{queued(ticketA, "crm", "locksql", "dev-1", "", false, "")}}
	e, dev, _ := hubEnv(t, h)
	e.d.SetAgents(&fakeAgents{rows: []agents.Row{{Key: dev, Name: "dev-1"}}})
	e.d.st.AddHubWait(dev, ticketA, "locksql", "crm", 1)
	if _, err := e.d.pollHubQueue(e.ctx, 0); err != nil {
		t.Fatal(err)
	}
	c := deck.Client{Sock: e.api}
	snap, err := c.Watch(e.ctx, "lea-a", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Agents) != 1 || len(snap.Agents[0].Hub) != 1 || snap.Agents[0].Hub[0].ID != ticketA || !snap.Agents[0].Hub[0].Verified {
		t.Fatalf("snapshot %+v", snap.Agents)
	}
	if len(snap.Locks) != 1 || snap.Locks[0].Lock != "locksql" || snap.Locks[0].Pending != 1 {
		t.Fatalf("locks %+v", snap.Locks)
	}
	card := snap.Agents[0].Hub[0]
	if err := c.HubDecide(e.ctx, "lea-a", card.Instance, card.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	got := h.decided
	h.queue = nil // the hub took the decision
	h.mu.Unlock()
	if len(got) != 1 || !strings.Contains(got[0], `"approved":true`) || !strings.Contains(got[0], `"facade":"deck"`) {
		t.Fatalf("decided %q", got)
	}
	if _, err := e.d.pollHubQueue(e.ctx, 0); err != nil {
		t.Fatal(err)
	}
	if snap, err = c.Watch(e.ctx, "lea-a", 0); err != nil || len(snap.Agents[0].Hub) != 0 || snap.Locks[0].Pending != 0 {
		t.Fatalf("after: %v %+v %+v", err, snap.Agents, snap.Locks)
	}
}

// An agent cannot answer a ticket: hub_decide is the host's alone. A typed name longer than the
// hub takes, or with control characters, never reaches it. A screen lea had to cut is marked,
// so the deck sends it to lock approve; the claimed name shows.
func TestHubDecideIsTheHostsAndTheScreenIsNeverCutSilently(t *testing.T) {
	long := queued(ticketA, "crm", "locksql", "dev-1\x1b[2J", "", true, "crm")
	var body []string
	for i := 0; i < maxHubLines+1; i++ {
		body = append(body, "line")
	}
	long["ticket"].(map[string]any)["screen"].(map[string]any)["body"] = body
	h := &fakeHub{queue: []map[string]any{long}}
	e, dev, _ := hubEnv(t, h)
	e.d.SetAgents(&fakeAgents{})
	e.d.st.AddHubWait(dev, ticketA, "locksql", "crm", 1)
	if _, err := e.d.pollHubQueue(e.ctx, 0); err != nil {
		t.Fatal(err)
	}
	c := cardsOf(e, dev).Agents[0].Hub
	if len(c) != 1 || !c[0].Truncated || strings.ContainsRune(c[0].Claimed, '\x1b') || !strings.HasPrefix(c[0].Claimed, "dev-1") {
		t.Fatalf("card %+v", c)
	}
	p := map[string]any{"container": "lea-a", "instance": "crm", "id": ticketA, "approved": true, "retype": "crm"}
	if err := call(t, e.bus, "dev-1", "hub_decide", p, nil); err == nil {
		t.Fatal("an agent decided a ticket")
	}
	for _, bad := range []string{strings.Repeat("c", 129), "cr\x1bm"} {
		p["retype"] = bad
		if err := call(t, e.api, "", "hub_decide", p, nil); err == nil {
			t.Fatalf("retype %q reached the hub", bad)
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.decided) != 0 {
		t.Fatalf("decided %q", h.decided)
	}
}

// One line of the Lock's screen longer than lea passes whole marks the card cut.
func TestHubLongLineMarksTheCardCut(t *testing.T) {
	q := queued(ticketA, "crm", "locksql", "dev-1", "", false, "")
	q["ticket"].(map[string]any)["screen"].(map[string]any)["body"] = []string{strings.Repeat("x", maxHubLine+1)}
	e, dev, _ := hubEnv(t, &fakeHub{queue: []map[string]any{q}})
	e.d.st.AddHubWait(dev, ticketA, "locksql", "crm", 1)
	if _, err := e.d.pollHubQueue(e.ctx, 0); err != nil {
		t.Fatal(err)
	}
	if c := cardsOf(e, dev).Agents[0].Hub; len(c) != 1 || !c[0].Truncated {
		t.Fatalf("card %+v", c)
	}
}

// A store that fails shows no ticket (none can be put on its agent).
func TestHubQueueShowsNothingWhenTheStoreFails(t *testing.T) {
	e, dev, _ := hubEnv(t, &fakeHub{queue: []map[string]any{queued(ticketA, "crm", "locksql", "dev-1", "", false, "")}})
	e.d.st.AddHubWait(dev, ticketA, "locksql", "crm", 1)
	if _, err := e.d.pollHubQueue(e.ctx, 0); err != nil {
		t.Fatal(err)
	}
	if c := cardsOf(e, dev).Agents[0].Hub; len(c) != 1 {
		t.Fatalf("card %+v", c)
	}
	e.d.st.Close()
	e.d.pollHubQueue(e.ctx, 0)
	if c := cardsOf(e, dev).Agents[0].Hub; len(c) != 0 {
		t.Fatalf("cards after a store failure %+v", c)
	}
}

// countingFacade answers the queue at once, always with the same version.
type countingFacade struct {
	stubFacade
	mu sync.Mutex
	n  int
}

func (f *countingFacade) Queue(ctx context.Context, since uint64, wait int) ([]HubTicket, uint64, error) {
	f.mu.Lock()
	f.n++
	f.mu.Unlock()
	return nil, 7, nil
}

// A hub that answers at once with nothing new is asked again a second later, not in a busy loop.
func TestHubQueueDoesNotSpin(t *testing.T) {
	e := start(t)
	f := &countingFacade{}
	e.d.SetHubFacade(f)
	ctx, cancel := context.WithTimeout(e.ctx, 1500*time.Millisecond)
	defer cancel()
	e.d.RunHubQueue(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.n > 3 {
		t.Fatalf("%d queue requests in 1.5 s", f.n)
	}
}

// A ticket lea stopped waiting on shows on no agent: forgotten while it waited (its waits
// dropped), its ticket stays on the hub, and a live agent of the same name, which its requester
// names, is not shown it as unverified; nor is it after the hub was switched off. Another agent
// that waits on it still has it, verified.
func TestHubQueueDoesNotShowAnAbandonedTicketOnALiveAgentOfTheSameName(t *testing.T) {
	h := &fakeHub{queue: []map[string]any{
		queued(ticketA, "crm", "locksql", "dev-1", "", false, ""),
		queued(ticketB, "rabbit", "lockmq", "dev-2", "", false, ""),
	}}
	e, dev, dev2 := hubEnv(t, h)
	if err := e.d.st.AddHubWait(dev, ticketA, "locksql", "crm", 1); err != nil {
		t.Fatal(err)
	}
	if err := e.d.st.ForgetAgent(dev); err != nil {
		t.Fatal(err)
	}
	again := spawned(t, e, "dev-1", "dev", "")
	if again != dev {
		t.Fatalf("new key %s", again)
	}
	if _, err := e.d.pollHubQueue(e.ctx, 0); err != nil {
		t.Fatal(err)
	}
	snap := cardsOf(e, again, dev2)
	if len(snap.Agents[0].Hub) != 0 {
		t.Fatalf("the forgotten agent's ticket shows on its namesake: %+v", snap.Agents[0].Hub)
	}
	if b := snap.Agents[1].Hub; len(b) != 1 || b[0].ID != ticketB || b[0].Verified {
		t.Fatalf("dev-2's cards %+v", b)
	}
	if v := lockView(e, "locksql"); v != nil && v.Pending != 0 {
		t.Fatalf("locksql counts the abandoned ticket: %+v", v)
	}

	// The hub switched off abandons dev-2's wait on ticketB: once on again, the ticket shows
	// nowhere either, but on an agent that waits on it anew, verified.
	if err := e.d.st.AddHubWait(dev2, ticketB, "lockmq", "rabbit", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.st.AbandonAllHubWaits("off", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.pollHubQueue(e.ctx, 0); err != nil {
		t.Fatal(err)
	}
	if b := cardsOf(e, dev2).Agents[0].Hub; len(b) != 0 {
		t.Fatalf("the abandoned ticket shows: %+v", b)
	}
	if err := e.d.st.AddHubWait(again, ticketB, "lockmq", "rabbit", 3); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.pollHubQueue(e.ctx, 0); err != nil {
		t.Fatal(err)
	}
	if a := cardsOf(e, again).Agents[0].Hub; len(a) != 1 || a[0].ID != ticketB || !a[0].Verified {
		t.Fatalf("a new wait's card %+v", a)
	}
	if v := lockView(e, "lockmq"); v == nil || v.Pending != 1 {
		t.Fatalf("lockmq %+v", v)
	}

	// The same agent waiting again on a ticket lea abandoned (the hub off, then on: its Lock
	// answers the same ticket) has it again, verified, and gets its decision.
	h.mu.Lock()
	h.queue = append(h.queue, queued(ticketC, "crm", "locksql", "dev-2", "", false, ""))
	h.mu.Unlock()
	if err := e.d.st.AddHubWait(dev2, ticketC, "locksql", "crm", 4); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.st.AbandonAllHubWaits("off", 5); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.pollHubQueue(e.ctx, 0); err != nil {
		t.Fatal(err)
	}
	if b := cardsOf(e, dev2).Agents[0].Hub; len(b) != 0 {
		t.Fatalf("the abandoned ticket shows: %+v", b)
	}
	if err := e.d.st.AddHubWait(dev2, ticketC, "locksql", "crm", 6); err != nil {
		t.Fatal(err)
	}
	if ws, err := e.d.st.PendingHubWaits(dev2); err != nil || len(ws) != 1 || ws[0].Ticket != ticketC {
		t.Fatalf("dev-2's waits %+v %v", ws, err)
	}
	if _, err := e.d.pollHubQueue(e.ctx, 0); err != nil {
		t.Fatal(err)
	}
	if b := cardsOf(e, dev2).Agents[0].Hub; len(b) != 1 || b[0].ID != ticketC || !b[0].Verified {
		t.Fatalf("dev-2's card after waiting again %+v", b)
	}
	h.decide(ticketC, "crm", "approved", "")
	if err := e.d.syncHub(e.ctx); err != nil {
		t.Fatal(err)
	}
	ms, err := e.d.st.Unread(dev2, 10)
	approved := 0
	for _, m := range ms {
		if strings.Contains(m.Body, "ticket "+ticketC+" on locksql (crm) approved") {
			approved++
		}
	}
	if err != nil || approved != 1 {
		t.Fatalf("dev-2's inbox %+v %v", ms, err)
	}
}
