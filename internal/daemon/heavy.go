package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"sort"
	"time"
)

// Heavy jobs (#132): test suites, static analysis, builds and browsers size themselves from the
// host's cores, so a dozen agents running them at once took every core of the host. The daemon
// keeps heavySlots slots per container; lea heavy waits for one, in order, and runs its command
// on heavyWorkers CPUs. A holder renews its slot while its command runs; one that stops
// (killed, its container gone) loses it after heavyTTL.

const (
	// DefaultHeavyWorkers is how many workers (CPUs) one heavy job gets (LEA_HEAVY_WORKERS).
	DefaultHeavyWorkers = 4
	// heavyRenew is how often lea heavy renews its slot; heavyTTL, how long a slot lives without.
	heavyRenew      = time.Minute
	defaultHeavyTTL = 3 * heavyRenew
	maxHeavyWait    = 600
	maxHeavyLabel   = 80
	maxHeavyQueue   = 64
	// defaultHeavyMaxHold caps how long one job holds a slot, renewals included: an agent cannot
	// keep one for ever from the others of its container.
	defaultHeavyMaxHold = 2 * time.Hour
	heavyPollRecheck    = 5 * time.Second
)

// DefaultHeavySlots is the slots of a host with cpus CPUs: three quarters of them, workers each,
// at least one.
func DefaultHeavySlots(cpus, workers int) int {
	if workers < 1 {
		workers = DefaultHeavyWorkers
	}
	return max(1, cpus*3/4/workers)
}

type heavyHolder struct {
	id      int64
	agent   string // container/name
	label   string
	slot    int
	expires time.Time
	since   time.Time
}

type heavyWaiter struct {
	agent, label string
	got          chan *heavyHolder // buffered: the grant is sent under d.mu
}

// heavyPool is one container's slots: holders[i] holds slot i (nil: free), queue waits in order.
type heavyPool struct {
	container string
	holders   []*heavyHolder
	queue     []*heavyWaiter
}

// heavyGrant is heavy_acquire's answer: a slot, or (Granted false) who holds them and how many
// jobs wait, this one included.
type heavyGrant struct {
	Granted bool     `json:"granted"`
	ID      int64    `json:"id,omitempty"`
	Slot    int      `json:"slot"`
	Slots   int      `json:"slots"`
	Workers int      `json:"workers"`
	Queued  int      `json:"queued,omitempty"`
	Busy    []string `json:"busy,omitempty"`
}

// SetHeavy sets the slots of each container and the workers of one job (LEA_HEAVY_SLOTS,
// LEA_HEAVY_WORKERS); 0 slots: three quarters of the host's CPUs, workers each.
func (d *Daemon) SetHeavy(slots, workers int) {
	if workers < 1 {
		workers = DefaultHeavyWorkers
	}
	if slots < 1 {
		slots = DefaultHeavySlots(runtime.NumCPU(), workers)
	}
	d.mu.Lock()
	d.heavySlots, d.heavyWorkers = slots, workers
	d.mu.Unlock()
}

// poolLocked is the container's pool, sized to the current setting. Callers hold d.mu.
func (d *Daemon) poolLocked(container string) *heavyPool {
	p := d.heavy[container]
	if p == nil {
		p = &heavyPool{container: container}
		d.heavy[container] = p
	}
	for len(p.holders) < d.heavySlots {
		p.holders = append(p.holders, nil)
	}
	return p
}

// tidyLocked frees the expired slots and gives the free ones to the jobs that wait, in order,
// while fewer jobs run than the pressure allows (heavySlotsLocked). Callers hold d.mu.
func (d *Daemon) tidyLocked(p *heavyPool) {
	now := time.Now()
	busy := 0
	for i, h := range p.holders {
		if h != nil && now.After(h.expires) {
			p.holders[i] = nil
		}
		if p.holders[i] != nil {
			busy++
		}
	}
	allowed := d.heavySlotsLocked(p.container)
	for i := 0; i < d.heavySlots && i < len(p.holders) && len(p.queue) > 0 && busy < allowed; i++ {
		if p.holders[i] != nil {
			continue
		}
		w := p.queue[0]
		p.queue = p.queue[1:]
		p.holders[i] = d.holdLocked(w.agent, w.label, i)
		busy++
		w.got <- p.holders[i]
	}
}

// holdLocked makes a holder of slot i. Callers hold d.mu.
func (d *Daemon) holdLocked(agent, label string, i int) *heavyHolder {
	d.heavySeq++
	ttl := d.heavyTTL
	if ttl <= 0 {
		ttl = defaultHeavyTTL
	}
	return &heavyHolder{id: d.heavySeq, agent: agent, label: label, slot: i, expires: time.Now().Add(ttl), since: time.Now()}
}

// busyLocked names the holders in slot order: "agent label" for the host's board, the agent alone
// for an agent (heavy_acquire): a label is its agent's text, never shown to another agent. Callers
// hold d.mu.
func busyLocked(p *heavyPool, labels bool) []string {
	var b []string
	for _, h := range p.holders {
		if h == nil {
			continue
		}
		if labels {
			b = append(b, shortName(h.agent)+" "+h.label)
		} else {
			b = append(b, shortName(h.agent))
		}
	}
	return b
}

func (d *Daemon) grantOf(h *heavyHolder) heavyGrant {
	return heavyGrant{Granted: true, ID: h.id, Slot: h.slot, Slots: d.heavySlots, Workers: d.heavyWorkers}
}

// heavyAcquire waits up to timeout_s for a slot of the caller's container, behind the jobs that
// already wait; 0 answers at once.
func (d *Daemon) heavyAcquire(ctx context.Context, o Origin, me string, raw json.RawMessage) (any, error) {
	if o.Container == "" {
		return nil, errors.New("heavy_acquire: agents only")
	}
	var p struct {
		Label    string `json:"label"`
		TimeoutS int    `json:"timeout_s"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, errors.New("heavy_acquire: invalid parameters")
	}
	if p.TimeoutS < 0 || p.TimeoutS > maxHeavyWait {
		return nil, fmt.Errorf("timeout_s must be between 0 (answer at once) and %d", maxHeavyWait)
	}
	if err := checkLine("label", p.Label, maxHeavyLabel); err != nil {
		return nil, err
	}
	w := &heavyWaiter{agent: me, label: p.Label, got: make(chan *heavyHolder, 1)}
	d.mu.Lock()
	pool := d.poolLocked(o.Container)
	if len(pool.queue) >= maxHeavyQueue {
		d.mu.Unlock()
		return nil, fmt.Errorf("heavy_acquire: %d jobs already wait in this container", maxHeavyQueue)
	}
	pool.queue = append(pool.queue, w)
	d.tidyLocked(pool)
	d.mu.Unlock()
	deadline := time.NewTimer(time.Duration(p.TimeoutS) * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(heavyPollRecheck)
	defer tick.Stop()
	for {
		select {
		case h := <-w.got:
			d.changed()
			return d.grantOf(h), nil
		case <-tick.C:
			// An expired holder frees its slot only when someone looks.
			d.mu.Lock()
			d.tidyLocked(pool)
			d.mu.Unlock()
			continue
		case <-deadline.C:
		case <-ctx.Done():
		}
		break
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	queued := len(pool.queue)
	if i := slices.Index(pool.queue, w); i >= 0 {
		pool.queue = slices.Delete(pool.queue, i, i+1)
	} else if h := <-w.got; h != nil {
		// Granted meanwhile: the client is gone, or the deadline won the race.
		if ctx.Err() == nil {
			return d.grantOf(h), nil
		}
		d.freeLocked(pool, h.id)
	}
	return heavyGrant{Slots: d.heavySlots, Workers: d.heavyWorkers, Queued: queued, Busy: busyLocked(pool, false)}, nil
}

// freeLocked frees the slot held under id and gives it to the next job. Callers hold d.mu.
func (d *Daemon) freeLocked(p *heavyPool, id int64) {
	for i, h := range p.holders {
		if h != nil && h.id == id {
			p.holders[i] = nil
		}
	}
	d.tidyLocked(p)
}

// heldLocked is the caller's holder id in its container, nil when no such slot is held (expired
// or released). Another agent's slot is an error. Callers hold d.mu.
func (d *Daemon) heldLocked(o Origin, me string, id int64) (*heavyPool, *heavyHolder, error) {
	p := d.heavy[o.Container]
	if p == nil {
		return nil, nil, nil
	}
	d.tidyLocked(p)
	for _, h := range p.holders {
		if h != nil && h.id == id {
			if h.agent != me {
				return nil, nil, fmt.Errorf("heavy slot %d is not yours", id)
			}
			return p, h, nil
		}
	}
	return p, nil, nil
}

func heavyID(raw json.RawMessage) (int64, error) {
	var p struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(raw, &p); err != nil || p.ID <= 0 {
		return 0, errors.New("invalid heavy slot id")
	}
	return p.ID, nil
}

// heavyRelease frees the caller's slot; one already free (expired, released) is no error.
func (d *Daemon) heavyRelease(o Origin, me string, raw json.RawMessage) (any, error) {
	id, err := heavyID(raw)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	p, h, err := d.heldLocked(o, me, id)
	if err == nil && h != nil {
		d.freeLocked(p, id)
	}
	d.mu.Unlock()
	if err == nil {
		d.changed()
	}
	return struct{}{}, err
}

// heavyRenew keeps the caller's slot heavyTTL longer; a slot it lost is an error, so that lea
// heavy says so.
func (d *Daemon) heavyRenew(o Origin, me string, raw json.RawMessage) (any, error) {
	id, err := heavyID(raw)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, h, err := d.heldLocked(o, me, id)
	if err != nil {
		return nil, err
	}
	if h == nil {
		return nil, fmt.Errorf("heavy slot %d is gone (expired or released)", id)
	}
	ttl := d.heavyTTL
	if ttl <= 0 {
		ttl = defaultHeavyTTL
	}
	hold := d.heavyMaxHold
	if hold <= 0 {
		hold = defaultHeavyMaxHold
	}
	if time.Since(h.since) >= hold {
		d.freeLocked(d.heavy[o.Container], id)
		return nil, fmt.Errorf("heavy slot %d held for %s, the most a job may: freed for the next one", id, hold)
	}
	h.expires = time.Now().Add(ttl)
	return struct{}{}, nil
}

// heavyView is one container's heavy jobs on the board.
type heavyView struct {
	Container string   `json:"container"`
	Slots     int      `json:"slots"`
	Running   []string `json:"running"`
	Queued    int      `json:"queued"`
}

// heavyBoard is the heavy jobs of container ("" every container) that run or wait.
func (d *Daemon) heavyBoard(container string) []heavyView {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := []heavyView{}
	for c, p := range d.heavy {
		if container != "" && c != container {
			continue
		}
		d.tidyLocked(p)
		b := busyLocked(p, true)
		if len(b) == 0 && len(p.queue) == 0 {
			continue
		}
		out = append(out, heavyView{Container: c, Slots: d.heavySlotsLocked(c), Running: b, Queued: len(p.queue)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Container < out[j].Container })
	return out
}
