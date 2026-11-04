package daemon

import (
	"strings"
	"testing"
)

// An idle agent whose ticket no Lock call of its own registered (the Lock run through the shell,
// #261) gets the decision as a new turn all the same: the ticket its requester claims shows on the
// agent, unverified, and its decision wakes the agent's rewake watch, once.
func TestHubDecisionReachesTheIdleAgentItsRequesterClaims(t *testing.T) {
	h := &fakeHub{queue: []map[string]any{queued(ticketA, "crm", "locksql", "dev-1", "", false, "")}}
	e, dev, _ := hubEnv(t, h)
	hookEvent(t, e, "dev-1", "SessionStart")
	hookEvent(t, e, "dev-1", "Stop")
	w := rewakeWatch(t, e, "dev-1")
	for range 2 { // a second poll neither verifies the claim nor tells the agent
		if _, err := e.d.pollHubQueue(e.ctx, 0); err != nil {
			t.Fatal(err)
		}
		if c := cardsOf(e, dev).Agents[0].Hub; len(c) != 1 || c[0].ID != ticketA || c[0].Verified {
			t.Fatalf("dev-1's cards %+v", c)
		}
	}
	quiet(t, w, "no decision yet")
	h.decide(ticketA, "crm", "approved", "")
	if err := e.d.syncHub(e.ctx); err != nil {
		t.Fatal(err)
	}
	if r := next(t, w); r.Count != 1 || r.Superseded {
		t.Fatalf("notice %+v", r)
	}
	ms := inbox(t, e, "dev-1")
	if len(ms) != 1 || !strings.Contains(ms[0].Text, "ticket "+ticketA+" on locksql (crm) approved: call its run again with ticket="+ticketA) {
		t.Fatalf("inbox %+v", ms)
	}
	if err := e.d.syncHub(e.ctx); err != nil {
		t.Fatal(err)
	}
	if ms := inbox(t, e, "dev-1"); len(ms) != 0 {
		t.Fatalf("told twice: %+v", ms)
	}
}

// A ticket lea claims for no one is told to no one: a requester no live agent bears, an expired
// ticket, LockHTTP's (its request waits in the gate, not in the agent's turn), one lea abandoned.
func TestHubClaimsOnlyALiveAgentsPendingTicket(t *testing.T) {
	expired := queued(ticketB, "crm", "locksql", "dev-1", "", false, "")
	h := &fakeHub{
		queue: []map[string]any{
			queued(ticketA, "crm", "locksql", "ghost", "", false, ""),
			queued(ticketC, "web", "lockhttp", "dev-1", "", false, ""),
		},
		expired: []map[string]any{expired},
	}
	e, dev, dev2 := hubEnv(t, h)
	if _, err := e.d.pollHubQueue(e.ctx, 0); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{dev, dev2} {
		if ws, err := e.d.st.PendingHubWaits(k); err != nil || len(ws) != 0 {
			t.Fatalf("%s waits %+v %v", k, ws, err)
		}
	}
	const ticketD = "01JB0000000000000000000004"
	if err := e.d.st.AddHubWait(dev2, ticketD, "locksql", "crm", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.st.AbandonAllHubWaits("off", 2); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	h.queue = []map[string]any{queued(ticketD, "crm", "locksql", "dev-2", "", false, "")}
	h.expired = nil
	h.mu.Unlock()
	if _, err := e.d.pollHubQueue(e.ctx, 0); err != nil {
		t.Fatal(err)
	}
	if ws, err := e.d.st.PendingHubWaits(dev2); err != nil || len(ws) != 0 {
		t.Fatalf("an abandoned ticket was claimed again: %+v %v", ws, err)
	}
}

// The agent's own wait on a ticket it was claimed for makes the card verified.
func TestHubOwnWaitVerifiesAClaimedTicket(t *testing.T) {
	h := &fakeHub{queue: []map[string]any{queued(ticketA, "crm", "locksql", "dev-1", "", false, "")}}
	e, dev, _ := hubEnv(t, h)
	if _, err := e.d.pollHubQueue(e.ctx, 0); err != nil {
		t.Fatal(err)
	}
	if err := e.d.st.AddHubWait(dev, ticketA, "locksql", "crm", 5); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.pollHubQueue(e.ctx, 0); err != nil {
		t.Fatal(err)
	}
	if c := cardsOf(e, dev).Agents[0].Hub; len(c) != 1 || !c[0].Verified {
		t.Fatalf("dev-1's cards %+v", c)
	}
}
