package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/DavidGodefroid/lea/internal/roles"
	"github.com/DavidGodefroid/lea/internal/store"
)

// Shedding (#132, P2): when a container stays critical (memory near its limit, memory or cpu
// stalls) for Shed.After, the daemon pauses its lowest-priority agent, its nested containers
// included (lea-agent pause), one per Shed.After; once the container and the host stay below the
// admission thresholds for Shed.Resume, it resumes them, the last paused first. A lead never is
// paused, nor an agent that holds a heavy slot (it would keep the slot from the others).

// Shed are the shedding thresholds (LEA_SHED_*), each 0 when off. MemPct is a percent of the
// container's memory limit (or of the host's memory, for the host).
type Shed struct {
	MemPct float64       `json:"mem_pct"`
	MemPSI float64       `json:"mem_psi"`
	CPUPSI float64       `json:"cpu_psi"`
	After  time.Duration `json:"after"`
	Resume time.Duration `json:"resume"`
}

// DefaultShed is the shedding when none is set.
var DefaultShed = Shed{MemPct: 95, MemPSI: 30, CPUPSI: 80, After: 30 * time.Second, Resume: 60 * time.Second}

// shedTimeout bounds one pause or resume (lea-agent walks the nested Podman).
const shedTimeout = time.Minute

// shedFile keeps, across a daemon restart, the agents lea paused (they stay frozen meanwhile).
const shedFile = "shed.json"

// critical names each shedding threshold r is above.
func (s Shed) critical(r Reading) []string {
	var out []string
	if p := r.MemPct(); s.MemPct > 0 && p >= s.MemPct {
		out = append(out, fmt.Sprintf("memory %.0f%% of %s > %.0f%%", p, gib(r.MemMax), s.MemPct))
	}
	if s.MemPSI > 0 && r.MemPSI >= s.MemPSI {
		out = append(out, fmt.Sprintf("memory PSI %.0f%% > %.0f%%", r.MemPSI, s.MemPSI))
	}
	if s.CPUPSI > 0 && r.CPUPSI >= s.CPUPSI {
		out = append(out, fmt.Sprintf("cpu PSI %.0f%% > %.0f%%", r.CPUPSI, s.CPUPSI))
	}
	return out
}

// shedState is one container's shedding: since when it is critical, since when it is calm, and
// the agents lea paused there, in order.
type shedState struct {
	critSince, calmSince time.Time
	paused               []shedEntry
}

type shedEntry struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
	At     int64  `json:"at"`
}

// SetShed sets the shedding thresholds; After 0 turns it off. It loads the agents a previous
// daemon paused, which this one resumes.
func (d *Daemon) SetShed(s Shed) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.shed = s
	b, err := os.ReadFile(filepath.Join(d.runDir, shedFile))
	if err != nil {
		return
	}
	var saved map[string][]shedEntry
	if json.Unmarshal(b, &saved) != nil {
		return
	}
	for c, es := range saved {
		if !containerName.MatchString(c) {
			continue
		}
		st := d.shedOfLocked(c)
		for _, e := range es {
			if strings.HasPrefix(e.Key, c+"/") && !slices.ContainsFunc(st.paused, func(x shedEntry) bool { return x.Key == e.Key }) {
				st.paused = append(st.paused, e)
			}
		}
	}
}

func (d *Daemon) shedOfLocked(c string) *shedState {
	if d.shedding == nil {
		d.shedding = map[string]*shedState{}
	}
	st := d.shedding[c]
	if st == nil {
		st = &shedState{}
		d.shedding[c] = st
	}
	return st
}

// saveShedLocked writes the agents lea paused. Callers hold d.mu.
func (d *Daemon) saveShedLocked() {
	saved := map[string][]shedEntry{}
	for c, st := range d.shedding {
		if len(st.paused) > 0 {
			saved[c] = st.paused
		}
	}
	p := filepath.Join(d.runDir, shedFile)
	if len(saved) == 0 {
		_ = os.Remove(p)
		return
	}
	b, _ := json.Marshal(saved)
	if err := os.MkdirAll(d.runDir, 0o700); err != nil {
		return
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err == nil {
		_ = os.Rename(tmp, p)
	}
}

// shedAction is one pause or resume the sample decided.
type shedAction struct {
	pause  bool
	key    string
	reason string
}

// shedStepLocked decides, for every sampled container, whether an agent is paused or resumed now.
// Callers hold d.mu.
func (d *Daemon) shedStepLocked(now time.Time) []shedAction {
	if d.shed.After <= 0 || d.shedBusy {
		return nil
	}
	hostCrit := d.shed.critical(d.pressHost)
	hostOver := len(d.thresholds.over(d.pressHost, true)) > 0
	var out []shedAction
	for _, c := range sortedKeys(d.pressScopes) {
		r := d.pressScopes[c]
		st := d.shedOfLocked(c)
		crit := d.shed.critical(r)
		why := ""
		if len(crit) > 0 {
			why = "container " + strings.Join(crit, ", ")
		} else if len(hostCrit) > 0 {
			why = "host " + strings.Join(hostCrit, ", ")
		}
		switch {
		case why != "":
			st.calmSince = time.Time{}
			if st.critSince.IsZero() {
				st.critSince = now
			}
			if now.Sub(st.critSince) >= d.shed.After {
				st.critSince = now // the next one after another After, once this one weighs
				if k := d.shedPickLocked(c); k != "" {
					out = append(out, shedAction{pause: true, key: k, reason: why})
				}
			}
		case !hostOver && len(d.thresholds.over(r, false)) == 0:
			st.critSince = time.Time{}
			if len(st.paused) == 0 {
				st.calmSince = time.Time{}
				continue
			}
			if st.calmSince.IsZero() {
				st.calmSince = now
			}
			if now.Sub(st.calmSince) >= d.shed.Resume {
				st.calmSince = now
				out = append(out, shedAction{key: st.paused[len(st.paused)-1].Key})
			}
		default:
			st.critSince, st.calmSince = time.Time{}, time.Time{}
		}
	}
	return out
}

// shedPickLocked is the agent of container c lea pauses first, "" when none may be: never a lead,
// a paused or ended agent, or one holding a heavy slot; idle agents first, then devs before
// reviewers, a lead's children before the user's own agents, the newest first. Callers hold d.mu.
func (d *Daemon) shedPickLocked(c string) string {
	as, err := d.st.SpawnedAgents(c)
	if err != nil {
		return ""
	}
	holds := map[string]bool{}
	if p := d.heavy[c]; p != nil {
		for _, h := range p.holders {
			if h != nil {
				holds[h.agent] = true
			}
		}
	}
	var cand []store.Agent
	for _, a := range as {
		r, err := roles.Get(a.Role)
		if err != nil || r.CanSpawn || a.Paused || holds[a.Key] || a.State == "done" || a.State == "stopped" {
			continue
		}
		cand = append(cand, a)
	}
	if len(cand) == 0 {
		return ""
	}
	rank := func(a store.Agent) [3]int {
		var r [3]int
		if a.State != "idle" {
			r[0] = 1
		}
		if a.Role != roles.Dev {
			r[1] = 1
		}
		if a.Parent == "" {
			r[2] = 1
		}
		return r
	}
	sort.SliceStable(cand, func(i, j int) bool {
		ri, rj := rank(cand[i]), rank(cand[j])
		if ri != rj {
			return slices.Compare(ri[:], rj[:]) < 0
		}
		return cand[i].Created > cand[j].Created
	})
	return cand[0].Key
}

// shedNow runs the pauses and resumes the sample decided, beside the sampler, one batch at a
// time, and tells each agent's lead.
func (d *Daemon) shedNow(acts []shedAction) {
	d.mu.Lock()
	a := d.agents
	if len(acts) == 0 || a == nil || d.shedBusy {
		d.mu.Unlock()
		return
	}
	d.shedBusy = true
	d.mu.Unlock()
	go func() {
		defer func() {
			d.mu.Lock()
			d.shedBusy = false
			d.mu.Unlock()
		}()
		for _, act := range acts {
			d.shedOne(a, act)
		}
		d.changed()
	}()
}

func (d *Daemon) shedOne(a Agents, act shedAction) {
	ctx, cancel := context.WithTimeout(context.Background(), shedTimeout)
	defer cancel()
	c, _, _ := strings.Cut(act.key, "/")
	ag, agErr := d.st.AgentByKey(act.key)
	if !act.pause {
		d.mu.Lock()
		st := d.shedOfLocked(c)
		st.paused = slices.DeleteFunc(st.paused, func(e shedEntry) bool { return e.Key == act.key })
		d.saveShedLocked()
		d.mu.Unlock()
		// Resumed by the user meanwhile, or gone: nothing to do.
		if agErr != nil || !ag.Paused || ag.State == "done" || ag.State == "stopped" {
			return
		}
		if err := a.SetPaused(ctx, act.key, false); err != nil {
			log.Printf("lea daemon: resume %s after the pressure fell: %v", act.key, err)
			return
		}
		log.Printf("lea daemon: resumed %s: the pressure fell", act.key)
		d.tellShed(ag, fmt.Sprintf("lea resumed %s: the pressure fell.", ag.Name))
		return
	}
	if agErr != nil {
		return
	}
	if err := a.SetPaused(ctx, act.key, true); err != nil {
		log.Printf("lea daemon: pause %s under pressure: %v", act.key, err)
		return
	}
	d.mu.Lock()
	st := d.shedOfLocked(c)
	st.paused = append(st.paused, shedEntry{Key: act.key, Reason: act.reason, At: d.now()})
	d.saveShedLocked()
	d.mu.Unlock()
	log.Printf("lea daemon: paused %s under pressure (%s)", act.key, act.reason)
	d.tellShed(ag, fmt.Sprintf("lea paused %s, its nested containers included, under pressure (%s); it resumes it once the pressure stays low %s.",
		ag.Name, act.reason, d.shedResume()))
}

func (d *Daemon) shedResume() time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.shed.Resume
}

// tellShed tells the agent's lead (none: the user, in the deck's row and the board).
func (d *Daemon) tellShed(a store.Agent, text string) {
	if a.Parent != "" {
		d.post("user", a.Parent, "note", text, 0)
	}
}

// shedLabelLocked is why lea paused key, for the board, "" when it did not. Callers hold d.mu.
func (d *Daemon) shedLabelLocked(key string) string {
	c, _, _ := strings.Cut(key, "/")
	st := d.shedding[c]
	if st == nil {
		return ""
	}
	for _, e := range st.paused {
		if e.Key == key {
			return "paused by lea at " + time.Unix(e.At, 0).Format("15:04") + " under pressure (" + e.Reason + ")"
		}
	}
	return ""
}
