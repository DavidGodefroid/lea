package daemon

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/DavidGodefroid/lea/internal/agents"
)

type lockAnswer struct {
	Status  string `json:"status"`
	Request int64  `json:"request"`
}

func lockAsk(t *testing.T, e env, agent, tool, profile string) (lockAnswer, error) {
	t.Helper()
	var r lockAnswer
	err := call(t, e.bus, agent, "lock_request", map[string]any{"tool": tool, "profile": profile}, &r)
	return r, err
}

func decideLock(t *testing.T, e env, container string, id int64, open bool) (string, error) {
	t.Helper()
	var r struct{ Status string }
	err := call(t, e.api, "", "lock_request_decide", map[string]any{"container": container, "id": id, "open": open}, &r)
	return r.Status, err
}

// TestLockRequestAsksTheUserAndOpensNothing: an agent's request is checked, recorded and rings;
// it opens nothing, and what the agent gets back is "requested" and an id.
func TestLockRequestAsksTheUserAndOpensNothing(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	var mu sync.Mutex
	var rang []string
	e.d.SetSounder(func(k string) { mu.Lock(); rang = append(rang, k); mu.Unlock() })
	spawned(t, e, "dev", "dev", "")
	r, err := lockAsk(t, e, "dev", "locksql", "prod")
	if err != nil || r.Status != "requested" || r.Request == 0 {
		t.Fatalf("lock_request: %+v %v", r, err)
	}
	for _, c := range fa.calls {
		if strings.HasPrefix(c, "lock-open") || strings.HasPrefix(c, "lock-close") || strings.HasPrefix(c, "lock-list") {
			t.Fatalf("a request reached a console: %q", fa.calls)
		}
	}
	if fa.calls[0] != `lock-check lea-a locksql "prod"` {
		t.Fatalf("calls=%q", fa.calls)
	}
	got, err := e.d.st.LockRequestByID(r.Request)
	if err != nil || got.Agent != "lea-a/dev" || got.Container != "lea-a" || got.Tool != "locksql" || got.Profile != "prod" || got.State != "pending" {
		t.Fatalf("audit row: %+v %v", got, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(rang) != 1 || rang[0] != SoundAttention {
		t.Fatalf("sounds=%q", rang)
	}
}

func TestLockRequestRefusals(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	spawned(t, e, "dev", "dev", "")
	// The host API is the user's: the user opens consoles with lea lock open.
	if err := call(t, e.api, "", "lock_request", map[string]any{"tool": "locksql", "profile": "prod"}, nil); err == nil || !strings.Contains(err.Error(), "agents only") {
		t.Fatalf("from the host: %v", err)
	}
	// A name lea did not start.
	if _, err := lockAsk(t, e, "ghost", "locksql", "prod"); err == nil || !strings.Contains(err.Error(), "only running agents lea started") {
		t.Fatalf("unknown agent: %v", err)
	}
	// The manager's checks (tool, profile, allow-list, deck) refuse: nothing is recorded.
	fa.mu.Lock()
	fa.lockCheckErr = errors.New(`profile "prod" is not one agents may ask for`)
	fa.mu.Unlock()
	if _, err := lockAsk(t, e, "dev", "locksql", "prod"); err == nil || !strings.Contains(err.Error(), "lock_request refused: profile") {
		t.Fatalf("not allowed: %v", err)
	}
	if ps, _ := e.d.st.PendingLockRequests("lea-a", agents.LockRequestTTL, 1000); len(ps) != 0 {
		t.Fatalf("a refused request was recorded: %+v", ps)
	}
	fa.mu.Lock()
	fa.lockCheckErr = nil
	fa.mu.Unlock()
	if _, err := lockAsk(t, e, "dev", "locksql", "prod"); err != nil {
		t.Fatal(err)
	}
	// One pending request per tool per container, whatever name asks.
	spawned(t, e, "dev2", "dev", "")
	if _, err := lockAsk(t, e, "dev2", "locksql", "staging"); err == nil || !strings.Contains(err.Error(), "waits for the user") {
		t.Fatalf("second pending: %v", err)
	}
}

func TestLockRequestDecideOpensTheRecordedConsoleOnce(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	spawned(t, e, "dev", "dev", "")
	r, err := lockAsk(t, e, "dev", "locksql", "prod")
	if err != nil {
		t.Fatal(err)
	}
	// Agents cannot answer: lock_request_decide is host only.
	if err := call(t, e.bus, "dev", "lock_request_decide", map[string]any{"container": "lea-a", "id": r.Request, "open": true}, nil); err == nil || !strings.Contains(err.Error(), "host only") {
		t.Fatalf("decide from an agent: %v", err)
	}
	// Another deck's container does not find it.
	if _, err := decideLock(t, e, "lea-b", r.Request, true); err == nil || !strings.Contains(err.Error(), "no lock request") {
		t.Fatalf("other deck: %v", err)
	}
	fa.calls = nil
	if st, err := decideLock(t, e, "lea-a", r.Request, true); err != nil || st != "opened" {
		t.Fatalf("decide: %q %v", st, err)
	}
	if len(fa.calls) != 1 || fa.calls[0] != `lock-open lea-a locksql "prod"` {
		t.Fatalf("calls=%q", fa.calls)
	}
	// Replay: answered once.
	for _, open := range []bool{true, false} {
		if _, err := decideLock(t, e, "lea-a", r.Request, open); err == nil || !strings.Contains(err.Error(), "already answered") {
			t.Fatalf("replay: %v", err)
		}
	}
	if len(fa.calls) != 1 {
		t.Fatalf("replay reached the manager: %q", fa.calls)
	}
	ms := inboxOf(t, e, "dev")
	if len(ms) != 1 || ms[0].From != "user" || !strings.Contains(ms[0].Text, "[status: ok] lock request #1 (locksql/prod): opened by the user.") {
		t.Fatalf("inbox=%+v", ms)
	}
	if got, _ := e.d.st.LockRequestByID(r.Request); got.State != "opened" || got.Decided != 1000 {
		t.Fatalf("audit: %+v", got)
	}
}

func TestLockRequestDeclinedFailedAndExpired(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	spawned(t, e, "dev", "dev", "")
	r, _ := lockAsk(t, e, "dev", "locksql", "prod")
	if st, err := decideLock(t, e, "lea-a", r.Request, false); err != nil || st != "declined" {
		t.Fatalf("decline: %q %v", st, err)
	}
	ms := inboxOf(t, e, "dev")
	if len(ms) != 1 || !strings.Contains(ms[0].Text, "[status: fail] lock request #1 (locksql/prod): declined by the user.") {
		t.Fatalf("inbox=%+v", ms)
	}

	// The console does not open: the user reads why; the agent learns only that it failed.
	e.clock.Add(lockRequestGap)
	r, err := lockAsk(t, e, "dev", "locksql", "prod")
	if err != nil {
		t.Fatal(err)
	}
	fa.mu.Lock()
	fa.lockOpenErr = errors.New("the locksql/prod console locksql-console-prod already runs in the deck of lea-b")
	fa.mu.Unlock()
	if _, err := decideLock(t, e, "lea-a", r.Request, true); err == nil || !strings.Contains(err.Error(), "already runs") {
		t.Fatalf("open failed: %v", err)
	}
	ms = inboxOf(t, e, "dev")
	if len(ms) != 1 || !strings.Contains(ms[0].Text, "[status: fail] lock request #2 (locksql/prod): the user said yes, but the console did not open.") ||
		strings.Contains(ms[0].Text, "lea-b") {
		t.Fatalf("inbox=%+v", ms)
	}
	if got, _ := e.d.st.LockRequestByID(r.Request); got.State != "failed" || !strings.Contains(got.Result, "already runs") {
		t.Fatalf("audit: %+v", got)
	}
	fa.mu.Lock()
	fa.lockOpenErr = nil
	fa.mu.Unlock()

	// Nobody answers: past its time it is refused, closed, and the agent is told.
	e.clock.Add(lockRequestGap)
	r, _ = lockAsk(t, e, "dev", "locksql", "prod")
	e.clock.Add(agents.LockRequestTTL)
	fa.calls = nil
	if _, err := decideLock(t, e, "lea-a", r.Request, true); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("late: %v", err)
	}
	if len(fa.calls) != 0 {
		t.Fatalf("a late answer reached the manager: %q", fa.calls)
	}
	ms = inboxOf(t, e, "dev")
	if len(ms) != 1 || !strings.Contains(ms[0].Text, "[status: fail] lock request #3 (locksql/prod): expired") {
		t.Fatalf("inbox=%+v", ms)
	}

	// The sweep closes the ones nobody answered and tells their agents.
	e.clock.Add(lockRequestGap)
	r, _ = lockAsk(t, e, "dev", "locksql", "prod")
	e.clock.Add(agents.LockRequestTTL)
	e.d.sweepLockRequests()
	if got, _ := e.d.st.LockRequestByID(r.Request); got.State != "expired" {
		t.Fatalf("sweep: %+v", got)
	}
	if ms = inboxOf(t, e, "dev"); len(ms) != 1 || !strings.Contains(ms[0].Text, "lock request #4 (locksql/prod): expired") {
		t.Fatalf("inbox=%+v", ms)
	}
}

// Refused requests are not recorded: a caller's tries are capped all the same.
func TestLockRequestTriesAreCapped(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{lockCheckErr: errors.New("not allowed")}
	e.d.SetAgents(fa)
	spawned(t, e, "dev", "dev", "")
	for i := 0; i < 5; i++ {
		if _, err := lockAsk(t, e, "dev", "locksql", "prod"); err == nil || !strings.Contains(err.Error(), "not allowed") {
			t.Fatalf("try %d: %v", i, err)
		}
	}
	if _, err := lockAsk(t, e, "dev", "locksql", "prod"); err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("sixth try: %v", err)
	}
}
