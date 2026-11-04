package deck

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// queueTicket is a ticket of the hub panel: for the agent key (verified or not), or for none.
func queueTicket(id, agentKey string, verified, prod bool) HubCard {
	h := HubCard{ID: id, Instance: "crm", Lock: "locksql", Risk: "write", Header: "db / crm · write",
		Body: []string{"UPDATE t SET a=1"}, Created: clock.Unix() - 300, Expires: clock.Unix() + 900,
		Agent: agentKey, Verified: verified}
	if agentKey != "" {
		h.AgentName = strings.TrimPrefix(agentKey, "lea-a/")
	}
	if prod {
		h.Production, h.Retype = true, "crm"
	}
	return h
}

// withQueue is the deck after a snapshot whose hub queue is q.
func withQueue(t *testing.T, api *fakeAPI, q []HubCard, as ...Agent) Model {
	t.Helper()
	m, _ := step(t, newModel(api), snapshotMsg{snap: Snapshot{Version: 7, Agents: as, HubQueue: q}})
	return wide(t, m)
}

// later moves the deck's clock past lockAskDelay after the selection came onto a ticket.
func later(m Model) Model {
	t := clock.Add(10 * lockAskDelay)
	m.now = func() time.Time { return t }
	return m
}

// The panel shows every ticket of the hub's queue without any key, with whom each is for, and the
// count in the top border; it is gone while the queue is empty and comes back with the next one.
func TestHubPanelShowsTheQueue(t *testing.T) {
	b := ag("b", "working", 1)
	b.Label = "builder"
	q := []HubCard{queueTicket("T1", "lea-a/b", true, true), queueTicket("T2", "lea-a/c", false, false), queueTicket("T3", "", false, false)}
	m := withQueue(t, &fakeAPI{}, q, b, ag("c", "idle", 2))
	v := m.View()
	for _, w := range []string{"hub 3 pending", "hub · 3 pending · H open", "locksql crm · PROD · write · waiting: builder (verified) · 5m ago · 15m left",
		"waiting: builder (verified)", "claims: c (unverified)", "no agent"} {
		if !strings.Contains(v, w) {
			t.Errorf("missing %q in\n%s", w, v)
		}
	}
	if strings.Contains(v, "UPDATE t SET") {
		t.Errorf("a card shows before H:\n%s", v)
	}
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Agents: []Agent{b}}})
	if v := m.View(); strings.Contains(v, "pending") {
		t.Fatalf("the panel stays with an empty queue:\n%s", v)
	}
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 9, Agents: []Agent{b}, HubQueue: q[2:]}})
	if v := m.View(); !strings.Contains(v, "hub 1 pending") || !strings.Contains(v, "no agent") {
		t.Fatalf("the panel did not come back:\n%s", v)
	}
}

// H moves the selection into the panel, on its oldest pending ticket; j and k move in it, esc
// leaves it; a key for a row does nothing there. The ticket itself shows in the slot, not in the
// deck (#284). r on an expired ticket waits lockAskDelay after the selection came onto it.
func TestHubPanelKeys(t *testing.T) {
	q := []HubCard{queueTicket("T1", "", false, false), queueTicket("T2", "", false, false)}
	q[0].Expired = true
	api := &fakeAPI{}
	m := withQueue(t, api, q, ag("b", "working", 1))
	m, _ = step(t, m, key("H"))
	if !m.panel || m.panelSel != "crm/T2" || strings.Contains(m.View(), "UPDATE t SET") {
		t.Fatalf("H: panel %v on %q:\n%s", m.panel, m.panelSel, m.View())
	}
	m = later(m)
	m, _ = step(t, m, key("k"))
	if m.panelSel != "crm/T1" {
		t.Fatalf("k: on %q", m.panelSel)
	}
	// k moved the selection onto T1, which the slot does not show yet (T1 expired: r renews it).
	if m2, cmd := step(t, m, key("r")); cmd != nil || !strings.Contains(m2.status, "not shown yet") {
		t.Fatalf("r right after k: %q", m2.status)
	}
	for _, k := range []string{"x", "p", "e", "n", "enter", "v"} {
		if m2, cmd := step(t, m, key(k)); cmd != nil || m2.confirm != "" || m2.editFor != "" || !m2.panel {
			t.Fatalf("%s acted on a row from the panel", k)
		}
	}
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.panel {
		t.Fatal("esc did not leave the panel")
	}
	for _, c := range api.got() {
		if !strings.HasPrefix(c, "hub-show ") && !strings.HasPrefix(c, "show ") {
			t.Fatalf("calls %q", api.got())
		}
	}
	// No queue: H says so.
	m = withQueue(t, &fakeAPI{}, nil, ag("b", "working", 1))
	if m, _ := step(t, m, key("H")); m.panel || !strings.Contains(m.status, "no ticket waits") {
		t.Fatalf("H with no queue: %v %q", m.panel, m.status)
	}
}

// The selected ticket decided elsewhere leaves the selection on the next one, with the delay
// again; the queue emptied closes the panel.
func TestHubPanelFollowsTheQueue(t *testing.T) {
	q := []HubCard{queueTicket("T1", "", false, false), queueTicket("T2", "", false, false)}
	m := withQueue(t, &fakeAPI{}, q, ag("b", "working", 1))
	m, _ = step(t, m, key("H"))
	m = later(m)
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Agents: []Agent{ag("b", "working", 1)}, HubQueue: q[1:]}})
	if !m.panel || m.panelSel != "crm/T2" {
		t.Fatalf("panel %v on %q", m.panel, m.panelSel)
	}
	// T2 was in view long ago, but the selection came onto it just now.
	if _, cmd := step(t, m, key("a")); cmd != nil {
		t.Fatal("a acted on the ticket that slid under the selection")
	}
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 9, Agents: []Agent{ag("b", "working", 1)}}})
	if m.panel || m.panelSel != "" {
		t.Fatal("the panel stays open with an empty queue")
	}
}

// A card's ticket is approved only when the whole card fits under the panel: the panel takes its
// lines from the deck's height.
func TestHubCardFitsUnderThePanel(t *testing.T) {
	b := ticketed("b", false)
	var q []HubCard
	for _, id := range []string{"T5", "T6", "T7", "T8", "T9"} {
		q = append(q, queueTicket(id, "", false, false))
	}
	api := &fakeAPI{}
	m, _ := step(t, newModel(api), snapshotMsg{snap: Snapshot{Version: 7, Shown: "lea-a/b", Agents: []Agent{b}}})
	m, _ = step(t, m, tea.WindowSizeMsg{Width: 60, Height: 16})
	m = readable(m)
	if _, cmd := step(t, m, key("a")); cmd == nil {
		t.Fatalf("the card does not fit without the panel: %q", m.status)
	}
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Shown: "lea-a/b", Agents: []Agent{b}, HubQueue: q}})
	if m2, cmd := step(t, m, key("a")); cmd != nil || !strings.Contains(m2.status, "lox approve") {
		t.Fatalf("approved a card the panel pushes off screen: %q\n%s", m2.status, m.View())
	}
}
