package deck

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// One question at a time (#95). H pauses the selected agent's inbox: the messages its agents send
// it wait, so the user can answer it in peace; H again releases them, at once. Q opens the menu
// of the selected tower's questions to the user: the open one, then the queue, where picking one
// drops it (it is never asked).

// Question is a control tower's question to the user (the daemon's question_list).
type Question struct {
	ID       int64  `json:"id"`
	Agent    string `json:"agent"`
	From     string `json:"from"`
	Priority string `json:"priority"`
	State    string `json:"state"`
	Body     string `json:"body"`
}

// questionTarget starts a questions menu item's target, the question's id after it.
const questionTarget = "\x00question:"

type questionsMsg struct {
	key  string
	list []Question
	err  error
}

// holdable reports whether H applies to a row: an agent that runs (a shell has no inbox).
func holdable(a Agent) bool { return pausable(a) }

// holdKey is H: pause the selected agent's inbox, or release it.
func (m Model) holdKey() (tea.Model, tea.Cmd) {
	a := m.selectedAgent()
	if a == nil || !holdable(*a) {
		return m, nil
	}
	key, held, api := a.Key, !a.Held, m.api
	if held {
		m.status = "holding the messages to " + display(*a) + ": H releases them"
	} else {
		m.status = "released the messages to " + display(*a)
	}
	return m, m.call(func(ctx context.Context) error { return api.SetHeld(ctx, key, held) })
}

// questionsKey is Q: load the selected agent's questions to the user, then open their menu.
func (m Model) questionsKey() (tea.Model, tea.Cmd) {
	a := m.selectedAgent()
	if a == nil || isShell(*a) {
		return m, nil
	}
	key, api := a.Key, m.api
	m.status = "questions…"
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		qs, err := api.Questions(ctx)
		return questionsMsg{key, qs, err}
	}
}

// questionsLoaded opens the questions menu of msg.key, or says it has none.
func (m Model) questionsLoaded(msg questionsMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.status = oneLine(msg.err.Error())
		return m, nil
	}
	i := m.index(msg.key)
	if i < 0 {
		m.status = ""
		return m, nil
	}
	var qs []Question
	for _, q := range msg.list {
		if q.Agent == msg.key {
			qs = append(qs, q)
		}
	}
	if len(qs) == 0 {
		m.status = "no question of " + display(m.agents[i]) + " waits for you"
		return m, nil
	}
	m.status = ""
	m.openMenu(msg.key)
	m.questions = qs
	return m, nil
}

// questionItems are the questions menu's items: the open question (nothing to pick), then the
// queued ones, numbered, each dropping its question.
func questionItems(qs []Question) []menuItem {
	var items []menuItem
	n := 0
	for _, q := range qs {
		text := strings.Join(strings.Fields(q.Body), " ")
		from := ""
		if q.From != "" {
			from = ", from " + shortKey(q.From)
		}
		if q.State == "open" {
			items = append(items, menuItem{fmt.Sprintf("#%d open%s: %s", q.ID, from, text), "", ""})
			continue
		}
		key := ""
		if n++; n <= 9 {
			key = strconv.Itoa(n)
		}
		items = append(items, menuItem{fmt.Sprintf("Drop #%d (%s%s): %s", q.ID, q.Priority, from, text), key,
			questionTarget + strconv.FormatInt(q.ID, 10)})
	}
	return items
}

// shortKey is an agent key without its container.
func shortKey(k string) string {
	if i := strings.LastIndexByte(k, '/'); i >= 0 {
		return k[i+1:]
	}
	return k
}

// pickQuestion runs a questions menu item: a queued question is dropped, the open one is left.
func (m Model) pickQuestion(it menuItem) (tea.Model, tea.Cmd) {
	m.closeMenu()
	s, ok := strings.CutPrefix(it.target, questionTarget)
	if !ok {
		return m, nil
	}
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return m, nil
	}
	api := m.api
	m.status = fmt.Sprintf("dropped question #%d", id)
	return m, m.call(func(ctx context.Context) error { return api.DropQuestion(ctx, id) })
}

// heldMark is a held agent's mark on its row: "⏸ 3", its held messages.
func (m Model) heldMark(a Agent) string {
	mark := "⏸"
	if m.ascii {
		mark = "||"
	}
	return fmt.Sprintf("%s %d", mark, a.HeldCount)
}
