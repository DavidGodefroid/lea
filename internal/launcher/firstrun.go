package launcher

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// noHub is the escape every refusal about lox names: lea without the Lock Family hub.
const noHub = " (LEA_HUB=0 lea runs without the hub)"

// loxCheck is the lox lea needs to start the Lock Family hub: on PATH and at least the version
// of config/host-minimums. A dev build is not compared.
func (l *Launcher) loxCheck() error {
	const reinstall = "reinstall lox (brew reinstall davidgodefroid/tap/lox), then run lea again" + noHub
	c := l.cmd("lox", "version")
	var o bytes.Buffer
	c.Stdout, c.Stderr = &o, l.Stderr
	if err := l.exec(c); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return errors.New("lox not found: lea starts the Lock Family hub with it: brew install davidgodefroid/tap/lox, then run lea again" + noHub)
		}
		return errors.New("lox version failed (above): " + reinstall)
	}
	v := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(o.String()), "lox"))
	if v == "" {
		return errors.New("lox version printed no version: " + reinstall)
	}
	if v == "dev" {
		l.logf("warning: lox dev: its version is not checked")
		return nil
	}
	if min := l.hostMinimum("lox"); min != "" && !versionAtLeast(v, min) {
		return fmt.Errorf("lox %s is older than %s, which lea needs to start the hub: brew upgrade lox, then run lea again"+noHub, v, min)
	}
	return nil
}

// hostOS is the host's operating system, a variable so a test can try both Docker hints.
var hostOS = runtime.GOOS

// refuseEarly is what refuses without Docker, whatever the machine's state: the deck's claude
// arguments, a missing tmux (deck or new) and, once a projects directory is set, its guards and
// a planted symlink. A machine that is not set up gets these answers before its first run.
func (l *Launcher) refuseEarly(deckArgs []string, tmux bool) error {
	if err := deckArgsRefused(deckArgs); err != nil {
		return err
	}
	if tmux {
		if err := l.tmuxCheck(); err != nil {
			return err
		}
	}
	if p := l.Cfg.Get("LEA_PROJECTS_DIR"); p != "" && isDir(p) {
		p = physDir(p)
		if err := l.guardProjects(p); err != nil {
			return err
		}
		return l.checkPaths(p)
	}
	return nil
}

// firstRun makes lea initialise itself. With no claude.json or no projects directory it checks
// Docker, checks the working directory when it must become the projects directory, then runs
// `lea init --first-run`. On a machine that is set up, a missing image is only pulled and verified.
// The configuration is read again afterwards.
func (l *Launcher) firstRun() error {
	full := !isRegular(l.Cfg.StateDir()+"/claude.json") || l.Cfg.Get("LEA_PROJECTS_DIR") == ""
	missing := false
	for _, k := range []string{"LEA_IMAGE", "LEA_PROXY_IMAGE"} {
		if full || missing {
			break
		}
		if _, err := l.output(false, "docker", "image", "inspect", "-f", "{{.Id}}", l.Cfg.Get(k)); err != nil {
			missing = true
		}
	}
	if !full && !missing {
		return nil
	}
	if _, err := l.output(false, "docker", "version", "--format", "{{.Server.Version}}"); err != nil {
		hint := "start the Docker engine (sudo systemctl start docker), then run lea again"
		if hostOS == "darwin" {
			hint = "start Docker Desktop (open -a Docker), then run lea again"
		}
		return die("Docker does not answer: %s", hint)
	}
	if full {
		if l.Cfg.Get("LEA_PROJECTS_DIR") == "" {
			if _, err := CheckProjectsDir(l.Cfg, l.Root, l.Cwd, l.Cwd); err != nil {
				return die("first run: %v: cd into the directory that holds your projects and run lea again, or LEA_PROJECTS_DIR=DIR lea", err)
			}
		}
		if err := l.sh("init", "--first-run"); err != nil {
			return err
		}
	} else if err := l.sh("ensure-images"); err != nil {
		return err
	}
	cfg, err := LoadConfig(l.Cfg.getenv, l.Root, l.Cwd)
	if err != nil {
		return err
	}
	l.Cfg = cfg
	return nil
}

// ensureHub starts the workspace's Lock Family hub before lea starts: `lox init` when it has none
// (loxInit), then, with LEA_LOX_UP=1, `lox up` when its hub or a lock's console is down (loxUp).
// Last it checks every lock up.json declares (consolesReady), so a console that waits for a
// human is said, and answered, on the terminal where lea starts.
func (l *Launcher) ensureHub(proj string) error {
	if err := l.loxInit(proj); err != nil {
		return err
	}
	l.loxUp(proj)
	l.consolesReady(proj)
	return nil
}

// loxInit starts the workspace's Lock Family hub with `lox init` when it has none: LEA_HUB is on
// and proj/.lockfamily/up.json is missing. lox init runs in the foreground, on the user's terminal.
func (l *Launcher) loxInit(proj string) error {
	if l.Cfg.Get("LEA_HUB") != "1" {
		return nil
	}
	if _, err := os.Lstat(proj + "/.lockfamily/up.json"); err == nil {
		return nil
	}
	if err := l.loxCheck(); err != nil {
		return die("%v", err)
	}
	l.logf("first run: starting the Lock Family hub (lox init)")
	c := l.cmd("lox", "init")
	c.Dir = proj
	c.Stdin, c.Stdout, c.Stderr = l.Stdin, l.Stdout, l.Stderr
	if err := l.exec(c); err != nil {
		return die("lox init failed (above): fix it, then run lea again" + noHub)
	}
	return nil
}

// loxUp runs `lox up` for the projects directory before lea starts, when the user opted in
// (LEA_LOX_UP=1), proj/.lockfamily/lockfamily.yaml exists and no hub of proj runs or a lock of
// up.json has no running console (after a reboot, a lox down, or a console that stopped). lox up
// runs in the foreground, on the user's terminal, where it reviews any change: lea's agents never
// start a console, the human who runs lea does. A failure only warns: lea starts anyway.
func (l *Launcher) loxUp(proj string) {
	if l.Cfg.Get("LEA_LOX_UP") != "1" || !isRegular(proj+"/.lockfamily/lockfamily.yaml") {
		return
	}
	before, err := l.loxServices(proj)
	if err != nil {
		l.logf("warning: LEA_LOX_UP: cannot list the Lock Family's containers (docker ps failed): lox up not run")
		return
	}
	down := before["lockhub"] == ""
	for _, lk := range l.upLocks(proj) {
		down = down || before["lock-"+lk.Instance] == ""
	}
	if !down {
		return
	}
	l.logf("starting the Lock Family hub and its locks (lox up, LEA_LOX_UP=1)")
	c := l.cmd("lox", "up")
	c.Dir = proj
	c.Stdin, c.Stdout, c.Stderr = l.Stdin, l.Stdout, l.Stderr
	if err := l.exec(c); err != nil {
		l.logf("warning: lox up failed (above): lea starts without it; fix it, then run lox up")
	}
}

// upLocks are the locks of up.json an agent container started now would mount (Config.UpLocks),
// without its warnings: the container's start says them.
func (l *Launcher) upLocks(proj string) []UpLock {
	return l.Cfg.UpLocks(proj, func(string, ...any) {})
}

// consoleGrace is how long a console that runs but does not listen yet may take to start before
// lea says it waits for a human; consoleStep is how often lea looks again meanwhile.
var consoleGrace, consoleStep = 3 * time.Second, 300 * time.Millisecond

// atTerminal is whether lea may ask its user a question: stdin and stdout are a terminal.
var atTerminal = func(l *Launcher) bool { return IsTerminal(l.Stdin) && IsTerminal(l.Stdout) }

// consolesReady checks every lock of up.json lea's agents would use (#259): its console must run
// (else: run lox up) and listen on its socket. A console that runs and does not listen waits for
// its start question (a production profile's name, a first start's policy review, a credential
// Vault does not give): nobody is attached to it after a restart. At a terminal lea offers to
// answer it there with `lox attach`, then checks again; elsewhere it names the command. lea
// starts anyway: an agent then gets "no console" from that lock.
func (l *Launcher) consolesReady(proj string) {
	locks := l.upLocks(proj)
	if len(locks) == 0 {
		return
	}
	svcs, err := l.loxServices(proj)
	if err != nil {
		l.logf("warning: cannot list the Lock Family's containers (docker ps failed): lea does not know whether its locks serve")
		return
	}
	var running []UpLock
	for _, lk := range locks {
		if svcs["lock-"+lk.Instance] == "" {
			l.logf("%s %s: its console is not running: run lox up", lk.Type, lk.Instance)
			continue
		}
		running = append(running, lk)
	}
	waiting := l.notListening(running, svcs)
	for i, lk := range waiting {
		name := lk.Instance
		if len(waiting) > 1 {
			name = fmt.Sprintf("%s (%d of %d)", lk.Instance, i+1, len(waiting))
		}
		l.logf("%s %s waits for its start question (its console runs but does not serve yet): lox attach %s", lk.Type, name, lk.Instance)
		if !atTerminal(l) || l.attachConsole(proj, lk, svcs["lock-"+lk.Instance]) && len(l.notListening([]UpLock{lk}, svcs)) == 0 {
			continue
		}
		l.logf("%s %s still waits for its start question: lea starts without it; lox attach %s to answer it", lk.Type, lk.Instance, lk.Instance)
	}
}

// attachConsole asks the user whether to answer lk's start question now and, on yes (the
// default), runs `lox attach` on lea's terminal; false when declined or the attach failed. At a
// real terminal lea detaches by itself once the console (container id) serves (#282); the user
// may still detach sooner with Ctrl-P Ctrl-Q.
func (l *Launcher) attachConsole(proj string, lk UpLock, id string) bool {
	fmt.Fprintf(l.Stderr, "lea: answer it now with lox attach %s? Its question is not shown again: type what it waits for "+
		"(a production lock: its profile name), then Enter: lea detaches by itself once it serves (Ctrl-P Ctrl-Q detaches sooner) [Y/n] ", lk.Instance)
	a, _ := bufio.NewReader(l.Stdin).ReadString('\n')
	if a = strings.ToLower(strings.TrimSpace(a)); a != "" && a != "y" && a != "yes" {
		return false
	}
	c := l.cmd("lox", "attach", lk.Instance)
	c.Dir = proj
	err := errNoPty
	served := false
	in, inOK := l.Stdin.(*os.File)
	out, outOK := l.Stdout.(*os.File)
	if inOK && outOK && IsTerminal(in) && IsTerminal(out) {
		served, err = l.attachUntilServes(c, in, out, func() bool { return l.consoleListens(id, lk.Type) })
	}
	if errors.Is(err, errNoPty) {
		c.Stdin, c.Stdout, c.Stderr = l.Stdin, l.Stdout, l.Stderr
		err = l.exec(c)
	}
	if err != nil && !served {
		l.logf("warning: lox attach %s failed (above)", lk.Instance)
		return false
	}
	if served {
		l.logf("%s %s serves: lea detached from its console, which keeps running", lk.Type, lk.Instance)
	}
	return true
}

// notListening are the locks of locks whose console (its container in svcs, by compose service)
// does not listen on a socket of its tool, once consoleGrace has passed for those still starting.
func (l *Launcher) notListening(locks []UpLock, svcs map[string]string) []UpLock {
	deadline := time.Now().Add(consoleGrace)
	for {
		var out []UpLock
		for _, lk := range locks {
			if !l.consoleListens(svcs["lock-"+lk.Instance], lk.Type) {
				out = append(out, lk)
			}
		}
		if len(out) == 0 || !time.Now().Before(deadline) {
			return out
		}
		locks = out
		time.Sleep(consoleStep)
	}
}

// consoleListens is whether the console container id listens on a unix socket of tool, from the
// kernel's table of its network namespace: a socket file in the volume may be a dead console's.
func (l *Launcher) consoleListens(id, tool string) bool {
	out, err := l.output(false, "docker", "exec", id, "cat", "/proc/net/unix")
	return err == nil && unixListening(out, tool)
}

// unixListening is whether /proc/net/unix (table) holds a listening socket in /run/<tool>/: the
// flag __SO_ACCEPTCON (00010000), state 01.
func unixListening(table, tool string) bool {
	for _, ln := range strings.Split(table, "\n") {
		f := strings.Fields(ln)
		if len(f) == 8 && f[3] == "00010000" && f[5] == "01" && strings.HasPrefix(f[7], "/run/"+tool+"/") {
			return true
		}
	}
	return false
}

// loxServices is the running containers of lox up's compose project for proj: container ID by
// compose service (lockhub, lock-<instance>).
func (l *Launcher) loxServices(proj string) (map[string]string, error) {
	out, err := l.output(false, "docker", "ps", "--filter", "label=com.docker.compose.project="+lockUpComposeProject(proj),
		"--format", `{{.Label "com.docker.compose.service"}}={{.ID}}`)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, f := range strings.Fields(out) {
		if svc, id, ok := strings.Cut(f, "="); ok && svc != "" && id != "" {
			m[svc] = id
		}
	}
	return m, nil
}
