package launcher

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
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
// (loxInit), then, with LEA_LOX_UP=1, `lox up` when its hub is down (loxUp).
func (l *Launcher) ensureHub(proj string) error {
	if err := l.loxInit(proj); err != nil {
		return err
	}
	l.loxUp(proj)
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
// (LEA_LOX_UP=1), proj/.lockfamily/lockfamily.yaml exists and no hub of proj runs (after a reboot
// or a lox down). lox up runs in the foreground, on the user's terminal, where it reviews any
// change: lea's agents never start a console, the human who runs lea does. A failure only warns:
// lea starts anyway. Each console lox up started or recreated waits for its start questions (its
// policy, the credentials it does not take from Vault), which lea lists with their lox attach.
func (l *Launcher) loxUp(proj string) {
	if l.Cfg.Get("LEA_LOX_UP") != "1" || !isRegular(proj+"/.lockfamily/lockfamily.yaml") {
		return
	}
	before, err := l.loxServices(proj)
	if err != nil {
		l.logf("warning: LEA_LOX_UP: cannot list the Lock Family's containers (docker ps failed): lox up not run")
		return
	}
	if before["lockhub"] != "" {
		return
	}
	l.logf("starting the Lock Family hub and its locks (lox up, LEA_LOX_UP=1)")
	c := l.cmd("lox", "up")
	c.Dir = proj
	c.Stdin, c.Stdout, c.Stderr = l.Stdin, l.Stdout, l.Stderr
	if err := l.exec(c); err != nil {
		l.logf("warning: lox up failed (above): lea starts without it; fix it, then run lox up")
		return
	}
	after, err := l.loxServices(proj)
	if err != nil {
		return
	}
	types := map[string]string{}
	if s, _ := l.readLockUp(proj, proj+"/.lockfamily"); s != nil {
		for n, lk := range s.Locks {
			types[n] = lk.Type
		}
	}
	var insts []string
	for svc, id := range after {
		if inst, ok := strings.CutPrefix(svc, "lock-"); ok && before[svc] != id {
			insts = append(insts, inst)
		}
	}
	sort.Strings(insts)
	for _, n := range insts {
		t := types[n]
		if t == "" {
			t = "lock"
		}
		l.logf("%s %s waits for its start questions: lox attach %s", t, n, n)
	}
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
