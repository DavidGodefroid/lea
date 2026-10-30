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
	e.clock.Add(20 * 60)
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

// resumeCalls is how many resumes of agent fa saw.
func resumeCalls(fa *fakeAgents, agent string) int {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	n := 0
	for _, c := range fa.calls {
		if strings.HasPrefix(c, "resume "+agent+":") {
			n++
		}
	}
	return n
}

// waitResumes waits until fa saw n resumes of agent.
func waitResumes(t *testing.T, fa *fakeAgents, agent string, n int) {
	t.Helper()
	for i := 0; resumeCalls(fa, agent) < n; i++ {
		if i == 300 {
			t.Fatalf("%d resumes of %s, want %d", resumeCalls(fa, agent), agent, n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitFails waits until lea counted n failed resumes of agent (0: it gave up or never failed).
func waitFails(t *testing.T, e env, agent string, n int) {
	t.Helper()
	for i := 0; ; i++ {
		e.d.mu.Lock()
		f := e.d.resumeFails[agent]
		e.d.mu.Unlock()
		if f.n == n {
			return
		}
		if i == 300 {
			t.Fatalf("%d failed resumes of %s, want %d", f.n, agent, n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A resume that keeps failing is not tried at every wake (#184): each failure arms a doubling
// backoff, and after maxSuspendedResumes tries lea gives up: the decision goes to the agent's
// inbox, once, and the agent is done.
func TestAFailingResumeBacksOffThenEndsInTheInbox(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{st: e.d.st, resumeErr: errors.New("no pane")}
	e.d.SetAgents(fa)
	k := suspendedOnTicket(t, e, fa, "dev-1")
	time.Sleep(50 * time.Millisecond) // the soft stop's own wake found nothing to resume
	if _, err := e.d.st.DecideHubWaits(testTicket, "crm-prod", store.DecisionApproved, "", e.d.now()); err != nil {
		t.Fatal(err)
	}
	backoff := resumeBackoff
	for try := 1; try <= maxSuspendedResumes; try++ {
		e.d.wakeSuspended(k)
		waitResumes(t, fa, k, try)
		if try == maxSuspendedResumes {
			break
		}
		waitFails(t, e, k, try)
		// Within the backoff, wakes (a hub sync, a message) do not try again.
		for range 3 {
			e.clock.Add(backoff - 1)
			e.d.wakeSuspended(k)
			e.clock.Add(-(backoff - 1))
		}
		time.Sleep(50 * time.Millisecond)
		if n := resumeCalls(fa, k); n != try {
			t.Fatalf("try %d: %d resumes within the backoff", try, n)
		}
		if s := agentState(t, e, k); s != store.StateSuspendedCold {
			t.Fatalf("try %d: state %s", try, s)
		}
		e.clock.Add(backoff)
		backoff *= 2
	}
	for i := 0; agentState(t, e, k) != "done"; i++ {
		if i == 300 {
			t.Fatalf("state %s after %d failed resumes", agentState(t, e, k), maxSuspendedResumes)
		}
		time.Sleep(10 * time.Millisecond)
	}
	waitDelivered(t, e, k)
	ms, err := e.d.st.Unread(k, 10)
	if err != nil || len(ms) != 1 || !strings.Contains(ms[0].Body, "approved: call its run again with ticket="+testTicket) {
		t.Fatalf("inbox %+v %v", ms, err)
	}
	e.clock.Add(3600)
	e.d.wakeSuspended(k)
	time.Sleep(50 * time.Millisecond)
	if n := resumeCalls(fa, k); n != maxSuspendedResumes {
		t.Fatalf("%d resumes after giving up", n)
	}
}

// A resume that succeeds after failures clears the count: the next failure starts at the base
// backoff again.
func TestASuccessfulResumeClearsTheBackoff(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{st: e.d.st, resumeErr: errors.New("no pane")}
	e.d.SetAgents(fa)
	k := suspendedOnTicket(t, e, fa, "dev-1")
	time.Sleep(50 * time.Millisecond) // the soft stop's own wake found nothing to resume
	if _, err := e.d.st.DecideHubWaits(testTicket, "crm-prod", store.DecisionApproved, "", e.d.now()); err != nil {
		t.Fatal(err)
	}
	e.d.wakeSuspended(k)
	waitResumes(t, fa, k, 1)
	waitFails(t, e, k, 1)
	fa.mu.Lock()
	fa.resumeErr = nil
	fa.mu.Unlock()
	e.clock.Add(resumeBackoff)
	e.d.wakeSuspended(k)
	waitResumes(t, fa, k, 2)
	waitDelivered(t, e, k)
	e.d.mu.Lock()
	_, left := e.d.resumeFails[k]
	e.d.mu.Unlock()
	if left {
		t.Fatal("the failure count outlived the successful resume")
	}
}

// Wakes that come while a resume runs (a hub sync, a message) start no other one: a slow outage
// counts as one failed try.
func TestWakesDuringAResumeCountOnce(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{st: e.d.st, resumeErr: errors.New("no pane")}
	e.d.SetAgents(fa)
	k := suspendedOnTicket(t, e, fa, "dev-1")
	time.Sleep(50 * time.Millisecond) // the soft stop's own wake found nothing to resume
	if _, err := e.d.st.DecideHubWaits(testTicket, "crm-prod", store.DecisionApproved, "", e.d.now()); err != nil {
		t.Fatal(err)
	}
	hold := make(chan struct{})
	fa.mu.Lock()
	fa.resumeHold = hold
	fa.mu.Unlock()
	e.d.wakeSuspended(k)
	waitResumes(t, fa, k, 1)
	for range 5 {
		e.d.wakeSuspended(k)
		go e.d.resumeSuspended(fa, k) // as a queued resume the lane lets through
	}
	time.Sleep(100 * time.Millisecond)
	close(hold)
	waitFails(t, e, k, 1)
	time.Sleep(50 * time.Millisecond)
	if n := resumeCalls(fa, k); n != 1 {
		t.Fatalf("%d resumes for one outage", n)
	}
}

// Giving up tells the agent's lead, and leaves an agent that woke meanwhile as it is.
func TestGivingUpTellsTheLead(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{st: e.d.st}
	e.d.SetAgents(fa)
	lead := spawned(t, e, "lead", "lead", "")
	k := spawned(t, e, "dev-1", "dev", lead)
	if err := e.d.st.SetState(k, store.StateSuspendedCold, testSession, e.d.now()); err != nil {
		t.Fatal(err)
	}
	e.d.giveUpResume(fa, k, maxSuspendedResumes)
	if s := agentState(t, e, k); s != "done" {
		t.Fatalf("state %s", s)
	}
	// Through the manager, which tears its stack down (#201).
	waitCall(t, fa, "end "+k)
	if ms, _ := e.d.st.Unread(lead, 10); len(ms) != 1 || !strings.Contains(ms[0].Body, "could not resume dev-1") {
		t.Fatalf("lead inbox %+v", ms)
	}
	w := spawned(t, e, "dev-2", "dev", lead)
	e.d.giveUpResume(fa, w, maxSuspendedResumes) // not suspended: left alone
	if s := agentState(t, e, w); s == "done" {
		t.Fatal("ended an agent that was not suspended")
	}
}
