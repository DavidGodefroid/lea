package daemon

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/DavidGodefroid/lea/internal/store"
)

// Health signals (#110, B3): the daemon watches every child lea started and tells its parent (a
// lead of its devs, a tower of its leads) once when the child
//   - passes the context threshold (LEA_CTX_WARN, percent; 0 turns it off);
//   - has been idle longer than LEA_IDLE_WARN with its task unfinished (no result sent yet);
//   - ended without sending its parent a result message;
//   - stopped on its provider's usage limit.
//
// Each signal is sent once per episode: a child back under the threshold, at work again or off
// its limit may be signalled again later. The parent acts (compacts, hands the task to a fresh
// agent, stops it) and escalates only what it cannot fix. The bookkeeping is in memory: a
// daemon restart may repeat a signal once.

const (
	// DefaultCtxWarn is LEA_CTX_WARN's default, in percent of the context window.
	DefaultCtxWarn = 70
	// DefaultIdleWarn is LEA_IDLE_WARN's default.
	DefaultIdleWarn = 30 * time.Minute
	// healthEvery is how often the daemon looks at its agents' health.
	healthEvery = 30 * time.Second
)

// healthMon is the health watch's bookkeeping: per child incarnation (key#incarnation), the
// signals already sent, and the children their parent stopped itself (no "ended" signal).
type healthMon struct {
	mu                         sync.Mutex
	set                        bool // SetHealth was called: ctxWarn and idleWarn are the user's
	ctxWarn                    int
	idleWarn                   time.Duration
	ctx, idle, dead, limit, by map[string]bool
}

func (h *healthMon) init() {
	if h.ctx == nil {
		h.ctx, h.idle, h.dead, h.limit, h.by = map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	}
	if !h.set {
		h.ctxWarn, h.idleWarn, h.set = DefaultCtxWarn, DefaultIdleWarn, true
	}
}

// SetHealth is the user's health thresholds: LEA_CTX_WARN (percent, 0 off) and LEA_IDLE_WARN (0
// off).
func (d *Daemon) SetHealth(ctxWarn int, idleWarn time.Duration) {
	d.health.mu.Lock()
	d.health.init()
	d.health.ctxWarn, d.health.idleWarn = ctxWarn, max(idleWarn, 0)
	d.health.mu.Unlock()
}

func incarnationID(a store.Agent) string { return a.Key + "#" + a.Incarnation }

// markParentStop records that a's parent stops it (agents_stop): its end is no health signal.
func (d *Daemon) markParentStop(a store.Agent) {
	d.health.mu.Lock()
	d.health.init()
	d.health.by[incarnationID(a)] = true
	d.health.mu.Unlock()
}

// RunHealth looks at the agents' health every healthEvery until ctx ends.
func (d *Daemon) RunHealth(ctx context.Context) {
	var arm func()
	arm = func() {
		d.afterFunc(healthEvery, func() {
			if ctx.Err() != nil {
				return
			}
			d.checkHealth()
			arm()
		})
	}
	arm()
}

// checkHealth sends the health signals that are due (see above).
func (d *Daemon) checkHealth() {
	as, err := d.st.Agents()
	if err != nil {
		log.Printf("lea daemon: health: %v", err)
		return
	}
	by := make(map[string]store.Agent, len(as))
	for _, a := range as {
		by[a.Key] = a
	}
	usages := map[string]map[string]store.Usage{}
	now := d.now()
	type signal struct{ to, body string }
	var out []signal
	d.health.mu.Lock()
	d.health.init()
	h := &d.health
	for _, a := range as {
		if a.Profile == "" || a.Parent == "" {
			continue
		}
		p, ok := by[a.Parent]
		if !ok || p.State == "done" || p.State == "stopped" {
			continue
		}
		id := incarnationID(a)
		if a.State == "done" {
			if h.dead[id] || h.by[id] {
				continue
			}
			h.dead[id] = true
			if sent, err := d.st.SentResult(a.Key, a.Parent, a.Created); err == nil && !sent {
				out = append(out, signal{a.Parent, fmt.Sprintf("lea health: your child %s ended without sending you a result. Check its branch and worktree, then start a fresh agent for what is left.", a.Name)})
			}
			continue
		}
		if a.State == "stopped" {
			continue
		}
		u, ok := usages[a.Container]
		if !ok {
			if u, err = d.st.Usages(a.Container); err != nil {
				u = map[string]store.Usage{}
			}
			usages[a.Container] = u
		}
		if pct := u[a.Key].CtxPct; h.ctxWarn > 0 && pct >= h.ctxWarn {
			if !h.ctx[id] {
				h.ctx[id] = true
				out = append(out, signal{a.Parent, fmt.Sprintf("lea health: your child %s has used %d%% of its context window (LEA_CTX_WARN %d%%). Have it write a handoff, then stop it and start a fresh agent on the same branch.", a.Name, pct, h.ctxWarn)})
			}
		} else {
			delete(h.ctx, id)
		}
		if a.LimitUntil != 0 {
			if !h.limit[id] {
				h.limit[id] = true
				when := "it waits for the user"
				if a.LimitUntil > 0 {
					when = "lea resumes it at " + time.Unix(a.LimitUntil, 0).UTC().Format("15:04 UTC")
				}
				out = append(out, signal{a.Parent, fmt.Sprintf("lea health: your child %s hit its provider's usage limit; %s. Hand its task to another agent if it cannot wait.", a.Name, when)})
			}
		} else {
			delete(h.limit, id)
		}
		if a.State != "idle" || h.idleWarn <= 0 {
			delete(h.idle, id)
			continue
		}
		if h.idle[id] || time.Duration(now-a.Updated)*time.Second < h.idleWarn {
			continue
		}
		if _, err := d.st.TaskOf(a.Key); err != nil {
			continue // no open task: nothing unfinished
		}
		if sent, err := d.st.SentResult(a.Key, a.Parent, a.Created); err != nil || sent {
			continue
		}
		h.idle[id] = true
		out = append(out, signal{a.Parent, fmt.Sprintf("lea health: your child %s has been idle for %s with its task unfinished (no result sent). Ask it for a status, or stop it and start a fresh agent.", a.Name, (time.Duration(now-a.Updated) * time.Second).Round(time.Minute))})
	}
	// Forget the incarnations whose row is gone or was reused.
	live := make(map[string]bool, len(as))
	for _, a := range as {
		live[incarnationID(a)] = true
	}
	for _, m := range []map[string]bool{h.ctx, h.idle, h.dead, h.limit, h.by} {
		for id := range m {
			if !live[id] {
				delete(m, id)
			}
		}
	}
	d.health.mu.Unlock()
	for _, s := range out {
		d.post("lea", s.to, "note", s.body, 0)
	}
}
