package daemon

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/agents"
)

// panelOf is the hub panel as the deck receives it, by ticket id: its attribution.
func panelOf(e env) ([]string, map[string]agents.HubCard) {
	var ids []string
	by := map[string]agents.HubCard{}
	for _, c := range e.d.hubPanel() {
		ids = append(ids, c.ID)
		by[c.ID] = c
	}
	return ids, by
}

// The hub panel lists every ticket of the queue, in the hub's order, attributed or not (#244): a
// ticket an agent's own wait names (verified), one whose requester claims a live agent
// (unverified), and one no agent claims, which shows on no card and counts in no container. The
// list equals the queue, as lox approve lists it.
func TestHubPanelListsEveryTicket(t *testing.T) {
	h := &fakeHub{queue: []map[string]any{
		queued(ticketA, "crm", "locksql", "someone", "", true, ""),
		queued(ticketB, "rabbit", "lockmq", "dev-x", "s-2", false, ""),
		queued(ticketC, "crm", "locksql", "ghost", "s-9", false, ""),
	}}
	e, dev, dev2 := hubEnv(t, h)
	e.d.st.AddHubWait(dev, ticketA, "locksql", "crm", 1)
	if _, err := e.d.pollHubQueue(e.ctx, 0); err != nil {
		t.Fatal(err)
	}
	ids, by := panelOf(e)
	if strings.Join(ids, ",") != ticketA+","+ticketB+","+ticketC {
		t.Fatalf("panel %q", ids)
	}
	if a := by[ticketA]; a.Agent != dev || a.AgentName != "dev-1" || !a.Verified || !a.Production || a.Retype != "crm" || a.Created == 0 {
		t.Fatalf("A %+v", a)
	}
	if b := by[ticketB]; b.Agent != dev2 || b.AgentName != "dev-2" || b.Verified {
		t.Fatalf("B %+v", b)
	}
	if c := by[ticketC]; c.Agent != "" || c.AgentName != "" || c.Verified || c.Claimed != "ghost" {
		t.Fatalf("C %+v", c)
	}
	snap := cardsOf(e, dev, dev2)
	for _, r := range snap.Agents {
		for _, c := range r.Hub {
			if c.ID == ticketC || c.Agent != "" {
				t.Fatalf("card %+v: an unattributed ticket on a card, or the panel's fields on one", c)
			}
		}
	}
	if v := lockView(e, "locksql"); v == nil || v.Pending != 1 {
		t.Fatalf("locksql counts the ticket for no agent: %+v", v)
	}
}

// Nothing an agent does adds, hides or reorders a panel entry: a fake wait or a requester naming
// another agent changes only the attribution. An abandoned ticket still shows, for no agent.
func TestHubPanelIsNotShapedByAgents(t *testing.T) {
	h := &fakeHub{queue: []map[string]any{
		queued(ticketA, "crm", "locksql", "dev-1", "", false, ""),
		queued(ticketB, "rabbit", "lockmq", "dev-2", "", false, ""),
		queued(ticketC, "crm", "locksql", "", "", false, ""),
	}}
	e, dev, dev2 := hubEnv(t, h)
	if _, err := e.d.pollHubQueue(e.ctx, 0); err != nil {
		t.Fatal(err)
	}
	before, by := panelOf(e)
	if by[ticketA].Agent != dev || by[ticketA].Verified || by[ticketB].Agent != dev2 || by[ticketC].Agent != "" {
		t.Fatalf("claims %+v", by)
	}
	// dev-2 registers a wait on dev-1's ticket and on the one for no agent.
	e.d.st.AddHubWait(dev2, ticketA, "locksql", "crm", 1)
	e.d.st.AddHubWait(dev2, ticketC, "locksql", "crm", 1)
	if _, err := e.d.pollHubQueue(e.ctx, 0); err != nil {
		t.Fatal(err)
	}
	after, by := panelOf(e)
	if strings.Join(after, ",") != strings.Join(before, ",") {
		t.Fatalf("the panel changed: %q, then %q", before, after)
	}
	if by[ticketA].Agent != dev2 || !by[ticketA].Verified || by[ticketC].Agent != dev2 {
		t.Fatalf("attribution %+v", by)
	}
	// Abandoned (the hub switched off): on no card, still in the panel, for no agent.
	if _, err := e.d.st.AbandonAllHubWaits("off", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.pollHubQueue(e.ctx, 0); err != nil {
		t.Fatal(err)
	}
	after, by = panelOf(e)
	if strings.Join(after, ",") != strings.Join(before, ",") || by[ticketC].Agent != "" {
		t.Fatalf("after abandon: %q %+v", after, by)
	}
	if c := cardsOf(e, dev2).Agents[0].Hub; len(c) != 1 || c[0].ID != ticketB {
		t.Fatalf("dev-2's cards %+v", c)
	}
}

// The panel decides any queued ticket, one for no agent too, from any deck; a card only its own
// container's. A production ticket still needs its instance name typed, and no agent can decide.
func TestHubPanelDecides(t *testing.T) {
	h := &fakeHub{queue: []map[string]any{
		queued(ticketA, "crm", "locksql", "", "", true, ""),
		queued(ticketB, "rabbit", "lockmq", "", "", false, ""),
	}}
	e, _, _ := hubEnv(t, h)
	e.d.SetAgents(&fakeAgents{})
	if _, err := e.d.pollHubQueue(e.ctx, 0); err != nil {
		t.Fatal(err)
	}
	decide := func(sock, from string, panel bool, id string, retype string) error {
		inst := map[string]string{ticketA: "crm", ticketB: "rabbit"}[id]
		return call(t, sock, from, "hub_decide", map[string]any{"container": "lea-a", "panel": panel, "instance": inst,
			"id": id, "approved": true, "retype": retype}, nil)
	}
	if err := decide(e.api, "", false, ticketB, ""); err == nil {
		t.Fatal("a card decided a ticket for no agent")
	}
	if err := decide(e.bus, "dev-1", true, ticketB, ""); err == nil {
		t.Fatal("an agent decided from the panel")
	}
	if err := decide(e.api, "", true, ticketA, ""); err == nil || !strings.Contains(err.Error(), "typed") {
		t.Fatalf("production approved with no name typed: %v", err)
	}
	if err := decide(e.api, "", true, ticketB, ""); err != nil {
		t.Fatal(err)
	}
	if err := decide(e.api, "", true, ticketA, "crm"); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	n := len(h.decided)
	h.mu.Unlock()
	if n != 2 {
		t.Fatalf("decided %d", n)
	}
	if ids, _ := panelOf(e); len(ids) != 0 {
		t.Fatalf("decided tickets still listed: %q", ids)
	}
}

// A ticket for no agent rings the attention sound while a deck shows a container, once when it
// arrives; a later poll of the same queue rings nothing.
func TestHubPanelTicketRings(t *testing.T) {
	h := &fakeHub{}
	e, _, _ := hubEnv(t, h)
	e.d.SetAgents(&fakeAgents{})
	var mu sync.Mutex
	var rang []string
	e.d.SetSounder(func(kind string) {
		mu.Lock()
		rang = append(rang, kind)
		mu.Unlock()
	})
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(rang)
	}
	if _, err := e.d.pollHubQueue(e.ctx, 0); err != nil {
		t.Fatal(err)
	}
	if count() != 0 {
		t.Fatal("rang for an empty queue")
	}
	h.mu.Lock()
	h.queue = []map[string]any{queued(ticketC, "crm", "locksql", "ghost", "", false, "")}
	h.mu.Unlock()
	if _, err := e.d.pollHubQueue(e.ctx, 0); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 1 || rang[0] != SoundAttention {
		t.Fatalf("rang %q", rang)
	}
	time.Sleep(soundGap + 100*time.Millisecond)
	if _, err := e.d.pollHubQueue(e.ctx, 0); err != nil {
		t.Fatal(err)
	}
	if count() != 1 {
		t.Fatalf("rang again for the same ticket: %q", rang)
	}
}
