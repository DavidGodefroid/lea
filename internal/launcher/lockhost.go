package launcher

import (
	"regexp"
	"strconv"
)

// lockHostTool is a lock tool whose console runs in its own container, started by `lox up`: lea's
// agents reach the console's socket in the lock's socket volume, and nothing else. No host
// account, no /etc/<name>/system.toml on the host: lea's image names the console account and the
// clients group with the ids of the tool's image (image/Containerfile).
type lockHostTool struct {
	name string // lockmq, lockfile: also /run/<name>
	env  string // its switch, LEA_LOCKMQ or LEA_LOCKFILE
	gid  string // the tool's clients group in its image (and in lea's)
}

// lockToolKeys are the switches of the lock tools lea wires, by tool.
var lockToolKeys = map[string]string{"locksql": "LEA_LOCKSQL", "lockmq": "LEA_LOCKMQ", "lockfile": "LEA_LOCKFILE"}

var lockHostTools = []lockHostTool{{"lockmq", "LEA_LOCKMQ", "10010"}, {"lockfile", "LEA_LOCKFILE", "10030"}}

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

// lockHostArgs are the docker run arguments that let the session's agents reach the consoles of
// the lock tools that are on: the socket volume of the tool's lock that `lox up` runs for the
// projects directory (ups, lockup.go) on /run/<name>, read-only, and the tool's clients group.
// The consoles run hub-attached only, under lox up: without such a lock the agents get no socket
// of the tool, and lea says to add one (`lox add <tool>`).
func (l *Launcher) lockHostArgs(ups map[string]string) []string {
	var a []string
	for _, t := range lockHostTools {
		_, up := ups[t.name]
		if !l.Cfg.toolOn(t.env, up) {
			continue
		}
		v, ok := ups[t.name]
		switch {
		case !ok:
			l.logf("warning: %s: no such lock yet: `lox add %s` (lea's agents get its socket at their next start)", t.name, t.name)
		case v == "":
			l.logf("warning: %s: lea's agents get no %s socket (see above)", t.name, t.name)
		default:
			a = append(a, "-e", t.env+"=1", "--group-add", t.gid, "--mount", lockUpMount(v, t.name))
		}
	}
	return a
}
