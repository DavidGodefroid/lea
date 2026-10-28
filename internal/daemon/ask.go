package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/DavidGodefroid/lea/internal/bus"
	"github.com/DavidGodefroid/lea/internal/store"
)

const (
	// askDefault and askMax bound how long an ask waits for its answer, in seconds.
	askDefault = 300
	askMax     = 600
	// askDepth caps a chain of agents waiting on one another (A asks B, which asks C...): the
	// asks of one chain, counted end to end.
	askDepth = 3
	// askQueue caps the questions waiting for one agent's answer at once.
	askQueue = 4
)

// askRecheck is how often a waiting ask checks that its target can still answer (a variable:
// tests shorten it).
var askRecheck = 2 * time.Second

// pendingAsk is an ask in flight: its asker waits for target to answer question id.
type pendingAsk struct {
	target string
	id     int64
}

// answerView is what ask returns: the answer, wrapped as untrusted data, or none when the
// timeout ended first.
type answerView struct {
	Question int64  `json:"question"`
	Answered bool   `json:"answered"`
	ID       int64  `json:"id,omitempty"`
	Text     string `json:"text"`
	to       string
}

// taken lets restore put the answer back in the asker's inbox when ask's result is never read.
func (v answerView) taken() (string, []int64) {
	if v.ID == 0 {
		return v.to, nil
	}
	return v.to, []int64{v.ID}
}

// checkAsk refuses an ask that would make a loop (target waits, directly or not, on asker), a
// chain longer than askDepth, a second ask from the same asker, or a full queue at target. It
// runs under d.mu. Every asker waits on one target at most, so the waits form chains: walking
// down from target and up from asker covers the whole chain the new ask joins.
func (d *Daemon) checkAsk(asker, target string) error {
	if _, busy := d.asks[asker]; busy {
		return errors.New("ask: you already wait for an answer: one ask at a time")
	}
	down := 0
	for k := target; ; {
		if k == asker {
			return fmt.Errorf("ask: loop: %s already waits, directly or not, on your answer", target)
		}
		p, ok := d.asks[k]
		if !ok {
			break
		}
		down++
		k = p.target
	}
	if up := d.askHeight(asker, 0); up+1+down > askDepth {
		return fmt.Errorf("ask: chain too long: %d agents would wait on one another (limit %d)", up+1+down, askDepth)
	}
	n := 0
	for _, p := range d.asks {
		if p.target == target {
			n++
		}
	}
	if n >= askQueue {
		return fmt.Errorf("ask: %s already has %d questions waiting (limit %d): try later or use msg_send", target, n, askQueue)
	}
	return nil
}

// askHeight is the longest chain of askers waiting, directly or not, on k.
func (d *Daemon) askHeight(k string, depth int) int {
	if depth > askDepth { // the chains are acyclic (checkAsk); this bounds the walk anyway
		return depth
	}
	h := 0
	for a, p := range d.asks {
		if p.target == k {
			h = max(h, 1+d.askHeight(a, depth+1))
		}
	}
	return h
}

// ask sends question to another agent and waits until it answers (msg_reply, or any message it
// sends in reply to the question) or timeoutS ends. The answer comes back wrapped as untrusted
// data, like every message. A late answer stays in the asker's inbox.
func (d *Daemon) ask(ctx context.Context, o Origin, me string, raw json.RawMessage) (answerView, error) {
	if o.Container == "" {
		return answerView{}, errors.New("ask: agents only")
	}
	var p struct {
		To       string `json:"to"`
		Question string `json:"question"`
		TimeoutS int    `json:"timeout_s"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return answerView{}, err
	}
	if p.TimeoutS == 0 {
		p.TimeoutS = askDefault
	}
	if p.TimeoutS < 1 || p.TimeoutS > askMax {
		return answerView{}, fmt.Errorf("timeout_s must be between 1 and %d", askMax)
	}
	if p.To == "user" {
		return answerView{}, errors.New("ask: the user is not asked here: use msg_send to user")
	}
	ks, err := d.keys()
	if err != nil {
		return answerView{}, err
	}
	target, err := bus.Resolve(p.To, ks)
	if err != nil {
		return answerView{}, err
	}
	if target == me {
		return answerView{}, errors.New("ask: you cannot ask yourself")
	}
	a, err := d.st.AgentByKey(target)
	if err != nil {
		return answerView{}, err
	}
	if err := unreachable(a); err != nil {
		return answerView{}, err
	}
	// The chain check and the registration happen under one lock: two asks racing to close a
	// loop cannot both pass. Only an ask the guards let through counts against the rate. The
	// question is sent after it; a refused send unregisters.
	d.mu.Lock()
	if err := d.checkAsk(me, target); err != nil {
		d.mu.Unlock()
		return answerView{}, err
	}
	if err := d.rates.allow("ask", me, d.now()); err != nil {
		d.mu.Unlock()
		return answerView{}, err
	}
	d.asks[me] = pendingAsk{target: target}
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.asks, me)
		d.mu.Unlock()
		d.changed()
	}()
	q, err := d.send(me, target, p.Question, "question", 0)
	if err != nil {
		return answerView{}, err
	}
	d.mu.Lock()
	d.asks[me] = pendingAsk{target: target, id: q.ID}
	d.mu.Unlock()

	deadline := time.After(time.Duration(p.TimeoutS) * time.Second)
	// A target that ends or is paused never wakes the asker: its state is checked again on
	// every wake and every askRecheck.
	tick := time.NewTicker(askRecheck)
	defer tick.Stop()
	for {
		ch := d.waitChan(me)
		v, ok, err := d.answer(me, target, q.ID)
		if err != nil || ok {
			return v, err
		}
		if a, err := d.st.AgentByKey(target); err != nil || unreachable(a) != nil {
			if err == nil {
				err = unreachable(a)
			} else if errors.Is(err, store.ErrNotFound) {
				err = fmt.Errorf("ask: %s is gone", target)
			}
			return answerView{}, fmt.Errorf("%w before answering question #%d", err, q.ID)
		}
		select {
		case <-ch:
		case <-tick.C:
		case <-deadline:
			return answerView{Question: q.ID, to: me, Text: fmt.Sprintf(
				"no answer from %s within %d s: question #%d stays in its inbox; a late answer arrives in yours (msg_inbox)",
				target, p.TimeoutS, q.ID)}, nil
		case <-ctx.Done():
			return answerView{}, ctx.Err()
		}
	}
}

// answer takes target's answer to question id, if there is one: it is marked read, since ask
// delivers it.
func (d *Daemon) answer(me, target string, id int64) (answerView, bool, error) {
	defer d.lockKey("take:" + me)()
	m, err := d.st.ReplyFrom(id, target, me)
	if errors.Is(err, store.ErrNotFound) {
		return answerView{}, false, nil
	}
	if err != nil {
		return answerView{}, false, err
	}
	if m.ReadAt != 0 { // already delivered (msg_inbox, a Stop hook): never twice
		return answerView{Question: id, Answered: true, to: me,
			Text: fmt.Sprintf("the answer to question #%d is message #%d, already delivered to you through your inbox", id, m.ID)}, true, nil
	}
	if err := d.st.MarkRead([]int64{m.ID}, d.now()); err != nil {
		return answerView{}, false, err
	}
	d.changed()
	// ID: an answer ask took goes back to the inbox when the result is lost.
	return answerView{Question: id, Answered: true, ID: m.ID, Text: bus.Wrap(m, d.senderLabel(m.From, me)), to: me}, true, nil
}

// unreachable refuses a target that will not answer: ended or paused.
func unreachable(a store.Agent) error {
	if a.State == "done" {
		return fmt.Errorf("ask: %s has ended", a.Key)
	}
	if a.Paused {
		return fmt.Errorf("ask: %s is paused", a.Key)
	}
	return nil
}
