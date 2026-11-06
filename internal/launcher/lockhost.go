package launcher

import (
	"regexp"
	"strconv"

	"github.com/DavidGodefroid/lea/internal/locks"
)

// The Locks lea wires are locks.Registry's: each one's console runs in its own container, started
// by lox, and lea's agents reach its socket in the lock's socket volume, and nothing else. No host
// account, no /etc/<name>/system.toml on the host: lea's image names the console account and the
// clients group with the ids of the Lock's image (image/Containerfile).

// lockSwitch is the switch of the Lock of type tool ("" for a type lea does not wire).
func lockSwitch(tool string) string {
	if lk, ok := locks.Get(tool); ok {
		return lk.Switch
	}
	return ""
}

var lockVersion = regexp.MustCompile(`^v?([0-9]+)\.([0-9]+)\.([0-9]+)$`)

// lockfileSafe reports a lockfile version of v0.1.1 or later: v0.1.0 lets a plugin escape its
// sandbox.
func lockfileSafe(v string) bool {
	m := lockVersion.FindStringSubmatch(v)
	if m == nil {
		return false
	}
	n := [3]int{}
	for i := range n {
		n[i], _ = strconv.Atoi(m[i+1])
	}
	return n[0] > 0 || n[1] > 1 || (n[1] == 1 && n[2] >= 1)
}

// lockArgs are the docker run arguments that let the session's agents reach the consoles of the
// Locks that are on (locks.Registry, in its order): the socket volume of each lock of the type
// that lox runs for the projects directory (ups, lockup.go) on /run/<name> (each of several on
// /run/<name>/<instance>, lockUpArgs), read-only, the Lock's clients group, and its profile
// directory read-only when it has one (locksql's .locksql).
// The consoles run hub-attached only, under lox: without such a lock the agents get no socket of
// the type, never a fixed volume another console could serve, and lea says to add one (`lox add
// <type>`).
func (l *Launcher) lockArgs(proj string, ups map[string][]lockUpSocket) ([]string, error) {
	var a []string
	for _, lk := range locks.Registry {
		v, up := ups[lk.Name]
		if !l.Cfg.toolOn(lk.Switch, up) {
			continue
		}
		switch {
		case !up:
			l.logf("warning: %s: no such lock yet: `lox add %s` (lea's agents get its socket once lea restarts: lea stop, then lea)", lk.Name, lk.Name)
		case len(v) == 0:
			l.logf("warning: %s: lea's agents get no %s socket (see above)", lk.Name, lk.Name)
		default:
			args := lockUpArgs(lk.Switch, lk.GID, v)
			if lk.ProfileDir != "" {
				d, err := roDir(proj+"/"+lk.ProfileDir, "the "+lk.Name+" console only writes there")
				if err != nil {
					return nil, err
				}
				args = append(args, "--mount", "type=bind,src="+d+",dst="+d+",readonly")
			}
			a = append(a, args...)
		}
	}
	return a, nil
}
