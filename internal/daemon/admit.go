package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/DavidGodefroid/lea/internal/agents"
	"github.com/DavidGodefroid/lea/internal/roles"
	"github.com/DavidGodefroid/lea/internal/store"
)

// Admission (#132, P2): above a threshold, agents_spawn is refused with the reason, or queued
// (queue: true) and started once the pressure falls; heavy slots are halved meanwhile.

// PressureSource samples the host and the given lea containers (HostPressure; tests: a fake).
type PressureSource interface {
	Sample(ctx context.Context, containers []string) (host Reading, scopes map[string]Reading)
}

const (
	// maxQueuedSpawns caps the spawns one lead queues; queuedSpawnTTL is how long one waits.
	maxQueuedSpawns = 4
	queuedSpawnTTL  = 30 * time.Minute
	// queuedStart bounds the start of a queued agent (a.New), as the MCP tool's own wait.
	queuedStart = 2 * time.Minute
)

// queuedSpawn is a spawn waiting for the pressure to fall.
type queuedSpawn struct {
	id   int64
	lead string
	nr   agents.NewRequest
	req  *store.SpawnRequest
	at   time.Time
}

// SetPressure sets what samples the pressure and the admission thresholds; nil src: no
// admission (every spawn is let through, the heavy slots stay whole).
func (d *Daemon) SetPressure(src PressureSource, t Thresholds) {
	d.mu.Lock()
	d.pressSrc, d.thresholds = src, t
	d.mu.Unlock()
}

// RunPressure samples every pressureEvery until ctx ends.
func (d *Daemon) RunPressure(ctx context.Context) {
	t := time.NewTicker(pressureEvery)
	defer t.Stop()
	for {
		d.samplePressure(ctx)
		select {
		case <-t.C:
		case <-ctx.Done():
			return
		}
	}
}

// samplePressure takes one sample of the host and of every attached container, gives the heavy
// slots back when the pressure fell and starts the queued spawns it now lets through.
func (d *Daemon) samplePressure(ctx context.Context) {
	d.mu.Lock()
	src := d.pressSrc
	cs := make([]string, 0, len(d.attached))
	for c := range d.attached {
		cs = append(cs, c)
	}
	d.mu.Unlock()
	if src == nil {
		return
	}
	host, scopes := src.Sample(ctx, cs)
	d.mu.Lock()
	before := d.gaugesLocked()
	now := d.pnow()
	d.pressHost, d.pressScopes, d.pressAt = host, scopes, now
	for _, p := range d.heavy {
		d.tidyLocked(p)
	}
	after := d.gaugesLocked()
	acts := d.shedStepLocked(now)
	d.mu.Unlock()
	d.shedNow(acts)
	d.startQueued(ctx)
	if fmt.Sprint(before) != fmt.Sprint(after) {
		d.changed()
	}
}

// pnow is the pressure's clock (a test seam: pressClock).
func (d *Daemon) pnow() time.Time {
	if d.pressClock != nil {
		return d.pressClock()
	}
	return time.Now()
}

// overLocked is why the container (and the host) are under pressure, "" when they are not or no
// recent sample says so. Callers hold d.mu.
func (d *Daemon) overLocked(container string) string {
	if d.pressSrc == nil || d.pressAt.IsZero() || d.pnow().Sub(d.pressAt) > pressureStale {
		return ""
	}
	var parts []string
	if r, ok := d.pressScopes[container]; ok {
		if o := d.thresholds.over(r, false); len(o) > 0 {
			parts = append(parts, "container "+strings.Join(o, ", "))
		}
	}
	if o := d.thresholds.over(d.pressHost, true); len(o) > 0 {
		parts = append(parts, "host "+strings.Join(o, ", "))
	}
	return strings.Join(parts, "; ")
}

// admitLocked is nil when a new agent may start in container, else the refusal with its reason
// and what runs. Callers hold d.mu.
func (d *Daemon) admitLocked(container string) error {
	why := d.overLocked(container)
	if why == "" {
		// The resume lane goes first: a spawn does not start before the resumes that wait.
		if n := d.resumesQueuedLocked(container); n > 0 {
			return fmt.Errorf("%d resumes wait to start in %s: retry later, or pass queue: true to start it after them", n, container)
		}
		return nil
	}
	busy := 0
	if p := d.heavy[container]; p != nil {
		busy = len(busyLocked(p, false))
	}
	n := 0
	if as, err := d.st.SpawnedAgents(container); err == nil {
		for _, a := range as {
			if a.State != "done" && a.State != "stopped" {
				n++
			}
		}
	}
	return fmt.Errorf("under pressure (%s); %d agents run, %d heavy slots busy: retry later, stop an agent, or pass queue: true to start it once the pressure falls", why, n, busy)
}

// heavySlotsLocked is how many heavy jobs may run in container now: half the slots, at least one,
// while it or the host is under pressure. Callers hold d.mu.
func (d *Daemon) heavySlotsLocked(container string) int {
	if d.overLocked(container) != "" {
		return max(1, d.heavySlots/2)
	}
	return d.heavySlots
}

// queueSpawn records a spawn to start once the pressure falls; the lead hears when it does.
func (d *Daemon) queueSpawn(lead string, nr agents.NewRequest, req *store.SpawnRequest) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, q := range d.spawnQueue {
		if q.lead == lead {
			n++
		}
	}
	if n >= maxQueuedSpawns {
		return 0, fmt.Errorf("agents_spawn: you already have %d spawns queued", maxQueuedSpawns)
	}
	d.spawnSeq++
	d.spawnQueue = append(d.spawnQueue, &queuedSpawn{id: d.spawnSeq, lead: lead, nr: nr, req: req, at: time.Now()})
	return d.spawnSeq, nil
}

// takeQueuedLocked drops the queued spawns that waited queuedSpawnTTL and takes the first one the
// pressure now lets through, nil when none, or a resume the pressure lets through before any
// spawn. Callers hold d.mu.
func (d *Daemon) takeQueuedLocked() (resume *queuedResume, start *queuedSpawn, dropped []*queuedSpawn) {
	resume = d.takeResumeLocked()
	keep := d.spawnQueue[:0]
	for _, s := range d.spawnQueue {
		switch {
		case time.Since(s.at) > queuedSpawnTTL:
			dropped = append(dropped, s)
		case start == nil && resume == nil && d.admitLocked(s.nr.Container) == nil:
			start = s
		default:
			keep = append(keep, s)
		}
	}
	clear(d.spawnQueue[len(keep):])
	d.spawnQueue = keep
	return resume, start, dropped
}

// startQueued starts at most one queued resume or spawn per sample, and never while another
// starts: one calm sample lets a single agent in, the next sample sees what it costs. The start
// runs beside the sampler, which keeps sampling meanwhile. Each lead hears of its own spawns.
func (d *Daemon) startQueued(ctx context.Context) {
	d.mu.Lock()
	var q *queuedSpawn
	var r *queuedResume
	var dropped []*queuedSpawn
	if !d.queueStarting {
		r, q, dropped = d.takeQueuedLocked()
	}
	a := d.agents
	if q != nil || r != nil {
		d.queueStarting = true
	}
	d.mu.Unlock()
	for _, s := range dropped {
		// A spawn request it answered stays pending: the lead may accept it again.
		d.post("user", s.lead, "result", fmt.Sprintf("[status: fail] queued spawn #%d dropped: the pressure did not fall within %s. Retry later or stop an agent.", s.id, queuedSpawnTTL), 0)
	}
	if q == nil && r == nil {
		return
	}
	go func() {
		defer func() {
			d.mu.Lock()
			d.queueStarting = false
			d.mu.Unlock()
		}()
		if r != nil {
			d.startResume(a, r)
			return
		}
		d.startOne(ctx, a, q)
	}()
}

// startOne starts a queued spawn its lead may still ask for.
func (d *Daemon) startOne(ctx context.Context, a Agents, q *queuedSpawn) {
	if a == nil {
		return
	}
	// The lead may have ended or changed meanwhile: it must still be allowed this spawn.
	if err := d.mayStillSpawn(q); err != nil {
		log.Printf("lea daemon: queued spawn #%d of %s dropped: %v", q.id, q.lead, err)
		return
	}
	sctx, cancel := context.WithTimeout(ctx, queuedStart)
	row, err := d.startSpawn(sctx, a, q.lead, q.nr, q.req)
	cancel()
	if err != nil {
		log.Printf("lea daemon: queued spawn #%d of %s: %v", q.id, q.lead, err)
		d.post("user", q.lead, "result", fmt.Sprintf("[status: fail] queued spawn #%d did not start: %v", q.id, err), 0)
		return
	}
	d.post("user", q.lead, "result", fmt.Sprintf("[status: ok] queued spawn #%d started: %s (role %s).", q.id, row.Name, row.Role), 0)
	d.changed()
}

// mayStillSpawn checks again, when a queued spawn starts, what agentsSpawn checked when it was
// queued: the lead still runs in that container and may give that role.
func (d *Daemon) mayStillSpawn(q *queuedSpawn) error {
	caller, err := d.spawnedCaller(Origin{Container: q.nr.Container}, q.lead, "agents_spawn")
	if err != nil {
		return err
	}
	if caller.Container != q.nr.Container {
		return errors.New("the lead is in another container")
	}
	cr, err := roles.Get(caller.Role)
	if err != nil {
		return err
	}
	return roles.CanGive(cr, q.nr.Role)
}

// Gauge is one scope's pressure as the deck and the board show it.
type Gauge struct {
	Scope   string  `json:"scope"` // "host" or the container
	Level   string  `json:"level"` // ok, warn (near a threshold), over
	CPUUsed float64 `json:"cpu_used"`
	CPUs    float64 `json:"cpus"`
	CPUPSI  float64 `json:"cpu_psi"`
	MemUsed int64   `json:"mem_used"`
	MemMax  int64   `json:"mem_max"`
	MemPSI  float64 `json:"mem_psi"`
	Load1   float64 `json:"load1,omitempty"`
	Heavy   int     `json:"heavy"`
	Slots   int     `json:"slots"`
	Queued  int     `json:"queued"`
	Spawns  int     `json:"spawns_queued,omitempty"`
	Resumes int     `json:"resumes_queued,omitempty"`
	Over    string  `json:"over,omitempty"`
}

// Line is the gauge on one line: "CPU 14.8/16 · PSI 22% · mem 18.1/24.0 GiB · heavy 3/4 (+2 queued)".
func (g Gauge) Line() string {
	var p []string
	cpus := "∞"
	if g.CPUs > 0 {
		cpus = strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%.1f", g.CPUs), "0"), ".")
	}
	if g.CPUUsed >= 0 {
		p = append(p, fmt.Sprintf("CPU %.1f/%s", g.CPUUsed, cpus))
	}
	if g.CPUPSI >= 0 {
		p = append(p, fmt.Sprintf("PSI %.0f%%", g.CPUPSI))
	}
	if g.MemUsed >= 0 && g.MemMax > 0 {
		p = append(p, fmt.Sprintf("mem %.1f/%s", float64(g.MemUsed)/(1<<30), gib(g.MemMax)))
	}
	if g.Scope == "host" && g.Load1 >= 0 {
		p = append(p, fmt.Sprintf("load %.1f", g.Load1))
	}
	if g.Scope != "host" {
		h := fmt.Sprintf("heavy %d/%d", g.Heavy, g.Slots)
		if g.Queued > 0 {
			h += fmt.Sprintf(" (+%d queued)", g.Queued)
		}
		p = append(p, h)
		if g.Resumes > 0 {
			p = append(p, fmt.Sprintf("%d resumes queued", g.Resumes))
		}
		if g.Spawns > 0 {
			p = append(p, fmt.Sprintf("%d spawns queued", g.Spawns))
		}
	}
	return strings.Join(p, " · ")
}

// Short is the gauge in a deck's width: "cpu 15.8/16 mem 18.1/24G heavy 2/2".
func (g Gauge) Short() string {
	var p []string
	if g.CPUUsed >= 0 {
		c := fmt.Sprintf("cpu %.1f", g.CPUUsed)
		if g.CPUs > 0 {
			c += "/" + strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%.1f", g.CPUs), "0"), ".")
		}
		p = append(p, c)
	}
	if g.MemUsed >= 0 && g.MemMax > 0 {
		p = append(p, fmt.Sprintf("mem %.1f/%.0fG", float64(g.MemUsed)/(1<<30), float64(g.MemMax)/(1<<30)))
	}
	if g.Scope != "host" {
		p = append(p, fmt.Sprintf("heavy %d/%d", g.Heavy, g.Slots))
	}
	return strings.Join(p, " ")
}

// deckPressure is the container's gauge for its deck, at the worse level of it and the host.
func (d *Daemon) deckPressure(container string) *agents.PressureView {
	var host, mine *Gauge
	gs := d.gauges(container)
	for i := range gs {
		if gs[i].Scope == "host" {
			host = &gs[i]
		} else {
			mine = &gs[i]
		}
	}
	if mine == nil {
		return nil
	}
	v := &agents.PressureView{Short: mine.Short(), Level: mine.Level, Over: mine.Over}
	rank := map[string]int{"ok": 0, "warn": 1, "over": 2}
	if host != nil && rank[host.Level] > rank[v.Level] {
		v.Level = host.Level
	}
	if host != nil && host.Over != "" {
		v.Over = strings.TrimPrefix(v.Over+"; host "+host.Over, "; ")
	}
	return v
}

func (d *Daemon) gaugeOfLocked(scope string, r Reading, host bool) Gauge {
	g := Gauge{Scope: scope, Level: "ok", CPUUsed: r.CPUUsed, CPUs: r.CPUs, CPUPSI: r.CPUPSI,
		MemUsed: r.MemUsed, MemMax: r.MemMax, MemPSI: r.MemPSI, Load1: r.Load1}
	if o := d.thresholds.over(r, host); len(o) > 0 {
		g.Level, g.Over = "over", strings.Join(o, ", ")
	} else if d.thresholds.near(r, host) {
		g.Level = "warn"
	}
	if !host {
		g.Slots = d.heavySlotsLocked(scope)
		if p := d.heavy[scope]; p != nil {
			g.Heavy, g.Queued = len(busyLocked(p, false)), len(p.queue)
		}
		for _, q := range d.spawnQueue {
			if q.nr.Container == scope {
				g.Spawns++
			}
		}
		g.Resumes = d.resumesQueuedLocked(scope)
	}
	return g
}

// gaugesLocked is the host's gauge, then each sampled container's; nil without a recent sample.
// Callers hold d.mu.
func (d *Daemon) gaugesLocked() []Gauge {
	if d.pressSrc == nil || d.pressAt.IsZero() || d.pnow().Sub(d.pressAt) > pressureStale {
		return nil
	}
	out := []Gauge{d.gaugeOfLocked("host", d.pressHost, true)}
	for _, c := range sortedKeys(d.pressScopes) {
		out = append(out, d.gaugeOfLocked(c, d.pressScopes[c], false))
	}
	return out
}

// gauges is gaugesLocked for container ("" every container).
func (d *Daemon) gauges(container string) []Gauge {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []Gauge
	for _, g := range d.gaugesLocked() {
		if container == "" || g.Scope == "host" || g.Scope == container {
			out = append(out, g)
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
