package launcher

import (
	"context"
	"strings"

	"github.com/DavidGodefroid/lea/internal/tmux"
)

// tmuxClient runs one allow-listed tmux command on lea's server, with lea's configuration, as
// one of the children Interrupt waits for.
func (l *Launcher) tmuxClient(s tmux.Server, name string, args ...string) error {
	return l.track(func() error { return s.Client(context.Background(), name, args...) })
}

type upState struct{ proj, wd, sid, name, fresh string }

// up is lea__up: guards, daemon, networks, proxy, container and bus. It returns holding the
// projects lock (release it with l.unlock(u.sid)); fresh is "--fresh" when it started the
// container.
func (l *Launcher) up(proj string) (*upState, error) {
	if err := l.guardProjects(proj); err != nil {
		return nil, err
	}
	if err := l.checkPaths(proj); err != nil {
		return nil, err
	}
	if !isRegular(l.Cfg.StateDir() + "/claude.json") {
		return nil, die("lea is not initialised on this workstation: run 'lea init' first")
	}
	u := &upState{proj: proj, wd: l.cwdIn(proj), sid: ID(proj), name: l.Cfg.ContainerName(proj)}
	if err := l.lock(u.sid); err != nil {
		return nil, err
	}
	held := true
	defer func() {
		if held {
			l.unlock(u.sid)
		}
	}()
	// Global lock: two first launches for different directories must not both create the proxy.
	if err := l.lock("global"); err != nil {
		return nil, err
	}
	sip, err := func() (string, error) {
		defer l.unlock("global")
		if err := l.ensureDaemon(); err != nil {
			return "", err
		}
		if err := l.ensureNetworks(u.sid); err != nil {
			return "", err
		}
		if err := l.ensureProxy(); err != nil {
			return "", err
		}
		return l.proxyAttach(u.sid)
	}()
	if err != nil {
		return nil, err
	}
	if !l.containerRunning(u.name) {
		if err := l.containerStart(proj, sip); err != nil {
			return nil, err
		}
		u.fresh = "--fresh"
	}
	_ = l.busAttach(proj)
	held = false
	return u, nil
}

// session is lea__session: the deck, unless --shell asks for lea-session.
func (l *Launcher) session(proj string, args []string) error {
	if len(args) == 0 || args[0] != "--shell" {
		return l.deck(proj, args)
	}
	u, err := l.up(proj)
	if err != nil {
		return err
	}
	if err := l.sessionBegin(u.sid, l.Getpid()); err != nil {
		l.unlock(u.sid)
		return err
	}
	if err := l.clipboardStart(); err != nil {
		l.sessionEnd(u.sid, l.Getpid())
		l.unlock(u.sid)
		return err
	}
	// The end runs to completion even when a signal comes meanwhile (lea__session_end starts with
	// trap '' INT TERM): atExit hands it a view that may still start children.
	finish := l.atExit(func(x *Launcher) {
		x.endSession(proj)
		if err := x.clipboardStop(false); err != nil {
			x.status(err)
		}
	})
	l.unlock(u.sid)
	err = l.containerExec(u.name, u.wd, append([]string{"lea-session"}, args...)...)
	finish()
	return err
}

// endSession is lea__session_end: the last one out runs the exit scan, stops the container
// (unless deck agents run in it) and releases its network.
func (l *Launcher) endSession(proj string) {
	id := ID(proj)
	if l.lock(id) != nil {
		return
	}
	defer l.unlock(id)
	l.sessionEnd(id, l.Getpid())
	if n, err := l.sessionCount(id); err == nil && n == 0 && !l.agentsLive(proj) {
		_ = l.sh("scan-finish", proj)
		_ = l.quiet("docker", "stop", l.Cfg.ContainerName(proj))
		// Best effort: a lock that times out skips the release.
		if l.lock("global") == nil {
			l.releaseNetwork(id)
			l.unlock("global")
		}
	}
}

// deck is lea__deck: deck-open, then the terminal attaches to the container's tmux session. No
// session is recorded: the deck's agents keep the container until lea stop.
func (l *Launcher) deck(proj string, args []string) error {
	if len(args) > 0 {
		a := strings.Join(args, " ")
		return die("with the deck, claude arguments go to a new agent: lea new -- %s", a)
	}
	if err := l.tmuxCheck(); err != nil {
		return err
	}
	u, err := l.up(proj)
	if err != nil {
		return err
	}
	if l.coreCall(append([]string{"deck-open", "--container", u.name, "--workdir", u.wd}, words(u.fresh)...)...) != nil {
		l.unlock(u.sid)
		return exitError{1}
	}
	if err := l.clipboardStart(); err != nil {
		l.unlock(u.sid)
		return err
	}
	s, _ := l.tmuxServer()
	argv, err := s.AttachArgv(u.name)
	l.unlock(u.sid)
	if err != nil {
		return die("%v", err)
	}
	if l.refused() {
		return errInterrupted
	}
	return l.Exec(argv[0], argv, l.env())
}

// newAgent is lea_new: one more agent in this projects dir's deck.
func (l *Launcher) newAgent(args []string) error {
	proj, err := l.projectsDir()
	if err != nil {
		return err
	}
	if err := l.tmuxCheck(); err != nil {
		return err
	}
	u, err := l.up(proj)
	if err != nil {
		return err
	}
	defer l.unlock(u.sid)
	if l.coreCall(append([]string{"deck-open", "--container", u.name, "--workdir", u.wd, "--no-default"}, words(u.fresh)...)...) != nil {
		return exitError{1}
	}
	if err := l.clipboardStart(); err != nil {
		return err
	}
	return l.coreCall(append([]string{"new", "--container", u.name, "--workdir", u.wd}, args...)...)
}

// coreCall runs a core subcommand on the launcher's terminal (its own messages included).
func (l *Launcher) coreCall(args ...string) error { return l.foreground(l.Core, args...) }

func words(s string) []string { return strings.Fields(s) }

// stop is lea_stop.
func (l *Launcher) stop(args []string) error {
	if len(args) > 0 && args[0] == "--all" {
		return l.stopAll()
	}
	proj, err := l.projectsDir()
	if err != nil {
		return err
	}
	id, name := ID(proj), l.Cfg.ContainerName(proj)
	n, err := l.sessionCount(id)
	if err != nil {
		return err
	}
	if n > 0 {
		l.logf("warning: %d lea session(s) still open on %s; stopping anyway", n, proj)
	}
	if l.containerRunning(name) || isRegular(l.scanStartFile(proj)) {
		// As `lea_scan_finish "$_tp" || true`: a failing scan (it prints its own warning) does
		// not abort the stop.
		_ = l.sh("scan-finish", proj)
	}
	if s, ok := l.tmuxServer(); ok {
		_ = l.tmuxClient(s, "kill-session", "-t", "="+name)
	}
	_ = l.quiet("docker", "stop", name)
	if err := l.clipboardStop(false); err != nil {
		return err
	}
	if err := l.lock("global"); err != nil {
		return err
	}
	l.releaseNetwork(id)
	l.unlock("global")
	return nil
}

// stopAll is lea_stop --all: every container of the prefix (after its exit scan), the prefix's
// internal networks, the deck server, the clipboard loop and the daemon.
func (l *Launcher) stopAll() error {
	out, _ := l.output(true, "docker", "ps", "--filter", "label=lea.prefix="+l.prefix(), "--format", "{{.ID}}\t{{.Label \"lea.projects\"}}")
	for _, line := range strings.Split(out, "\n") {
		c, p, _ := strings.Cut(line, "\t")
		if c = strings.TrimSpace(c); c == "" {
			continue
		}
		if p != "" && isDir(p) {
			_ = l.shNoInput("scan-finish", p)
		}
		_ = l.run("docker", "stop", c)
	}
	nets, _ := l.output(false, "docker", "network", "ls", "--filter", "label=lea.prefix="+l.prefix(), "--format", "{{.Name}}")
	for _, n := range strings.Fields(nets) {
		if id, ok := strings.CutPrefix(n, l.prefix()+"-internal-"); ok {
			l.releaseNetwork(id)
		}
	}
	if s, ok := l.tmuxServer(); ok {
		_ = l.tmuxClient(s, "kill-server")
	}
	if err := l.clipboardStop(true); err != nil {
		return err
	}
	l.stopDaemon()
	return nil
}
