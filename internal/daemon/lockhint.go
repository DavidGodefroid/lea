package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
)

// A lock without a socket (#343): the agent's client finds no console, and the agent asks lea
// (lock_hint) what the user must do. lea answers from what it knows of the lock, never from a
// guess: whether lox run serves the workspace (loxRun), whether up.json names the lock and
// whether the agent's container mounts it. A lock that lox run runs and the container mounts has
// a console that waits for the user's answer at its terminal (its start questions, after an idle
// session ended): the hint says to answer in lox run, never a command that restarts or recreates
// a lock.

// lockHintName is a lock's instance name, as lox run names it (internal/launcher's
// lockUpInstance).
var lockHintName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// loxRunStartHint is what the user must do when no lox run serves the workspace.
const loxRunStartHint = "lox run is not running: start lox run in the projects directory, in a terminal of its own outside lea"

// lockHint is what the user must do for the lock name, which has no socket the agent can reach:
// loxRun is the daemon's lox run state ("serving", "off", "" unknown), known whether lox run's
// up.json names the lock, mounted whether the agent's container mounts its socket volume, and
// running the locks up.json names (for an unknown name).
func lockHint(name, loxRun string, known, mounted bool, running []string) string {
	switch {
	case loxRun == "off":
		return "lock " + name + " has no console: " + loxRunStartHint
	case !known && len(running) == 0:
		return "lox run runs no lock " + name + " for this projects directory (lox add sets one up, then lox run)"
	case !known:
		return "lox run runs no lock " + name + " for this projects directory (it runs: " + strings.Join(running, ", ") + ")"
	case !mounted:
		return lockAddedHint(name)
	case loxRun == "serving":
		return lockWaitsHint(name)
	}
	// lea cannot tell whether lox run serves: both cases, the answer first.
	return lockWaitsHint(name) + "; if lox run is not running, start it in the projects directory"
}

// lockWaitsHint is the hint for a lock whose console waits for the user's answer.
func lockWaitsHint(name string) string {
	return "lock " + name + " waits for you: answer in lox run (or lox attach " + name + ")"
}

// lockAddedHint is the hint for a lock lox run runs that the container does not mount: Docker
// cannot add a mount to a running container (the deck's lockAddedHint says the same).
func lockAddedHint(name string) string {
	return "lock " + name + " added: restart lea to use it (lea stop, then lea)"
}

// lockHintCall is lock_hint: the hint for the lock the agent names, or for each lock up.json
// names when it names none ("lock": "").
func (d *Daemon) lockHintCall(ctx context.Context, o Origin, raw json.RawMessage) (any, error) {
	if o.Container == "" {
		return nil, errors.New("lock_hint: agents only")
	}
	var p struct {
		Lock string `json:"lock"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
	}
	if p.Lock != "" && !lockHintName.MatchString(p.Lock) {
		return nil, errors.New("lock must be a lock's instance name (lowercase letters, digits and -)")
	}
	loxRun := d.loxRunState()
	running := d.upLockNames()
	added := d.locksAdded(ctx, o.Container)
	names := running
	if p.Lock != "" {
		names = []string{p.Lock}
	}
	var hints []string
	if len(names) == 0 {
		if loxRun == "off" {
			hints = append(hints, loxRunStartHint)
		} else {
			hints = append(hints, "lox run runs no lock for this projects directory (lox add sets one up, then lox run)")
		}
	}
	for _, n := range names {
		known := slices.Contains(running, n)
		hints = append(hints, lockHint(n, loxRun, known, known && !slices.Contains(added, n), running))
	}
	return map[string][]string{"hints": hints}, nil
}
