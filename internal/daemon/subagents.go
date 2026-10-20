package daemon

import (
	"encoding/json"
	"log"
	"regexp"
	"time"
)

const (
	// maxSubagents caps the subagents counted per agent: the hooks come from inside the agent's
	// container, so the set they fill is bounded.
	maxSubagents = 32
	// subagentTTL forgets an agent's subagents once no hook came from it for that long: a lost
	// SubagentStop must not keep its row busy for ever.
	subagentTTL = 30 * time.Minute
)

// subagentID is an agent_id lea keeps: a plain identifier. Anything else is ignored.
var subagentID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// subagents are the subagents an agent runs, by agent_id, and the timer that forgets them.
type subagents struct {
	ids   map[string]bool
	timer *time.Timer
}

// trackSubagents follows the subagents an agent runs from its hook events, for display only:
// SubagentStart adds one, SubagentStop removes it, an ended session forgets them all, and any
// other hook of the agent keeps them for another TTL. SessionStart does not forget them: it also
// comes after a compaction, while background subagents go on. The count goes to the store (the deck
// and agents_list read it); it never changes the agent's state, its approvals or its stop.
func (d *Daemon) trackSubagents(me, event string, payload json.RawMessage) {
	defer d.lockKey("sub:" + me)()
	d.mu.Lock()
	s := d.subs[me]
	before := 0
	if s != nil {
		before = len(s.ids)
	}
	switch event {
	case "SubagentStart", "SubagentStop":
		var p struct {
			AgentID string `json:"agent_id"`
		}
		if json.Unmarshal(payload, &p) != nil || !subagentID.MatchString(p.AgentID) {
			break
		}
		if event == "SubagentStart" {
			if s == nil {
				s = &subagents{ids: map[string]bool{}}
				d.subs[me] = s
			}
			if len(s.ids) < maxSubagents {
				s.ids[p.AgentID] = true
			}
		} else if s != nil {
			delete(s.ids, p.AgentID)
		}
	case "SessionEnd":
		if s != nil {
			clear(s.ids)
		}
	}
	after := 0
	if s != nil {
		after = len(s.ids)
		if s.timer != nil {
			s.timer.Stop()
			s.timer = nil
		}
		if after == 0 {
			delete(d.subs, me)
		} else {
			var t *time.Timer
			t = time.AfterFunc(d.subTTL, func() { d.expireSubagents(me, t) })
			s.timer = t
		}
	}
	d.mu.Unlock()
	if after != before {
		d.storeSubagents(me, after)
	}
}

// expireSubagents forgets an agent's subagents when its timer t is still the current one.
func (d *Daemon) expireSubagents(me string, t *time.Timer) {
	defer d.lockKey("sub:" + me)()
	d.mu.Lock()
	s := d.subs[me]
	mine := s != nil && s.timer == t
	if mine {
		delete(d.subs, me)
	}
	d.mu.Unlock()
	if mine {
		d.storeSubagents(me, 0)
	}
}

// forgetSubagentsLocked drops the subagents of an agent whose row is gone (d.mu held).
func (d *Daemon) forgetSubagentsLocked(me string) {
	if s := d.subs[me]; s != nil {
		if s.timer != nil {
			s.timer.Stop()
		}
		delete(d.subs, me)
	}
}

func (d *Daemon) storeSubagents(me string, n int) {
	changed, err := d.st.SetSubagents(me, n)
	if err != nil {
		log.Printf("lea daemon: set subagents of %s: %v", me, err)
		return
	}
	if changed {
		d.changed()
	}
}
