package deck

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/DavidGodefroid/lea/internal/textsafe"
)

// Message is one message of an agent's thread (the daemon's thread_list).
type Message struct {
	ID     int64  `json:"id"`
	Thread int64  `json:"thread"`
	From   string `json:"from"`
	To     string `json:"to"`
	Kind   string `json:"kind"`
	Body   string `json:"body"`
	At     int64  `json:"at"`
	ReadAt int64  `json:"read_at"`
	// ReplyTo is the message this one answers, 0 when none.
	ReplyTo int64 `json:"reply_to"`
}

// ThreadAPI is what the thread popup asks of the daemon.
type ThreadAPI interface {
	Thread(ctx context.Context, key string) ([]Message, error)
	Send(ctx context.Context, key, body string) error
	// Label is the agent's display label ("" when none).
	Label(ctx context.Context, key string) (string, error)
}

const (
	pollDelay  = 2 * time.Second
	draftLines = 3
	loadFailed = "cannot load the thread: "
)

type (
	threadLoaded struct {
		msgs  []Message
		err   error
		label *string // nil: unknown (the label could not be read)
	}
	threadPoll struct{}
	threadSent struct{ err error }
)

// ThreadView is the thread popup (`lea deck --thread KEY`): the messages an agent sent and
// received, newest at the bottom, and an editor to write to it as the user. It runs in a tmux
// popup over the deck; q or esc closes it.
type ThreadView struct {
	api           ThreadAPI
	key           string
	ascii         bool
	now           func() time.Time
	msgs          []Message
	label         string // the agent's display label, "" when none
	draft         []rune
	composing     bool
	oneShot       bool // opened by m: closes after sending, or on esc with nothing written
	offset        int  // lines scrolled up from the newest
	status        string
	width, height int
	wrapped       *wrapCache // shared by the copies of the view: Bubble Tea runs it on one goroutine
}

// wrapCache keeps each message's wrapped body for one width and charset: bodies never change,
// so a keypress or a poll re-wraps only new messages, not up to 200 bodies of 16 KiB.
type wrapCache struct {
	width int
	ascii bool
	ids   map[int64][]string
	// all is the last full rendering of the thread, reused while nothing it shows changed: the
	// same messages, width and charset, within the same second (headers show ages).
	all    []string
	allKey linesKey
}

type linesKey struct {
	width int
	ascii bool
	n     int
	first *Message
	sec   int64
}

// prune drops the cached bodies of messages no longer in the thread.
func (v ThreadView) prune() {
	if v.wrapped == nil || v.wrapped.ids == nil {
		return
	}
	keep := make(map[int64]bool, len(v.msgs))
	for _, m := range v.msgs {
		keep[m.ID] = true
	}
	for id := range v.wrapped.ids {
		if !keep[id] {
			delete(v.wrapped.ids, id)
		}
	}
}

// body returns the wrapped lines of m's body, from the cache when it has them.
func (v ThreadView) body(m Message, w int, c charset) []string {
	if v.wrapped == nil {
		return wrap(m.Body, w, c)
	}
	if v.wrapped.width != w || v.wrapped.ascii != c.ascii || v.wrapped.ids == nil {
		*v.wrapped = wrapCache{width: w, ascii: c.ascii, ids: map[int64][]string{}}
	}
	ls, ok := v.wrapped.ids[m.ID]
	if !ok {
		ls = wrap(m.Body, w, c)
		v.wrapped.ids[m.ID] = ls
	}
	return ls
}

func NewThread(api ThreadAPI, key string, compose, ascii bool, now func() time.Time) ThreadView {
	return ThreadView{api: api, key: key, ascii: ascii, now: now, composing: compose, oneShot: compose,
		wrapped: &wrapCache{}}
}

func (v ThreadView) Init() tea.Cmd { return v.load() }

func (v ThreadView) load() tea.Cmd {
	api, key := v.api, v.key
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		ms, err := api.Thread(ctx, key)
		var label *string
		if l, lerr := api.Label(ctx, key); lerr == nil {
			label = &l
		}
		return threadLoaded{ms, err, label}
	}
}

func (v ThreadView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width, v.height = msg.Width, msg.Height
	case threadLoaded:
		if msg.label != nil {
			v.label = *msg.label
		}
		if msg.err != nil {
			v.status = loadFailed + oneLine(msg.err.Error())
		} else {
			v.msgs = msg.msgs
			v.prune()
			if strings.HasPrefix(v.status, loadFailed) {
				v.status = ""
			}
		}
		return v, tea.Tick(pollDelay, func(time.Time) tea.Msg { return threadPoll{} })
	case threadPoll:
		return v, v.load()
	case threadSent:
		if msg.err != nil {
			v.status = oneLine(msg.err.Error())
			return v, nil
		}
		if v.oneShot {
			return v, tea.Quit
		}
		v.draft, v.composing, v.offset, v.status = nil, false, 0, "sent"
		return v, v.load()
	case tea.KeyMsg:
		return v.onKey(msg)
	}
	return v, nil
}

func (v ThreadView) onKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	if v.composing {
		switch {
		case s == "ctrl+c":
			return v, tea.Quit
		case s == "esc":
			if v.oneShot && len(v.draft) == 0 {
				return v, tea.Quit
			}
			v.composing = false
		case s == "enter":
			body := strings.TrimSpace(string(v.draft))
			if body == "" {
				return v, nil
			}
			api, key := v.api, v.key
			v.status = "sending…"
			return v, func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
				defer cancel()
				return threadSent{api.Send(ctx, key, body)}
			}
		case s == "ctrl+j":
			v.draft = append(v.draft, '\n')
		case s == "backspace":
			if len(v.draft) > 0 {
				v.draft = v.draft[:len(v.draft)-1]
			}
		case s == "ctrl+u":
			v.draft = nil
		case k.Type == tea.KeySpace:
			v.draft = append(v.draft, ' ')
		case k.Type == tea.KeyRunes:
			for _, r := range k.Runes {
				if r == '\r' {
					r = '\n'
				}
				if r == '\n' || r == '\t' || !textsafe.Unsafe(r) {
					v.draft = append(v.draft, r)
				}
			}
		}
		return v, nil
	}
	switch s {
	case "q", "esc", "ctrl+c":
		return v, tea.Quit
	case "m", "r":
		v.composing = true
	case "k", "up":
		v.offset = min(v.offset+1, v.maxOffset())
	case "j", "down":
		v.offset = max(0, v.offset-1)
	case "pgup":
		v.offset = min(v.offset+max(1, v.rows()-1), v.maxOffset())
	case "pgdown":
		v.offset = max(0, v.offset-max(1, v.rows()-1))
	case "G", "end":
		v.offset = 0
	}
	return v, nil
}

func (v ThreadView) size() (int, int) {
	w, h := v.width, v.height
	if w < 20 {
		w = 80
	}
	if h < 6 {
		h = 24
	}
	return w, h
}

// maxOffset is how far up the thread scrolls: its first line at the top.
func (v ThreadView) maxOffset() int {
	w, _ := v.size()
	return max(0, len(v.lines(w, charsetFor(v.ascii)))-v.rows())
}

// rows is the height left for messages: the title line and the footer take the rest.
func (v ThreadView) rows() int {
	_, h := v.size()
	return max(1, h-1-len(v.footer(charsetFor(v.ascii))))
}

func (v ThreadView) View() string {
	w, _ := v.size()
	c := charsetFor(v.ascii)
	what := "messages" // every message the agent sent or received
	if v.oneShot {
		what = "message to" // opened by m, to write one
	}
	title := lipgloss.NewStyle().Foreground(AgentColor(v.key)).Bold(true).
		Render(fit(what+" "+c.dot+" "+v.title(), w, c))
	lines := v.lines(w, c)
	rows := v.rows()
	off := min(v.offset, max(0, len(lines)-rows))
	end := len(lines) - off
	body := append([]string(nil), lines[max(0, end-rows):end]...)
	for len(body) < rows {
		body = append(body, "")
	}
	out := append([]string{title}, body...)
	return strings.Join(append(out, v.footer(c)...), "\n")
}

// title names the agent: "label (name)" when the user gave it a label.
func (v ThreadView) title() string {
	if v.label != "" {
		return v.label + " (" + nameOf(v.key) + ")"
	}
	return nameOf(v.key)
}

// nameOf is how a sender or recipient key is shown: the agent's name, "you" for the user.
func nameOf(key string) string {
	if key == "user" {
		return "you"
	}
	if i := strings.LastIndex(key, "/"); i >= 0 {
		return key[i+1:]
	}
	return key
}

func (v ThreadView) lines(w int, c charset) []string {
	if len(v.msgs) == 0 {
		return []string{muted.Render(fit("No messages yet: m writes one.", w, c))}
	}
	k := linesKey{w, c.ascii, len(v.msgs), &v.msgs[0], v.now().Unix()}
	if v.wrapped != nil && v.wrapped.all != nil && v.wrapped.allKey == k {
		return v.wrapped.all
	}
	out := v.render(w, c)
	if v.wrapped != nil {
		v.wrapped.all, v.wrapped.allKey = out, k
	}
	return out
}

// render draws every message: a header, then the wrapped body.
func (v ThreadView) render(w int, c charset) []string {
	var out []string
	for i, m := range v.msgs {
		if i > 0 {
			out = append(out, "")
		}
		head := fmt.Sprintf("#%d %s %s %s %s %s %s %s", m.ID, nameOf(m.From), c.mark, nameOf(m.To),
			c.dot, kindOf(m), c.dot, age(v.now().Unix()-m.At))
		color := AgentColor(m.From)
		if m.From == "user" {
			color = theme.Accent
		}
		out = append(out, lipgloss.NewStyle().Foreground(color).Bold(true).Render(fit(head, w, c)))
		for _, l := range v.body(m, w-2, c) {
			out = append(out, "  "+l)
		}
	}
	return out
}

// kindOf is how a message's kind is shown: an answer names the message it answers ("result to
// #12"), so an ask's question and its answer read as a pair.
func kindOf(m Message) string {
	if m.ReplyTo > 0 {
		return fmt.Sprintf("%s to #%d", m.Kind, m.ReplyTo)
	}
	return m.Kind
}

// wrap cleans each line of s and cuts it into pieces of at most w cells.
func wrap(s string, w int, c charset) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = safe(strings.ReplaceAll(line, "\t", "    "), c)
		var cur strings.Builder
		cw := 0
		for _, g := range clusters(line) {
			if cw+g.w > w && cw > 0 {
				out = append(out, cur.String())
				cur.Reset()
				cw = 0
			}
			cur.WriteString(g.s)
			cw += g.w
		}
		out = append(out, cur.String())
	}
	return out
}

func (v ThreadView) footer(c charset) []string {
	w, _ := v.size()
	var out []string
	if v.status != "" {
		style := alert
		if v.status == "sent" || v.status == "sending…" {
			style = muted
		}
		out = append(out, style.Render(fit(v.status, w, c)))
	}
	if !v.composing {
		return append(out, muted.Render(fit("m write "+c.dot+" j/k scroll "+c.dot+" q close", w, c)))
	}
	cursor := "▏"
	if v.ascii {
		cursor = "_"
	}
	lines := strings.Split(string(v.draft), "\n")
	if len(lines) > draftLines {
		lines = lines[len(lines)-draftLines:]
	}
	for i, l := range lines {
		prefix := "  "
		if i == 0 {
			prefix = c.mark + " "
		}
		if i == len(lines)-1 {
			l = safe(l, c) + cursor
		} else {
			l = safe(l, c)
		}
		out = append(out, prefix+tail(l, w-2))
	}
	return append(out, muted.Render(fit("enter send to its inbox "+c.dot+" ctrl+j new line "+c.dot+" esc cancel", w, c)))
}
