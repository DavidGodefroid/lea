package daemon

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/agents"
)

const ticketB, ticketC = "01JB0000000000000000000002", "01JB0000000000000000000003"

// queued is one ticket of GET /v1/queue as lockhub serves it: its signed lockkit ticket verbatim
// (retype is lockkit's flags.retype) and, for production, the instance name to type (lockhub#105).
func queued(id, instance, lock, agent, session string, production bool, retype string) map[string]any {
	it := queuedAs(id, instance, lock, agent, session, production, retype)
	if production {
		it["retype"] = instance
	}
	return it
}

// queuedAs is a queue item with no name to type: the hub needs none (lockhub#105).
func queuedAs(id, instance, lock, agent, session string, production bool, retype string) map[string]any {
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

// queuedTicket is what lea knows of a queued ticket: which agent waits on it, verified (its own
// wait names it) or claimed (its requester names the agent).
type queuedTicket struct {
	ID, Lock, Instance string
	Verified, Expired  bool
}

// queuedFor is, for each agent key, the queued tickets lea attributes to it, oldest first.
type queuedFor struct {
	Agents []struct{ Hub []queuedTicket }
}

func cardsOf(e env, keys ...string) queuedFor {
	e.d.mu.Lock()
	entries := e.d.hubQueue
	e.d.mu.Unlock()
	var out queuedFor
	for _, k := range keys {
		var row struct{ Hub []queuedTicket }
		for _, q := range entries {
			if q.agent != "" && q.agent == k {
				row.Hub = append(row.Hub, queuedTicket{ID: q.t.ID, Lock: q.t.Lock, Instance: q.t.Instance, Verified: q.verified, Expired: q.t.Expired})
			}
		}
		out.Agents = append(out.Agents, row)
	}
	return out
}

func lockView(e env, lock string) *agents.LockView {
	for _, v := range e.d.lockViews("lea-a") {
		if v.Lock == lock {
			return &v
		}
	}
	return nil
}

// The hub's pending tickets are attributed to the agents that wait on them: one the agent's own
// wait names (verified), one whose requester claims the agent's session (unverified); a ticket no
// agent of lea claims is no agent's. The Lock's counter follows the queue. lea shows no ticket.
func TestHubQueueShowsEachTicketOnItsAgent(t *testing.T) {
	h := &fakeHub{queue: []map[string]any{
		queued(ticketA, "crm", "locksql", "someone", "", true, "r"), // lockkit's retype flag: not what is typed
		queued(ticketB, "rabbit", "lockmq", "dev-x", "s-2", false, "r"),
		queued(ticketC, "crm", "locksql", "ghost", "s-9", false, ""),
	}}
	e, dev, dev2 := hubEnv(t, h)
	e.d.st.AddHubWait(dev, ticketA, "locksql", "crm", 1)
	if _, err := e.d.pollHubQueue(e.ctx, 0); err != nil {
		t.Fatal(err)
	}
	snap := cardsOf(e, dev, dev2)
	a, b := snap.Agents[0].Hub, snap.Agents[1].Hub
	if len(a) != 1 || a[0].ID != ticketA || a[0].Lock != "locksql" || a[0].Instance != "crm" || !a[0].Verified {
		t.Fatalf("dev-1's cards %+v", a)
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

// A ticket for no agent of lea rings no deck (#294, #300): lox run, outside lea, is where it is
// decided, and where the user is called.
func TestHubQueueRingsNoDeck(t *testing.T) {
	e, rang := soundEnv(t)
	spawned(t, e, "dev-1", "dev", "")
	h := &fakeHub{queue: []map[string]any{queued(ticketA, "crm", "locksql", "ghost", "s-9", false, "")}}
	e.d.SetHubFacade(SocketFacade{Path: serveHub(t, h)})
	if _, err := e.d.pollHubQueue(e.ctx, 0); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if r := rang(); len(r) != 0 {
		t.Fatalf("sounds for a hub ticket: %v", r)
	}
}
