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
	Role      string `json:"role"`
	Parent    string `json:"parent"`
	State     string `json:"state"`
	Repo      string `json:"repo"`
	Branch    string `json:"branch"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	Next      string `json:"next"`
	BlockedOn string `json:"blocked_on"`
	Updated   int64  `json:"updated"`
	Ended     int64  `json:"ended"`
}

type boardView struct {
	Items  []boardItem `json:"items"`
	Leases []leaseView `json:"leases"`
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
	by := map[string]store.Agent{}
	for _, a := range as {
		by[a.Key] = a
	}
	out := boardView{Items: []boardItem{}, Leases: []leaseView{}}
	for i, t := range ts {
		if i == boardMax {
			break
		}
		it := boardItem{Task: t.ID, Container: t.Container, Agent: shortName(t.Agent), State: "gone", Title: t.Title,
			Status: t.StatusText, Next: t.Next, BlockedOn: t.BlockedOn, Updated: t.Updated, Ended: t.Ended}
		if a, ok := by[t.Agent]; ok && t.Ended == 0 {
			it.Role, it.State, it.Repo, it.Branch = a.Role, a.State, a.Repo, a.Branch
			if a.Parent != "" {
				it.Parent = shortName(a.Parent)
			}
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
	return out, nil
}

type leaseView struct {
	ID        int64  `json:"id"`
	Container string `json:"container"`
	Agent     string `json:"agent"`
	Glob      string `json:"glob"`
	Expires   int64  `json:"expires"`
}
