package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/DavidGodefroid/lea/internal/limits"
	"github.com/DavidGodefroid/lea/internal/roles"
)

// Live limits (#420, #404): the user tightens or loosens them on a running lea (lea limits set,
// the deck's settings screen) and the daemon applies a change at once, with no restart; running
// agents are never stopped by one. limits_set is host only and writes the user configuration
// (LimitStore); every agent reads them (limits_get), none sets them: an agent must not raise its
// own budget.

// LimitValue is a live key's value and where it comes from: "default", "file" (the user
// configuration) or "environment".
type LimitValue struct{ Value, Source string }

// LimitStore is where the live limits persist, by configuration key (LEA_MAX_RUNNING): the
// user configuration on the host (cmd/lea), a map in tests.
type LimitStore interface {
	// Load is each key's value and source as the files and the environment set it now.
	Load() (map[string]LimitValue, error)
	// Save writes value for key in the user configuration; Reset removes it from there.
	Save(key, value string) error
	Reset(key string) error
}

type limitVal struct{ value, source string }

// SetLimits gives the daemon where its live limits persist and applies what it holds (nil:
// lea's defaults, and limits_set is refused).
func (d *Daemon) SetLimits(st LimitStore) error {
	var vals map[string]LimitValue
	var err error
	if st != nil {
		vals, err = st.Load()
	}
	d.mu.Lock()
	d.limitStore = st
	d.applyLimitsLocked(vals)
	d.mu.Unlock()
	return err
}

// applyLimitsLocked takes each live key's value from vals; a missing or invalid one is the
// default's. Callers hold d.mu.
func (d *Daemon) applyLimitsLocked(vals map[string]LimitValue) {
	m := make(map[string]limitVal, len(limits.Defs))
	for _, def := range limits.Defs {
		v, ok := vals[def.Env]
		switch {
		case !ok || v.Value == "":
			m[def.Name] = limitVal{def.Default, "default"}
		case def.Check(v.Value) != nil:
			log.Printf("lea daemon: %s: %v: the default %s applies", def.Env, def.Check(v.Value), def.Default)
			m[def.Name] = limitVal{def.Default, "default"}
		default:
			m[def.Name] = limitVal{v.Value, v.Source}
		}
	}
	d.limitVals = m
}

// limitLocked is the live key's value (the default's before SetLimits). Callers hold d.mu.
func (d *Daemon) limitLocked(def limits.Def) string {
	if v, ok := d.limitVals[def.Name]; ok {
		return v.value
	}
	return def.Default
}

// maxRunningLocked is the max_running cap, 0 for none. Callers hold d.mu.
func (d *Daemon) maxRunningLocked() int {
	def, _ := limits.Lookup("max_running")
	return def.Int(d.limitLocked(def))
}

// childrenLocked counts what max_running caps: the children of leads that run, host-wide (not
// done or stopped; a lead and the user's own sessions are not counted), and the ones whose start
// passed the cap and is in flight. Callers hold d.mu.
func (d *Daemon) childrenLocked() int {
	n := d.childStarts
	as, err := d.st.Agents()
	if err != nil {
		log.Printf("lea daemon: max_running: %v", err)
		return n
	}
	for _, a := range as {
		if a.Parent != "" && a.Profile != "" && a.Role != roles.Lead && a.State != "done" && a.State != "stopped" {
			n++
		}
	}
	return n
}

// capLocked is nil when one more child may start under max_running, else the refusal. Callers
// hold d.mu.
func (d *Daemon) capLocked() error {
	max := d.maxRunningLocked()
	if max == 0 {
		return nil
	}
	if n := d.childrenLocked(); n >= max {
		return fmt.Errorf("%d children already running (limit max_running %d): retry with queue: true or wait for one to end", n, max)
	}
	return nil
}

// capReasonLocked is why a queued spawn waits, as the board and agents_list show it: "queued:
// max_running 3/3", "queued: under pressure", or "queued: starting" when nothing holds it any
// more. Callers hold d.mu.
func (d *Daemon) capReasonLocked(container string) string {
	if max := d.maxRunningLocked(); max > 0 {
		if n := d.childrenLocked(); n >= max {
			return fmt.Sprintf("queued: max_running %d/%d", n, max)
		}
	}
	if why := d.budgetWhyLocked(); why != "" {
		return "queued: " + why
	}
	if why := d.overLocked(container); why != "" {
		return "queued: under pressure (" + why + ")"
	}
	if d.resumesQueuedLocked(container) > 0 {
		return "queued: after the resumes"
	}
	return "queued: starting"
}

// admitChildLocked is nil when a child may start in container now: under the max_running cap,
// then admission (the pressure, the resumes that wait). Nil reserves the start (childStarts):
// the caller ends it with endChildStart. Callers hold d.mu.
func (d *Daemon) admitChildLocked(container string) error {
	if err := d.capLocked(); err != nil {
		return err
	}
	if err := d.budgetLocked(); err != nil {
		return err
	}
	if err := d.admitLocked(container); err != nil {
		return err
	}
	d.childStarts++
	return nil
}

// endChildStart ends a start admitChildLocked reserved: the agent now runs (and counts as such)
// or did not start.
func (d *Daemon) endChildStart() {
	d.mu.Lock()
	d.childStarts--
	d.mu.Unlock()
}

type limitView struct {
	Key       string `json:"key"`
	Env       string `json:"env"`
	Value     string `json:"value"`
	Default   string `json:"default"`
	Source    string `json:"source"`
	Help      string `json:"help"`
	AppliesTo string `json:"applies_to"`
	// InUse is how much of the limit is taken now (max_running: the children running;
	// budget_5h_pct: the account's 5-hour usage, when known).
	InUse *int `json:"in_use,omitempty"`
}

// limitsGet is limits_get: every live key with its value, default and source; for the host and
// for every agent (read-only).
func (d *Daemon) limitsGet() (any, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]limitView, 0, len(limits.Defs))
	for _, def := range limits.Defs {
		out = append(out, d.limitViewLocked(def))
	}
	// The account's usage (#421), read-only; none while unknown.
	usage := d.usageViewLocked()
	if usage == nil {
		return map[string]any{"limits": out}, nil
	}
	return map[string]any{"limits": out, "usage": usage}, nil
}

func (d *Daemon) limitViewLocked(def limits.Def) limitView {
	v, ok := d.limitVals[def.Name]
	if !ok {
		v = limitVal{def.Default, "default"}
	}
	lv := limitView{Key: def.Name, Env: def.Env, Value: v.value, Default: def.Default, Source: v.source, Help: def.Help, AppliesTo: def.AppliesTo}
	if def.Name == "max_running" {
		n := d.childrenLocked()
		lv.InUse = &n
	}
	if w, ok := d.usageLocked("5h"); ok && def.Name == "budget_5h_pct" {
		lv.InUse = &w.pct
	}
	return lv
}

// limitsSet is limits_set (host only): it checks the value, writes it to the user configuration
// (reset: removes it from there), and applies what the configuration now says at once. A raised
// max_running starts the queued spawns it lets in (one per pressure sample, the first now); a
// lowered one stops no agent: it holds new spawns until enough children end.
func (d *Daemon) limitsSet(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Key   string `json:"key"`
		Value string `json:"value"`
		Reset bool   `json:"reset"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, errors.New("limits_set: invalid parameters")
	}
	def, err := limits.Lookup(p.Key)
	if err != nil {
		return nil, err
	}
	if !p.Reset {
		if err := def.Check(p.Value); err != nil {
			return nil, err
		}
	}
	d.mu.Lock()
	st := d.limitStore
	d.mu.Unlock()
	if st == nil {
		return nil, errors.New("limits_set: this daemon has no user configuration to write: lea's defaults apply")
	}
	// Written first: a value the configuration refuses is never applied.
	if p.Reset {
		err = st.Reset(def.Env)
	} else {
		err = st.Save(def.Env, p.Value)
	}
	if err != nil {
		return nil, err
	}
	vals, err := st.Load()
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.applyLimitsLocked(vals)
	v := d.limitViewLocked(def)
	d.mu.Unlock()
	d.changed()
	// A lowered budget notes the leads when usage is now above it; a raised one drains the queue.
	d.budgetEval(d.lifetime(ctx))
	d.startQueued(d.lifetime(ctx))
	return v, nil
}
