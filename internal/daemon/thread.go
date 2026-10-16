package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/DavidGodefroid/lea/internal/store"
)

const (
	threadDefault = 200
	threadMax     = 500
	// threadBody caps one body in the thread view; the agent's own delivery is never cut.
	threadBody = 16 << 10
	cutMark    = "\n… (cut: read it with lea inbox)"
)

type threadView struct {
	ID     int64  `json:"id"`
	Thread int64  `json:"thread"`
	From   string `json:"from"`
	To     string `json:"to"`
	Kind   string `json:"kind"`
	Body   string `json:"body"`
	At     int64  `json:"at"`
	ReadAt int64  `json:"read_at"`
}

// cutBody cuts s to at most n bytes without splitting a UTF-8 sequence, marking the cut.
func cutBody(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + cutMark
}

// threadList returns the last messages an agent sent or received, oldest first, for the deck's
// thread popup. The newest come first into the answer, which stays under batchBytes. Showing a
// message to the user is reading it: the returned messages from that agent to the user are
// marked read; the agent's own inbox is never touched.
func (d *Daemon) threadList(raw json.RawMessage) ([]threadView, error) {
	var p struct {
		Key   string `json:"key"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.Limit == 0 {
		p.Limit = threadDefault
	}
	if p.Limit < 1 || p.Limit > threadMax {
		return nil, fmt.Errorf("limit must be between 1 and %d", threadMax)
	}
	if _, err := d.st.AgentByKey(p.Key); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("unknown agent %q", p.Key)
		}
		return nil, err
	}
	ms, err := d.st.Thread(p.Key, p.Limit)
	if err != nil {
		return nil, err
	}
	var rev []threadView
	var read []int64
	size := 2
	for i := len(ms) - 1; i >= 0; i-- {
		m := ms[i]
		v := threadView{m.ID, m.Thread, m.From, m.To, m.Kind, cutBody(m.Body, threadBody), m.At, m.ReadAt}
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		if len(rev) > 0 && size+len(b)+1 > batchBytes {
			break
		}
		size += len(b) + 1
		rev = append(rev, v)
		if m.From == p.Key && m.To == "user" && m.ReadAt == 0 {
			read = append(read, m.ID)
		}
	}
	if err := d.st.MarkRead(read, d.now()); err != nil {
		return nil, err
	}
	if len(read) > 0 {
		d.changed()
	}
	out := make([]threadView, len(rev))
	for i, v := range rev {
		out[len(rev)-1-i] = v
	}
	return out, nil
}
