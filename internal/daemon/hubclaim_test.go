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

// decidedTicket is the request of a GET /v1/decided item (lockhub#200): the decided ticket as its
// Lock signed it, the same JSON as a queue item's ticket.
func decidedTicket(id, instance, lock, agent, session string) map[string]any {
	return queuedAs(id, instance, lock, agent, session, false, "")["ticket"].(map[string]any)
}

// A ticket an auto rule of lox run decided before any queue lea reads showed it, and whose
// requester's Lock call registered no wait, still reaches that agent: lea claims the wait from the
// decision's request (#440), approved or denied, once.
func TestHubAutoDecisionReachesTheAgentItsRequesterClaims(t *testing.T) {
	h := &fakeHub{}
	e, dev, dev2 := hubEnv(t, h)
	h.decideRequested(ticketA, "crm", "approved", "rule default:read", decidedTicket(ticketA, "crm", "locksql", "dev-1", ""))
	h.decideRequested(ticketB, "crm", "denied", "rule deny:write", decidedTicket(ticketB, "crm", "locksql", "dev-1", "s-2"))
	for range 2 { // read again (a cursor that did not move), it is told once
		if err := e.d.syncHub(e.ctx); err != nil {
			t.Fatal(err)
		}
		e.d.st.SetHubCursor("0")
	}
	ms := inbox(t, e, "dev-1")
	if len(ms) != 1 || !strings.Contains(ms[0].Text, "[status: ok] ticket "+ticketA+" on locksql (crm) approved: call its run again with ticket="+ticketA) {
		t.Fatalf("dev-1's inbox %+v", ms)
	}
	// The session names dev-2 before the name dev-1 does.
	ms = inbox(t, e, "dev-2")
	if len(ms) != 1 || !strings.Contains(ms[0].Text, "[status: fail] ticket "+ticketB+" on locksql (crm) denied: rule deny:write. Do not retry it as is.") {
		t.Fatalf("dev-2's inbox %+v", ms)
	}
	for _, k := range []string{dev, dev2} {
		ws, err := e.d.st.AllHubWaits()
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range ws {
			if w.Agent == k && !w.Claimed {
				t.Fatalf("%s's wait is not a claim: %+v", k, w)
			}
		}
	}
	if err := e.d.syncHub(e.ctx); err != nil {
		t.Fatal(err)
	}
	if ms := append(inbox(t, e, "dev-1"), inbox(t, e, "dev-2")...); len(ms) != 0 {
		t.Fatalf("told twice: %+v", ms)
	}
}

// A decision lea claims for no one is told to no one: no request (a hub older than lockhub#200),
// a requester no live agent bears, LockHTTP's ticket, an abandoned ticket. A ticket the agent's
// own wait names is told to that agent alone, not also to the agent its requester claims.
func TestHubAutoDecisionClaimsOnlyALiveAgentsTicket(t *testing.T) {
	const ticketD, ticketE = "01JB0000000000000000000004", "01JB0000000000000000000005"
	h := &fakeHub{}
	e, dev, dev2 := hubEnv(t, h)
	if err := e.d.st.AddHubWait(dev2, ticketD, "locksql", "crm", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.st.AbandonAllHubWaits("off", 2); err != nil {
		t.Fatal(err)
	}
	if err := e.d.st.AddHubWait(dev2, ticketE, "locksql", "crm", 3); err != nil {
		t.Fatal(err)
	}
	h.decide(ticketA, "crm", "approved", "")
	h.decideRequested(ticketB, "crm", "approved", "", decidedTicket(ticketB, "crm", "locksql", "ghost", ""))
	h.decideRequested(ticketC, "web", "approved", "", decidedTicket(ticketC, "web", "lockhttp", "dev-1", ""))
	h.decideRequested(ticketD, "crm", "approved", "", decidedTicket(ticketD, "crm", "locksql", "dev-2", ""))
	h.decideRequested(ticketE, "crm", "approved", "", decidedTicket(ticketE, "crm", "locksql", "dev-1", ""))
	// A request that is not the decided ticket's is none.
	h.decideRequested("01JB0000000000000000000006", "crm", "approved", "", decidedTicket(ticketA, "crm", "locksql", "dev-1", ""))
	if err := e.d.syncHub(e.ctx); err != nil {
		t.Fatal(err)
	}
	if ms := inbox(t, e, "dev-1"); len(ms) != 0 {
		t.Fatalf("dev-1 told %+v", ms)
	}
	// dev-2 hears that lea stopped waiting on ticketD, and of ticketE's approval: not of ticketD's.
	ms := inbox(t, e, "dev-2")
	if len(ms) != 2 || !strings.Contains(ms[0].Text, "lea no longer waits on ticket "+ticketD) ||
		!strings.Contains(ms[1].Text, "ticket "+ticketE+" on locksql (crm) approved") {
		t.Fatalf("dev-2's inbox %+v", ms)
	}
	ws, err := e.d.st.AllHubWaits()
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range ws {
		if w.Agent == dev || w.Claimed {
			t.Fatalf("claimed %+v", w)
		}
	}
}
