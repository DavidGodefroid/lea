package agents

import (
	"encoding/json"
	"strings"

	"github.com/DavidGodefroid/lea/internal/store"
	"github.com/DavidGodefroid/lea/internal/textsafe"
)

// Approval is the pending permission request a row shows as a card (the agent's oldest).
type Approval struct {
	ID      int64  `json:"id"`
	Tool    string `json:"tool"`
	Summary string `json:"summary"`
	Always  bool   `json:"always"`
}

// facts are what a row shows besides the agent's own record: its unread messages, the unread
// messages it sent the user, its usage, its running subagents and its oldest pending approval.
type facts struct {
	unread, forUser map[string]int
	usage           map[string]store.Usage
	approval        map[string]*Approval
	status          map[string]store.TaskState
	subagents       map[string]int
}

func loadFacts(st *store.Store, container string) (facts, error) {
	var f facts
	var err error
	if f.unread, err = st.UnreadCounts(); err != nil {
		return f, err
	}
	if f.status, err = st.TaskStatuses(container); err != nil {
		return f, err
	}
	if f.forUser, err = st.UnreadBySender("user"); err != nil {
		return f, err
	}
	if f.usage, err = st.Usages(container); err != nil {
		return f, err
	}
	if f.subagents, err = st.Subagents(container); err != nil {
		return f, err
	}
	pend, err := st.PendingApprovals()
	if err != nil {
		return f, err
	}
	f.approval = map[string]*Approval{}
	for _, p := range pend { // ordered by id: the first one per agent is the oldest
		if _, ok := f.approval[p.Agent]; !ok {
			f.approval[p.Agent] = &Approval{ID: p.ID, Tool: oneLineCap(p.Tool, maxToolRunes), Summary: Summary(p.Tool, p.Input), Always: p.Always}
		}
	}
	return f, nil
}

func (f facts) row(r Row) Row {
	r.Unread, r.ForUser = f.unread[r.Key], f.forUser[r.Key]
	if u, ok := f.usage[r.Key]; ok {
		r.CtxPct, r.CostUSD = u.CtxPct, u.CostUSD
	}
	r.Approval = f.approval[r.Key]
	ts := f.status[r.Key]
	r.Status, r.Waiting, r.BlockedOn = ts.Status, ts.Next, ts.Blocked
	r.Subagents = f.subagents[r.Key]
	return r
}

// summaryKeys are the input fields that name what a tool call would do, most telling first.
var summaryKeys = []string{"command", "file_path", "path", "url", "pattern"}

// Summary is one line naming what an approval would allow: the command of a shell tool, the
// path of a file tool, else the input itself; whitespace runs become one space, controls and
// invisible characters become "?", cut at 200 characters (so its encoding stays small). The deck
// cleans it again before drawing it.
func Summary(tool, input string) string {
	s := input
	var in map[string]any
	if json.Unmarshal([]byte(input), &in) == nil {
		for _, k := range summaryKeys {
			if v, ok := in[k].(string); ok && v != "" {
				s = v
				break
			}
		}
	}
	r := []rune(strings.Map(safeRune, strings.Join(strings.Fields(s), " ")))
	if len(r) > 200 {
		return string(r[:200]) + "…"
	}
	return string(r)
}

// maxToolRunes caps the tool name a snapshot carries.
const maxToolRunes = 64

func oneLineCap(s string, n int) string {
	r := []rune(strings.Join(strings.Fields(strings.ToValidUTF8(s, "?")), " "))
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return string(r)
}

// safeRune maps C0, DEL, C1, bidi and zero-width characters to '?' (the deck's clean set).
func safeRune(r rune) rune {
	if textsafe.Unsafe(r) {
		return '?'
	}
	return r
}
