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
// with what the deck said of it (a refusal and its fix, the name awaited, the approve-all report)
// in a banner above it that no ticket text can imitate. It answers nothing itself: the deck does,
// and lea's tmux keeps the keyboard on the deck when the ticket is clicked or ctrl+space is
// pressed (#289). The wheel scrolls it (and the arrows, j, k and the page
// keys, should it have the keyboard); any other key typed here says the deck answers. It never
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
	// hint: a key this pane does not use was typed here; the banner says the deck answers.
	hint bool
}

// ticketPaneHint is what the ticket pane says of a key typed in it that it does not use (#289).
const ticketPaneHint = "This pane answers nothing: a, d and Y act in the deck. Click the deck (left) or press ctrl+space."

// The banner (#289) is lea's own voice in the ticket pane: the deck's note (TicketNote: a refusal
// and its fix, the name awaited, the approve-all report), the hint above and a daemon error. It
// is drawn first, above the ticket, never scrolls, and each of its lines starts at column 0 with
// bannerMark. Every line of the ticket, whose text the Lock and the agent wrote, starts with a
// space at column 0: no text a ticket carries can draw a line that looks like the banner's.
const bannerMark, bannerMarkASCII = "▌", "|"

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
			p.shown, p.top, p.hint = what, 0, false
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
		default:
			// The keyboard is here (lea's tmux gives it to the deck on a click or ctrl+space;
			// something else moved it): this pane answers nothing, so it says where the keys act.
			p.hint = true
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

// room is how many lines of the ticket show at once, under the banner (one is kept for the
// scroll mark).
func (p ticketPane) room() int {
	if p.height <= 1 {
		return 1 << 20
	}
	return max(1, p.height-1-len(p.banner()))
}

// banner is the banner's text, wrapped to the pane, with a blank line under it; none when lea
// has nothing to say. It takes at most half the pane: a longer note ends with a mark.
func (p ticketPane) banner() []styled {
	width := max(20, p.width-2)
	var out []styled
	add := func(st lipgloss.Style, s string) {
		for _, l := range strings.Split(s, "\n") {
			for _, w := range wrapWords(clean(l), width, 1<<20) {
				out = append(out, styled{st, w})
			}
		}
	}
	if p.err != "" {
		add(alert, p.err)
	}
	if p.hint {
		add(alert.Bold(true), ticketPaneHint)
	}
	if p.snap.TicketNote != "" {
		add(alert, p.snap.TicketNote)
	}
	if len(out) == 0 {
		return nil
	}
	if limit := max(2, (p.height-2)/2); p.height > 0 && len(out) > limit {
		out = append(out[:limit-1], styled{muted, "… (cut: the deck's status line says the rest)"})
	}
	return append(out, styled{plain, ""})
}

// lines is the whole ticket, p.width cells wide (unstyled; View styles them).
func (p ticketPane) lines() []styled {
	m := Model{ascii: p.ascii, now: p.now, agents: p.snap.Agents}
	return m.ticketLines(p.snap, max(20, p.width-2))
}

func (p ticketPane) View() string {
	c := charsetFor(p.ascii)
	mark := bannerMark
	if p.ascii {
		mark = bannerMarkASCII
	}
	var b strings.Builder
	for _, l := range p.banner() {
		if l.s == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString(alert.Bold(true).Render(mark) + l.st.Render(fit(l.s, max(1, p.width-2), c)) + "\n")
	}
	ls := p.lines()
	end := min(len(ls), p.top+p.room())
	for _, l := range ls[min(p.top, end):end] {
		b.WriteString(" " + l.st.Render(fit(l.s, max(1, p.width-2), c)) + "\n")
	}
	if more := len(ls) - end; more > 0 {
		b.WriteString(" " + muted.Render(fmt.Sprintf("↓ %d more lines: the wheel scrolls", more)))
	} else if p.top > 0 {
		b.WriteString(" " + muted.Render("↑ the wheel scrolls back"))
	}
	return b.String()
}

// styled is one line of the ticket pane and its style.
type styled struct {
	st lipgloss.Style
	s  string
}

// ticketLines are the lines of the ticket the snapshot names, width cells wide: the whole ticket
// and its facts; nothing for the deck's note alone (the banner shows it); and a line that says so
// when the ticket is no longer in the hub's queue, or when no ticket shows. The deck's note is
// never among them: the banner shows it, apart from the ticket's own text.
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
	if snap.Ticket == "" {
		if snap.TicketNote == "" {
			return []styled{{muted, "No ticket shown. In the deck: H opens the hub panel."}}
		}
		return nil
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
		return out
	}
	warn := "⚠"
	if m.ascii {
		warn = "!"
	}
	// Everything but the Lock's screen comes first: a long screen never pushes the warnings, the
	// facts out of view, while a acts at once.
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
	blank()
	add(text.Bold(true), clean(h.Header), "")
	for _, l := range h.Body {
		add(text, clean(l), "  ")
	}
	return out
}
