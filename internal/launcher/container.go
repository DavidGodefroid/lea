package launcher

import (
	"errors"
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

// claudeShared is LEA_CLAUDE_SHARED: the ~/.claude entries mounted read-only.
var claudeShared = []string{"settings.json", "keybindings.json", "hooks", "skills", "plugins", "mods", "dev-mods",
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
	return r
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
func (l *Launcher) limitArgs() []string { return l.resolvedLimits().args() }

// sizeBytes is a docker size (dockerSize: 512m, 8g, 1024) in bytes, or -1 as is.
func sizeBytes(s string) int64 {
	if s == "-1" {
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

// applyLimits gives a running lea container the configured limits it lacks (#132): one started
// by an older lea, or before a limit changed, ran without them until it restarted. Docker and
// Podman both change them in place (docker update); a limit turned off is only taken away by a
// restart. When the engine refuses (rootless Podman without the cpu or memory controller), a
// warning says how to apply them.
func (l *Launcher) applyLimits(name string) {
	out, err := l.output(false, "docker", "inspect", "--format",
		"{{.HostConfig.NanoCpus}} {{.HostConfig.Memory}} {{.HostConfig.MemorySwap}} {{.HostConfig.PidsLimit}}", name)
	f := strings.Fields(out)
	if err != nil || len(f) != 4 {
		return
	}
	n := make([]int64, 4)
	for i, v := range f {
		if v == "<nil>" {
			continue
		}
		x, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return
		}
		n[i] = x
	}
	want := l.resolvedLimits()
	var fix limits
	if want.cpus != "" {
		c, _ := strconv.ParseFloat(want.cpus, 64)
		if n[0] != int64(math.Round(c*1e9)) {
			fix.cpus = want.cpus
		}
	}
	if want.mem != "" && (n[1] != sizeBytes(want.mem) || n[2] != sizeBytes(want.swap)) {
		fix.mem, fix.swap = want.mem, want.swap
	}
	if want.pids != "" && strconv.FormatInt(n[3], 10) != want.pids {
		fix.pids = want.pids
	}
	a := fix.args()
	if len(a) == 0 {
		return
	}
	if l.quiet("docker", append(append([]string{"update"}, a...), name)...) != nil {
		l.logf("warning: could not apply the limits %s to the running %s; run 'lea stop', then lea, to restart it with them",
			strings.Join(a, " "), name)
		return
	}
	l.logf("applied the limits %s to the running %s", strings.Join(a, " "), name)
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

// pluginsData lays a private read-write plugins/data over the read-only ~/.claude/plugins: Claude
// Code mkdirs each plugin's CLAUDE_PLUGIN_DATA there before running its hooks (#90). The mount point
// must exist in the host's plugins; when lea cannot make it, it says so and the agent starts without.
func (l *Launcher) pluginsData(src, priv, sbase string) ([]string, error) {
	p := src + "/plugins"
	if !isDir(p) || isSymlink(p) {
		return nil, nil
	}
	if err := Mkdirs(src, p+"/data"); err != nil {
		l.logf("no writable %s/data: %v", p, err)
		return nil, nil
	}
	if err := Mkdirs(sbase, priv+"/plugins-data"); err != nil {
		return nil, die("%v", err)
	}
	return []string{"--mount", "type=bind,src=" + priv + "/plugins-data,dst=" + p + "/data"}, nil
}

// claudeMounts is lea_claude_mounts: a private ~/.claude per projects dir, the allow-list of
// host entries read-only, the project's transcripts and lea's own .claude.json.
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
	m, err := l.pluginsData(src, priv, sbase)
	if err != nil {
		return nil, err
	}
	a = append(a, m...)
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
	if err := noComma(proj, home, st, l.Root); err != nil {
		return nil, err
	}
	if err := Mkdirs(l.Cfg.DataDir(), st+"/ssh", st+"/git", st+"/gh", st+"/glab-cli", st+"/containers", st+"/go", st+"/kube"); err != nil {
		return nil, die("%v", err)
	}
	p := l.prefix()
	a := []string{"-d", "--init", "--name", l.Cfg.ContainerName(proj), "--hostname", "lea",
		"--network", l.Cfg.InternalNetwork(id),
		"--label", "lea.prefix=" + p, "--label", "lea.role=session", "--label", "lea.projects=" + proj,
		"-e", "LEA_UID=" + uid, "-e", "LEA_GID=" + gid, "-e", "LEA_USER=" + l.containerUser(), "-e", "HOME=" + home,
		"-e", "LEA_PROJECTS=" + proj, "-e", "LEA_PROXY_IP=" + pip, "-e", "LEA_NESTED_NETWORKS=" + l.Cfg.Get("LEA_NESTED_NETWORKS"),
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
		"--mount", "type=bind,src=" + st + "/kube,dst=" + home + "/.kube/lea,readonly",
		"--mount", "type=volume,src=" + p + "-podman-" + id + ",dst=/lea/podman"}
	if l.Cfg.Locksql() {
		// The locksql console's socket volume, and its client group (locksql-clients). Its own
		// directory read-only (locksqlDir).
		d, err := locksqlDir(proj)
		if err != nil {
			return nil, err
		}
		a = append(a, "-e", "LEA_LOCKSQL=1", "--group-add", "10000", "--mount", "type=volume,src=locksql-run,dst=/run/locksql",
			"--mount", "type=bind,src="+d+",dst="+d+",readonly")
	}
	// lockmq and lockfile: only the socket of a console on the host (lockhost.go).
	a = append(a, l.lockHostArgs()...)
	a = append(a, l.securityArgs()...)
	a = append(a, l.limitArgs()...)
	for _, part := range []func() ([]string, error){
		func() ([]string, error) { return l.busArgs(proj) },
		l.clipboardArgs,
		func() ([]string, error) { return l.claudeMounts(home, proj, st) },
		func() ([]string, error) { return l.codexMounts(home, proj, st) },
	} {
		m, err := part()
		if err != nil {
			return nil, err
		}
		a = append(a, m...)
	}
	return append(a, l.Cfg.Get("LEA_IMAGE")), nil
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

// locksqlHint names the command that starts the locksql console (libexec/lib/locksql.sh).
func (l *Launcher) locksqlHint() {
	l.logf("locksql console: run locksql (or lea locksql) in a terminal you keep in view")
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
		return exitError{1}
	}
	_ = l.quiet("docker", "rm", "-f", name)
	if err := l.run("docker", append([]string{"run"}, args...)...); err != nil {
		if l.isInterrupted() {
			return errInterrupted
		}
		return exitError{1}
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
	if l.Cfg.Locksql() {
		l.locksqlHint()
	}
	return nil
}

// containerExec is lea_container_exec: docker exec on the launcher's terminal.
func (l *Launcher) containerExec(name, wd string, cmd ...string) error {
	tty := "-i"
	if IsTerminal(l.Stdin) && IsTerminal(l.Stdout) {
		tty = "-it"
	}
	args := append([]string{"exec", tty, "-u", uidgid(), "-e", "TERM=" + nonEmpty(l.Cfg.Get("TERM"), "xterm-256color"),
		"-w", wd, name}, cmd...)
	return l.foreground("docker", args...)
}

// agentsLive is lea__agents_live: the container runs deck agents.
func (l *Launcher) agentsLive(proj string) bool {
	out, err := l.output(false, "docker", "exec", "-u", uidgid(), l.Cfg.ContainerName(proj), "lea-agent", "ls")
	return err == nil && out != ""
}

// IsTerminal is `[ -t N ]` for the launcher's stdin or stdout.
func IsTerminal(x any) bool {
	f, ok := x.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(f.Fd())
}

// locksqlDir is the locksql console's own directory of the projects directory (<proj>/.locksql:
// its config and PII rules), made when missing. The agents get it read-only: a mount point cannot
// be removed, renamed or replaced, so no agent swaps it for a symbolic link, which the console's
// read-write bind mount of it would follow on this host (#121). A link or a file is refused.
func locksqlDir(proj string) (string, error) {
	d := proj + "/.locksql"
	if err := os.Mkdir(d, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return "", die("could not create %s: %v", d, err)
	}
	if fi, err := os.Lstat(d); err != nil || !fi.IsDir() {
		return "", die("%s is not a directory (a symbolic link or a file): the locksql console only writes there; remove it and retry", d)
	}
	return d, nil
}
