package daemon

import (
	"strings"
	"testing"

	"github.com/DavidGodefroid/lea/internal/agents"
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
