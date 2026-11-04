package deck

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The ticket pane (#284): lea deck --ticket CONTAINER, a pane of lea's own the daemon swaps into
// the slot when the deck's hub panel selects a ticket. It follows the daemon's snapshot and shows
// the ticket it names (Snapshot.Ticket) whole, from the hub queue: the Lock's screen, the agent's
// note, the instance, production, whom it is for, when it was asked and expires, and the keys,
// then what the deck said of it (a refusal and its fix, or the approve-all report). It answers
// nothing itself: the deck does. The wheel, the arrows, j, k and the page keys scroll it; it never
// quits on its own, so the slot never collapses.

// WatchAPI is what the ticket pane asks of the daemon.
type WatchAPI interface {
	Watch(ctx context.Context, container string, since int64) (Snapshot, error)
}

type ticketPane struct {
	api           WatchAPI
	container     string
	ascii         bool
	now           func() time.Time
	snap          Snapshot
	err           string
	width, height int
	top           int    // the first line shown (scroll)
	shown         string // the ticket (or note) the scroll belongs to
}

// RunTicket draws the container's ticket pane until its pane is closed.
func RunTicket(api WatchAPI, container string, ascii bool) error {
	p := ticketPane{api: api, container: container, ascii: ascii, now: time.Now}
	_, err := tea.NewProgram(p, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	return err
}

func (p ticketPane) Init() tea.Cmd { return p.watch() }

func (p ticketPane) watch() tea.Cmd {
	api, c, v := p.api, p.container, p.snap.Version
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), (watchWait+10)*time.Second)
		defer cancel()
		snap, err := api.Watch(ctx, c, v)
		return snapshotMsg{snap, err}
	}
}

func (p ticketPane) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.width, p.height = msg.Width, msg.Height
	case snapshotMsg:
		if msg.err != nil {
			p.err = "daemon unreachable, retrying: " + oneLine(msg.err.Error())
			return p, tea.Tick(retryDelay, func(time.Time) tea.Msg { return retryMsg{} })
		}
		p.err, p.snap = "", msg.snap
		if what := p.snap.Ticket + "\x00" + p.snap.TicketNote; what != p.shown {
			p.shown, p.top = what, 0
		}
		return p, p.watch()
	case retryMsg:
		return p, p.watch()
	case tea.KeyMsg:
		switch msg.String() {
		case "j", "down":
			p.top++
		case "k", "up":
			p.top--
		case "pgdown", " ":
			p.top += max(1, p.height-2)
		case "pgup":
			p.top -= max(1, p.height-2)
		}
	case tea.MouseMsg:
		switch msg.Button {
		case tea.MouseButtonWheelDown:
			p.top += 3
		case tea.MouseButtonWheelUp:
			p.top -= 3
		}
	}
	p.top = max(0, min(p.top, len(p.lines())-p.room()))
	return p, nil
}

// room is how many lines of the ticket show at once (one is kept for the scroll mark).
func (p ticketPane) room() int {
	if p.height <= 1 {
		return 1 << 20
	}
	return p.height - 1
}

// lines is the whole ticket, p.width cells wide (unstyled; View styles them).
func (p ticketPane) lines() []styled {
	m := Model{ascii: p.ascii, now: p.now, agents: p.snap.Agents}
	return m.ticketLines(p.snap, max(20, p.width-2))
}

func (p ticketPane) View() string {
	ls := p.lines()
	if p.err != "" {
		ls = append([]styled{{alert, p.err}, {plain, ""}}, ls...)
	}
	end := min(len(ls), p.top+p.room())
	c := charsetFor(p.ascii)
	var b strings.Builder
	for _, l := range ls[min(p.top, end):end] {
		b.WriteString(" " + l.st.Render(fit(l.s, max(1, p.width-2), c)) + "\n")
	}
	if more := len(ls) - end; more > 0 {
		b.WriteString(" " + muted.Render(fmt.Sprintf("↓ %d more lines: the wheel or j scrolls", more)))
	} else if p.top > 0 {
		b.WriteString(" " + muted.Render("↑ the wheel or k scrolls back"))
	}
	return b.String()
}

// styled is one line of the ticket pane and its style.
type styled struct {
	st lipgloss.Style
	s  string
}

// ticketLines are the lines of the ticket the snapshot names, width cells wide: the whole ticket
// and its facts, then the deck's note; the note alone without a ticket; and a line that says so
// when the ticket is no longer in the hub's queue, or when no ticket shows.
func (m Model) ticketLines(snap Snapshot, width int) []styled {
	c := m.cs()
	text := lipgloss.NewStyle().Foreground(theme.Text)
	var out []styled
	add := func(st lipgloss.Style, s string, indent string) {
		for _, w := range wrap(s, max(8, width-len(indent)), c) {
			out = append(out, styled{st, indent + w})
		}
	}
	blank := func() { out = append(out, styled{plain, ""}) }
	note := func() {
		if snap.TicketNote == "" {
			return
		}
		blank()
		for _, l := range strings.Split(snap.TicketNote, "\n") {
			add(alert, clean(l), "")
		}
	}
	if snap.Ticket == "" {
		if snap.TicketNote == "" {
			return []styled{{muted, "No ticket shown. In the deck: H opens the hub panel."}}
		}
		note()
		return out[1:]
	}
	var h *HubCard
	for i := range snap.HubQueue {
		if q := &snap.HubQueue[i]; q.Instance+"/"+q.ID == snap.Ticket {
			h = q // the first, as the deck takes it
			break
		}
	}
	if h == nil {
		out = append(out, styled{muted, "This ticket is no longer pending: decided, or gone from the hub."})
		note()
		return out
	}
	warn := "⚠"
	if m.ascii {
		warn = "!"
	}
	// Everything but the Lock's screen comes first: a long screen never pushes the warnings, the
	// facts or the deck's note out of view, while a acts at once.
	head := warn + " " + clean(h.Lock) + " " + clean(h.Instance) + " " + c.dot + " " + clean(h.Risk)
	if h.Expired {
		head += " " + c.dot + " EXPIRED"
	}
	add(alert.Bold(true), head, "")
	if h.Production {
		add(alert.Bold(true), "PRODUCTION", "")
	}
	if queueCount(snap.HubQueue, snap.Ticket) > 1 {
		add(alert.Bold(true), "this ticket shows twice in the hub's queue: the deck answers neither (lox approve)", "")
	}
	if r := h.Renews; r != nil {
		for _, l := range renewLines(*r) {
			add(alert, l, "")
		}
	}
	for _, l := range h.Warnings {
		add(alert, warn+" "+clean(l), "")
	}
	if h.Truncated {
		add(alert, "lea had to cut this ticket: lox approve shows it all", "")
	}
	if h.Note != "" {
		add(text, "agent's note: "+clean(h.Note), "")
	}
	add(text, "instance: "+clean(h.Instance), "")
	prod := "no"
	if h.Production {
		prod = "yes"
	}
	add(text, "production: "+prod, "")
	if !h.Verified && h.Claimed != "" && h.Agent == "" {
		add(alert, "unverified: claims \""+clean(h.Claimed)+"\"", "")
	}
	for _, l := range m.ticketDetails(*h) {
		add(text, l, "")
	}
	add(muted, "ticket: "+clean(h.ID), "")
	keys := "in the deck: a approve " + c.dot + " d deny " + c.dot + " Y approve all " + c.dot + " o clear values " + c.dot + " esc back"
	if h.Expired {
		keys = "expired: r in the deck asks the lock again, approved at once if nothing changed"
	}
	add(muted, keys, "")
	if h.Retype != "" && !h.Expired {
		add(muted, "approving asks for "+clean(h.Retype)+", typed in the deck", "")
	}
	note()
	blank()
	add(text.Bold(true), clean(h.Header), "")
	for _, l := range h.Body {
		add(text, clean(l), "  ")
	}
	return out
}
