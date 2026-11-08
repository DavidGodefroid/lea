package launcher

import "runtime"

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
		// No LEA_DECK configured: lea init asks the deck question, on a terminal (#335).
		if l.Cfg.DeckUnset() {
			l.Cfg.Export("LEA_DECK_ASK", "1")
		}
		if err := l.sh("init", "--first-run"); err != nil {
			return err
		}
	} else if err := l.sh("ensure-images"); err != nil {
		return err
	}
	return l.reloadConfig()
}

// reloadConfig reads the configuration again, after lea-sh wrote the user config.
func (l *Launcher) reloadConfig() error {
	cfg, err := LoadConfig(l.Cfg.getenv, l.Root, l.Cwd)
	if err != nil {
		return err
	}
	l.Cfg = cfg
	return nil
}

// deckTerminal is whether lea talks to a terminal it can ask the deck question on, a variable so
// a test can have one.
var deckTerminal = func(l *Launcher) bool { return IsTerminal(l.Stdin) && IsTerminal(l.Stderr) }

// deckQuestion asks a lea with no LEA_DECK in its configuration (a new machine, or an install
// made before the deck was optional, #335) whether it wants the deck or its own tool, once, on a
// terminal, before anything depends on it: lea-sh deck-question records
// the answer (the end of input keeps the deck, recorded too), and the configuration is read again
// so that this lea applies it. Without a terminal it asks nothing: the deck, as before.
func (l *Launcher) deckQuestion() error {
	if !l.Cfg.DeckUnset() || !deckTerminal(l) {
		return nil
	}
	l.Cfg.Export("LEA_DECK_ASK", "1")
	if err := l.sh("deck-question"); err != nil {
		return err
	}
	return l.reloadConfig()
}

// DeckUnset reports whether neither the environment nor the user configuration sets LEA_DECK
// (config/lea.conf's default is no choice): the deck question was never answered.
func (c *Config) DeckUnset() bool {
	if c.getenv("LEA_DECK") != "" {
		return false
	}
	_, f, ok := c.Assignment("LEA_DECK")
	return !ok || f == c.defaults
}

// ensureHub is what lea checks of the Lock Family before it starts (#300): it runs no lox
// command (approvals live in lox run, a terminal of its own outside lea, never in lea). When the
// workspace declares locks and no lox run serves their decisions, lea says to run it, then starts
// anyway: the agents' Lock requests wait at the hub until lox run decides them. A LEA_LOX_UP
// still set is obsolete: lea says so and ignores it.
func (l *Launcher) ensureHub(proj string) error {
	if v := l.Cfg.Get("LEA_LOX_UP"); v != "" {
		l.logf("warning: LEA_LOX_UP=%s is obsolete and ignored: lea no longer runs lox up; remove it from your configuration (lox run, in another terminal, starts the hub and the locks)", v)
	}
	if !l.Cfg.HasLocks(proj) {
		return nil
	}
	if st := LoxRunState(l.Cfg.getenv, proj); st == LoxRunOff {
		l.logf("%s", LoxRunHint)
	}
	return nil
}

// HasLocks is whether the workspace proj declares Lock Family locks: up.json names one an agent
// container started now would mount, or .lockfamily/lockfamily.yaml exists (lox run has not
// applied it yet: the agents get no socket until it has, and lea restarts).
func (c *Config) HasLocks(proj string) bool {
	return isRegular(proj+"/.lockfamily/lockfamily.yaml") || len(c.UpLocks(proj, func(string, ...any) {})) > 0
}
