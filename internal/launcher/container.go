package launcher

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"golang.org/x/sys/unix"
)

// claudeShared is LEA_CLAUDE_SHARED: the ~/.claude entries mounted read-only. settings.json and
// plugins are not among them: each container gets its own writable copy (claudeLayered, #111).
var claudeShared = []string{"keybindings.json", "hooks", "skills", "mods", "dev-mods",
	"output-styles", "agents", "commands"}

// userName is lea_user_name: `id -un` (it asks NSS; the pure-Go os/user reads /etc/passwd only),
// else u<uid>.
func (l *Launcher) userName() string {
	if out, err := l.output(false, "id", "-un"); err == nil {
		return out
	}
	return "u" + strconv.Itoa(os.Getuid())
}

// containerUser is lea_container_user: the host name if it is a portable login name
// (^[a-z_][a-z0-9_-]*$, at most 32 characters), else u<uid>.
func (l *Launcher) containerUser() string {
	u := l.userName()
	ok := u != "" && len(u) <= 32 && (u[0] == '_' || u[0] >= 'a' && u[0] <= 'z') &&
		strings.Trim(u, "abcdefghijklmnopqrstuvwxyz0123456789_-") == ""
	if !ok {
		return "u" + strconv.Itoa(os.Getuid())
	}
	return u
}

// securityArgs is lea_security_args. CAP_SYS_CHROOT stays: the nested Podman unpacks image
// layers in a chroot (containers/storage), and a pull fails without it.
func (l *Launcher) securityArgs() []string {
	a := []string{"--cap-drop", "NET_RAW", "--cap-drop", "MKNOD", "--cap-drop", "AUDIT_WRITE",
		"--device", "/dev/fuse", "--device", "/dev/net/tun", "--security-opt", "seccomp=" + l.Root + "/image/seccomp.json"}
	if out, _ := l.output(false, "docker", "info", "--format", "{{json .SecurityOptions}}"); strings.Contains(out, "name=apparmor") {
		a = append(a, "--security-opt", "apparmor=lea")
	}
	return a
}

// meminfoPath is the host's memory, the last resort of engineMemory (a variable for the tests).
var meminfoPath = "/proc/meminfo"

// engineMemory is the memory of the container engine in bytes, 0 when unknown: Docker's
// {{.MemTotal}} (a VM's under Docker Desktop), Podman's {{.Host.MemTotal}} (Podman has no
// .MemTotal), else MemTotal of /proc/meminfo, where the engine runs on the host itself (Linux).
func (l *Launcher) engineMemory() int64 {
	for _, f := range []string{"{{.MemTotal}}", "{{.Host.MemTotal}}"} {
		out, err := l.output(false, "docker", "system", "info", "--format", f)
		if n, perr := strconv.ParseInt(strings.TrimSpace(out), 10, 64); err == nil && perr == nil && n > 0 {
			return n
		}
	}
	b, _ := os.ReadFile(meminfoPath)
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) == 3 && f[0] == "MemTotal:" && f[2] == "kB" {
			if n, err := strconv.ParseInt(f[1], 10, 64); err == nil && n > 0 {
				return n << 10
			}
		}
	}
	return 0
}

// hostCPUs is the CPUs this process sees, the last resort of engineCPUs (a variable for the
// tests).
var hostCPUs = runtime.NumCPU

// engineCPUs is the number of CPUs of the container engine, 0 when unknown: Docker's {{.NCPU}} (a
// VM's under Docker Desktop), Podman's {{.Host.CPUs}}, else the CPUs this process sees.
func (l *Launcher) engineCPUs() int {
	for _, f := range []string{"{{.NCPU}}", "{{.Host.CPUs}}"} {
		out, err := l.output(false, "docker", "system", "info", "--format", f)
		if n, perr := strconv.Atoi(strings.TrimSpace(out)); err == nil && perr == nil && n > 0 {
			return n
		}
	}
	return hostCPUs()
}

// limits are the lea container's resolved resource limits, docker flag values; "" is no limit.
type limits struct{ cpus, mem, swap, pids string }

// minCPUs is the floor of a share of CPUs (LEA_CPUS=N%): one agent's tools and its nested stack.
const minCPUs = 2

// resolvedLimits turns LEA_CPUS, LEA_MEMORY, LEA_MEMORY_SWAP and LEA_PIDS_LIMIT into the lea
// container's limits. A share of CPUs or memory is taken from the engine's (engineCPUs,
// engineMemory); memory plus swap is the memory itself unless LEA_MEMORY_SWAP says otherwise.
func (l *Launcher) resolvedLimits() limits {
	var r limits
	cpus := l.Cfg.Get("LEA_CPUS")
	if strings.HasSuffix(cpus, "%") {
		pct, _ := strconv.Atoi(strings.TrimSuffix(cpus, "%"))
		if total := l.engineCPUs(); total <= 0 {
			l.logf("warning: Docker did not tell its CPUs; the lea container runs without a CPU limit (set LEA_CPUS to a number, 8 for example)")
			cpus = "0"
		} else {
			// Hundredths of a CPU, rounded down; at least minCPUs, never more than there are.
			c := total * pct
			c = max(c, min(minCPUs, total)*100)
			cpus = strconv.FormatFloat(float64(c)/100, 'f', -1, 64)
		}
	}
	if cpus != "0" {
		r.cpus = cpus
	}
	mem := l.Cfg.Get("LEA_MEMORY")
	if strings.HasSuffix(mem, "%") {
		pct, _ := strconv.ParseInt(strings.TrimSuffix(mem, "%"), 10, 64)
		if total := l.engineMemory(); total <= 0 {
			l.logf("warning: Docker did not tell its memory; the lea container runs without a memory limit (set LEA_MEMORY to a size, 16g for example)")
			mem = "0"
		} else {
			mem = strconv.FormatInt(total*pct/100>>20, 10) + "m"
		}
	}
	if mem != "0" {
		r.mem, r.swap = mem, nonEmpty(l.Cfg.Get("LEA_MEMORY_SWAP"), mem)
	}
	if p := l.Cfg.Get("LEA_PIDS_LIMIT"); p != "0" {
		r.pids = p
	}
	return l.withoutMissingControllers(r)
}

// delegateFix is how a user of rootless Podman gives their containers the cgroup controllers
// lea's limits need: systemd delegates them to user@.service.
const delegateFix = "sudo systemctl edit user@.service, add [Service] Delegate=cpu cpuset io memory pids, log in again, then lea stop and lea"

// missingControllers is the cgroup controllers among cpu, memory and pids that rootless Podman
// does not get (its {{.Host.CgroupControllers}}, which the user's systemd delegates): a limit of
// one of them makes podman run fail. Nil for Docker, rootful Podman or when Podman does not tell.
func (l *Launcher) missingControllers() map[string]bool {
	out, err := l.output(false, "docker", "system", "info", "--format", "{{.Host.Security.Rootless}} {{.Host.CgroupControllers}}")
	rest, rootless := strings.CutPrefix(strings.TrimSpace(out), "true ")
	if err != nil || !rootless {
		return nil
	}
	have := map[string]bool{}
	for _, c := range strings.Fields(strings.Trim(rest, "[]")) {
		have[c] = true
	}
	if len(have) == 0 {
		return nil
	}
	miss := map[string]bool{}
	for _, c := range []string{"cpu", "memory", "pids"} {
		if !have[c] {
			miss[c] = true
		}
	}
	return miss
}

// withoutMissingControllers drops from r the limits whose controller rootless Podman lacks
// (#132): the lea container starts with the others rather than not at all, and one warning per
// lea names the fix. lea doctor reports it too.
func (l *Launcher) withoutMissingControllers(r limits) limits {
	miss := l.missingControllers()
	var dropped, names []string
	if miss["cpu"] && r.cpus != "" {
		dropped, names, r.cpus = append(dropped, "--cpus "+r.cpus), append(names, "cpu"), ""
	}
	if miss["memory"] && r.mem != "" {
		dropped, names, r.mem, r.swap = append(dropped, "--memory "+r.mem), append(names, "memory"), "", ""
	}
	if miss["pids"] && r.pids != "" {
		dropped, names, r.pids = append(dropped, "--pids-limit "+r.pids), append(names, "pids"), ""
	}
	if root := l.root(); len(dropped) > 0 && !root.ctrlWarned {
		root.ctrlWarned = true
		l.logf("warning: rootless Podman does not get the %s cgroup controller: the lea container runs without %s; to apply it: %s",
			strings.Join(names, " and "), strings.Join(dropped, ", "), delegateFix)
	}
	return r
}

// digest is the lea.limits label of a container started with limits r: equal limits give the
// same digest, and any limit that changes changes it.
func (r limits) digest() string {
	h := sha256.Sum256([]byte("cpus=" + r.cpus + "\nmemory=" + r.mem + "\nswap=" + r.swap + "\npids=" + r.pids + "\n"))
	return hex.EncodeToString(h[:8])
}

// nanos is r as the engine's inspect tells limits: NanoCpus, Memory, MemorySwap and PidsLimit, 0
// for no limit (lea doctor compares them, LEA_LIMITS_WANT).
func (r limits) nanos() string {
	c, _ := strconv.ParseFloat(r.cpus, 64)
	p, _ := strconv.ParseInt(r.pids, 10, 64)
	return fmt.Sprintf("%d %d %d %d", int64(math.Round(c*1e9)), sizeBytes(r.mem), sizeBytes(r.swap), p)
}

// args are the flags of limits r, for docker run and docker update.
func (r limits) args() []string {
	var a []string
	if r.cpus != "" {
		a = append(a, "--cpus", r.cpus)
	}
	if r.mem != "" {
		a = append(a, "--memory", r.mem, "--memory-swap", r.swap)
	}
	if r.pids != "" {
		a = append(a, "--pids-limit", r.pids)
	}
	return a
}

// limitArgs are the lea container's resource limits (#97, #132): without them a pile of nested
// stacks takes the whole host down (OOM, swap thrash, every core busy) instead of being held
// inside lea.
func (l *Launcher) limitArgs() []string {
	r := l.resolvedLimits()
	return append(r.args(), "--label", "lea.limits="+r.digest())
}

// sizeBytes is a docker size (dockerSize: 512m, 8g, 1024) in bytes, -1 as is, 0 for "" (no
// limit).
func sizeBytes(s string) int64 {
	switch s {
	case "":
		return 0
	case "-1":
		return -1
	}
	mult := int64(1)
	switch s[len(s)-1] {
	case 'b':
		s = s[:len(s)-1]
	case 'k':
		mult, s = 1<<10, s[:len(s)-1]
	case 'm':
		mult, s = 1<<20, s[:len(s)-1]
	case 'g':
		mult, s = 1<<30, s[:len(s)-1]
	}
	n, _ := strconv.ParseInt(s, 10, 64)
	return n * mult
}

// headroom is the share of a new memory or pids limit the usage must stay under for lea to lower
// the limit of a running container: above it, the agents' work would be OOM-killed or fail to
// fork.
const headroom = 0.8

// usage is a running container's memory in bytes and its number of processes, read from its own
// cgroup (Docker and Podman give a container its cgroup namespace on cgroup v2); ok is false when
// they cannot be read.
func (l *Launcher) usage(name string) (mem, pids int64, ok bool) {
	out, err := l.output(false, "docker", "exec", name, "cat", "/sys/fs/cgroup/memory.current", "/sys/fs/cgroup/pids.current")
	f := strings.Fields(out)
	if err != nil || len(f) != 2 {
		return 0, 0, false
	}
	mem, merr := strconv.ParseInt(f[0], 10, 64)
	pids, perr := strconv.ParseInt(f[1], 10, 64)
	return mem, pids, merr == nil && perr == nil
}

// limitsRefused is where lea remembers the limits the engine refused for a container (rootless
// Podman without the cpu or memory controller): the next lea neither retries nor warns again.
func (l *Launcher) limitsRefused(name string) string { return l.Cfg.HostDir() + "/limits/" + name }

// pidsLimitFormat is the container's pids limit for docker inspect, 0 for none: Docker's is a
// pointer, which recent releases print as "<no value>" (older ones "<nil>") when unset.
const pidsLimitFormat = "{{with .HostConfig.PidsLimit}}{{.}}{{else}}0{{end}}"

// applyLimits gives a running lea container the configured limits it lacks (#132): one started
// by an older lea, or before a limit changed, ran without them until it restarted. Docker and
// Podman both change them in place (docker update). The CPUs only throttle: always applied. A
// memory or pids limit is lowered only while the usage stays under headroom of it. When one is
// held back that way, or the engine refuses the update, applyLimits says so once and returns true:
// the container needs a restart, which up does at once when nothing runs in it, and otherwise
// leaves to a later lea (or lea stop, then lea). A limit turned off is only taken away by a
// restart.
func (l *Launcher) applyLimits(name string) (restart bool) {
	out, err := l.output(false, "docker", "inspect", "--format",
		"{{.Id}} {{.HostConfig.NanoCpus}} {{.HostConfig.Memory}} {{.HostConfig.MemorySwap}} "+pidsLimitFormat, name)
	f := strings.Fields(out)
	if err != nil || len(f) != 5 {
		return false
	}
	id, n := f[0], make([]int64, 4)
	for i, v := range f[1:] {
		if v == "<nil>" {
			continue
		}
		x, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return false
		}
		n[i] = x
	}
	cpus, memory, swap, pidsLimit := n[0], n[1], n[2], n[3]
	want := l.resolvedLimits()
	var fix limits
	if want.cpus != "" {
		c, _ := strconv.ParseFloat(want.cpus, 64)
		if cpus != int64(math.Round(c*1e9)) {
			fix.cpus = want.cpus
		}
	}
	wantMem, wantPids := sizeBytes(want.mem), int64(0)
	if want.mem != "" && (memory != wantMem || swap != sizeBytes(want.swap)) {
		fix.mem, fix.swap = want.mem, want.swap
	}
	if want.pids != "" {
		wantPids, _ = strconv.ParseInt(want.pids, 10, 64)
		if pidsLimit != wantPids {
			fix.pids = want.pids
		}
	}
	if len(fix.args()) == 0 {
		return false
	}
	// A limit is lowered when there was none or it was higher: only with headroom.
	lowerMem := fix.mem != "" && (memory == 0 || memory > wantMem)
	lowerPids := fix.pids != "" && (pidsLimit == 0 || pidsLimit > wantPids)
	var held, heldFlags []string
	if lowerMem || lowerPids {
		mem, pids, ok := l.usage(name)
		if lowerMem && (!ok || float64(mem) >= headroom*float64(wantMem)) {
			if ok {
				held = append(held, fmt.Sprintf("--memory %s (%d MiB in use)", fix.mem, mem>>20))
			} else {
				held = append(held, fmt.Sprintf("--memory %s (usage unknown)", fix.mem))
			}
			heldFlags = append(heldFlags, "--memory "+fix.mem)
			fix.mem, fix.swap = "", ""
		}
		if lowerPids && (!ok || float64(pids) >= headroom*float64(wantPids)) {
			if ok {
				held = append(held, fmt.Sprintf("--pids-limit %s (%d processes in use)", fix.pids, pids))
			} else {
				held = append(held, fmt.Sprintf("--pids-limit %s (usage unknown)", fix.pids))
			}
			heldFlags = append(heldFlags, "--pids-limit "+fix.pids)
			fix.pids = ""
		}
	}
	a := fix.args()
	// What lea remembers it said about this container: the next lea neither retries nor warns
	// again, and still knows the container needs a restart.
	stampOf := func(a []string) string {
		st := id + " " + strings.Join(a, " ")
		if len(heldFlags) > 0 {
			st += " held " + strings.Join(heldFlags, " ")
		}
		return st
	}
	stamp := stampOf(a)
	refused := l.limitsRefused(name)
	// The file is lea's own, below the data directory (SafePath: no symbolic link on the way).
	safe := SafePath(l.Cfg.DataDir(), refused) == nil
	// A refusal is not tried again for the same container; a held limit is weighed again at each
	// lea (the usage may have fallen), without saying the same thing twice.
	seen := safe && readTrim(refused) == stamp
	if seen && len(heldFlags) == 0 {
		return true
	}
	remember := func() {
		if safe && Mkdirs(l.Cfg.DataDir(), parentText(refused)) == nil {
			_ = WriteFile(l.Cfg.DataDir(), refused, []byte(stamp+"\n"))
		}
	}
	if len(held) > 0 && !seen {
		l.logf("warning: the running %s keeps its limits for now: %s; lea restarts it with them once no agent or session runs in it (or run 'lea stop', then lea, at a quiet time)",
			name, strings.Join(held, ", "))
	}
	if len(a) == 0 {
		remember()
		return true
	}
	if l.quiet("docker", append(append([]string{"update"}, a...), name)...) != nil {
		if !seen {
			l.logf("warning: could not apply the limits %s to the running %s; lea restarts it with them once no agent or session runs in it (or run 'lea stop', then lea)",
				strings.Join(a, " "), name)
		}
		remember()
		return true
	}
	l.logf("applied the limits %s to the running %s", strings.Join(a, " "), name)
	if len(held) > 0 {
		// The next lea finds these applied: only the held ones are left to remember.
		stamp = stampOf(nil)
		remember()
		return true
	}
	if safe {
		os.Remove(refused)
	}
	return false
}

// containerQuiet: nothing runs in the projects dir's container that a restart would cut: no lea
// session (lea --shell), no deck agent, no deck in tmux.
func (l *Launcher) containerQuiet(proj string) bool {
	if n, err := l.sessionCount(ID(proj)); err != nil || n > 0 {
		return false
	}
	// An agent count lea cannot read is not "none": an exec that fails (the engine busy, the
	// container paused) keeps the container rather than cutting agents it did not see.
	if live, err := l.agentsRunning(proj); err != nil || live {
		return false
	}
	if s, ok := l.tmuxServer(); ok && l.tmuxClient(s, "has-session", "-t", "="+l.Cfg.ContainerName(proj)) == nil {
		return false
	}
	return true
}

// startedWithLimits: the container was started with the limits the configuration resolves to
// now (its lea.limits label is their digest). The engine then holds it below them as well as it
// can: a restart would start it again with the same flags (rootless Podman on cgroup v1, which
// names no controller, ignores them at run as at update), so lea does not restart it again and
// again (#132). A container without the label (an older lea) or with another digest was not.
func (l *Launcher) startedWithLimits(name string) bool {
	out, err := l.output(false, "docker", "container", "inspect", "--format", `{{index .Config.Labels "lea.limits"}}`, name)
	got := strings.TrimSpace(out)
	return err == nil && got != "" && got == l.resolvedLimits().digest()
}

// restartForLimits stops the projects dir's quiet container as lea stop does (its exit scan
// included) and starts it again with the configured limits (#132).
func (l *Launcher) restartForLimits(proj, pip string) error {
	name := l.Cfg.ContainerName(proj)
	l.logf("restarting the idle %s to apply its limits (no agent or session runs in it)", name)
	l.deckStop(name, false)
	_ = l.sh("scan-finish", proj)
	_ = l.quiet("docker", "stop", name)
	if p := l.limitsRefused(name); SafePath(l.Cfg.DataDir(), p) == nil {
		os.Remove(p)
	}
	return l.containerStart(proj, pip)
}

// busArgs is lea_bus_args: the core binary read-only at /usr/local/bin/lea and the container's
// own bus directory at /run/lea/bus. The daemon's API socket is never mounted.
func (l *Launcher) busArgs(proj string) ([]string, error) {
	bd := l.Cfg.HostDir() + "/run/" + l.Cfg.ContainerName(proj)
	if err := noComma(bd, l.Core); err != nil {
		return nil, err
	}
	if err := Mkdirs(l.Cfg.DataDir(), l.Cfg.HostDir()+"/run", bd); err != nil {
		return nil, die("%v", err)
	}
	return []string{"--mount", "type=bind,src=" + l.Core + ",dst=/usr/local/bin/lea,readonly",
		"--mount", "type=bind,src=" + bd + ",dst=/run/lea/bus", "-e", "LEA_BUS_SOCK=/run/lea/bus/bus.sock"}, nil
}

// clipboardFifo is lea_clipboard_fifo.
func (l *Launcher) clipboardFifo() string { return l.Cfg.StateDir() + "/clipboard/copy" }

// clipboardArgs is lea_clipboard_args: the copy FIFO and the WAYLAND_DISPLAY marker; the FIFO is
// created, or recreated when something else sits at its path.
func (l *Launcher) clipboardArgs() ([]string, error) {
	if nonEmpty(l.Cfg.Get("LEA_CLIPBOARD"), "1") == "0" {
		return nil, nil
	}
	f := l.clipboardFifo()
	if err := Mkdirs(l.Cfg.DataDir(), parentText(f)); err != nil {
		return nil, die("%v", err)
	}
	if err := SafePath(l.Cfg.DataDir(), f); err != nil {
		return nil, die("%v", err)
	}
	if exists(f) && !isFifo(f) {
		os.Remove(f)
	}
	if !isFifo(f) {
		// mkfifo -m 600: the mode is set whatever the umask.
		if unix.Mkfifo(f, 0o600) != nil || os.Chmod(f, 0o600) != nil {
			return nil, die("cannot create the clipboard FIFO %s", f)
		}
	}
	return []string{"--mount", "type=bind,src=" + f + ",dst=/lea/clipboard/copy", "-e", "WAYLAND_DISPLAY=lea-clipboard"}, nil
}

// claudeRO is lea__claude_ro: a read-only bind of a real ~/.claude entry, its mount point
// created in the private base; a symlinked entry is skipped.
func (l *Launcher) claudeRO(sbase, src, placeholder, dst string) ([]string, error) {
	if isSymlink(src) {
		l.logf("skipped %s: it is a symbolic link (lea mounts only real entries of ~/.claude)", src)
		return nil, nil
	}
	switch {
	case isDir(src):
		if err := Mkdirs(sbase, placeholder); err != nil {
			return nil, die("%v", err)
		}
	case isRegular(src):
		if err := SafePath(sbase, placeholder); err != nil {
			return nil, die("%v", err)
		}
		if !exists(placeholder) {
			f, err := os.OpenFile(placeholder, os.O_WRONLY|os.O_CREATE, 0o666)
			if err != nil {
				return nil, die("%v", err)
			}
			f.Close()
		}
	default:
		return nil, nil
	}
	return []string{"--mount", "type=bind,src=" + src + ",dst=" + dst + ",readonly"}, nil
}

// claudeMounts is lea_claude_mounts: a private ~/.claude per projects dir, holding its own
// writable copies of settings.json and plugins (claudeLayer), the host's permission rules for the
// managed settings (hostPermissionsFile), the allow-list of host entries read-only, the project's
// transcripts and lea's own .claude.json. No host entry is ever mounted
// writable but this projects dir's transcripts.
func (l *Launcher) claudeMounts(home, proj, st string) ([]string, error) {
	if err := noComma(home, proj, st); err != nil {
		return nil, err
	}
	sbase := parentText(st)
	src, priv, slug := home+"/.claude", st+"/claude/"+ID(proj), Slug(proj)
	if err := SafePath(sbase, st+"/claude.json"); err != nil {
		return nil, die("%v", err)
	}
	if !isRegular(st + "/claude.json") {
		return nil, die("%s/claude.json is missing: run 'lea init'", st)
	}
	if err := Mkdirs(sbase, priv+"/projects/"+slug); err != nil {
		return nil, die("%v", err)
	}
	if err := Mkdirs(src, src+"/projects/"+slug); err != nil {
		return nil, die("%v", err)
	}
	a := []string{"--mount", "type=bind,src=" + priv + ",dst=" + src}
	add := func(m []string, err error) error { a = append(a, m...); return err }
	for _, e := range claudeShared {
		if err := add(l.claudeRO(sbase, src+"/"+e, priv+"/"+e, src+"/"+e)); err != nil {
			return nil, err
		}
	}
	if err := l.claudeLayer(src, priv, sbase); err != nil {
		return nil, err
	}
	if err := add(l.statusLineMounts(src, priv, sbase)); err != nil {
		return nil, err
	}
	if err := add(l.hostPermissionsFile(src, priv, sbase)); err != nil {
		return nil, err
	}
	// The shell's "$_src"/*.md: $_src is quoted, so HOME is never a pattern; no hidden names;
	// byte order (dash sorts its globs with strcmp).
	var mds []string
	ents, _ := os.ReadDir(src)
	for _, e := range ents {
		if ok, _ := filepath.Match("*.md", e.Name()); ok && !strings.HasPrefix(e.Name(), ".") {
			mds = append(mds, e.Name())
		}
	}
	sort.Strings(mds)
	for _, b := range mds {
		f := src + "/" + b
		if !(isRegular(f) || isSymlink(f)) {
			continue
		}
		if err := add(l.claudeRO(sbase, f, priv+"/"+b, src+"/"+b)); err != nil {
			return nil, err
		}
	}
	a = append(a, "--mount", "type=bind,src="+src+"/projects/"+slug+",dst="+src+"/projects/"+slug,
		"--mount", "type=bind,src="+st+"/claude.json,dst="+home+"/.claude.json")
	return a, nil
}

// codexMounts is Codex's state (~/.codex: its login, sessions and config), private to the projects
// dir like ~/.claude, and never shared with the host's own ~/.codex.
func (l *Launcher) codexMounts(home, proj, st string) ([]string, error) {
	priv := st + "/codex/" + ID(proj)
	if err := noComma(home, priv); err != nil {
		return nil, err
	}
	if err := Mkdirs(l.Cfg.DataDir(), priv); err != nil {
		return nil, die("%v", err)
	}
	// Its login (auth.json) is the user's only.
	if err := os.Chmod(priv, 0o700); err != nil {
		return nil, die("%v", err)
	}
	return []string{"--mount", "type=bind,src=" + priv + ",dst=" + home + "/.codex"}, nil
}

// dnsNowhere is the agent container's upstream resolver (#98): a loopback address with no server.
const dnsNowhere = "127.0.0.1"

// runArgs is lea_run_args: the docker run arguments, image last. The state directories and mount
// points are a user-facing contract (lea init creates them in shell).
func (l *Launcher) runArgs(proj, pip string) ([]string, error) {
	l.checkHostEngine()
	st, id, home := l.Cfg.StateDir(), ID(proj), l.Cfg.Get("HOME")
	uid, gid := strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid())
	git := l.Cfg.GitSSH()
	if _, err := gitEndpoints(git); err != nil {
		return nil, die("%v", err)
	}
	proxy := "http://" + pip + ":3128"
	noProxy := "localhost,127.0.0.1,::1,lea-proxy," + pip
	var lh []string
	if l.Cfg.LockHTTP() {
		// The gate, on the same address; lea-keyproxy (4400) is still reached directly.
		p, nested, err := l.lockHTTPAgentEnv(l.Cfg.ContainerName(proj), pip)
		if err != nil {
			return nil, err
		}
		proxy = p
		lh = append([]string{"-e", "LEA_LOCKHTTP=1", "-e", "LEA_NESTED_PROXY_TOKEN=" + nested}, l.lockHTTPAgentCA()...)
	}
	if err := noComma(proj, home, st, l.Root); err != nil {
		return nil, err
	}
	if err := Mkdirs(l.Cfg.DataDir(), st+"/ssh", st+"/git", st+"/gh", st+"/glab-cli", st+"/containers", st+"/go", st+"/kube"); err != nil {
		return nil, die("%v", err)
	}
	// No GitHub token where an agent reads it (#103): the proxy holds it.
	what, adopted, err := l.migrateGitHubToken()
	if err != nil {
		return nil, err
	}
	l.logGitHubMigration(what, adopted)
	rt, vm, err := l.agentRuntime()
	if err != nil {
		return nil, err
	}
	p := l.prefix()
	// The nested Podman's storage: a volume of its own, kept across restarts. Under Kata (#106) a
	// volume is a virtio-fs share, where the rootless Podman cannot unpack an image layer (its
	// chroot's pivot directory gets EACCES): a tmpfs inside the VM instead, its images pulled again
	// at each start. Buildah's working directory (/var/tmp, where a RUN step's root is made) is in
	// the image's file system, a virtio-fs share too (mkdir in the nested user namespace gets
	// EACCES): a tmpfs in the VM as well (#100).
	podmanStore := []string{"--mount", "type=volume,src=" + p + "-podman-" + id + ",dst=/lea/podman"}
	if vm {
		podmanStore = []string{"--mount", "type=tmpfs,dst=/lea/podman,tmpfs-mode=0755", "--mount", "type=tmpfs,dst=/var/tmp,tmpfs-mode=1777"}
	}
	a := []string{"-d", "--init", "--name", l.Cfg.ContainerName(proj), "--hostname", "lea",
		"--network", l.Cfg.InternalNetwork(id),
		// No DNS out (#98): the embedded resolver answers the names of the network's containers
		// (lea-proxy) and forwards the rest to 127.0.0.1 in this container, where nothing listens,
		// not to the host's resolver. The agent needs none: the proxy resolves the names it reaches.
		"--dns", dnsNowhere,
		"--label", "lea.prefix=" + p, "--label", "lea.role=session", "--label", "lea.projects=" + proj,
		"-e", "LEA_UID=" + uid, "-e", "LEA_GID=" + gid, "-e", "LEA_USER=" + l.containerUser(), "-e", "HOME=" + home,
		"-e", "LEA_PROJECTS=" + proj, "-e", "LEA_PROXY_IP=" + pip, "-e", "LEA_NESTED_NETWORKS=" + l.Cfg.Get("LEA_NESTED_NETWORKS"),
		"-e", "LEA_AGENT_CPUS=" + l.Cfg.Get("LEA_AGENT_CPUS"),
		"-e", "XDG_RUNTIME_DIR=/run/user/" + uid, "-e", "DOCKER_HOST=unix:///run/user/" + uid + "/podman/podman.sock",
		"-e", "HTTP_PROXY=" + proxy, "-e", "HTTPS_PROXY=" + proxy, "-e", "http_proxy=" + proxy, "-e", "https_proxy=" + proxy,
		"-e", "NO_PROXY=" + noProxy, "-e", "no_proxy=" + noProxy,
		"-e", "KUBECONFIG=" + home + "/.kube/lea/config",
		"-e", "LEA_GIT_SSH=" + git,
		"--mount", "type=bind,src=" + proj + ",dst=" + proj,
		"--mount", "type=bind,src=" + st + "/ssh,dst=/lea/state/ssh,readonly",
		"--mount", "type=bind,src=" + st + "/git,dst=/lea/state/git",
		"--mount", "type=bind,src=" + st + "/gh,dst=/lea/state/gh",
		"--mount", "type=bind,src=" + st + "/glab-cli,dst=/lea/state/glab-cli",
		"--mount", "type=bind,src=" + st + "/containers,dst=/lea/state/containers",
		"--mount", "type=bind,src=" + st + "/go,dst=/lea/state/go",
		"--mount", "type=bind,src=" + st + "/kube,dst=" + home + "/.kube/lea,readonly"}
	a = append(a, podmanStore...)
	// The Lock Family's directory (hub config, public keys, routing) read-only, as a Lock's profile directory
	// (lockArgs):
	// an agent may read it, never change what the locks and the hub trust.
	lf, err := lockfamilyDir(proj)
	if err != nil {
		return nil, err
	}
	a = append(a, "--mount", "type=bind,src="+lf+",dst="+lf+",readonly")
	// A Lock is on when the user's configuration turns it on, or lox run runs a lock of its type
	// and the user did not turn it off (Config.toolOn): a lock the user added is meant for the agents.
	ups := l.lockUpSockets(proj, lf, func(tool string) bool {
		if key := lockSwitch(tool); key != "" {
			return l.Cfg.toolOn(key, true)
		}
		return false
	})
	a = append(a, lh...)
	// Each Lock of locks.Registry: only the socket of a console in its own container, in the
	// lock's socket volume, and its clients group (lockhost.go).
	la, err := l.lockArgs(proj, ups)
	if err != nil {
		return nil, err
	}
	a = append(a, la...)
	l.lockCompatAtStart(proj, ups)
	a = append(a, rt...)
	a = append(a, l.securityArgs()...)
	a = append(a, l.limitArgs()...)
	for _, part := range []func() ([]string, error){
		func() ([]string, error) { return l.busArgs(proj) },
		func() ([]string, error) {
			if vm {
				// Kata (#106): a host FIFO does not cross into its VM, and the container would not start.
				l.logf("the clipboard is off under %s: a virtual machine runtime cannot share lea's clipboard FIFO; its agents cannot reach lea's bus (messaging) or the Lock consoles either: a host Unix socket does not cross into the VM", l.Cfg.Get("LEA_RUNTIME"))
				return nil, nil
			}
			return l.clipboardArgs()
		},
		func() ([]string, error) { return l.claudeMounts(home, proj, st) },
		func() ([]string, error) { return l.codexMounts(home, proj, st) },
	} {
		m, err := part()
		if err != nil {
			return nil, err
		}
		a = append(a, m...)
	}
	if err := l.noEngineSocket(a); err != nil {
		return nil, err
	}
	return append(a, l.Cfg.Get("LEA_IMAGE")), nil
}

// engineSockets are where a container engine of the host may listen: DOCKER_HOST's unix socket,
// Docker's default ones and a rootless Podman's or Docker's under XDG_RUNTIME_DIR.
func (l *Launcher) engineSockets() []string {
	s := []string{"/var/run/docker.sock", "/run/docker.sock", "/run/podman/podman.sock"}
	if p, ok := strings.CutPrefix(l.Cfg.Get("DOCKER_HOST"), "unix://"); ok && p != "" {
		s = append(s, p)
	}
	if run := l.Cfg.Get("XDG_RUNTIME_DIR"); run != "" {
		s = append(s, run+"/podman/podman.sock", run+"/docker.sock")
	}
	return s
}

// noEngineSocket refuses agent run args with a bind mount that is an engine's socket or a
// directory above one: whoever reaches the socket runs containers as the user, outside lea.
func (l *Launcher) noEngineSocket(args []string) error {
	socks := l.engineSockets()
	for i := 0; i+1 < len(args); i++ {
		if args[i] != "--mount" {
			continue
		}
		for _, f := range strings.Split(args[i+1], ",") {
			src, ok := strings.CutPrefix(f, "src=")
			if !ok {
				continue
			}
			for _, sock := range socks {
				if under(phys(src), phys(sock)) || under(src, sock) {
					return die("refusing to start: %s would give the agents the container engine's socket %s", src, sock)
				}
			}
		}
	}
	return nil
}

// waitReady is lea_wait_ready: 60 tries a second apart, then the container's last logs and its
// removal.
func (l *Launcher) waitReady(name string) error {
	for i := 0; l.quiet("docker", "exec", name, "test", "-e", "/run/lea/ready") != nil; {
		if l.isInterrupted() {
			return errInterrupted
		}
		i++
		if i >= 60 {
			c := l.cmd("docker", "logs", "--tail", "30", name)
			c.Stdout, c.Stderr = l.Stderr, l.Stderr // docker logs >&2
			_ = l.exec(c)
			_ = l.quiet("docker", "rm", "-f", name)
			return die("lea did not become ready in 60 s (container logs above; the container is removed)")
		}
		l.Sleep(time.Second)
	}
	return nil
}

// busAttach is lea_bus_attach: the daemon binds the container's bus socket.
func (l *Launcher) busAttach(proj string) error {
	return l.run(l.Core, "attach-container", l.Cfg.ContainerName(proj))
}

// locksqlHint says where the locksql console runs: under lox run only (lea starts none).
func (l *Launcher) locksqlHint() {
	l.logf("locksql console: it runs under lox run only; its queries wait for your approval in lox run, in another terminal (outside lea)")
}

// containerStart is lea_container_start: the run arguments, the start snapshot (lea-sh
// scan-begin), a fresh container, ready, its bus socket bound.
func (l *Launcher) containerStart(proj, pip string) error {
	name := l.Cfg.ContainerName(proj)
	args, err := l.runArgs(proj, pip)
	if err != nil {
		return err
	}
	if err := l.sh("scan-begin", proj); err != nil {
		if l.isInterrupted() {
			return errInterrupted
		}
		return die("the start snapshot failed (above): lea doctor checks the setup")
	}
	_ = l.quiet("docker", "rm", "-f", name)
	if err := l.run("docker", append([]string{"run"}, args...)...); err != nil {
		if l.isInterrupted() {
			return errInterrupted
		}
		return die("could not start the container %s (docker's message above): lea doctor checks the setup", name)
	}
	if err := l.waitReady(name); err != nil {
		return err
	}
	if l.busAttach(proj) != nil {
		if l.isInterrupted() {
			return errInterrupted
		}
		l.logf("warning: the container's bus socket could not be bound; agents run without lea messaging")
	}
	if l.Cfg.ToolsOn(proj, func(string, ...any) {})["locksql"] {
		l.locksqlHint()
	}
	return nil
}

// defaultTerm is the TERM a docker exec gives the container when the caller has none.
const defaultTerm = "xterm-256color"

// termEnv is the TERM a docker exec gives the container: the caller's, else defaultTerm.
func (l *Launcher) termEnv() string { return "TERM=" + nonEmpty(l.Cfg.Get("TERM"), defaultTerm) }

// termEnvShell is termEnv as a printed command line says it: the shell that runs the line
// expands it with the TERM of its own terminal, under the same rule.
const termEnvShell = `"TERM=${TERM:-` + defaultTerm + `}"`

// containerExec is lea_container_exec: docker exec on the launcher's terminal.
func (l *Launcher) containerExec(name, wd string, cmd ...string) error {
	tty := "-i"
	if IsTerminal(l.Stdin) && IsTerminal(l.Stdout) {
		tty = "-it"
	}
	args := append([]string{"exec", tty, "-u", uidgid(), "-e", l.termEnv(),
		"-w", wd, name}, cmd...)
	return l.foreground("docker", args...)
}

// agentsLive is lea__agents_live: the container runs deck agents.
func (l *Launcher) agentsLive(proj string) bool {
	live, err := l.agentsRunning(proj)
	return err == nil && live
}

// agentsRunning is whether the container runs deck agents, with the error of the docker exec that
// lists them: a caller about to cut them (containerQuiet) treats an error as agents running.
func (l *Launcher) agentsRunning(proj string) (bool, error) {
	out, err := l.output(false, "docker", "exec", "-u", uidgid(), l.Cfg.ContainerName(proj), "lea-agent", "ls")
	if err != nil {
		return false, err
	}
	return out != "", nil
}

// IsTerminal is `[ -t N ]` for the launcher's stdin or stdout.
func IsTerminal(x any) bool {
	f, ok := x.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(f.Fd())
}

// lockfamilyDir is the Lock Family's directory of the projects directory (<proj>/.lockfamily: the
// hub's config, the locks' public keys, the routing), made by lea, owned by the user on the host,
// when missing. The agents get it read-only, so none rewrites what the locks and the hub trust
// (the hub plan's I11); a link or a file is refused, as for a Lock's profile directory.
func lockfamilyDir(proj string) (string, error) {
	return roDir(proj+"/.lockfamily", "lox run writes there, on the host only")
}

// roDir makes d when missing and refuses a d that is not a directory of its own; why says who
// writes there.
func roDir(d, why string) (string, error) {
	if err := os.Mkdir(d, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return "", die("could not create %s: %v", d, err)
	}
	if fi, err := os.Lstat(d); err != nil || !fi.IsDir() {
		return "", die("%s is not a directory (a symbolic link or a file): %s; remove it and retry", d, why)
	}
	return d, nil
}
