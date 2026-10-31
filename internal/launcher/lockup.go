package launcher

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// Locks started by `lock up` (lockhub's docs/lock-up.md): lock up runs each lock's console in its
// own compose service, with its socket in the volume <project>_<instance>-run, and says so in
// <proj>/.lockfamily/up.json, which the agents only read (lockfamilyDir). For a lock tool that is
// on, lea mounts that volume on /run/<tool> (the consoles run hub-attached only: there is no other
// socket to mount), once docker confirms it is that lock's socket volume (compose's
// project label and lock up's role label): never a lock's home (its private key) or another
// volume, whatever up.json says. One instance per tool: with two, lea leaves the tool out.

// lockUpFile is lock up's state in the Lock Family's directory.
const lockUpFile = "up.json"

type lockUpState struct {
	Version   int                   `json:"version"`
	Project   string                `json:"project"`
	Workspace string                `json:"workspace"`
	Locks     map[string]lockUpLock `json:"locks"`
	// The hub, for lea's LockHTTP gate in hub mode (lockhubgate.go): its network (only lea's proxy
	// joins it), its URL there and its public key; the host facades' keys the locks pin (a reveal
	// is sealed only to them); the approval devices lock up pinned (none paired: absent).
	HubNetwork string          `json:"hub_network"`
	HubURL     string          `json:"hub_url"`
	HubKey     string          `json:"hub_key"`
	FacadeKeys json.RawMessage `json:"facade_keys,omitempty"`
	// LockHTTPGate is the version of what lock up says of a lockhttp lock's gate (1: the reveal,
	// devices, require_device and seal_clear of each lock, the paired devices): lea's gate asks
	// the hub only then.
	LockHTTPGate int             `json:"lockhttp_gate,omitempty"`
	Devices      json.RawMessage `json:"devices,omitempty"`
}

type lockUpLock struct {
	Type   string `json:"type"`
	Socket string `json:"socket_volume"`
	GID    string `json:"client_gid"`
	// Profile is the profile the lock's console serves (lockfamily.yaml's profile; "" its default):
	// lea's gate asks that console.
	Profile string `json:"profile,omitempty"`
	// GateHome is a lockhttp lock's gate volume (role gate-home), lea's gate's --home.
	GateHome string `json:"gate_home_volume,omitempty"`
	// Reveal: lock up lets the lock reveal a ticket's clear values (locks.<name>.reveal): the gate
	// pins the facade keys (--facade-keys-file, lockhttp v0.5.0).
	Reveal bool `json:"reveal,omitempty"`
	// DevicesOn: the lock pins the paired devices (locks.<name>.devices in lockfamily.yaml);
	// RequireDevice and SealClear are hub.mobile's for it (--require-device, --seal-clear).
	DevicesOn     bool     `json:"devices,omitempty"`
	RequireDevice []string `json:"require_device,omitempty"`
	SealClear     bool     `json:"seal_clear,omitempty"`
}

var (
	lockUpProject  = regexp.MustCompile(`^lockfamily-[0-9a-f]{8}$`)
	lockUpInstance = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
)

// readLockUp is lock up's state for the projects directory proj (its Lock Family directory lf),
// and its path; nil (lea says why) when there is none lea can trust: a link, a file not the user's
// or others may write, not version 1, or of another projects directory.
func (l *Launcher) readLockUp(proj, lf string) (*lockUpState, string) {
	p := lf + "/" + lockUpFile
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, p
	}
	if err != nil {
		l.logf("warning: %s: %v (a link?): lea ignores it", p, err)
		return nil, p
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, p
	}
	if why := lockUpUntrusted(lf, p, fi); why != "" {
		l.logf("warning: %s: %s: lea ignores it", p, why)
		return nil, p
	}
	var s lockUpState
	if err := json.NewDecoder(io.LimitReader(f, 1<<20)).Decode(&s); err != nil || s.Version != 1 || !lockUpProject.MatchString(s.Project) {
		l.logf("warning: %s: not lock up's state (version 1): lea ignores it", p)
		return nil, p
	}
	// lock up's compose project of this very projects directory (lockhub's family.Project).
	h := sha256.Sum256([]byte(proj))
	if want := "lockfamily-" + hex.EncodeToString(h[:4]); s.Project != want || s.Workspace != proj {
		l.logf("warning: %s: project %s of %q, not this projects directory's (%s of %q): lea ignores it", p, s.Project, s.Workspace, want, proj)
		return nil, p
	}
	return &s, p
}

// lockUpSockets are the socket volumes lock up runs for the projects directory proj (its Lock
// Family directory lf), by tool, for the tools on reports on; nil without lock up. A tool lock up
// runs maps to "" when lea cannot use its lock (two instances, a volume docker does not confirm):
// the agents then get no socket of that tool, never the fixed one of another console.
func (l *Launcher) lockUpSockets(proj, lf string, on func(tool string) bool) map[string]string {
	s, p := l.readLockUp(proj, lf)
	if s == nil {
		return nil
	}
	byType := map[string][]string{}
	off := map[string]bool{}
	for name, lk := range s.Locks {
		if !on(lk.Type) {
			off[lk.Type] = true
			continue
		}
		if !lockUpInstance.MatchString(name) {
			l.logf("warning: %s: lock %q: not an instance name: %s left out", p, name, lk.Type)
			byType[lk.Type] = append(byType[lk.Type], "")
			continue
		}
		byType[lk.Type] = append(byType[lk.Type], name)
	}
	// A lock lox up runs for a tool lea leaves off (locksql, lockmq and lockfile are off by
	// default): said once, the agents get none of its socket.
	for _, t := range []struct{ typ, env string }{{"locksql", "LEA_LOCKSQL"}, {"lockmq", "LEA_LOCKMQ"}, {"lockfile", "LEA_LOCKFILE"}} {
		if off[t.typ] {
			l.logf("%s is off (%s=0), though lox up runs a %s lock here: `lea init --%s` turns it on", t.typ, t.env, t.typ, t.typ)
		}
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
	return l.lockUpServersWrong(vol, project)
}

// lockUpServersWrong is why a container that mounts vol may not be lox up's, or "": every
// container compose made that mounts it must come from lox up's own compose run, from its state
// directory (loxStateDir) with the file on stdin or that directory's compose.yaml. Compose's
// project and service labels alone are anyone's: a compose file an agent wrote in the workspace
// (name: lockfamily-<hash>, its service, this volume), run there by the user, would serve the
// socket from an image of the agent's. Containers no compose made (lea's own: its agents, its
// gate) only reach the socket.
func (l *Launcher) lockUpServersWrong(vol, project string) string {
	o, err := l.output(false, "docker", "ps", "--no-trunc", "-a", "--filter", "volume="+vol, "--format", "{{.ID}}")
	if err != nil {
		return "cannot list the containers of " + vol + " (docker ps)"
	}
	state := loxStateDir(l.Cfg, project)
	for _, id := range strings.Fields(o) {
		// Each container's labels as JSON, decoded: no field separator a path could hold.
		if !dockerID.MatchString(id) {
			return "cannot read the containers of " + vol + " (docker ps gave " + strconv.Quote(id) + ")"
		}
		j, err := l.output(false, "docker", "inspect", "--type", "container", "-f", "{{json .Config.Labels}}", id)
		var lb map[string]string
		if err != nil || json.Unmarshal([]byte(j), &lb) != nil {
			return "cannot read the containers of " + vol + " (docker inspect " + id + ")"
		}
		compose := false
		for k := range lb {
			compose = compose || strings.HasPrefix(k, "com.docker.compose.")
		}
		if !compose {
			continue // no compose made it: lea's own, which only reach the socket
		}
		p, wd, cf := lb["com.docker.compose.project"], lb["com.docker.compose.project.working_dir"], lb["com.docker.compose.project.config_files"]
		if p != project || wd != state || (cf != "-" && cf != state+"/compose.yaml") {
			return fmt.Sprintf("volume %s is mounted by container %s of compose project %q run from %q (%q), not by lox up from %s: lea trusts none of its socket", vol, id, p, wd, cf, state)
		}
	}
	return ""
}

// lockUpMount is the agents' mount of a lock up socket volume: read-only (the console made the
// directory; connecting needs no write).
func lockUpMount(vol, tool string) string {
	return "type=volume,src=" + vol + ",dst=/run/" + tool + ",readonly"
}

var dockerID = regexp.MustCompile(`^[0-9a-f]{64}$`)

// loxStateDir is lox up's state directory of compose project project: $XDG_STATE_HOME/lockfamily/
// <project>, ~/.local/state without XDG_STATE_HOME (lockhub's cmd/lox stateDir); lox up runs
// compose from there.
func loxStateDir(cfg *Config, project string) string {
	return nonEmpty(cfg.Get("XDG_STATE_HOME"), cfg.Get("HOME")+"/.local/state") + "/lockfamily/" + project
}

// LoxConsole is lox up's running console of a lock of type tool for the projects directory proj
// (its physical path), that serves profile ("": the tool's only lock): the lock's instance and its
// container's id, for the deck's pane that attaches to it (internal/agents' LockOpen). lea trusts
// only what it trusts for the agents' sockets (lockUpSockets): up.json that is the user's, of this
// projects directory, the lock's socket volume as docker labels it, and then the one container of
// lox up's compose service lock-<instance> that runs and mounts that very volume on /run/<tool>.
// An error says why there is none, and to run `lox up`.
func LoxConsole(cfg *Config, environ []string, proj, tool, profile string) (instance, container string, err error) {
	var warn bytes.Buffer
	l := &Launcher{Cfg: cfg, Environ: environ, Stderr: &warn}
	s, p := l.readLockUp(proj, proj+"/.lockfamily")
	if s == nil {
		why := strings.TrimSpace(strings.ReplaceAll(warn.String(), "lea: warning: ", ""))
		if why == "" {
			why = "no " + p
		}
		return "", "", fmt.Errorf("no %s console of lox up: %s; run `lox up`", tool, why)
	}
	var names []string
	for name, lk := range s.Locks {
		// lox up leaves out the profile a lock serves by default: "default" names it.
		if lk.Type == tool && lockUpInstance.MatchString(name) && (profile == "" || nonEmpty(lk.Profile, "default") == profile) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	switch {
	case len(names) == 0 && profile == "":
		return "", "", fmt.Errorf("no %s lock of lox up for %s: add one with lox add %s <name>, then run `lox up`", tool, proj, tool)
	case len(names) == 0:
		return "", "", fmt.Errorf("no %s lock of lox up serves the profile %q (a lock's profile: in .lockfamily/lockfamily.yaml), then run `lox up`", tool, profile)
	case len(names) > 1:
		return "", "", fmt.Errorf("lox up runs %d %s locks (%s): name the profile of one", len(names), tool, strings.Join(names, ", "))
	}
	name := names[0]
	vol := s.Project + "_" + name + "-run"
	if s.Locks[name].Socket != vol {
		return "", "", fmt.Errorf("lock %s: socket volume %q is not lox up's %q: run `lox up`", name, s.Locks[name].Socket, vol)
	}
	if why := l.lockUpVolumeWrong(vol, s.Project, proj, name, tool); why != "" {
		return "", "", fmt.Errorf("lock %s (%s): %s", name, tool, why)
	}
	out, err := l.output(false, "docker", "ps", "-a", "--no-trunc", "--filter", "label=com.docker.compose.project="+s.Project,
		"--filter", "label=com.docker.compose.service=lock-"+name, "--format", "{{.ID}} {{.State}}")
	if err != nil {
		return "", "", fmt.Errorf("lock %s: could not list lox up's containers (docker ps): %v", name, err)
	}
	var rows [][]string
	for _, ln := range strings.Split(out, "\n") {
		if f := strings.Fields(ln); len(f) > 0 {
			rows = append(rows, f)
		}
	}
	switch {
	case len(rows) == 0:
		return "", "", fmt.Errorf("lock %s: lox up runs no console for it: run `lox up`", name)
	case len(rows) > 1:
		return "", "", fmt.Errorf("lock %s: %d containers of lox up's service lock-%s: run `lox up`", name, len(rows), name)
	case len(rows[0]) != 2 || rows[0][1] != "running":
		return "", "", fmt.Errorf("lock %s: its console is not running (%s): run `lox up`", name, strings.Join(rows[0][1:], " "))
	case !dockerID.MatchString(rows[0][0]):
		return "", "", fmt.Errorf("lock %s: docker ps gave no container id (%q)", name, rows[0][0])
	}
	// One inspect of that very container: its id, state and labels again, and its socket mount.
	// Compose's project and service labels are anyone's to set: a compose file an agent wrote in
	// the workspace (name: lockfamily-<hash>, a service lock-<instance>, that volume), run there
	// by the user, would recreate the service with an image of the agent's. lox up runs compose
	// from its own state directory, $XDG_STATE_HOME/lockfamily/<project>, with the file on stdin
	// (or that directory's compose.yaml): compose's working_dir and config_files labels say so.
	state := loxStateDir(cfg, s.Project)
	m, err := l.output(false, "docker", "inspect", "--type", "container", "-f",
		`{{.Id}}|{{.State.Running}}|{{index .Config.Labels "com.docker.compose.project"}}|{{index .Config.Labels "com.docker.compose.service"}}|`+
			`{{index .Config.Labels "com.docker.compose.project.working_dir"}}|{{index .Config.Labels "com.docker.compose.project.config_files"}}|`+
			`{{range .Mounts}}{{if eq .Destination "/run/`+tool+`"}}{{.Name}}{{end}}{{end}}`, rows[0][0])
	if err != nil {
		return "", "", fmt.Errorf("lock %s: could not inspect its console (docker inspect): %v", name, err)
	}
	f := strings.Split(strings.TrimSpace(m), "|")
	switch {
	case len(f) != 7 || f[0] != rows[0][0] || f[1] != "true" || f[2] != s.Project || f[3] != "lock-"+name:
		return "", "", fmt.Errorf("lock %s: container %s is not lox up's running console of it: run `lox up`", name, rows[0][0])
	case f[4] != state || (f[5] != "-" && f[5] != state+"/compose.yaml"):
		return "", "", fmt.Errorf("lock %s: container %s was not started by lox up (compose ran from %q with %q, not lox up's %s): not its console; run `lox up`", name, rows[0][0], f[4], f[5], state)
	case f[6] != vol:
		return "", "", fmt.Errorf("lock %s: container %s does not serve lox up's socket volume %s on /run/%s: not its console", name, rows[0][0], vol, tool)
	}
	id := f[0]
	return name, id, nil
}
