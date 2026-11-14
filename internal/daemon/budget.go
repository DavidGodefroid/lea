package daemon

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/DavidGodefroid/lea/internal/agents"
	"github.com/DavidGodefroid/lea/internal/limits"
	"github.com/DavidGodefroid/lea/internal/roles"
)

// Usage budget (#421, #404): the account's 5-hour and 7-day usage, as the agents' status lines
// report it (subscription accounts only), shows on the deck and in limits_get; above the live
// limit budget_5h_pct, new children of leads are held like at the max_running cap (refused, or
// queued with queue: true). Running agents are never stopped or paused. Unknown usage (no report
// yet, an API account, a window that ended) never holds a spawn.

// windowSlack is how far two reports' reset times of one window may differ: the reset time is
// the provider's, read at each report, and may move by a few seconds.
const windowSlack = 120

// windowSpan is each window's length: a report whose reset time lies beyond it (or in the past)
// is not believed. The status reports are the agents' own, untrusted: this bounds how long a
// made-up report can hold or free spawns.
var windowSpan = map[string]int64{"5h": 5 * 3600, "7d": 7 * 24 * 3600}

// acctWindow is the account's latest usage of one window: its used share and when it resets (0:
// unknown).
type acctWindow struct {
	pct      int
	resetsAt int64
}

// newerWindow reports whether report r is newer than the kept window k: a later window (a later
// reset), or the same window with as much usage or more (usage only grows within a window). A
// report from an earlier window, or with less usage in the same one, is an older status line
// (an agent idle since) and is not kept. A report without a reset time replaces only a kept window
// without one.
func newerWindow(k, r acctWindow) bool {
	switch {
	case k.resetsAt == 0:
		return true
	case r.resetsAt == 0:
		return false
	case r.resetsAt > k.resetsAt+windowSlack:
		return true
	case r.resetsAt < k.resetsAt-windowSlack:
		return false
	}
	return r.pct >= k.pct
}

// recordAccount keeps the account's usage of each window ws reports ("5h", "7d") when it is
// newer than what lea holds, and its reset time within the window's span from now; it reports
// whether anything changed.
func (d *Daemon) recordAccount(ws map[string]acctWindow) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.account == nil {
		d.account = map[string]acctWindow{}
	}
	now := d.now()
	changed := false
	for label, r := range ws {
		if r.resetsAt != 0 && (r.resetsAt <= now || r.resetsAt > now+windowSpan[label]+windowSlack) {
			continue
		}
		k, ok := d.account[label]
		if ok && k.resetsAt > 0 && now >= k.resetsAt {
			ok = false // that window reset: any report is newer
		}
		if ok && (k == r || !newerWindow(k, r)) {
			continue
		}
		d.account[label] = r
		changed = true
	}
	return changed
}

// usageLocked is the account's usage of window label, ok false when unknown: no report yet, or
// the window it reported has reset since. Callers hold d.mu.
func (d *Daemon) usageLocked(label string) (acctWindow, bool) {
	w, ok := d.account[label]
	if !ok || (w.resetsAt > 0 && d.now() >= w.resetsAt) {
		return acctWindow{}, false
	}
	return w, true
}

// budget5hLocked is the budget_5h_pct value, 0 when off. Callers hold d.mu.
func (d *Daemon) budget5hLocked() int {
	def, _ := limits.Lookup("budget_5h_pct")
	return def.Int(d.limitLocked(def))
}

// budgetWhyLocked is why the budget holds new spawns ("5h usage 64 % ≥ budget_5h_pct 60"), ""
// when it does not: off, unknown usage, or usage below it. Callers hold d.mu.
func (d *Daemon) budgetWhyLocked() string {
	b := d.budget5hLocked()
	if b == 0 {
		return ""
	}
	w, ok := d.usageLocked("5h")
	if !ok || w.pct < b {
		return ""
	}
	return fmt.Sprintf("5h usage %d %% ≥ budget_5h_pct %d", w.pct, b)
}

// budgetLocked is nil when the budget lets one more child start, else the refusal. Callers hold
// d.mu.
func (d *Daemon) budgetLocked() error {
	why := d.budgetWhyLocked()
	if why == "" {
		return nil
	}
	when := ""
	if w, _ := d.usageLocked("5h"); w.resetsAt > 0 {
		when = " at " + time.Unix(w.resetsAt, 0).Format("15:04")
	}
	return fmt.Errorf("%s: retry with queue: true to start it once the 5-hour window resets%s or the user raises the budget", why, when)
}

// budgetEval follows the budget's crossings: above it, each running lead gets one note (not one
// per spawn) and the deck turns amber; below it again (usage fell, the window reset, the user
// raised or turned off the budget), the queued spawns start. It stops no agent.
func (d *Daemon) budgetEval(ctx context.Context) {
	d.mu.Lock()
	why := d.budgetWhyLocked()
	over := why != ""
	was := d.budgetOver
	d.budgetOver = over
	d.mu.Unlock()
	switch {
	case over && !was:
		as, err := d.st.Agents()
		if err != nil {
			log.Printf("lea daemon: budget_5h_pct: %v", err)
		}
		for _, a := range as {
			if a.Role == roles.Lead && a.State != "done" && a.State != "stopped" {
				d.post("user", a.Key, "note", "lea: "+why+": new spawns are held (agents_spawn with queue: true waits) until the 5-hour window resets or the user raises the budget (lea limits set budget_5h_pct N). Running agents go on.", 0)
			}
		}
		d.changed()
	case !over && was:
		d.changed()
		d.startQueued(ctx)
	}
}

// usageView is the account's usage as the deck and limits_get show it, nil when no window is
// known.
func (d *Daemon) usageView() *agents.UsageView {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.usageViewLocked()
}

// usageViewLocked is usageView. Callers hold d.mu.
func (d *Daemon) usageViewLocked() *agents.UsageView {
	var v agents.UsageView
	if w, ok := d.usageLocked("5h"); ok {
		v.FiveHour = &agents.UsageWindow{Pct: w.pct, ResetsAt: w.resetsAt}
	}
	if w, ok := d.usageLocked("7d"); ok {
		v.SevenDay = &agents.UsageWindow{Pct: w.pct, ResetsAt: w.resetsAt}
	}
	if v.FiveHour == nil && v.SevenDay == nil {
		return nil
	}
	v.Budget = d.budget5hLocked()
	v.Over = d.budgetWhyLocked() != ""
	return &v
}
