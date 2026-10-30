package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/store"
)

// LEA_HUB=0: a cold agent suspended on a ticket is resumed with the "hub disabled" decision, its
// wait abandoned (a later decision of the hub does not replace it); a warm one hears it in its
// inbox; a suspended agent with no wait gets the message too; and no wait is registered any more.
func TestDisableHubResumesTheSuspendedAgents(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{st: e.d.st}
	e.d.SetAgents(fa)
	cold := suspendedOnTicket(t, e, fa, "dev-1")
	warm := spawned(t, e, "dev-2", "dev", "")
	const other = "01JB8Z5Q3W4X7Y9ABCDEFGHJKN"
	if err := e.d.st.AddHubWait(warm, other, "lockfile", "docs", e.d.now()); err != nil {
		t.Fatal(err)
	}
	e.d.SetKeepalive(time.Hour)
	if err := e.d.suspend(warm); err != nil {
		t.Fatal(err)
	}
	bare := spawned(t, e, "dev-3", "dev", "")
	if err := e.d.suspend(bare); err != nil {
		t.Fatal(err)
	}

	e.d.DisableHub()

	c := waitCall(t, fa, "resume "+cold)
	if !strings.Contains(c, "lea no longer waits on ticket "+testTicket+" on locksql (crm-prod): the approval hub is disabled in lea (LEA_HUB=0)") {
		t.Fatalf("resume prompt %q", c)
	}
	waitDelivered(t, e, cold)
	ms, err := e.d.st.Unread(warm, 10)
	if err != nil || len(ms) != 1 || !strings.Contains(ms[0].Body, "lea no longer waits on ticket "+other+" on lockfile (docs)") {
		t.Fatalf("warm inbox %+v %v", ms, err)
	}
	if ms, err := e.d.st.Unread(bare, 10); err != nil || len(ms) != 1 || !strings.Contains(ms[0].Body, "hub is disabled") {
		t.Fatalf("bare inbox %+v %v", ms, err)
	}
	if ws, err := e.d.st.AllPendingHubWaits(); err != nil || len(ws) != 0 {
		t.Fatalf("pending %+v %v", ws, err)
	}
	// A late decision of the hub does not replace lea's.
	if ws, err := e.d.st.DecideHubWaits(other, "docs", store.DecisionApproved, "", e.d.now()); err != nil || len(ws) != 0 {
		t.Fatalf("decided again %+v %v", ws, err)
	}

	// The suspend hook's call is refused: the hook leaves the turn alone.
	k := spawned(t, e, "dev-4", "dev", "")
	err = call(t, e.bus, "dev-4", "hub_wait_register", map[string]any{"ticket": testTicket, "lock": "locksql", "instance": "crm-prod"}, nil)
	if err == nil || !strings.Contains(err.Error(), "LEA_HUB=0") {
		t.Fatalf("err=%v", err)
	}
	if s := agentState(t, e, k); store.Suspended(s) {
		t.Fatalf("suspended with the hub off: %s", s)
	}
	if ws, _ := e.d.st.PendingHubWaits(k); len(ws) != 0 {
		t.Fatalf("wait recorded with the hub off: %+v", ws)
	}
}

// With the hub off no hub is read, even one set before: any call to it would panic.
func TestDisableHubDropsTheFacade(t *testing.T) {
	e := start(t)
	e.d.SetHubFacade(struct{ HubFacade }{})
	e.d.DisableHub()
	if err := e.d.syncHub(t.Context()); err != nil {
		t.Fatal(err)
	}
}
