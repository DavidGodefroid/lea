package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/DavidGodefroid/lea/internal/store"
	"github.com/DavidGodefroid/lea/internal/textsafe"
)

const (
	maxStatusText = 200
	maxNext       = 500
	maxBlockedOn  = 200
	boardEnded    = 20
	// boardMax keeps a board_list answer under the wire limit (every text field is capped).
	boardMax = 100
)

// checkLine accepts one line of agent text: valid UTF-8, no control, bidi or zero-width
// character, at most max characters. The error never echoes the value.
func checkLine(field, v string, max int) error {
	if !utf8.ValidString(v) {
		return fmt.Errorf("%s is not valid UTF-8", field)
	}
	for _, r := range v {
		if textsafe.Unsafe(r) {
			return fmt.Errorf("%s must be one line without control, bidi or zero-width characters", field)
		}
	}
	if n := utf8.RuneCountInString(v); n > max {
		return fmt.Errorf("%s is %d characters, limit %d", field, n, max)
	}
	return nil
}

// taskUpdate records the calling agent's report on its own task (spec §6.1); it feeds the deck's
// row and lea board. No method writes another agent's task.
func (d *Daemon) taskUpdate(o Origin, me string, raw json.RawMessage) (any, error) {
	if _, err := d.spawnedCaller(o, me, "task_update"); err != nil {
		return nil, err
	}
	var p struct {
		StatusText string `json:"status_text"`
		Next       string `json:"next"`
		BlockedOn  string `json:"blocked_on"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, errors.New("task_update: invalid parameters")
	}
	for _, f := range []struct {
		name, v string
		max     int
	}{{"status_text", p.StatusText, maxStatusText}, {"next", p.Next, maxNext}, {"blocked_on", p.BlockedOn, maxBlockedOn}} {
		if err := checkLine(f.name, f.v, f.max); err != nil {
			return nil, err
		}
	}
	if err := d.rates.allow("task_update", me, d.now()); err != nil {
		return nil, err
	}
	changed, err := d.st.UpdateTask(me, p.StatusText, p.Next, p.BlockedOn, d.now())
	if err != nil {
		return nil, err
	}
	if changed {
		d.changed()
	}
	return struct{}{}, nil
}

type boardItem struct {
	Task      int64  `json:"task"`
	Container string `json:"container"`
	Agent     string `json:"agent"`
	// Label is the user's display name for the agent (see agentLabel), "" when none.
	Label     string `json:"label,omitempty"`
	Role      string `json:"role"`
	Parent    string `json:"parent"`
	State     string `json:"state"`
	Subagents int    `json:"subagents,omitempty"`
	Repo      string `json:"repo"`
	Branch    string `json:"branch"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	Next      string `json:"next"`
	BlockedOn string `json:"blocked_on"`
	Updated   int64  `json:"updated"`
	Ended     int64  `json:"ended"`
	// WaitsOn is the agent its ask waits on (container/name), Question the question's id: an ask
	// in flight. Both are the daemon's, never an agent's text.
	WaitsOn  string `json:"waits_on,omitempty"`
	Question int64  `json:"question,omitempty"`
	// Stack is what lea stopped of the agent's nested containers while it was idle (#132), the
	// daemon's text, until the agent works again.
	Stack string `json:"stack,omitempty"`
	// Shed says lea paused the agent under pressure (#132, P2), the daemon's text, until it
	// resumes it.
	Shed string `json:"shed,omitempty"`
}

type boardView struct {
	Items  []boardItem `json:"items"`
	Leases []leaseView `json:"leases"`
	// Heavy is the heavy jobs that run or wait (#132), per container.
	Heavy []heavyView `json:"heavy"`
	// Pressure is the host's gauge, then each container's (#132, P2), none without a recent sample.
	Pressure []Gauge `json:"pressure,omitempty"`
}

// boardList is the board (spec A13; host only): every open task with its agent, then the last
// ended ones. Its text is the agents'; the CLI cleans it when it prints it.
func (d *Daemon) boardList(raw json.RawMessage) (boardView, error) {
	var p struct {
		Container string `json:"container"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return boardView{}, errors.New("board_list: invalid parameters")
		}
	}
	if p.Container != "" && !containerName.MatchString(p.Container) {
		return boardView{}, errors.New("board_list: invalid container name")
	}
	ts, err := d.st.Tasks(p.Container, boardEnded)
	if err != nil {
		return boardView{}, err
	}
	as, err := d.st.Agents()
	if err != nil {
		return boardView{}, err
	}
	subs, err := d.st.Subagents(p.Container)
	if err != nil {
		return boardView{}, err
	}
	by := map[string]store.Agent{}
	for _, a := range as {
		by[a.Key] = a
	}
	d.mu.Lock()
	asks := make(map[string]pendingAsk, len(d.asks))
	for k, v := range d.asks {
		asks[k] = v
	}
	parked := make(map[string]string, len(d.parked))
	for k, v := range d.parked {
		parked[k] = v
	}
	shed := map[string]string{}
	for _, t := range ts {
		if l := d.shedLabelLocked(t.Agent); l != "" {
			shed[t.Agent] = l
		}
	}
	d.mu.Unlock()
	out := boardView{Items: []boardItem{}, Leases: []leaseView{}}
	for i, t := range ts {
		if i == boardMax {
			break
		}
		it := boardItem{Task: t.ID, Container: t.Container, Agent: shortName(t.Agent), State: "gone", Title: t.Title,
			Status: t.StatusText, Next: t.Next, BlockedOn: t.BlockedOn, Updated: t.Updated, Ended: t.Ended}
		if a, ok := by[t.Agent]; ok && t.Ended == 0 {
			it.Role, it.State, it.Repo, it.Branch, it.Subagents = a.Role, a.State, a.Repo, a.Branch, subs[t.Agent]
			it.Label = a.Label
			if a.Parent != "" {
				it.Parent = shortName(a.Parent)
			}
			if q, ok := asks[t.Agent]; ok {
				it.WaitsOn, it.Question = q.target, q.id
			}
			it.Stack = parked[t.Agent]
			it.Shed = shed[t.Agent]
		}
		out.Items = append(out.Items, it)
	}
	ls, err := d.st.Leases(p.Container, d.now())
	if err != nil {
		return boardView{}, err
	}
	for i, l := range ls {
		if i == boardLeases {
			break
		}
		out.Leases = append(out.Leases, leaseView{l.ID, l.Container, shortName(l.Agent), l.Glob, l.Expires})
	}
	out.Heavy = d.heavyBoard(p.Container)
	out.Pressure = d.gauges(p.Container)
	return out, nil
}

type leaseView struct {
	ID        int64  `json:"id"`
	Container string `json:"container"`
	Agent     string `json:"agent"`
	Glob      string `json:"glob"`
	Expires   int64  `json:"expires"`
}

// boardRead is the board for an agent (board_read): the tasks and leases of the caller's own
// container, the host's lea board limited to it. Every text an agent or a brief wrote (a lease's
// glob included) is marked untrusted, as agents_list marks the task line.
func (d *Daemon) boardRead(o Origin) (boardView, error) {
	raw, _ := json.Marshal(map[string]string{"container": o.Container})
	b, err := d.boardList(raw)
	if err != nil {
		return b, err
	}
	mark := func(s *string) {
		if *s != "" {
			*s = "untrusted: " + *s
		}
	}
	for i := range b.Items {
		it := &b.Items[i]
		it.Label = safeLabel(it.Label)
		mark(&it.Title)
		mark(&it.Status)
		mark(&it.Next)
		mark(&it.BlockedOn)
	}
	for i := range b.Leases {
		mark(&b.Leases[i].Glob)
	}
	// A heavy job's label is its agent's text.
	for i := range b.Heavy {
		for j := range b.Heavy[i].Running {
			mark(&b.Heavy[i].Running[j])
		}
	}
	return b, nil
}
