package daemon

import (
	"fmt"
	"strings"
	"sync"
)

// rate is at most n calls in span seconds.
type rate struct {
	n    int
	span int64
}

// methodRates caps the agent-tree, board and lease methods per caller. Agents are not trusted to
// pace themselves (spec §6.3 caps messages the same way: 10 a minute).
var methodRates = map[string]rate{
	"agents_spawn": {3, 60}, "agents_request_spawn": {3, 60}, "agents_stop": {10, 60},
	"task_update": {30, 60}, "lease_acquire": {30, 60}, "lease_release": {30, 60},
}

// rates keeps each caller's recent calls per method, in memory: a daemon restart forgets them.
type rates struct {
	mu   sync.Mutex
	hits map[string][]int64
}

// allow records a call of method by caller at now, or refuses it when the window is full.
func (r *rates) allow(method, caller string, now int64) error {
	lim, ok := methodRates[method]
	if !ok {
		return nil
	}
	k := method + " " + caller
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hits == nil {
		r.hits = map[string][]int64{}
	}
	keep := r.hits[k][:0]
	for _, t := range r.hits[k] {
		if t > now-lim.span {
			keep = append(keep, t)
		}
	}
	if len(keep) >= lim.n {
		r.hits[k] = keep
		return fmt.Errorf("%s: rate limit: %d calls in the last %d s (limit %d)", method, len(keep), lim.span, lim.n)
	}
	r.hits[k] = append(keep, now)
	return nil
}

// prune drops the windows of the callers gone reports true for (agents whose row is gone), so
// the map does not grow with every agent ever started.
func (r *rates) prune(gone func(caller string) bool) {
	r.mu.Lock()
	callers := map[string]bool{}
	for k := range r.hits {
		_, c, _ := strings.Cut(k, " ")
		callers[c] = false
	}
	r.mu.Unlock()
	for c := range callers {
		callers[c] = gone(c)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for k := range r.hits {
		if _, c, _ := strings.Cut(k, " "); callers[c] {
			delete(r.hits, k)
		}
	}
}
