package launcher

import (
	"fmt"
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
	var img *imageIDs
	for _, t := range lockHostTools {
		if !l.Cfg.LockHost(t.env) {
			continue
		}
		if img == nil {
			img = l.imageIDs()
		}
		args, why := l.lockHostTool(t, img)
		if why != "" {
			l.logf("warning: %s: %s; lea's agents cannot reach a %s console (lea doctor, lea %s)", t.name, why, t.name, t.name)
			continue
		}
		a = append(a, args...)
	}
	return a
}

// lockHostTool checks the host setup of t and returns its arguments, or why it is left out.
func (l *Launcher) lockHostTool(t lockHostTool, img *imageIDs) ([]string, string) {
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
	// The entrypoint names the account and the group in lea's image with these ids: an id the
	// image already uses would give the agents that account's group (--group-add) for nothing.
	if lockReservedIDs[uid] || lockReservedIDs[gid] {
		return nil, "uid " + strconv.Itoa(uid) + " or gid " + strconv.Itoa(gid) + " is locksql's in lea's image (10000, 10001)"
	}
	if img.err != "" {
		return nil, img.err
	}
	if img.uids[uid] {
		return nil, "uid " + strconv.Itoa(uid) + " of the " + t.name + " account is already used in lea's image (give it a free one on the host: sudo usermod -u ID " + t.name + ")"
	}
	if img.gids[gid] {
		return nil, "gid " + strconv.Itoa(gid) + " of the " + t.name + "-clients group is already used in lea's image (give it a free one on the host: sudo groupmod -g ID " + t.name + "-clients)"
	}
	fi, err := os.Lstat(run)
	if err != nil {
		return nil, run + " is absent (the console's socket directory: " + t.name + " doctor, in the " + t.name + " account)"
	}
	st, _ := fi.Sys().(*syscall.Stat_t)
	if !fi.IsDir() || st == nil || int(st.Uid) != uid || int(st.Gid) != gid || fi.Mode().Perm()&0o027 != 0 {
		return nil, run + " is not a directory of the " + t.name + " account and group, closed to others and not writable by the group"
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

// lockSysLine is the only line shape lea reads in a system.toml: a bare key, then a basic string
// without escapes or a one-line array of integers, then an optional comment.
var lockSysLine = regexp.MustCompile(`^([a-z_][a-z0-9_]*)[ \t]*=[ \t]*("([^"\\]*)"|\[[0-9, \t]*\])[ \t]*(#.*)?$`)

// readLockSys reads the keys of a lock tool's system.toml. Any other line (a table, a quoted key, a
// multi-line string, an escape) is an error: lea does not guess how the tool would read it.
func readLockSys(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for n, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := lockSysLine.FindStringSubmatch(line)
		if f == nil {
			return nil, fmt.Errorf("line %d: not a key = \"value\" line lea can read", n+1)
		}
		if _, dup := m[f[1]]; dup {
			return nil, fmt.Errorf("line %d: %s set twice", n+1, f[1])
		}
		m[f[1]] = f[3]
	}
	return m, nil
}

// imageIDs are the uids and gids of lea's image (LEA_IMAGE), from its /etc/passwd and
// /etc/group, read once per image id (host dir image-ids/<id>); err says why they are unknown.
type imageIDs struct {
	uids, gids map[int]bool
	err        string
}

// lockReservedIDs are the ids of locksql's account and clients group in lea's image.
var lockReservedIDs = map[int]bool{10000: true, 10001: true}

var imageID = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func (l *Launcher) imageIDs() *imageIDs {
	img := l.Cfg.Get("LEA_IMAGE")
	cache := ""
	if id, err := l.output(false, "docker", "image", "inspect", "-f", "{{.Id}}", img); err == nil && imageID.MatchString(id) {
		cache = l.Cfg.HostDir() + "/image-ids/" + strings.TrimPrefix(id, "sha256:")
	}
	var out string
	if b, err := os.ReadFile(cache); cache != "" && err == nil {
		out = string(b)
	} else {
		o, err := l.output(false, "docker", "run", "--rm", "--network", "none", "--entrypoint", "cat", img, "/etc/passwd", "/etc/group")
		if err != nil || !strings.Contains(o, "root:x:0:") {
			return &imageIDs{err: "cannot read the accounts of lea's image " + img}
		}
		out = o
		if cache != "" && Mkdirs(l.Cfg.DataDir(), l.Cfg.HostDir()+"/image-ids") == nil {
			_ = WriteFile(l.Cfg.DataDir(), cache, []byte(out))
		}
	}
	r := &imageIDs{uids: map[int]bool{}, gids: map[int]bool{}}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, ":")
		n, err := 0, error(nil)
		if len(f) >= 3 {
			n, err = strconv.Atoi(f[2])
		}
		switch {
		case err != nil || len(f) < 3:
		case len(f) == 7: // passwd: its primary group is the group file's business
			r.uids[n] = true
		case len(f) == 4:
			r.gids[n] = true
		}
	}
	return r
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
