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
	{Name: "relay_tokens", Env: "LEA_RELAY_TOKENS", Default: "120000",
		Help:      "the context size, in tokens, at which a control tower relays a child to a fresh agent (0: above 50% of its context window)",
		AppliesTo: "the tower's next board check (agents_list relay: true); running agents are never stopped",
		Check:     count("relay_tokens", 0, 10000000)},
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
