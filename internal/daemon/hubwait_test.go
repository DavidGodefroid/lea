package daemon

import (
	"strings"
	"testing"

	"github.com/DavidGodefroid/lea/internal/store"
)

// The suspend hook's call records the caller's wait and suspends it; the wait is never another
// agent's, and a malformed ticket is refused.
func TestHubWaitRegisterSuspendsTheCaller(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{st: e.d.st}
	e.d.SetAgents(fa)
	kid := spawned(t, e, "kid", "dev", "")
	other := spawned(t, e, "other", "dev", "")
	e.d.SetKeepalive(DefaultKeepalive)
	var r struct{ Suspended bool }
	if err := call(t, e.bus, "kid", "hub_wait_register", map[string]any{"ticket": testTicket, "lock": "locksql", "instance": "crm-prod",
		"agent": "other"}, &r); err != nil {
		t.Fatal(err)
	}
	if !r.Suspended || agentState(t, e, kid) != store.StateSuspendedWarm || agentState(t, e, other) == store.StateSuspendedWarm {
		t.Fatalf("r=%+v kid=%s other=%s", r, agentState(t, e, kid), agentState(t, e, other))
	}
	if ws, err := e.d.st.PendingHubWaits(kid); err != nil || len(ws) != 1 || ws[0].Instance != "crm-prod" {
		t.Fatalf("kid waits %+v %v", ws, err)
	}
	if ws, err := e.d.st.PendingHubWaits(other); err != nil || len(ws) != 0 {
		t.Fatalf("other waits %+v %v", ws, err)
	}
	err := call(t, e.bus, "other", "hub_wait_register", map[string]any{"ticket": "42", "lock": "locksql", "instance": "crm-prod"}, nil)
	if err == nil || !strings.Contains(err.Error(), "invalid ticket") {
		t.Fatalf("err=%v", err)
	}
	if s := agentState(t, e, other); s == store.StateSuspendedWarm {
		t.Fatalf("other suspended on a refused ticket")
	}
}

// From the host (no container) there is no agent to suspend.
func TestHubWaitRegisterIsForAgents(t *testing.T) {
	e := start(t)
	err := call(t, e.api, "", "hub_wait_register", map[string]any{"ticket": testTicket, "lock": "locksql", "instance": "crm-prod"}, nil)
	if err == nil {
		t.Fatal("registered from the host")
	}
}
