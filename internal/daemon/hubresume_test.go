package daemon

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/store"
)

const testTicket = "01JB8Z5Q3W4X7Y9ABCDEFGHJKM"

// suspendedOnTicket is an agent whose Lock call waits on testTicket, stopped once its keepalive
// went by: suspended_cold.
func suspendedOnTicket(t *testing.T, e env, fa *fakeAgents, name string) string {
	t.Helper()
	k := spawned(t, e, name, "dev", "")
	if err := e.d.st.AddHubWait(k, testTicket, "locksql", "crm-prod", e.d.now()); err != nil {
		t.Fatal(err)
	}
	e.d.SetKeepalive(0)
	if err := e.d.suspend(k); err != nil {
		t.Fatal(err)
	}
	waitCall(t, fa, "softstop "+k)
	for i := 0; agentState(t, e, k) != store.StateSuspendedCold; i++ {
		if i == 300 {
			t.Fatalf("state %s, want %s", agentState(t, e, k), store.StateSuspendedCold)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return k
}

// The full path: an agent suspends on a pending ticket, is stopped after its keepalive, the
// ticket is approved 20 minutes later, and the agent resumes with the decision as its next turn,
// told to run the ticket; the decision is delivered once.
func TestAnApprovedTicketResumesTheAgentWithTheDecision(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{st: e.d.st}
	e.d.SetAgents(fa)
	k := suspendedOnTicket(t, e, fa, "dev-1")
	*e.clock += 20 * 60
	if _, err := e.d.st.DecideHubWaits(testTicket, "crm-prod", store.DecisionApproved, "", e.d.now()); err != nil {
		t.Fatal(err)
	}
	e.d.wakeSuspended(k)
	c := waitCall(t, fa, "resume "+k)
	if !strings.Contains(c, "ticket "+testTicket+" on locksql (crm-prod) approved: call its run again with ticket="+testTicket) {
		t.Fatalf("resume prompt %q", c)
	}
	if strings.Contains(c, "unread message") {
		t.Fatalf("resume prompt %q: no message came", c)
	}
	waitDelivered(t, e, k)
}

// waitDelivered waits until agent has no undelivered decision.
func waitDelivered(t *testing.T, e env, agent string) {
	t.Helper()
	for i := 0; ; i++ {
		ws, err := e.d.st.UndeliveredHubWaits(agent)
		if err == nil && len(ws) == 0 {
			return
		}
		if i == 300 {
			t.Fatalf("undelivered %+v %v", ws, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A denial reaches the resumed agent with the hub's reason, and the unread messages are named
// after it.
func TestADeniedTicketResumesTheAgentWithTheReason(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{st: e.d.st}
	e.d.SetAgents(fa)
	k := suspendedOnTicket(t, e, fa, "dev-1")
	if _, err := e.d.st.Send(store.Message{From: "user", To: k, Kind: "note", Body: "hi", At: e.d.now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.st.DecideHubWaits(testTicket, "crm-prod", store.DecisionDenied, "not on\nprod", e.d.now()); err != nil {
		t.Fatal(err)
	}
	e.d.wakeSuspended(k)
	c := waitCall(t, fa, "resume "+k)
	if !strings.Contains(c, "denied: not on prod. Do not retry it as is.") || !strings.Contains(c, "1 unread message(s)") {
		t.Fatalf("resume prompt %q", c)
	}
}

// A resume that fails does not lose the decision: it stays undelivered, and the next sync wakes
// the agent again.
func TestAFailedResumeKeepsTheDecisionUndelivered(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{st: e.d.st, resumeErr: errors.New("no pane")}
	e.d.SetAgents(fa)
	k := suspendedOnTicket(t, e, fa, "dev-1")
	if _, err := e.d.st.DecideHubWaits(testTicket, "crm-prod", store.DecisionApproved, "", e.d.now()); err != nil {
		t.Fatal(err)
	}
	e.d.wakeSuspended(k)
	waitCall(t, fa, "resume "+k)
	time.Sleep(100 * time.Millisecond)
	if ws, err := e.d.st.UndeliveredHubWaits(k); err != nil || len(ws) != 1 {
		t.Fatalf("undelivered %+v %v", ws, err)
	}
	if ms, _ := e.d.st.Unread(k, 10); len(ms) != 0 {
		t.Fatalf("inbox %+v", ms)
	}
}

// The hub sync wakes a stopped suspended agent whose ticket it finds decided: the agent resumes
// with the decision, with no other call (#163).
func TestTheHubSyncResumesAColdAgentWithItsDecision(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{st: e.d.st}
	e.d.SetAgents(fa)
	k := spawned(t, e, "dev-1", "dev", "")
	if err := e.d.st.AddHubWait(k, testTicket, "locksql", "crm-prod", 1); err != nil {
		t.Fatal(err)
	}
	if err := e.d.st.SetState(k, store.StateSuspendedCold, testSession, 2); err != nil {
		t.Fatal(err)
	}
	hub := &fakeHub{}
	hub.decide(testTicket, "crm-prod", "approved", "")
	e.d.SetHubFacade(SocketFacade{Path: serveHub(t, hub)})
	if err := e.d.syncHub(e.ctx); err != nil {
		t.Fatal(err)
	}
	if c := waitCall(t, fa, "resume "+k); !strings.Contains(c, "approved: call its run again with ticket="+testTicket) {
		t.Fatalf("resume prompt %q", c)
	}
	waitDelivered(t, e, k)
	if ms, _ := e.d.st.Unread(k, 10); len(ms) != 0 {
		t.Fatalf("told twice, in the inbox too: %+v", ms)
	}
}

// A restarted daemon resumes a stopped agent whose ticket was decided while it was down.
func TestTheSweepResumesAnAgentWhoseTicketWasDecided(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{st: e.d.st}
	e.d.SetAgents(fa)
	k := spawned(t, e, "dev-1", "dev", "")
	if err := e.d.st.AddHubWait(k, testTicket, "locksql", "crm-prod", 1); err != nil {
		t.Fatal(err)
	}
	if err := e.d.st.SetState(k, store.StateSuspendedCold, testSession, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.st.DecideHubWaits(testTicket, "crm-prod", store.DecisionApproved, "", 3); err != nil {
		t.Fatal(err)
	}
	if err := call(t, e.api, "", "agents_sweep_stacks", map[string]any{"container": "lea-a"}, nil); err != nil {
		t.Fatal(err)
	}
	if c := waitCall(t, fa, "resume "+k); !strings.Contains(c, "approved") {
		t.Fatalf("resume prompt %q", c)
	}
}
