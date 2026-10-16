package daemon

import (
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/DavidGodefroid/lea/internal/wire"
)

type verdictResult struct {
	ID      int64  `json:"id"`
	Verdict string `json:"verdict"`
}

type pendingView struct {
	ID     int64  `json:"id"`
	Agent  string `json:"agent"`
	Tool   string `json:"tool"`
	Always bool   `json:"always"`
}

// ask sends an approval request from agent dev in the background (timeoutS ≤ 3: call gives up
// after 5 s).
func ask(t *testing.T, e env, timeoutS int, always bool) <-chan verdictResult {
	t.Helper()
	got := make(chan verdictResult, 1)
	go func() {
		var r verdictResult
		if err := call(t, e.bus, "dev", "approval_request", map[string]any{"tool": "Bash",
			"input": map[string]any{"command": "make test"}, "timeout_s": timeoutS, "always": always}, &r); err != nil {
			t.Error(err)
		}
		got <- r
	}()
	return got
}

func waitPending(t *testing.T, e env) pendingView {
	t.Helper()
	for i := 0; i < 100; i++ {
		var pend []pendingView
		if err := call(t, e.api, "", "approval_list", nil, &pend); err != nil {
			t.Fatal(err)
		}
		if len(pend) == 1 {
			return pend[0]
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no pending approval appeared")
	return pendingView{}
}

func receive(t *testing.T, got <-chan verdictResult) verdictResult {
	t.Helper()
	select {
	case r := <-got:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("approval_request did not return")
	}
	return verdictResult{}
}

func TestALateDecisionIsRefusedAsExpired(t *testing.T) {
	e := start(t)
	var r verdictResult
	if err := call(t, e.bus, "dev", "approval_request", map[string]any{"tool": "Bash", "input": map[string]any{}, "timeout_s": 1}, &r); err != nil || r.Verdict != "" {
		t.Fatalf("r=%+v err=%v", r, err)
	}
	err := call(t, e.api, "", "approval_decide", map[string]any{"id": r.ID, "verdict": "allow"}, nil)
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("a late verdict: err=%v", err)
	}
	var pend []pendingView
	if call(t, e.api, "", "approval_list", nil, &pend); len(pend) != 0 {
		t.Fatalf("an expired approval is listed: %+v", pend)
	}
}

func TestAVerdictStoredAtTheDeadlineWins(t *testing.T) {
	e := start(t)
	got := ask(t, e, 1, false)
	p := waitPending(t, e)
	// The verdict is stored but reaches no waiter (as when the deck answers just as the
	// timeout fires): the expiry finds it and delivers it.
	if err := e.d.st.SetVerdict(p.ID, "deny"); err != nil {
		t.Fatal(err)
	}
	if r := receive(t, got); r.Verdict != "deny" {
		t.Fatalf("verdict=%q, want the stored deny", r.Verdict)
	}
}

func TestAlwaysIsAVerdictTheAgentOffers(t *testing.T) {
	e := start(t)
	got := ask(t, e, 3, true)
	p := waitPending(t, e)
	if !p.Always || p.Agent != "lea-a/dev" || p.Tool != "Bash" {
		t.Fatalf("pending=%+v", p)
	}
	if err := call(t, e.api, "", "approval_decide", map[string]any{"id": p.ID, "verdict": "sometimes"}, nil); err == nil {
		t.Fatal("an unknown verdict was accepted")
	}
	if err := call(t, e.api, "", "approval_decide", map[string]any{"id": p.ID, "verdict": "always"}, nil); err != nil {
		t.Fatal(err)
	}
	if r := receive(t, got); r.Verdict != "always" {
		t.Fatalf("verdict=%q", r.Verdict)
	}
}

func TestDecideErrors(t *testing.T) {
	e := start(t)
	if err := call(t, e.api, "", "approval_decide", map[string]any{"id": 99, "verdict": "allow"}, nil); err == nil || !strings.Contains(err.Error(), "no approval #99") {
		t.Fatalf("unknown id: err=%v", err)
	}
	got := ask(t, e, 3, false)
	p := waitPending(t, e)
	if err := call(t, e.api, "", "approval_decide", map[string]any{"id": p.ID, "verdict": "allow"}, nil); err != nil {
		t.Fatal(err)
	}
	receive(t, got)
	err := call(t, e.api, "", "approval_decide", map[string]any{"id": p.ID, "verdict": "deny"}, nil)
	if err == nil || !strings.Contains(err.Error(), "already decided") {
		t.Fatalf("second verdict: err=%v", err)
	}
}

func TestWithoutAnAttachedDeckClaudeAsksAtOnce(t *testing.T) {
	e := start(t)
	e.d.SetAgents(&fakeAgents{unattached: true})
	began := time.Now()
	var r verdictResult
	if err := call(t, e.bus, "dev", "approval_request", map[string]any{"tool": "Bash", "input": map[string]any{}, "timeout_s": 30}, &r); err != nil {
		t.Fatal(err)
	}
	if r.Verdict != "" || time.Since(began) > 2*time.Second {
		t.Fatalf("r=%+v after %s: the request waited for a deck nobody sees", r, time.Since(began))
	}
	var pend []pendingView
	if call(t, e.api, "", "approval_list", nil, &pend); len(pend) != 0 {
		t.Fatalf("pending=%+v", pend)
	}
}

func TestADeckVerdictSetsWorkingAndNotifies(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "PermissionRequest", "session_id": "s"}, nil)
	got := ask(t, e, 3, false)
	p := waitPending(t, e)
	fa.mu.Lock()
	before := fa.changed
	fa.mu.Unlock()
	if err := call(t, e.api, "", "approval_decide", map[string]any{"id": p.ID, "verdict": "allow"}, nil); err != nil {
		t.Fatal(err)
	}
	receive(t, got)
	var as []agentView
	call(t, e.api, "", "agents_list", nil, &as)
	if len(as) != 1 || as[0].State != "working" {
		t.Fatalf("agents=%+v", as)
	}
	fa.mu.Lock()
	after := fa.changed
	fa.mu.Unlock()
	if after <= before {
		t.Fatalf("changed %d → %d: the deck was not told", before, after)
	}
}

func TestADisconnectedHookLeavesTheAgentWorkingNotBlocked(t *testing.T) {
	e := start(t)
	e.d.SetAgents(&fakeAgents{})
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "PermissionRequest", "session_id": "s"}, nil)
	var as []agentView
	call(t, e.api, "", "agents_list", nil, &as)
	if len(as) != 1 || as[0].State != "blocked" {
		t.Fatalf("agents=%+v", as)
	}
	// The hook process dies (the dialog was answered in the terminal, or the turn was cut): no
	// verdict, and possibly no later hook.
	c, err := net.Dial("unix", e.bus)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := json.Marshal(map[string]any{"tool": "Bash", "input": map[string]any{}, "timeout_s": 30})
	if err := wire.Write(c, wire.Request{Method: "approval_request", Agent: "dev", Params: p}); err != nil {
		t.Fatal(err)
	}
	waitPending(t, e)
	c.Close()
	for i := 0; i < 100; i++ {
		call(t, e.api, "", "agents_list", nil, &as)
		if as[0].State == "working" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("agent still %q after its hook vanished", as[0].State)
}

func TestADeniedVerdictResumesTheAgent(t *testing.T) {
	e := start(t)
	e.d.SetAgents(&fakeAgents{})
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "PermissionRequest", "session_id": "s"}, nil)
	got := ask(t, e, 3, false)
	p := waitPending(t, e)
	if err := call(t, e.api, "", "approval_decide", map[string]any{"id": p.ID, "verdict": "deny"}, nil); err != nil {
		t.Fatal(err)
	}
	receive(t, got)
	var as []agentView
	call(t, e.api, "", "agents_list", nil, &as)
	if len(as) != 1 || as[0].State != "working" {
		t.Fatalf("agents=%+v", as)
	}
}

func TestAlwaysIsRefusedWhenNotOffered(t *testing.T) {
	e := start(t)
	got := ask(t, e, 3, false)
	p := waitPending(t, e)
	err := call(t, e.api, "", "approval_decide", map[string]any{"id": p.ID, "verdict": "always"}, nil)
	if err == nil || !strings.Contains(err.Error(), "always is not offered") {
		t.Fatalf("err=%v", err)
	}
	if err := call(t, e.api, "", "approval_decide", map[string]any{"id": p.ID, "verdict": "allow"}, nil); err != nil {
		t.Fatalf("the row must stay decidable: %v", err)
	}
	receive(t, got)
}

// Claude keeps the PermissionRequest hook running when the user answers its own dialog in the
// terminal (Claude Code 2.1.296): the card would stay in the deck until the timeout. The end of
// the turn (Stop) proves no dialog is open any more: the request is withdrawn, the hook gets no
// verdict, and the agent stays idle.
func TestTheEndOfTheTurnWithdrawsAPendingApproval(t *testing.T) {
	e := start(t)
	e.d.SetAgents(&fakeAgents{})
	ev := func(name string) {
		t.Helper()
		if err := call(t, e.bus, "dev", "hook_event", map[string]any{"event": name, "session_id": "s"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	ev("UserPromptSubmit")
	ev("PermissionRequest")
	got := ask(t, e, 3, false)
	p := waitPending(t, e)
	start := time.Now()
	ev("Stop") // answered in the terminal: the tool ran and the turn ended
	r := receive(t, got)
	if r.Verdict != "" || time.Since(start) > 2*time.Second {
		t.Fatalf("hook got %+v after %v, want no verdict at once", r, time.Since(start))
	}
	var pend []pendingView
	if call(t, e.api, "", "approval_list", nil, &pend); len(pend) != 0 {
		t.Fatalf("still pending: %+v", pend)
	}
	var as []agentView
	call(t, e.api, "", "agents_list", nil, &as)
	if len(as) != 1 || as[0].State != "idle" {
		t.Fatalf("agents=%+v, want idle", as)
	}
	if err := call(t, e.api, "", "approval_decide", map[string]any{"id": p.ID, "verdict": "allow"}, nil); err == nil {
		t.Fatal("a withdrawn approval accepted a verdict")
	}
}

// A withdraw that lands exactly as the timer fires must still withdraw: the marker is read after
// the drain, so the row expires and the state is left alone (no zombie card, no forced working).
func TestAWithdrawRacingTheTimeoutStillWithdraws(t *testing.T) {
	e := start(t)
	e.d.SetAgents(&fakeAgents{})
	e.d.afterWait = func() { e.d.withdrawApprovals("lea-a/dev") }
	if err := call(t, e.bus, "dev", "hook_event", map[string]any{"event": "PermissionRequest", "session_id": "s"}, nil); err != nil {
		t.Fatal(err)
	}
	r := receive(t, ask(t, e, 1, false))
	if r.Verdict != "" {
		t.Fatalf("verdict %q", r.Verdict)
	}
	var pend []pendingView
	if call(t, e.api, "", "approval_list", nil, &pend); len(pend) != 0 {
		t.Fatalf("zombie card: %+v", pend)
	}
	var as []agentView
	call(t, e.api, "", "agents_list", nil, &as)
	if len(as) != 1 || as[0].State == "working" {
		t.Fatalf("agents=%+v, state must not be forced to working", as)
	}
}

func TestApprovalInputIsCappedAndTheListStaysUnderTheWireLimit(t *testing.T) {
	e := start(t)
	big := strings.Repeat("é", 100<<10)
	for i := 0; i < 8; i++ {
		go call(t, e.bus, "dev", "approval_request", map[string]any{"tool": "Write",
			"input": map[string]any{"content": big}, "timeout_s": 3}, nil)
	}
	var raw json.RawMessage
	var n int
	for i := 0; i < 100 && n < 8; i++ {
		time.Sleep(30 * time.Millisecond)
		if err := call(t, e.api, "", "approval_list", nil, &raw); err != nil {
			t.Fatal(err)
		}
		var l []json.RawMessage
		json.Unmarshal(raw, &l)
		n = len(l)
	}
	as, _ := e.d.st.PendingApprovals()
	for _, a := range as {
		if len(a.Input) > maxApprovalInput || !utf8.ValidString(a.Input) {
			t.Fatalf("stored input is %d bytes (valid utf8 %v)", len(a.Input), utf8.ValidString(a.Input))
		}
	}
	if len(raw) > batchBytes {
		t.Fatalf("approval_list is %d bytes", len(raw))
	}
}

func TestApprovalListIsMeasuredEncoded(t *testing.T) {
	e := start(t)
	in := strings.Repeat("<", 8<<10) // 6 bytes each once JSON-encoded
	for i := 0; i < 40; i++ {
		if _, err := e.d.st.AddApproval("lea-test/dev", "Write", in, false, 1); err != nil {
			t.Fatal(err)
		}
	}
	var raw json.RawMessage
	if err := call(t, e.api, "", "approval_list", nil, &raw); err != nil {
		t.Fatal(err)
	}
	var l []json.RawMessage
	if err := json.Unmarshal(raw, &l); err != nil {
		t.Fatal(err)
	}
	if len(l) == 0 || len(l) == 40 || len(raw) > batchBytes {
		t.Fatalf("%d entries, %d encoded bytes (cap %d)", len(l), len(raw), batchBytes)
	}
}
