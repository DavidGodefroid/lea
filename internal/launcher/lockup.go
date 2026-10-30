package launcher

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"sort"
	"syscall"
)

// Locks started by `lock up` (lockhub's docs/lock-up.md): lock up runs each lock's console in its
// own compose service, with its socket in the volume <project>_<instance>-run, and says so in
// <proj>/.lockfamily/up.json, which the agents only read (lockfamilyDir). For a lock tool that is
// on, lea mounts that volume on /run/<tool> instead of the fixed <tool>-run of a console started
// by the tool's own launcher, once docker confirms it is that lock's socket volume (compose's
// project label and lock up's role label): never a lock's home (its private key) or another
// volume, whatever up.json says. One instance per tool: with two, lea leaves the tool out.

// lockUpFile is lock up's state in the Lock Family's directory.
const lockUpFile = "up.json"

type lockUpState struct {
	Version   int                   `json:"version"`
	Project   string                `json:"project"`
	Workspace string                `json:"workspace"`
	Locks     map[string]lockUpLock `json:"locks"`
}

type lockUpLock struct {
	Type   string `json:"type"`
	Socket string `json:"socket_volume"`
	GID    string `json:"client_gid"`
}

var (
	lockUpProject  = regexp.MustCompile(`^lockfamily-[0-9a-f]{8}$`)
	lockUpInstance = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
)

// lockUpSockets are the socket volumes lock up runs for the projects directory proj (its Lock
// Family directory lf), by tool, for the tools on reports on; nil without lock up. A tool lock up
// runs maps to "" when lea cannot use its lock (two instances, a volume docker does not confirm):
// the agents then get no socket of that tool, never the fixed one of another console.
func (l *Launcher) lockUpSockets(proj, lf string, on func(tool string) bool) map[string]string {
	p := lf + "/" + lockUpFile
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		l.logf("warning: %s: %v (a link?): lea ignores it", p, err)
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	if why := lockUpUntrusted(lf, p, fi); why != "" {
		l.logf("warning: %s: %s: lea ignores it", p, why)
		return nil
	}
	var s lockUpState
	if err := json.NewDecoder(io.LimitReader(f, 1<<20)).Decode(&s); err != nil || s.Version != 1 || !lockUpProject.MatchString(s.Project) {
		l.logf("warning: %s: not lock up's state (version 1): lea ignores it", p)
		return nil
	}
	// lock up's compose project of this very projects directory (lockhub's family.Project).
	h := sha256.Sum256([]byte(proj))
	if want := "lockfamily-" + hex.EncodeToString(h[:4]); s.Project != want || s.Workspace != proj {
		l.logf("warning: %s: project %s of %q, not this projects directory's (%s of %q): lea ignores it", p, s.Project, s.Workspace, want, proj)
		return nil
	}
	byType := map[string][]string{}
	for name, lk := range s.Locks {
		if !on(lk.Type) {
			continue
		}
		if !lockUpInstance.MatchString(name) {
			l.logf("warning: %s: lock %q: not an instance name: %s left out", p, name, lk.Type)
			byType[lk.Type] = append(byType[lk.Type], "")
			continue
		}
		byType[lk.Type] = append(byType[lk.Type], name)
	}
	out := map[string]string{}
	for typ, names := range byType {
		out[typ] = ""
		sort.Strings(names)
		if len(names) > 1 {
			l.logf("warning: lock up runs %d %s locks: lea's agents reach one per tool, so none of them", len(names), typ)
			continue
		}
		name := names[0]
		if name == "" {
			continue
		}
		vol := s.Project + "_" + name + "-run"
		if s.Locks[name].Socket != vol {
			l.logf("warning: %s: lock %s: socket volume %q is not lock up's %q: left out", p, name, s.Locks[name].Socket, vol)
			continue
		}
		if g, ok := lockGID[typ]; ok && s.Locks[name].GID != "" && s.Locks[name].GID != g {
			l.logf("warning: lock %s (%s): clients group %s, lea's image knows %s: left out (update lea)", name, typ, s.Locks[name].GID, g)
			continue
		}
		if why := l.lockUpVolumeWrong(vol, s.Project, proj, name, typ); why != "" {
			l.logf("warning: lock %s (%s): %s: lea's agents cannot reach it", name, typ, why)
			continue
		}
		out[typ] = vol
	}
	return out
}

// lockGID are the clients groups of the lock tools' images, as lea's image names them.
var lockGID = map[string]string{"locksql": "10000", "lockmq": "10010", "lockfile": "10030"}

// lockUpUntrusted is why up.json (p, described by fi, in the Lock Family's directory lf) cannot be
// lock up's: not a regular file, or the file or its directory not the user's or writable by others.
func lockUpUntrusted(lf, p string, fi os.FileInfo) string {
	if !fi.Mode().IsRegular() {
		return "not a regular file"
	}
	di, err := os.Lstat(lf)
	if err != nil || !di.IsDir() {
		return "its directory is not a directory"
	}
	for _, x := range []os.FileInfo{fi, di} {
		if st, ok := x.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
			return "not the user's"
		}
		if x.Mode().Perm()&0o022 != 0 {
			return "writable by others than the user"
		}
	}
	return ""
}

// lockUpVolumeWrong is why vol is not the socket volume lock up made for the lock name of type typ
// in compose project project, or "".
func (l *Launcher) lockUpVolumeWrong(vol, project, proj, name, typ string) string {
	o, err := l.output(false, "docker", "volume", "inspect", "--format", "{{json .Labels}}", vol)
	if err != nil {
		return "no volume " + vol + " (run lock up)"
	}
	var lb map[string]string
	if json.Unmarshal([]byte(o), &lb) != nil {
		return "cannot read the labels of " + vol
	}
	for k, v := range map[string]string{"com.docker.compose.project": project, "com.docker.compose.volume": name + "-run",
		"dev.lockfamily.role": "socket", "dev.lockfamily.instance": name, "dev.lockfamily.type": typ,
		"dev.lockfamily.workspace": proj} {
		if lb[k] != v {
			return "volume " + vol + " is not lock up's socket volume (label " + k + ")"
		}
	}
	return ""
}

// lockUpMount is the agents' mount of a lock up socket volume: read-only (the console made the
// directory; connecting needs no write).
func lockUpMount(vol, tool string) string {
	return "type=volume,src=" + vol + ",dst=/run/" + tool + ",readonly"
}
