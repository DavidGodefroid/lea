package launcher

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/DavidGodefroid/lea/internal/locks"
)

// Launcher is one run of the host launcher.
type Launcher struct {
	Root    string // the lea checkout (LEA_ROOT)
	Core    string // the lea binary, <Root>/build/lea: agents, daemon, deck-open, new, attach-container
	Cwd     string // the physical working directory
	Cfg     *Config
	Environ []string // the process environment
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	// Exec replaces the process (syscall.Exec in production; tests record the argv).
	Exec   func(argv0 string, argv, env []string) error
	Sleep  func(time.Duration)
	Getpid func() int
	// Version is the launcher's build (main.version: vX.Y.Z for a release, dev otherwise); Now
	// is the clock of the update check (time.Now when nil).
	Version string
	Now     func() time.Time
	// bridgeAddrs reads the host's IPv4 addresses on a network bridge (hostBridgeAddrs when nil).
	bridgeAddrs func(name string) (addrs []string, found bool, err error)

	// drainOf is set on the view an exit function runs with: the launcher whose run state it
	// shares. While its function runs (active), the view may start children and take locks even
	// after Interrupt; the launcher itself may not.
	drainOf *Launcher
	active  bool // on root().mu

	// Run state, used through root() only.
	mu          sync.Mutex
	pending     []*exitFunc
	locks       map[string]func() // held locks and their release
	running     int               // children started and not yet ended; Interrupt waits for them
	childDone   *sync.Cond        // on mu: broadcast when a child ends
	interrupted bool              // Interrupt ran: no new child, lock or pending exit function
	exiting     int               // exit functions running; Interrupt waits for them
	drained     bool              // Interrupt has run every exit function
	exited      bool              // exitSettled called exit (a test's exit returns)
	ctrlWarned  bool              // withoutMissingControllers warned (once per lea)
}

// root is the launcher that holds the run state: l itself, or the launcher a drain view was
// made from.
func (l *Launcher) root() *Launcher {
	if l.drainOf != nil {
		return l.drainOf
	}
	return l
}

// drainView is the launcher as an exit function gets it: the same fields and run state, and
// leave to start children and take locks while that function runs (runExit makes it active).
func (l *Launcher) drainView() *Launcher {
	r := l.root()
	return &Launcher{Root: r.Root, Core: r.Core, Cwd: r.Cwd, Cfg: r.Cfg, Environ: r.Environ,
		Stdin: r.Stdin, Stdout: r.Stdout, Stderr: r.Stderr, Exec: r.Exec, Sleep: r.Sleep, Getpid: r.Getpid,
		drainOf: r}
}

// admittedLocked tells whether l may start a child or take a lock now; root().mu is held.
// Before Interrupt everything is admitted; after it, only the view of an exit function that is
// running.
func (l *Launcher) admittedLocked() bool {
	r := l.root()
	return !r.interrupted || l.drainOf != nil && l.active
}

// refused is !admittedLocked, taking the lock.
func (l *Launcher) refused() bool {
	r := l.root()
	r.mu.Lock()
	defer r.mu.Unlock()
	return !l.admittedLocked()
}

// dieError is lea_die: the run ends with "lea: error: <msg>" and status 1.
type dieError struct{ msg string }

func (e dieError) Error() string { return e.msg }

func die(format string, a ...any) error { return dieError{fmt.Sprintf(format, a...)} }

// exitError ends the run with a status and no message (a child or the usage printed it).
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }

func (l *Launcher) logf(format string, a ...any) { fmt.Fprintf(l.Stderr, "lea: "+format+"\n", a...) }

// status prints what the shell would and returns the exit status.
func (l *Launcher) status(err error) int {
	var d dieError
	var x exitError
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &x):
		return x.code
	case errors.As(err, &d):
		fmt.Fprintf(l.Stderr, "lea: error: %s\n", d.msg)
		return 1
	case errors.As(err, &ee):
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		if c := ee.ExitCode(); c > 0 {
			return c
		}
		return 1
	default:
		fmt.Fprintf(l.Stderr, "lea: error: %s\n", err)
		return 1
	}
}

func (l *Launcher) env() []string { return l.Cfg.Environ(l.Environ) }

// cmd is a child with the launcher's environment; stdin is /dev/null unless the caller sets it.
// No signal is ever set to "ignore" before a child starts: SIG_IGN survives exec.
func (l *Launcher) cmd(name string, args ...string) *exec.Cmd {
	c := exec.Command(name, args...)
	c.Env = l.env()
	return c
}

// errInterrupted ends a run whose launcher was interrupted: status 130, the signal said it all.
var errInterrupted = exitError{130}

func (l *Launcher) isInterrupted() bool {
	r := l.root()
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.interrupted
}

// cond is childDone, made on first use; root().mu is held.
func (l *Launcher) cond() *sync.Cond {
	r := l.root()
	if r.childDone == nil {
		r.childDone = sync.NewCond(&r.mu)
	}
	return r.childDone
}

// exec runs c to its end as one of the children Interrupt waits for, as sh runs a trapped
// signal's trap only once the command it is running has ended.
func (l *Launcher) exec(c *exec.Cmd) error { return l.track(c.Run) }

// track runs one child (run starts it and waits for its end) as one of those Interrupt waits
// for. After Interrupt no child starts, except those of the exit functions it runs (through
// their drain view).
func (l *Launcher) track(run func() error) error {
	r := l.root()
	r.mu.Lock()
	if !l.admittedLocked() {
		r.mu.Unlock()
		return errInterrupted
	}
	r.running++
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.running--
		l.cond().Broadcast()
		r.mu.Unlock()
	}()
	return run()
}

// run is `cmd >/dev/null`: stderr shown.
func (l *Launcher) run(name string, args ...string) error {
	c := l.cmd(name, args...)
	c.Stderr = l.Stderr
	return l.exec(c)
}

// quiet is `cmd >/dev/null 2>&1`.
func (l *Launcher) quiet(name string, args ...string) error { return l.exec(l.cmd(name, args...)) }

// output is "$(cmd)" (trailing newlines trimmed); showErr keeps stderr (else 2>/dev/null).
func (l *Launcher) output(showErr bool, name string, args ...string) (string, error) {
	c := l.cmd(name, args...)
	var o bytes.Buffer
	c.Stdout = &o
	if showErr {
		c.Stderr = l.Stderr
	}
	err := l.exec(c)
	return strings.TrimRight(o.String(), "\n"), err
}

// foreground runs a child on the launcher's terminal; Interrupt waits for it to end, as for
// every child.
func (l *Launcher) foreground(name string, args ...string) error {
	c := l.cmd(name, args...)
	c.Stdin, c.Stdout, c.Stderr = l.Stdin, l.Stdout, l.Stderr
	return l.exec(c)
}

type exitFunc struct {
	once sync.Once
	f    func(*Launcher)
}

// runExit runs e once, with a view of the launcher that may start children and take locks
// until e returns, whether a signal comes meanwhile or came before: as the shell's EXIT trap
// runs its commands with INT and TERM ignored (lea__session_end).
func (l *Launcher) runExit(e *exitFunc) {
	r := l.root()
	e.once.Do(func() {
		v := r.drainView()
		r.mu.Lock()
		v.active = true
		r.exiting++
		r.mu.Unlock()
		defer func() {
			r.mu.Lock()
			v.active = false
			r.exiting--
			l.cond().Broadcast()
			r.mu.Unlock()
		}()
		e.f(v)
	})
}

// atExit registers f, as the shell launcher set an EXIT trap: Interrupt runs it, or the returned finish
// does on the normal path; either way once, through runExit. Registered after Interrupt, it
// runs at once: nobody would run it later. (Interrupt has returned by then and does not wait for
// such a late function; the process exit does: see exitSettled.)
func (l *Launcher) atExit(f func(*Launcher)) (finish func()) {
	r := l.root()
	e := &exitFunc{f: f}
	r.mu.Lock()
	if r.interrupted {
		r.mu.Unlock()
		l.runExit(e)
		return func() {}
	}
	r.pending = append(r.pending, e)
	r.mu.Unlock()
	return func() {
		l.runExit(e)
		r.mu.Lock()
		defer r.mu.Unlock()
		for i, p := range r.pending {
			if p == e {
				r.pending = append(r.pending[:i], r.pending[i+1:]...)
				break
			}
		}
	}
}

// Interrupt ends the run as the shell's EXIT traps do on INT or TERM: no new child starts, the
// running children are waited for (they got the signal too), then the pending exit functions
// run, newest first, and Interrupt returns once every exit function under way (one the normal
// path was running included) and every child has ended. A second call runs nothing again; it
// returns once the first is done. The caller exits 130 afterwards.
//
// After Interrupt only an exit function's own view starts children and takes locks: a child the
// main path starts is refused, so none outlives the exit that follows.
func (l *Launcher) Interrupt() {
	r := l.root()
	r.mu.Lock()
	if r.interrupted {
		for !r.drained {
			l.cond().Wait()
		}
		r.mu.Unlock()
		return
	}
	r.interrupted = true
	for r.running > 0 {
		l.cond().Wait()
	}
	fs := r.pending
	r.pending = nil
	r.mu.Unlock()
	for i := len(fs) - 1; i >= 0; i-- {
		l.runExit(fs[i])
	}
	r.mu.Lock()
	for r.exiting > 0 || r.running > 0 {
		l.cond().Wait()
	}
	r.drained = true
	l.cond().Broadcast()
	r.mu.Unlock()
}

// exitSettled ends the process with status code (130 once interrupted), but only when nothing
// is left to run: Interrupt's drain is over, and no exit function or child is under way. That
// includes a late exit function, which atExit runs at once on the main path when it was
// registered after Interrupt returned: it is waited for, not cut short. exit is called with the
// run state locked and is not released: an exit function that has not started by then never
// starts (atExit blocks on the lock), so none is cut half-way. Such a function's resource (a
// session file whose end was not registered yet) is what a run killed at that point leaves; the
// next session count removes a dead pid's file. A second call does nothing (exit is os.Exit; a
// test's returns).
func (l *Launcher) exitSettled(code int, exit func(int)) {
	r := l.root()
	r.mu.Lock()
	defer r.mu.Unlock()
	for r.interrupted && !r.drained || r.exiting > 0 || r.running > 0 {
		l.cond().Wait()
	}
	if r.exited {
		return
	}
	if r.interrupted {
		code = 130
	}
	r.exited = true
	exit(code)
}

// Run is Main for the process: the first signal on sigs interrupts the run as the shell's EXIT
// traps do on INT or TERM (Interrupt), and the process exits 130 once the drain is over; without
// one it exits with Main's status. Either way, through exitSettled, so neither path exits while
// the other still runs an exit function or a child. Signals are caught, never ignored: an
// ignored signal would be inherited by every child across exec. exit is os.Exit.
func (l *Launcher) Run(args []string, sigs <-chan os.Signal, exit func(int)) {
	go func() {
		if _, ok := <-sigs; ok {
			l.Interrupt()
			l.exitSettled(130, exit)
		}
	}()
	l.exitSettled(l.Main(args), exit)
}

// noComma is lea_no_comma: --mount uses commas as separators.
func noComma(paths ...string) error {
	for _, p := range paths {
		if strings.Contains(p, ",") {
			return die("path contains a comma, which Docker mounts cannot express: %s", p)
		}
	}
	return nil
}

func uidgid() string { return fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()) }

// Main runs one launcher command and returns the exit status.
func (l *Launcher) Main(args []string) int { return l.status(l.dispatch(args)) }

func (l *Launcher) dispatch(args []string) error {
	a0 := ""
	if len(args) > 0 {
		a0 = args[0]
	}
	rest := []string{}
	if len(args) > 1 {
		rest = args[1:]
	}
	// LEA_CTL=1 (#165): lea runs in its control container, but for what installs or configures it.
	if l.Cfg.Ctl() && l.Cfg.Get("LEA_CTL_INSIDE") != "1" && !ctlHostOnly[a0] {
		return l.viaCtl(args)
	}
	switch a0 {
	case "-h", "--help", "help":
		_, err := l.Stdout.Write([]byte(Usage))
		return err
	case "init":
		return l.sh("init", rest...)
	case "locksql", "lockmq", "lockfile", "locklog":
		l.exportToolsOn()
		return l.sh(a0, rest...)
	case "lock":
		return l.lockConsole(rest)
	case "allow":
		return l.allow(rest)
	case "delivery":
		return l.delivery(rest)
	case "grant":
		return l.grant(rest)
	case "github":
		return l.github(rest)
	case "shell":
		if len(rest) > 0 {
			if err := l.oldForm(rest[0]); err != nil {
				return err
			}
		}
		return l.shell(rest)
	case "new":
		return l.newAgent(rest)
	case "kube":
		if len(rest) == 0 || rest[0] != "refresh" {
			l.Stderr.Write([]byte(Usage))
			return exitError{2}
		}
		return l.sh("kube-refresh")
	case "stop":
		if len(rest) > 0 {
			if err := l.oldForm(rest[0]); err != nil {
				return err
			}
		}
		return l.stop(rest)
	case "update":
		return l.sh("update", rest...)
	case "rollback":
		return l.sh("rollback", rest...)
	case "doctor":
		return l.doctor()
	case "config":
		if len(rest) > 0 && rest[0] == "ctl" {
			return l.configCtl(rest[1:])
		}
		if len(rest) > 0 && rest[0] == "deck" {
			return l.configDeck(rest[1:])
		}
		if len(rest) > 0 && rest[0] == "tool" {
			return l.configTool(rest[1:])
		}
		if len(rest) > 0 && rest[0] == "claude" {
			return l.configClaude(rest[1:])
		}
		return l.config(rest)
	case "egress":
		return l.egress(rest)
	case "attach":
		return l.attach(rest)
	case "ctl":
		return l.ctl(rest)
	default:
		if len(args) > 0 {
			if err := l.oldForm(args[0]); err != nil {
				return err
			}
		}
		// LEA_DECK=0 (#135): the user's own tool in the same container, without the deck or tmux.
		noDeck := l.Cfg.NoDeck()
		deckArgs := args
		if noDeck {
			deckArgs = nil
		}
		if err := l.refuseEarly(deckArgs, !noDeck); err != nil {
			return err
		}
		if err := l.firstRun(); err != nil {
			return err
		}
		proj, err := l.projectsDir()
		if err != nil {
			return err
		}
		if err := l.ensureHub(proj); err != nil {
			return err
		}
		l.autoUpdate()
		if l.Cfg.NoDeck() {
			return l.session(proj, l.toolArgs(args))
		}
		return l.session(proj, args)
	}
}

// ToolsOnVar names, in lea-sh, the lock tools the launcher wires for the projects directory
// (ToolsOn), space separated: lea locksql, lea lockmq, lea lockfile, lea locklog and lea
// doctor take this
// decision instead of making their own.
const ToolsOnVar = "LEA_TOOLS_ON"

// exportToolsOn exports ToolsOnVar for lea-sh: the tools on for the configured projects directory
// (ProjectsLockTools, silently: up.json's warnings belong to the session's start).
func (l *Launcher) exportToolsOn() {
	on, _ := l.Cfg.ProjectsLockTools()
	var ts []string
	for _, t := range locks.Names() {
		if on[t] {
			ts = append(ts, t)
		}
	}
	l.Cfg.Export(ToolsOnVar, strings.Join(ts, " "))
}

// exportLimits hands lea doctor the limits the configuration resolves to (#132): LEA_LIMITS_WANT
// as the engine's inspect tells them, LEA_LIMITS_DIGEST the lea.limits label of a container
// started with them. Doctor reports a missing cgroup controller itself, so the launcher's warning
// about it is not printed here.
func (l *Launcher) exportLimits() {
	l.root().ctrlWarned = true
	r := l.resolvedLimits()
	l.Cfg.Export("LEA_LIMITS_WANT", r.nanos())
	l.Cfg.Export("LEA_LIMITS_DIGEST", r.digest())
}

// lockConsole is lea lock, removed (#300): lea shows no lock console; lox run, in a terminal of
// its own outside lea, shows what the locks ask.
func (l *Launcher) lockConsole([]string) error {
	l.logf("error: lea lock was removed: lea shows no lock console. " + LoxRunHint + " (lox attach NAME reaches a lock's own console)")
	return exitError{2}
}

// doctor is lea doctor: the workstation's checks (lea-sh doctor), then, when the projects
// directory resolves, lea's Lock clients against the locks lox runs (doctorLocks) and its lea's allowances (#92, #319) through the core, which reads the grants
// from the daemon and the credentials from lea's state. The worst of the two is the status.
func (l *Launcher) doctor() error {
	l.exportToolsOn()
	l.exportLimits()
	err := l.sh("doctor")
	proj, perr := l.projectsDir()
	if perr != nil {
		return err
	}
	if msg := l.installCheck(proj); msg != "" {
		l.logf("error: %s", msg)
		if err == nil {
			err = exitError{1}
		}
	}
	// Lea's Lock clients against the locks lox runs (lea#134).
	if lerr := l.doctorLocks(proj); lerr != nil && err == nil {
		err = lerr
	}
	// The user's own base settings for Claude (#334).
	if cerr := l.claudeDoctor(); cerr != nil && err == nil {
		err = cerr
	}
	if l.coreCall("allow", "--container", l.Cfg.ContainerName(proj), "--projects", proj, "check", "--state", l.Cfg.StateDir()) != nil && err == nil {
		return exitError{1}
	}
	return err
}

// allow is lea allow [ARGS] (#319), through the core, which the launcher tells this projects
// dir's container (LEA_PROJECTS_DIR for another lea) and the directory whose repositories'
// origins it pins. The core checks the arguments.
func (l *Launcher) allow(args []string) error {
	for _, a := range args {
		if a == "--container" || a == "--projects" {
			l.logf("error: lea allow names no --container or --projects: run it from the lea's projects directory (or set LEA_PROJECTS_DIR)")
			return exitError{2}
		}
	}
	proj, err := l.projectsDir()
	if err != nil {
		return err
	}
	if l.coreCall(append([]string{"allow", "--container", l.Cfg.ContainerName(proj), "--projects", proj}, args...)...) != nil {
		return exitError{1}
	}
	return nil
}

// aliasNote tells the user the new form of an old command, which goes away in the next minor
// release.
func (l *Launcher) aliasNote(old, now string) {
	family := strings.Join(strings.Fields(old)[:2], " ")
	l.logf("note: %s is now %s (%s goes away in the next minor release)", old, now, family)
}

// delivery is lea delivery on [--yes DIGEST] [--gitlab HOST]...|off|status (issue #92), an alias
// of lea allow (#319): on is lea allow deliver, off lea allow off deliver, status lea allow.
func (l *Launcher) delivery(args []string) error {
	ok := len(args) > 0 && (args[0] == "on" || (len(args) == 1 && (args[0] == "off" || args[0] == "status")))
	for i := 1; ok && i < len(args); i += 2 {
		ok = (args[i] == "--yes" || args[i] == "--gitlab") && i+1 < len(args) && args[i+1] != ""
	}
	if !ok {
		l.logf("error: usage: lea delivery on [--yes DIGEST] [--gitlab HOST]...|off|status (now: lea allow deliver|off deliver|lea allow)")
		return exitError{2}
	}
	var now []string
	switch args[0] {
	case "on":
		now = append([]string{"deliver"}, args[1:]...)
	case "off":
		now = []string{"off", "deliver"}
	}
	l.aliasNote("lea delivery "+args[0], strings.TrimSpace("lea allow "+strings.Join(now, " ")))
	return l.allow(now)
}

// grant is lea grant: add and ls are aliases of lea allow (#319), translated here, where the
// container is known; requests, approve, deny and revoke stay the core's.
func (l *Launcher) grant(args []string) error {
	if len(args) == 0 || (args[0] != "add" && args[0] != "ls") {
		if l.coreCall(append([]string{"grant"}, args...)...) != nil {
			return exitError{1}
		}
		return nil
	}
	usage := func() error {
		l.logf("error: usage: lea grant add <action> [--repo R] [--github OWNER/NAME] [--branch GLOB] [--agent NAME] [--tag GLOB] [--expires D] | lea grant ls (now: lea allow)")
		return exitError{2}
	}
	if args[0] == "ls" {
		if len(args) > 2 || (len(args) == 2 && args[1] != "--all") {
			return usage()
		}
		l.aliasNote("lea grant ls", "lea allow")
		return l.allow(nil)
	}
	if len(args) < 2 {
		return usage()
	}
	capName, ok := actionCapability[args[1]]
	if !ok {
		return usage()
	}
	now := []string{capName}
	container := ""
	for i := 2; i < len(args); i += 2 {
		if i+1 >= len(args) || args[i+1] == "" {
			return usage()
		}
		v := args[i+1]
		switch args[i] {
		case "--container", "--lea":
			container = v
		case "--repo", "--branch", "--agent", "--tag":
			now = append(now, args[i], v)
		case "--github":
			now = append(now, "--check-pin", v)
		case "--expires":
			now = append(now, "--for", v)
		default:
			return usage()
		}
	}
	proj, err := l.projectsDir()
	if err != nil {
		return err
	}
	if container != "" && container != l.Cfg.ContainerName(proj) {
		l.logf("error: --container %s is not this projects directory's lea (%s): run lea allow from that lea's projects directory, or set LEA_PROJECTS_DIR",
			container, l.Cfg.ContainerName(proj))
		return exitError{2}
	}
	shown := []string{}
	for i := 0; i < len(now); i++ {
		if now[i] == "--check-pin" {
			i++
			continue
		}
		shown = append(shown, now[i])
	}
	l.aliasNote("lea grant add", "lea allow "+strings.Join(shown, " "))
	return l.allow(now)
}

// actionCapability maps lea grant add's action names to lea allow's capabilities.
var actionCapability = map[string]string{"commit-identity": "push", "pr-create": "pr", "merge": "merge", "delete-own-branch": "delete-branch",
	"close-issue": "issues", "stop-finished": "stop-finished", "release-tag": "release-tag",
	"release-create": "release"}

// shell is lea_shell: a shell, or a command, in the same lea.
func (l *Launcher) shell(args []string) error {
	if err := l.refuseEarly(nil, false); err != nil {
		return err
	}
	if err := l.firstRun(); err != nil {
		return err
	}
	proj, err := l.projectsDir()
	if err != nil {
		return err
	}
	if err := l.ensureHub(proj); err != nil {
		return err
	}
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	return l.session(proj, append([]string{"--shell"}, args...))
}

// oldForm refuses the former `lea <projects-dir>` form instead of passing the path on.
func (l *Launcher) oldForm(a string) error {
	if strings.HasPrefix(a, "-") || !isDir(a) {
		return nil
	}
	l.logf("error: lea no longer takes a directory: LEA_PROJECTS_DIR=%s lea (or rerun 'lea init' to change the default)", a)
	return exitError{2}
}
