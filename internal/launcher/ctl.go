package launcher

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// lea-ctl (#165): with LEA_CTL=1, lea itself runs in a control container: the daemon, the tmux
// server and the deck. It alone gets the Docker socket (no agent container ever does). Every host
// path lea reads or gives an agent container is mounted at the same absolute path, so the binds
// it asks the Docker engine for resolve on the host. The hub's facade socket directory comes in
// the same way; lockhub never runs in it.

// CtlName is the control container's name: <prefix>-ctl.
func (l *Launcher) CtlName() string { return l.prefix() + "-ctl" }

// ctlRunArgs are the docker run arguments of the control container for the projects
// directories, with the engine's socket sock (whose group is sockGID).
func (l *Launcher) ctlRunArgs(projects []string, sock string, sockGID int) ([]string, error) {
	home, data := l.Cfg.Get("HOME"), l.Cfg.DataDir()
	projects = append([]string(nil), projects...)
	sort.Strings(projects)
	if err := noComma(append([]string{sock, l.Root, data, home}, projects...)...); err != nil {
		return nil, err
	}
	a := []string{"run", "-d", "--init", "--name", l.CtlName(), "--restart", "unless-stopped",
		"--label", "lea.prefix=" + l.prefix(), "--label", "lea.role=ctl",
		"--label", "lea.projects=" + strings.Join(projects, ":"),
		"-u", uidgid(), "--group-add", fmt.Sprint(sockGID),
		// The pressure gauge reads the host's /proc and each agent container's cgroup by its host
		// pid (lea-ctl holds the engine's socket: it is the host's already).
		"--pid=host", "--cgroupns=host",
		"--mount", "type=bind,src=" + sock + ",dst=" + sock,
		// $HOME is the user's own (the engine would create the parents of the binds below it,
		// owned by root); only what is bound below comes from the host.
		"--tmpfs", home + ":rw,nosuid,nodev,uid=" + fmt.Sprint(os.Getuid()) + ",gid=" + fmt.Sprint(os.Getgid()) + ",mode=0700"}
	bind := func(p string, ro bool) {
		m := "type=bind,src=" + p + ",dst=" + p
		if ro {
			m += ",readonly"
		}
		a = append(a, "--mount", m)
	}
	// The host's accounts, read-only: lea and tmux name the user (id -un, tmux-<uid>), as on the
	// host (Linux).
	bind("/etc/passwd", true)
	bind("/etc/group", true)
	// lea's install (read-only: the launcher's auto-update runs on the host), its data, the
	// projects directories.
	bind(l.Root, true)
	if err := os.MkdirAll(data, 0o700); err != nil {
		return nil, err
	}
	bind(data, false)
	// ~/.claude: lea creates each projects dir's projects/<slug> there and gives it to the agent
	// container, as on the host.
	if err := os.MkdirAll(home+"/.claude", 0o700); err != nil {
		return nil, err
	}
	bind(home+"/.claude", false)
	for _, p := range projects {
		bind(p, false)
	}
	// What lea reads but never writes: the user's configuration, ~/.claude.json and ~/.codex. A path missing on the host is left out (the engine would
	// create it, owned by root).
	opt := []string{filepath.Dir(l.Cfg.UserConfigPath()), home + "/.claude.json", home + "/.codex"}
	for _, p := range opt {
		if _, err := os.Lstat(p); err == nil && !strings.Contains(p, ",") {
			bind(p, true)
		}
	}
	env := []string{"HOME=" + home, "XDG_DATA_HOME=" + nonEmpty(l.Cfg.Get("XDG_DATA_HOME"), home+"/.local/share"),
		"XDG_CONFIG_HOME=" + nonEmpty(l.Cfg.Get("XDG_CONFIG_HOME"), home+"/.config"), "LEA_CTL_INSIDE=1",
		// No desktop session in lea-ctl: no clipboard loop (sounds are the terminal bell).
		"LEA_CLIPBOARD=0"}
	// The hub's facade socket, for the deck and the decision sync (never the hub's network).
	if run := l.Cfg.Get("XDG_RUNTIME_DIR"); run != "" {
		env = append(env, "XDG_RUNTIME_DIR="+run)
		if fi, err := os.Stat(run + "/lockhub"); err == nil && fi.IsDir() && !strings.Contains(run, ",") {
			bind(run+"/lockhub", false)
		}
	}
	for _, e := range env {
		a = append(a, "-e", e)
	}
	return append(a, l.Cfg.Get("LEA_CTL_IMAGE")), nil
}

// ctlHostOnly are the commands that stay on the host with LEA_CTL=1: they install, configure or
// update lea itself (lea-ctl mounts the install and the configuration read-only).
var ctlHostOnly = map[string]bool{"-h": true, "--help": true, "help": true, "init": true, "config": true,
	"update": true, "rollback": true, "doctor": true, "ctl": true}

// ctl is lea ctl up|down|attach: lea-ctl by hand (debugging).
func (l *Launcher) ctl(args []string) error {
	if len(args) != 1 {
		l.logf("error: usage: lea ctl up|down|attach")
		return exitError{2}
	}
	switch args[0] {
	case "up":
		proj, err := l.projectsDir()
		if err != nil {
			return err
		}
		return l.ensureCtl(proj)
	case "down":
		return l.quiet("docker", "rm", "-f", l.CtlName())
	case "attach":
		return l.ctlExec([]string{"tmux", "-L", "lea", "attach"})
	}
	l.logf("error: usage: lea ctl up|down|attach")
	return exitError{2}
}

// configCtl is lea config ctl on|off: LEA_CTL in the user configuration.
func (l *Launcher) configCtl(args []string) error {
	v := map[string]string{"on": "1", "off": "0"}
	if len(args) != 1 || v[args[0]] == "" {
		l.logf("error: usage: lea config ctl on|off")
		return exitError{2}
	}
	if err := WriteUserConfig(l.Cfg.UserConfigFile(l.Cwd), "LEA_CTL", v[args[0]]); err != nil {
		return die("%v", err)
	}
	l.logf("LEA_CTL=%s: lea runs %s from the next command (lea stop --all first to move running agents)",
		v[args[0]], map[string]string{"1": "in lea-ctl", "0": "on the host"}[v[args[0]]])
	return nil
}

// viaCtl runs lea's command args in lea-ctl, started first if needed, at the same working
// directory, on this terminal.
func (l *Launcher) viaCtl(args []string) error {
	proj, err := l.projectsDir()
	if err != nil {
		return err
	}
	if err := l.ensureCtl(proj); err != nil {
		return err
	}
	return l.ctlExec(append([]string{l.Core}, args...))
}

// ctlEnv are the variables but LEA_* lea in lea-ctl takes from the host's lea.
var ctlEnv = map[string]bool{"TERM": true, "COLORTERM": true, "LANG": true, "LC_ALL": true,
	"XDG_DATA_HOME": true, "XDG_CONFIG_HOME": true, "XDG_RUNTIME_DIR": true}

// ctlExec runs argv in lea-ctl on this terminal, at the same working directory, as the user.
func (l *Launcher) ctlExec(cmd []string) error {
	docker, err := exec.LookPath("docker")
	if err != nil {
		return die("lea-ctl needs the docker CLI on the host")
	}
	flags := "-i"
	if IsTerminal(l.Stdin) && IsTerminal(l.Stdout) {
		flags = "-it"
	}
	argv := []string{docker, "exec", flags, "-u", uidgid(), "-w", l.Cwd}
	// The environment lea reads its configuration from (LEA_*, XDG_*, the terminal's): -e KEY
	// takes the value from the docker CLI's environment, so none shows on its command line.
	for _, kv := range l.Environ {
		k, _, _ := strings.Cut(kv, "=")
		if k != "LEA_CTL_INSIDE" && (strings.HasPrefix(k, "LEA_") || ctlEnv[k]) {
			argv = append(argv, "-e", k)
		}
	}
	argv = append(append(argv, l.CtlName()), cmd...)
	if l.refused() {
		return errInterrupted
	}
	return l.Exec(argv[0], argv, l.env())
}

// ensureCtl starts lea-ctl unless it runs for proj with the current LEA_CTL_IMAGE. Replacing it
// ends its tmux server (the agents' containers keep running; the deck opens them again).
func (l *Launcher) ensureCtl(proj string) error {
	name, image := l.CtlName(), l.Cfg.Get("LEA_CTL_IMAGE")
	got, _ := l.output(false, "docker", "inspect", "-f", `{{.State.Running}}|{{index .Config.Labels "lea.projects"}}|{{.Image}}`, name)
	if f := strings.Split(got, "|"); len(f) == 3 && f[0] == "true" {
		current, err := l.output(false, "docker", "image", "inspect", "-f", "{{.Id}}", image)
		if err != nil || current == "" {
			current = f[2]
		}
		if f[1] == proj && strings.TrimPrefix(f[2], "sha256:") == strings.TrimPrefix(current, "sha256:") {
			return nil
		}
		l.logf("restarting %s for %s and the image %s", name, proj, image)
	}
	sock, gid, err := l.engineSocket()
	if err != nil {
		return err
	}
	a, err := l.ctlRunArgs([]string{proj}, sock, gid)
	if err != nil {
		return err
	}
	_ = l.quiet("docker", "rm", "-f", name)
	return l.run("docker", a...)
}

// engineSocket is the Docker engine's unix socket (DOCKER_HOST, else /var/run/docker.sock) and
// its group.
func (l *Launcher) engineSocket() (string, int, error) {
	sock := "/var/run/docker.sock"
	if h := l.Cfg.Get("DOCKER_HOST"); h != "" {
		p, ok := strings.CutPrefix(h, "unix://")
		if !ok {
			return "", 0, die("lea-ctl needs the engine's unix socket, not DOCKER_HOST=%s", h)
		}
		sock = p
	}
	fi, err := os.Stat(sock)
	if err != nil {
		return "", 0, die("no Docker engine socket at %s", sock)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "", 0, die("cannot read the group of %s", sock)
	}
	return sock, int(st.Gid), nil
}
