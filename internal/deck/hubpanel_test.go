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
	for _, w := range []string{"hub 3 pending", "hub · 3 pending · H to act", "locksql crm · PROD · write · waiting: builder (verified) · 5m ago · 15m left",
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

// H moves the selection into the panel, on its oldest pending ticket, and shows its card; j and k
// move in it, esc leaves it; a key for a row does nothing there. a and d answer the selected
// ticket from the panel, also one for no agent, not before it was under the selection for
// lockAskDelay.
func TestHubPanelKeys(t *testing.T) {
	q := []HubCard{queueTicket("T1", "", false, false), queueTicket("T2", "", false, false)}
	q[0].Expired = true
	for k, want := range map[string]string{"a": `hub-decide lea-a panel crm/T2 true ""`, "d": `hub-decide lea-a panel crm/T2 false ""`} {
		api := &fakeAPI{}
		m := withQueue(t, api, q, ag("b", "working", 1))
		m, _ = step(t, m, key("H"))
		if !m.panel || m.panelSel != "crm/T2" || !strings.Contains(m.View(), "UPDATE t SET") {
			t.Fatalf("H: panel %v on %q:\n%s", m.panel, m.panelSel, m.View())
		}
		if m2, cmd := step(t, m, key(k)); cmd != nil || !strings.Contains(m2.status, "read the locksql ticket first") {
			t.Fatalf("%s answered before the delay: %q", k, m2.status)
		}
		m = later(m)
		m, cmd := step(t, m, key(k))
		if cmd == nil {
			t.Fatalf("%s: no call (%q)", k, m.status)
		}
		step(t, m, cmd())
		if got := api.got(); len(got) != 1 || got[0] != want {
			t.Fatalf("%s: calls %q", k, got)
		}
	}
	api := &fakeAPI{}
	m := later(withQueue(t, api, q, ag("b", "working", 1)))
	m, _ = step(t, m, key("H"))
	m, _ = step(t, m, key("k"))
	if m.panelSel != "crm/T1" {
		t.Fatalf("k: on %q", m.panelSel)
	}
	// k moved the selection onto T1: the delay starts again (T1 expired: r renews it).
	if m2, cmd := step(t, m, key("r")); cmd != nil || !strings.Contains(m2.status, "read the locksql ticket first") {
		t.Fatalf("r right after k: %q", m2.status)
	}
	for _, k := range []string{"x", "p", "e", "n"} {
		if m2, cmd := step(t, m, key(k)); cmd != nil || m2.confirm != "" || m2.editFor != "" || !m2.panel {
			t.Fatalf("%s acted on a row from the panel", k)
		}
	}
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.panel || strings.Contains(m.View(), "UPDATE t SET") {
		t.Fatal("esc did not leave the panel")
	}
	if len(api.got()) != 0 {
		t.Fatalf("calls %q", api.got())
	}
	// No queue: H says so.
	m = withQueue(t, &fakeAPI{}, nil, ag("b", "working", 1))
	if m, _ := step(t, m, key("H")); m.panel || !strings.Contains(m.status, "no ticket waits") {
		t.Fatalf("H with no queue: %v %q", m.panel, m.status)
	}
}

// A production ticket in the panel needs its instance name typed, as on a card.
func TestHubPanelProductionNeedsItsName(t *testing.T) {
	api := &fakeAPI{}
	m := withQueue(t, api, []HubCard{queueTicket("T1", "", false, true)}, ag("b", "working", 1))
	m, _ = step(t, m, key("H"))
	m = later(m)
	m, cmd := step(t, m, key("a"))
	if cmd != nil || m.hubFor != "crm/T1" || !strings.Contains(m.View(), "type crm:") {
		t.Fatalf("no field for the name:\n%s", m.View())
	}
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("crm")})
	m, cmd = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("not approved: %q", m.status)
	}
	step(t, m, cmd())
	if got := api.got(); len(got) != 1 || got[0] != `hub-decide lea-a panel crm/T1 true "crm"` {
		t.Fatalf("calls %q", got)
	}
}

// A refusal of a (#260) stays under the selected ticket, with what answers it: the next key
// replaces the status line, not the note; the selection moving off the ticket drops it.
func TestHubPanelRefusalStaysUnderTheTicket(t *testing.T) {
	q := []HubCard{queueTicket("T1", "", false, true), queueTicket("T2", "", false, false)}
	for i := 0; i < 40; i++ {
		q[0].Body = append(q[0].Body, "line")
	}
	m := withQueue(t, &fakeAPI{}, q, ag("b", "working", 1))
	m, _ = step(t, m, key("H"))
	m = later(m)
	m, _ = step(t, m, key("v"))
	m, cmd := step(t, m, key("a"))
	if cmd != nil || m.hubFor != "" {
		t.Fatal("a ticket too long for the deck was approved")
	}
	m, _ = step(t, m, key("x"))
	if !strings.Contains(m.status, "esc leaves it") {
		t.Fatalf("status %q", m.status)
	}
	if v := m.View(); !strings.Contains(v, "too long for the deck: answer it with lox approve") {
		t.Fatalf("the refusal is gone after the next key:\n%s", v)
	}
	m, _ = step(t, m, key("j"))
	if v := m.View(); strings.Contains(v, "too long for the deck") {
		t.Fatalf("the refusal stays on the next ticket:\n%s", v)
	}
	// A name typed wrong is said under the ticket too.
	m, _ = step(t, m, key("k"))
	q[0].Body = q[0].Body[:1]
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Agents: []Agent{ag("b", "working", 1)}, HubQueue: q}})
	at := clock.Add(20 * lockAskDelay) // k put the selection back: its delay again
	m.now = func() time.Time { return at }
	m, _ = step(t, m, key("a"))
	if m.hubFor != "crm/T1" {
		t.Fatalf("no field for the name: %q", m.status)
	}
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("CRM")})
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = step(t, m, key("x"))
	if v := m.View(); !strings.Contains(v, "does not match: not approved") {
		t.Fatalf("the mismatch is gone after the next key:\n%s", v)
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
