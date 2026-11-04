package deck

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// A hub panel ticket shown whole in the slot (#284): selecting a ticket in the panel (H, the
// arrows, a click) shows it in the large pane at the right, where the agent's session shows;
// leaving the panel brings the agent back. a approves the ticket shown, d denies it: it is shown
// whole, so nothing asks to unfold or read it first; a key acts only once the slot shows the
// ticket, ticketDelay long, so a key pressed twice never answers the next ticket. Y approves every
// pending ticket after one y.

// ticketDelay is how long the slot shows a panel ticket before a or d acts on it.
const ticketDelay = 500 * time.Millisecond

// ticketAsk is what the deck asks the slot to show: a panel ticket (ref, instance/id) with a note
// under it, or a note alone (ref ""); on false: no ticket, the selected agent.
type ticketAsk struct {
	on        bool
	ref, note string
}

// wantTicket is what the slot should show now: the panel's selected ticket with the refusal said
// of it, or the approve-all report; nothing out of the panel.
func (m Model) wantTicket() ticketAsk {
	switch {
	case !m.panel:
		return ticketAsk{}
	case m.hubReport != "":
		return ticketAsk{on: true, note: m.hubReport}
	case m.panelSel == "":
		return ticketAsk{}
	}
	note := ""
	if m.hubNote.ref == m.panelSel {
		note = m.hubNote.text
	}
	return ticketAsk{on: true, ref: m.panelSel, note: note}
}

// syncTicket asks the daemon to show what wantTicket says: a ticket shows in the slot; out of the
// panel, the selected agent takes the slot back (else the placeholder). It asks again when the
// slot does not show what it was asked (the call failed, the daemon restarted, the pane died,
// another deck changed it), once no call is under way and retryDelay after the last one. Each
// call carries a sequence number that only grows: the daemon drops one older than the last it
// took, so calls that overtake each other never leave the slot on a ticket no longer selected.
func (m *Model) syncTicket() tea.Cmd {
	if m.api == nil {
		return nil
	}
	want := m.wantTicket()
	if want == m.ticketAsked {
		if m.ticketBusy || m.now().Sub(m.ticketSentAt) < retryDelay {
			return nil
		}
		shows := m.ticketShown == want.ref && m.ticketNoteShown == want.note // want.on false: both ""
		if !m.ticketFailed && shows {
			return nil
		}
	}
	m.ticketAsked, m.ticketBusy, m.ticketFailed, m.ticketSentAt = want, true, false, m.now()
	m.ticketSeq = max(m.ticketSeq+1, time.Now().UnixNano())
	api, c, seq := m.api, m.container, m.ticketSeq
	show := func(ctx context.Context) error { return api.HubShow(ctx, c, want.ref, want.note, seq) }
	if !want.on {
		show = func(ctx context.Context) error { return api.HubShow(ctx, c, "", "", seq) }
		if a := m.selectedAgent(); a != nil {
			key := a.Key
			m.seq++ // this show replaces a delayed one
			m.ask(key)
			show = func(ctx context.Context) error {
				// The hide first: its number drops a ticket call still on its way.
				if err := api.HubShow(ctx, c, "", "", seq); err != nil {
					return err
				}
				return api.Show(ctx, key, false)
			}
		}
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		return ticketShownMsg{seq, show(ctx)}
	}
}

// ticketShownMsg is the answer to the slot call seq.
type ticketShownMsg struct {
	seq int64
	err error
}

// ticketShown1 takes a slot call's answer: a failure is said, and the next message asks again
// (retryDelay later).
func (m Model) ticketShown1(msg ticketShownMsg) Model {
	if msg.seq != m.ticketSeq {
		return m
	}
	m.ticketBusy = false
	if msg.err != nil {
		m.status = oneLine(msg.err.Error())
		m.ticketFailed = true
	}
	return m
}

// ticketReady says why a key may not answer the panel ticket ref yet ("" when it may): the slot
// does not show it yet, or shows it since less than ticketDelay, or this deck answered it already.
func (m Model) ticketReady(ref string) string {
	switch {
	case m.hubSent[ref]:
		return "answered: waiting for the hub"
	case queueCount(m.hubQueue, ref) > 1:
		return "this ticket shows twice in the hub's queue: answer it with lox approve"
	case m.ticketShown != ref || m.ticketAsked.ref != ref:
		return "the ticket is not shown yet: look at it on the right"
	case m.now().Sub(m.ticketAt) < ticketDelay || m.now().Sub(m.panelAt) < ticketDelay:
		return "the ticket just showed: look at it on the right"
	}
	return ""
}

// answerPanel answers the panel ticket h, shown whole in the slot: approve (a) or deny (d). A
// ticket the hub asks a name for (Retype) opens the field for it first. A ticket lea had to cut
// is not approved here: the slot cannot show all of it.
func (m Model) answerPanel(h HubCard, approve bool) (tea.Model, tea.Cmd) {
	ref := h.Instance + "/" + h.ID
	if why := m.ticketReady(ref); why != "" {
		m.status = why
		return m, nil
	}
	if approve && h.Truncated {
		return m.refuseHub(ref, "lea had to cut this ticket: answer it with lox approve"), nil
	}
	if approve && h.Retype != "" {
		m.hubFor, m.hubDraft, m.status, m.hubNote = ref, nil, "", hubNoted{}
		return m, nil
	}
	m.sent(ref)
	m.status = ""
	return m, m.decideHub(h, approve, "")
}

// sent marks the ticket ref answered here.
func (m *Model) sent(ref string) {
	if m.hubSent == nil {
		m.hubSent = map[string]bool{}
	}
	m.hubSent[ref] = true
}

// hubDecidedMsg is the daemon's answer to a decision on ticket ref.
type hubDecidedMsg struct {
	ref            string
	panel, approve bool
	err            error
}

// hubDecided takes a decision's answer: a refusal of a panel ticket shows in the slot, under the
// ticket, with what to do; on a card it is the status, as before.
func (m Model) hubDecided(msg hubDecidedMsg) Model {
	if msg.err == nil {
		m.status = "approved"
		if !msg.approve {
			m.status = "denied"
		}
		return m
	}
	delete(m.hubSent, msg.ref)
	m.status = oneLine(msg.err.Error())
	if msg.panel {
		m.hubNote = hubNoted{msg.ref, hubFix(msg.err)}
	}
	return m
}

// hubFixText is what to do about a hub's refusal.
const hubFixText = "A hub older than lockhub 0.7.0 refuses a production approval from the deck: " +
	"lox up updates it. Or answer the ticket with lox approve."

// hubFix is a hub's refusal and what to do about it, as the slot shows it.
func hubFix(err error) string {
	return "Not answered: " + oneLine(err.Error()) + "\n" + hubFixText
}

// queueCount counts the tickets of q whose reference is ref: more than one, the deck answers
// none of them (the slot shows the first).
func queueCount(q []HubCard, ref string) int {
	n := 0
	for _, h := range q {
		if h.Instance+"/"+h.ID == ref {
			n++
		}
	}
	return n
}

// approveAll is what Y asks to approve: the tickets, and those it leaves out with why.
type approveAll struct {
	tickets []HubCard
	skipped []string
}

// ticketName names a ticket in a list: its Lock, instance and id.
func ticketName(h HubCard) string {
	return clean(h.Lock) + " " + clean(h.Instance) + " " + clean(h.ID)
}

// approvable is what Y would approve now: every pending ticket of the queue, but one that needs
// a name typed, that lea had to cut or whose reference shows twice (left out, a on it), and one
// answered here already.
func (m Model) approvable() approveAll {
	var all approveAll
	for _, h := range m.hubQueue {
		ref := h.Instance + "/" + h.ID
		switch {
		case h.Expired || m.hubSent[ref]:
		case h.Retype != "":
			all.skipped = append(all.skipped, ticketName(h)+": needs its name typed (a on it)")
		case h.Truncated:
			all.skipped = append(all.skipped, ticketName(h)+": cut by lea (lox approve)")
		case queueCount(m.hubQueue, ref) > 1:
			all.skipped = append(all.skipped, ticketName(h)+": shows twice in the queue (lox approve)")
		default:
			all.tickets = append(all.tickets, h)
		}
	}
	return all
}

// sameTickets reports whether a and b count the same tickets, the same way.
func sameTickets(a, b approveAll) bool {
	if len(a.tickets) != len(b.tickets) || len(a.skipped) != len(b.skipped) {
		return false
	}
	for i := range a.tickets {
		if x, y := a.tickets[i], b.tickets[i]; x.Instance != y.Instance || x.ID != y.ID || x.Production != y.Production {
			return false
		}
	}
	return true
}

// askApproveAll is Y: it counts the pending tickets it would approve, how many are production,
// and asks y/n. The question holds only while what it counted holds: a click, a move or a
// queue that changes what it counts drops it (keepApproveAll).
func (m Model) askApproveAll() Model {
	all := m.approvable()
	if len(all.tickets) == 0 {
		m.status = "no ticket to approve at once"
		if len(all.skipped) > 0 {
			m.status += fmt.Sprintf(": %d need a on each", len(all.skipped))
		}
		return m
	}
	prod := 0
	for _, h := range all.tickets {
		if h.Production {
			prod++
		}
	}
	m.allFor = &all
	noun := "tickets"
	if len(all.tickets) == 1 {
		noun = "ticket"
	}
	m.status = fmt.Sprintf("approve %d %s, %d production? y/n", len(all.tickets), noun, prod)
	if len(all.skipped) > 0 {
		m.status += fmt.Sprintf(" (%d skipped)", len(all.skipped))
	}
	return m
}

// keepApproveAll drops Y's question when it no longer holds: out of the panel, or when the queue
// changed what it counts.
func (m *Model) keepApproveAll() {
	if m.allFor == nil {
		return
	}
	if !m.panel {
		m.allFor, m.status = nil, ""
		return
	}
	if !sameTickets(*m.allFor, m.approvable()) {
		m.allFor, m.status = nil, "the hub's queue changed: Y counts again"
	}
}

// answerApproveAll takes the key after Y: y approves the tickets it counted, one after the
// other; any other key cancels. A ticket gone from the queue (or expired) since is not approved,
// and the report says so.
func (m Model) answerApproveAll(yes bool) (tea.Model, tea.Cmd) {
	all := m.allFor
	m.allFor, m.status = nil, ""
	if !yes {
		m.status = "nothing approved"
		return m, nil
	}
	var tickets []HubCard
	var gone []string
	for _, h := range all.tickets {
		ref := h.Instance + "/" + h.ID
		i := -1
		for j, q := range m.hubQueue {
			if q.Instance+"/"+q.ID == ref {
				i = j
				break
			}
		}
		if i < 0 || m.hubQueue[i].Expired || m.hubSent[ref] {
			gone = append(gone, ticketName(h)+": gone from the queue, or expired, meanwhile")
			continue
		}
		tickets = append(tickets, m.hubQueue[i])
		m.sent(ref)
	}
	m.status = fmt.Sprintf("approving %d tickets…", len(tickets))
	api, c, skipped := m.api, m.container, append(append([]string(nil), all.skipped...), gone...)
	return m, func() tea.Msg {
		r := approveAllMsg{skipped: skipped}
		for _, h := range tickets {
			ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
			err := api.HubDecide(ctx, c, true, h.Instance, h.ID, true, "")
			cancel()
			if err != nil {
				r.refused = append(r.refused, ticketName(h)+": "+oneLine(err.Error()))
				r.failed = append(r.failed, h.Instance+"/"+h.ID)
				continue
			}
			r.approved++
		}
		return r
	}
}

// approveAllMsg is what Y approved, left out and the hub refused.
type approveAllMsg struct {
	approved         int
	skipped, refused []string
	failed           []string // refs of the refused tickets
}

// approvedAll says what approve all did: in the status, and, when a ticket was left out or
// refused, in a report the slot shows until the selection moves.
func (m Model) approvedAll(r approveAllMsg) Model {
	for _, ref := range r.failed {
		delete(m.hubSent, ref)
	}
	m.status = fmt.Sprintf("approved %d", r.approved)
	if n := len(r.skipped); n > 0 {
		m.status += fmt.Sprintf(", %d skipped", n)
	}
	if n := len(r.refused); n > 0 {
		m.status += fmt.Sprintf(", %d refused", n)
	}
	if len(r.skipped) == 0 && len(r.refused) == 0 {
		return m
	}
	m.status += ": see the right pane"
	var b strings.Builder
	fmt.Fprintf(&b, "Approve all: %d approved.\n", r.approved)
	if len(r.skipped) > 0 {
		b.WriteString("\nLeft out:\n")
		for _, s := range r.skipped {
			b.WriteString("  " + s + "\n")
		}
	}
	if len(r.refused) > 0 {
		b.WriteString("\nRefused by the hub:\n")
		for _, s := range r.refused {
			b.WriteString("  " + s + "\n")
		}
		b.WriteString("\n" + hubFixText)
	}
	m.hubReport = strings.TrimRight(b.String(), "\n")
	if m.panel {
		return m
	}
	m.status = strings.TrimSuffix(m.status, ": see the right pane")
	m.hubReport = ""
	return m
}
