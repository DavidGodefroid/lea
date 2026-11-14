// Package limits is the catalog of lea's live limits (#420, #404): typed keys the user changes on
// a running lea (lea limits set, the deck's settings screen) and that the daemon applies at once,
// without a restart. Each key is also a key of the user configuration (Env), where the daemon
// persists a change; the environment stays a fallback, as for any unprotected key. The daemon
// stamps leave every live key out: changing one never restarts the daemon.
package limits

import (
	"fmt"
	"strconv"
	"strings"
)

// Def is one live key.
type Def struct {
	// Name is the key lea limits and the daemon methods take (max_running).
	Name string
	// Env is its key in the user configuration and the environment (LEA_MAX_RUNNING).
	Env string
	// Default is its value when nothing sets it.
	Default string
	// Help says what it limits, AppliesTo when and to what a change applies.
	Help, AppliesTo string
	// Check refuses a value of the wrong type or out of range; the error never echoes more than
	// the value.
	Check func(string) error
}

// Defs are the live keys, in the order lea limits shows them. Each issue of #404 adds its own.
var Defs = []Def{
	{Name: "max_running", Env: "LEA_MAX_RUNNING", Default: "3",
		Help:      "the most children of leads that run at once, host-wide (0: no cap); your own sessions and the leads do not count",
		AppliesTo: "new spawns: a spawn at the cap waits (queue: true) or is refused; running agents are never stopped",
		Check:     count("max_running", 0, 100)},
	{Name: "budget_usd_agent", Env: "LEA_BUDGET_USD_AGENT", Default: "0",
		Help:      "the most one agent may spend, in USD, as its status line reports it (0: off); an agent of unknown cost is never paused",
		AppliesTo: "running agents: one over it is paused (kept, not stopped) until you raise it or resume it",
		Check:     dollars("budget_usd_agent")},
	{Name: "budget_usd_total", Env: "LEA_BUDGET_USD_TOTAL", Default: "0",
		Help:      "the most all agents may spend together, in USD, since the last lea limits reset budget_usd_total (0: off)",
		AppliesTo: "running agents and new spawns: over it every agent is paused and spawns wait (queue: true) or are refused; reset starts a new period",
		Check:     dollars("budget_usd_total")},
	{Name: "budget_usd_step", Env: "LEA_BUDGET_USD_STEP", Default: "20",
		Help:      "how much more an agent you resume over a budget may spend before lea pauses it again, in percent of that budget",
		AppliesTo: "agents you resume over a budget",
		Check:     count("budget_usd_step", 1, 100)},
}

// Lookup is the live key named name (max_running) or its configuration key (LEA_MAX_RUNNING).
func Lookup(name string) (Def, error) {
	for _, d := range Defs {
		if name == d.Name || name == d.Env {
			return d, nil
		}
	}
	names := make([]string, len(Defs))
	for i, d := range Defs {
		names[i] = d.Name
	}
	if len(name) > 64 {
		name = name[:64] + "…"
	}
	return Def{}, fmt.Errorf("%q is not a live limit (one of: %s)", name, strings.Join(names, ", "))
}

// ByEnv is the live key whose configuration key is env, ok false when none.
func ByEnv(env string) (Def, bool) {
	for _, d := range Defs {
		if d.Env == env {
			return d, true
		}
	}
	return Def{}, false
}

// Int is a checked integer value; a value Check would refuse gives the default's.
func (d Def) Int(v string) int {
	if d.Check(v) != nil {
		v = d.Default
	}
	n, _ := strconv.Atoi(v)
	return n
}

// USD is a checked amount in dollars; a value Check would refuse gives the default's.
func (d Def) USD(v string) float64 {
	if d.Check(v) != nil {
		v = d.Default
	}
	f, _ := strconv.ParseFloat(v, 64)
	return f
}

// dollars checks an amount in USD from 0 to 1e6, with at most two decimals ("10", "12.5").
func dollars(name string) func(string) error {
	return func(v string) error {
		whole, frac, dot := strings.Cut(v, ".")
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || whole == "" || strings.Trim(whole, "0123456789") != "" || (len(whole) > 1 && whole[0] == '0') ||
			dot && (frac == "" || len(frac) > 2 || strings.Trim(frac, "0123456789") != "") || f > 1e6 {
			if len(v) > 32 {
				v = v[:32] + "…"
			}
			return fmt.Errorf("%s is an amount in USD from 0 to 1000000, at most two decimals, not %q", name, v)
		}
		return nil
	}
}

// count checks a whole number from lo to hi, written without sign or leading zeros.
func count(name string, lo, hi int) func(string) error {
	return func(v string) error {
		n, err := strconv.Atoi(v)
		if err != nil || v == "" || strings.Trim(v, "0123456789") != "" || (len(v) > 1 && v[0] == '0') || n < lo || n > hi {
			if len(v) > 32 {
				v = v[:32] + "…"
			}
			return fmt.Errorf("%s is a whole number from %d to %d, not %q", name, lo, hi, v)
		}
		return nil
	}
}
