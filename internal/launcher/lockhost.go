package launcher

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

// lockHostTool is a lock tool whose console runs in its own container, started by the tool's
// launcher (scripts/<name>, installed by its install.sh): lea's agents reach the console's socket
// in the named volume <name>-run, as locksql's, and nothing else. No host account, no
// /etc/<name>/system.toml on the host: lea's image names the console account and the clients
// group with the ids of the tool's image (image/Containerfile).
type lockHostTool struct {
	name string // lockmq, lockfile: also the volume <name>-run and /run/<name>
	env  string // its switch, LEA_LOCKMQ or LEA_LOCKFILE
	gid  string // the tool's clients group in its image (and in lea's)
	uid  string // the tool's console account in its image (and in lea's)
}

var lockHostTools = []lockHostTool{{"lockmq", "LEA_LOCKMQ", "10010", "10011"}, {"lockfile", "LEA_LOCKFILE", "10030", "10031"}}

// lockHostRoot and lockHostOwner: the host's root directory and the uid that must own its system
// files (lockhttp's launcher; tests only change them).
var (
	lockHostRoot  = ""
	lockHostOwner = 0
)

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
// the lock tools that are on: the tool's socket volume on /run/<name> and the tool's clients
// group. The volume is mounted read-write, as locksql-run: an empty volume is then initialised
// from lea's image with the console's directory (owner, mode 2710 or 2750), and that mode lets
// the clients group enter it and connect to the socket, never write in it. A volume still holding a release's older ids leaves the tool out with a
// warning: lea works without it.
func (l *Launcher) lockHostArgs() []string {
	var a []string
	for _, t := range lockHostTools {
		if !l.Cfg.LockHost(t.env) {
			continue
		}
		if why := l.lockVolumeStale(t); why != "" {
			l.logf("warning: %s: %s; lea's agents cannot reach a %s console until then", t.name, why, t.name)
			continue
		}
		a = append(a, "-e", t.env+"=1", "--group-add", t.gid, "--mount", "type=volume,src="+t.name+"-run,dst=/run/"+t.name)
	}
	return a
}

// lockVolumeStale is why the socket volume of t cannot be used yet, or "": lockfile before 0.3
// owned it with locksql's ids (10001:10000), which lockfile's launcher moves to its own when it
// next starts the console (lockfile migrate-ids). A volume that does not exist yet is created
// from lea's image with the right ids. Once seen right, it is not checked again (host dir).
func (l *Launcher) lockVolumeStale(t lockHostTool) string {
	if t.name != "lockfile" {
		return ""
	}
	mark := l.Cfg.HostDir() + "/lockfile-run-ids"
	if _, err := os.Stat(mark); err == nil {
		return ""
	}
	if _, err := l.output(false, "docker", "volume", "inspect", t.name+"-run"); err != nil {
		return ""
	}
	owner, err := l.output(false, "docker", "run", "--rm", "--network", "none", "--entrypoint", "stat",
		"--mount", "type=volume,src="+t.name+"-run,dst=/r,readonly", l.Cfg.Get("LEA_IMAGE"), "-c", "%u:%g", "/r")
	switch strings.TrimSpace(owner) {
	case t.uid + ":" + t.gid:
		if Mkdirs(l.Cfg.DataDir(), l.Cfg.HostDir()) == nil {
			_ = WriteFile(l.Cfg.DataDir(), mark, []byte(owner))
		}
		return ""
	case "10001:10000":
		return "the lockfile-run volume still has the ids of lockfile before 0.3: start the console once with lockfile's launcher (lockfile console --profile P), which moves it to its own ids"
	}
	if err != nil {
		return "cannot read the owner of the lockfile-run volume"
	}
	return "the lockfile-run volume is owned by " + strings.TrimSpace(owner) + ", not lockfile's console (" + t.uid + ":" + t.gid + ")"
}

// rootOwned is why path is not a root-owned file (dir: directory) that only root may write, or "".
// An agent able to edit the system file could point the client at a console of its own.
func rootOwned(path string, dir bool) string {
	fi, err := os.Lstat(path)
	if err != nil {
		return path + " is absent"
	}
	st, _ := fi.Sys().(*syscall.Stat_t)
	if fi.IsDir() != dir || (!dir && !fi.Mode().IsRegular()) || st == nil || int(st.Uid) != lockHostOwner || fi.Mode().Perm()&0o022 != 0 {
		return path + " is not owned and writable by root only"
	}
	return ""
}
