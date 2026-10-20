package launcher

import (
	"bufio"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

// lockHostTool is a lock tool whose console runs on the host, in the tool's own account and login
// session (sudo <name> install): lockkit's isolation checks refuse a console in a container. lea
// lets its agents reach the console's socket and nothing else (libexec/lib/lockhost.sh).
type lockHostTool struct {
	name string // lockmq, lockfile: also the console account, /etc/<name>, /run/<name>
	env  string // its switch, LEA_LOCKMQ or LEA_LOCKFILE
}

var lockHostTools = []lockHostTool{{"lockmq", "LEA_LOCKMQ"}, {"lockfile", "LEA_LOCKFILE"}}

// lockHostRoot, lockHostOwner, lockHostSelf and lockHostSelfGID: the host's root directory, the
// uid that must own its system files, and the session's uid and gid (tests only change them).
var (
	lockHostRoot  = ""
	lockHostOwner = 0
	lockHostSelf  = os.Getuid
	// lockHostSelfGID is the session's primary group: the entrypoint refuses it as a clients group.
	lockHostSelfGID = os.Getgid
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

// lockHostArgs are the docker run arguments that let the session's agents reach the host
// consoles of the lock tools that are on: the tool's root-owned system.toml and its socket
// directory, both read-only, the clients group, and the host ids of the console account and
// the clients group (the entrypoint names them, so that the client can check that the socket is
// served by the console account). A tool whose host setup is missing or unsafe is left out with
// a warning: lea works without it.
func (l *Launcher) lockHostArgs() []string {
	var a []string
	for _, t := range lockHostTools {
		if !l.Cfg.LockHost(t.env) {
			continue
		}
		args, why := l.lockHostTool(t)
		if why != "" {
			l.logf("warning: %s: %s; lea's agents cannot reach a %s console (lea doctor, lea %s)", t.name, why, t.name, t.name)
			continue
		}
		a = append(a, args...)
	}
	return a
}

// lockHostTool checks the host setup of t and returns its arguments, or why it is left out.
func (l *Launcher) lockHostTool(t lockHostTool) ([]string, string) {
	root := lockHostRoot
	etc, sys, run := root+"/etc/"+t.name, root+"/etc/"+t.name+"/system.toml", root+"/run/"+t.name
	if why := rootOwned(etc, true); why != "" {
		return nil, why + " (sudo " + t.name + " install)"
	}
	if why := rootOwned(sys, false); why != "" {
		return nil, why + " (sudo " + t.name + " install)"
	}
	conf, err := readLockSys(sys)
	if err != nil {
		return nil, "cannot read " + sys + ": " + err.Error()
	}
	// The fixed names of TOOL install's defaults: the entrypoint creates them in the container,
	// and a socket directory elsewhere would need a mount lea does not make.
	for k, want := range map[string]string{"service_user": t.name, "client_group": t.name + "-clients", "socket_dir": "/run/" + t.name} {
		if v, ok := conf[k]; ok && v != want {
			return nil, sys + ": " + k + " is '" + v + "'; lea supports only '" + want + "'"
		}
	}
	uid, ok := lookupID(root+"/etc/passwd", t.name, 2)
	if !ok {
		return nil, "no " + t.name + " account in /etc/passwd"
	}
	gid, ok := lookupID(root+"/etc/group", t.name+"-clients", 2)
	if !ok {
		return nil, "no " + t.name + "-clients group in /etc/group"
	}
	if uid == 0 || uid == lockHostSelf() || gid == 0 || gid == lockHostSelfGID() {
		return nil, "the " + t.name + " account or group is root or yours: no separation"
	}
	fi, err := os.Lstat(run)
	if err != nil {
		return nil, run + " is absent (the console's socket directory: " + t.name + " doctor, in the " + t.name + " account)"
	}
	st, _ := fi.Sys().(*syscall.Stat_t)
	if !fi.IsDir() || st == nil || int(st.Uid) != uid || int(st.Gid) != gid || fi.Mode().Perm()&0o007 != 0 {
		return nil, run + " is not a directory of the " + t.name + " account and group, closed to others"
	}
	if t.name == "lockfile" {
		bin := root + "/usr/local/bin/lockfile"
		if why := rootOwned(root+"/usr/local/bin", true); why != "" {
			return nil, why
		}
		if why := rootOwned(bin, false); why != "" {
			return nil, why
		}
		out, err := l.output(false, bin, "version")
		f := strings.Fields(out)
		if err != nil || len(f) == 0 || !lockfileSafe(f[len(f)-1]) {
			return nil, "lockfile on this host is below v0.1.1 or of unknown version (a plugin could escape its sandbox): lea init installs " + l.Cfg.Get("LEA_LOCKFILE_REF")
		}
	}
	if !inGroup(root+"/etc/group", t.name+"-clients", l.userName()) {
		l.logf("warning: %s: you are not in the %s-clients group: the console will refuse lea's agents (sudo usermod -aG %s-clients %s, then log in again)",
			t.name, t.name, t.name, l.userName())
	}
	if err := noComma(sys, run); err != nil {
		return nil, err.Error()
	}
	u, g := strconv.Itoa(uid), strconv.Itoa(gid)
	return []string{"-e", t.env + "=1", "-e", t.env + "_UID=" + u, "-e", t.env + "_GID=" + g, "--group-add", g,
		"--mount", "type=bind,src=" + sys + ",dst=/etc/" + t.name + "/system.toml,readonly",
		"--mount", "type=bind,src=" + run + ",dst=/run/" + t.name + ",readonly"}, ""
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

// readLockSys reads the key = "value" lines of a lock tool's system.toml (the keys lea checks).
func readLockSys(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	m := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok || strings.HasPrefix(strings.TrimSpace(k), "#") {
			continue
		}
		v = strings.TrimSpace(v)
		if i := strings.Index(v, "#"); i >= 0 && !strings.HasPrefix(v, `"`) {
			v = strings.TrimSpace(v[:i])
		}
		if len(v) >= 2 && v[0] == '"' {
			if j := strings.Index(v[1:], `"`); j >= 0 {
				v = v[1 : j+1]
			}
		}
		m[strings.TrimSpace(k)] = v
	}
	return m, sc.Err()
}

// lookupID is the numeric field col (2: the id) of the line of name in a passwd or group file.
func lookupID(path, name string, col int) (int, bool) {
	for _, f := range colonLines(path) {
		if len(f) > col && f[0] == name {
			n, err := strconv.Atoi(f[col])
			return n, err == nil
		}
	}
	return 0, false
}

// inGroup reports whether user is listed as a member of group in a group file.
func inGroup(path, group, user string) bool {
	for _, f := range colonLines(path) {
		if len(f) > 3 && f[0] == group {
			for _, m := range strings.Split(f[3], ",") {
				if m == user {
					return true
				}
			}
		}
	}
	return false
}

func colonLines(path string) [][]string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out [][]string
	for _, line := range strings.Split(string(b), "\n") {
		out = append(out, strings.Split(line, ":"))
	}
	return out
}
