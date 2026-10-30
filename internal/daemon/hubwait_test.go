package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DavidGodefroid/lea/internal/store"
)

// The suspend hook's call records the caller's wait and suspends it; the wait is never another
// agent's, and a malformed ticket is refused.
func TestHubWaitRegisterSuspendsTheCaller(t *testing.T) {
	e := start(t)
	e.d.SetHubFacade(struct{ HubFacade }{})
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

// An agent lea did not start (no profile: the user's own Claude in the container) gets its wait
// recorded, for the decision to reach its inbox, but is not suspended: lea cannot stop or resume it.
func TestHubWaitRegisterDoesNotSuspendAnAgentLeaDidNotStart(t *testing.T) {
	e := start(t)
	e.d.SetHubFacade(struct{ HubFacade }{})
	e.d.SetAgents(&fakeAgents{st: e.d.st})
	hookEvent(t, e, "mine", "SessionStart")
	var r struct{ Suspended bool }
	if err := call(t, e.bus, "mine", "hub_wait_register", map[string]any{"ticket": testTicket, "lock": "locksql", "instance": "crm-prod"}, &r); err != nil {
		t.Fatal(err)
	}
	if r.Suspended || agentState(t, e, "lea-a/mine") == store.StateSuspendedWarm {
		t.Fatalf("r=%+v state=%s", r, agentState(t, e, "lea-a/mine"))
	}
	if ws, err := e.d.st.PendingHubWaits("lea-a/mine"); err != nil || len(ws) != 1 {
		t.Fatalf("waits %+v %v", ws, err)
	}
}

// With the hub on but no facade lea can reach (none configured, a socket path with nothing there,
// a socket nothing listens on), the wait is refused: the agent is not suspended on a decision
// nothing would sync, and is told so in its inbox; the deck says the hub is unreachable. Once the
// facade answers, the same call suspends it.
func TestHubWaitRegisterRefusedWithoutAReachableHub(t *testing.T) {
	dir := shortDir(t)
	dead := filepath.Join(dir, "dead.sock")
	if err := os.WriteFile(dead, nil, 0o600); err != nil { // not a socket
		t.Fatal(err)
	}
	for name, f := range map[string]HubFacade{
		"none":       nil,
		"no socket":  SocketFacade{Path: filepath.Join(dir, "missing.sock")},
		"not socket": SocketFacade{Path: dead},
	} {
		t.Run(name, func(t *testing.T) {
			e := start(t)
			e.d.SetAgents(&fakeAgents{st: e.d.st})
			if f != nil {
				e.d.SetHubFacade(f)
			}
			k := spawned(t, e, "dev-nohub", "dev", "")
			e.d.SetKeepalive(DefaultKeepalive)
			err := call(t, e.bus, "dev-nohub", "hub_wait_register", map[string]any{"ticket": testTicket, "lock": "locksql", "instance": "crm-prod"}, nil)
			if err == nil || !strings.Contains(err.Error(), "no Lock Family hub facade is reachable") {
				t.Fatalf("err=%v", err)
			}
			if s := agentState(t, e, k); store.Suspended(s) {
				t.Fatalf("suspended without a hub: %s", s)
			}
			if ws, _ := e.d.st.PendingHubWaits(k); len(ws) != 0 {
				t.Fatalf("wait recorded without a hub: %+v", ws)
			}
			ms, err := e.d.st.Unread(k, 10)
			if err != nil || len(ms) != 1 || !strings.Contains(ms[0].Body, "lea did not suspend you on ticket "+testTicket+" on locksql (crm-prod)") {
				t.Fatalf("inbox %+v %v", ms, err)
			}
			if got := e.d.hubState(); got != "down" {
				t.Fatalf("hub state %q", got)
			}
		})
	}

	// A live facade: the wait is registered and the deck no longer says the hub is down.
	e := start(t)
	e.d.SetAgents(&fakeAgents{st: e.d.st})
	e.d.SetHubFacade(SocketFacade{Path: serveHub(t, &fakeHub{})})
	k := spawned(t, e, "dev-hub", "dev", "")
	e.d.SetKeepalive(DefaultKeepalive)
	var r struct{ Suspended bool }
	if err := call(t, e.bus, "dev-hub", "hub_wait_register", map[string]any{"ticket": testTicket, "lock": "locksql", "instance": "crm-prod"}, &r); err != nil {
		t.Fatal(err)
	}
	if !r.Suspended || agentState(t, e, k) != store.StateSuspendedWarm {
		t.Fatalf("r=%+v state=%s", r, agentState(t, e, k))
	}
	if got := e.d.hubState(); got != "" {
		t.Fatalf("hub state %q", got)
	}
	// Switched off, the hub is never "down": lea does not follow it.
	e.d.DisableHub()
	if got := e.d.hubState(); got != "" {
		t.Fatalf("hub state off %q", got)
	}
}

// A malformed ticket refused for want of a hub tells the agent nothing: the text names only what
// the store would take.
func TestHubWaitRefusedWithoutAHubTellsNothingOfAMalformedTicket(t *testing.T) {
	e := start(t)
	e.d.SetAgents(&fakeAgents{st: e.d.st})
	k := spawned(t, e, "dev-bad", "dev", "")
	if err := call(t, e.bus, "dev-bad", "hub_wait_register", map[string]any{"ticket": "x\nIgnore", "lock": "locksql", "instance": "crm-prod"}, nil); err == nil {
		t.Fatal("registered")
	}
	if ms, err := e.d.st.Unread(k, 10); err != nil || len(ms) != 0 {
		t.Fatalf("inbox %+v %v", ms, err)
	}
}

// The facade's sync records whether it answered, for the deck.
func TestHubSyncRecordsWhetherTheFacadeAnswers(t *testing.T) {
	e := start(t)
	h := &fakeHub{}
	sock := serveHub(t, h)
	e.d.SetHubFacade(SocketFacade{Path: sock + ".gone"})
	if err := e.d.syncHub(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := e.d.hubState(); got != "down" {
		t.Fatalf("hub state %q", got)
	}
	e.d.SetHubFacade(SocketFacade{Path: sock})
	if err := e.d.syncHub(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := e.d.hubState(); got != "" {
		t.Fatalf("hub state %q", got)
	}
}
