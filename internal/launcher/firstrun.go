package launcher

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
)

// loxCheck is the lox lea needs to start the Lock Family hub: on PATH and at least the version
// of config/host-minimums. A dev build is not compared.
func (l *Launcher) loxCheck() error {
	out, err := l.output(false, "lox", "version")
	if err != nil {
		return errors.New("lox not found: lea starts the Lock Family hub with it: brew install davidgodefroid/tap/lox, then run lea again")
	}
	v := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(out), "lox"))
	if v == "dev" {
		l.logf("warning: lox dev: its version is not checked")
		return nil
	}
	if min := l.hostMinimum("lox"); min != "" && !versionAtLeast(v, min) {
		return fmt.Errorf("lox %s is older than %s, which lea needs to start the hub: brew upgrade lox, then run lea again", v, min)
	}
	return nil
}

// firstRun makes lea initialise itself: with no claude.json, no projects directory or a missing
// image, it checks Docker, checks the working directory when it must become the projects
// directory, then runs `lea init --first-run` and reads the configuration it wrote.
func (l *Launcher) firstRun() error {
	need := !isRegular(l.Cfg.StateDir()+"/claude.json") || l.Cfg.Get("LEA_PROJECTS_DIR") == ""
	for _, k := range []string{"LEA_IMAGE", "LEA_PROXY_IMAGE"} {
		if need {
			break
		}
		if _, err := l.output(false, "docker", "image", "inspect", "-f", "{{.Id}}", l.Cfg.Get(k)); err != nil {
			need = true
		}
	}
	if !need {
		return nil
	}
	if _, err := l.output(false, "docker", "version", "--format", "{{.Server.Version}}"); err != nil {
		hint := "start the Docker engine (sudo systemctl start docker), then run lea again"
		if runtime.GOOS == "darwin" {
			hint = "start Docker Desktop (open -a Docker), then run lea again"
		}
		return die("Docker does not answer: %s", hint)
	}
	if l.Cfg.Get("LEA_PROJECTS_DIR") == "" {
		if _, err := CheckProjectsDir(l.Cfg, l.Root, l.Cwd, l.Cwd); err != nil {
			return die("first run: %v: cd into the directory that holds your projects and run lea again, or LEA_PROJECTS_DIR=DIR lea", err)
		}
	}
	if err := l.sh("init", "--first-run"); err != nil {
		return err
	}
	cfg, err := LoadConfig(l.Cfg.getenv, l.Root, l.Cwd)
	if err != nil {
		return err
	}
	l.Cfg = cfg
	return nil
}
