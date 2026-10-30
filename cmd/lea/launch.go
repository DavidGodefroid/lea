package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/DavidGodefroid/lea/internal/launcher"
)

// coreCommands are the subcommands of the core binary; every other first word belongs to the
// launcher (whose default opens lea), as in the retired shell launcher.
var coreCommands = map[string]bool{
	"version": true, "daemon": true, "agents": true, "msg": true, "inbox": true, "approvals": true, "approve": true,
	"attach-container": true, "role": true, "requests": true, "request": true, "board": true, "sound": true, "grant": true, "deck": true,
	"deck-open": true, "deck-stop": true, "hook": true, "mcp": true, "statusline": true, "acp-probe": true, "launcher-check": true, "heavy": true,
	"repair-transcript": true,
}

// launcherCommand reports whether args are a launcher command. `new --container NAME --workdir
// DIR …` is the core's: the form the launcher calls it in (open.go). Any other `new` is the
// user's `lea new`, which goes through the launcher's steps with its arguments passed on,
// `--container` included.
func launcherCommand(args []string) bool {
	if len(args) == 0 {
		return true
	}
	if args[0] == "new" {
		return len(args) < 5 || args[1] != "--container" || args[3] != "--workdir"
	}
	if args[0] == "lock" || args[0] == "delivery" { // the launcher names the container, then runs the core form
		return len(args) < 3 || args[1] != "--container"
	}
	return !coreCommands[args[0]]
}

// insideLea: the binary is bind-mounted into every lea container, where the launcher must never
// run (an agent typing `lea stop --all` there gets a refusal, not a docker call).
func insideLea(getenv func(string) string, exists func(string) bool) bool {
	return getenv("LEA_BUS_SOCK") != "" || exists("/run/lea/ready")
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// Seams for the tests: the launcher's own path, and the exec that replaces the process.
var (
	executable  = os.Executable
	execProcess = execve
)

// insideWords are the core words Claude Code starts inside lea, always with pipes on stdin.
var insideWords = map[string]bool{"hook": true, "mcp": true, "statusline": true}

// typedOnTheHost reports an inside word typed on the host: the retired shell launcher sent any
// such word to its default case, lea_open, so the launcher takes it too. deck, deck-open and the
// core form of new stay core words: the launcher and lea's tmux session run them on the host.
func typedOnTheHost(args []string, stdin io.Reader) bool {
	return len(args) > 0 && insideWords[args[0]] && !insideLea(os.Getenv, fileExists) && stdinIsTerminal(stdin)
}

var stdinIsTerminal = func(r io.Reader) bool { return launcher.IsTerminal(r) }

// handOver replaces the process with argv (argv[0] is the program). It returns only when the exec
// fails.
func handOver(stderr io.Writer, argv []string, env []string) int {
	err := execProcess(argv[0], argv, env)
	fmt.Fprintln(stderr, "lea: error:", err)
	return 1
}

// withEnv is environ with key set to value, every previous entry of key dropped.
func withEnv(environ []string, key, value string) []string {
	out := make([]string, 0, len(environ)+1)
	for _, e := range environ {
		if !strings.HasPrefix(e, key+"=") {
			out = append(out, e)
		}
	}
	return append(out, key+"="+value)
}

// runLauncher runs a launcher command. It returns only when the launcher does not start (inside
// lea, outside a checkout, an unreadable configuration): once started, the run ends the process
// itself (Launcher.Run), so that a signal's exit functions are never cut short.
func runLauncher(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if insideLea(os.Getenv, fileExists) {
		fmt.Fprintln(stderr, "lea: the launcher runs on the host; inside lea, use lea msg, lea inbox and the agents_* tools")
		return 2
	}
	exe, err := executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		fmt.Fprintln(stderr, "lea: error:", err)
		return 1
	}
	root := filepath.Dir(filepath.Dir(exe)) // <root>/build/lea
	if !fileExists(filepath.Join(root, "config", "lea.conf")) {
		fmt.Fprintf(stderr, "lea: error: %s is not in a lea checkout (no config/lea.conf next to it); run the launcher from <checkout>/build/lea\n", exe)
		return 1
	}
	cwd, err := os.Getwd()
	if err == nil {
		cwd, err = filepath.EvalSymlinks(cwd)
	}
	if err != nil {
		fmt.Fprintln(stderr, "lea: error: the working directory:", err)
		return 1
	}
	cfg, err := launcher.LoadConfig(os.Getenv, root, cwd)
	if err != nil {
		msg := clean(err.Error(), false)
		if len(args) > 0 && args[0] == "doctor" {
			// lea doctor reports the line instead of being blocked by it: libexec/lea-sh doctor
			// loads the configuration in shell, as it always does, and is told what Go refused.
			sh := filepath.Join(root, "libexec", "lea-sh")
			return handOver(stderr, []string{sh, "doctor"}, withEnv(os.Environ(), "LEA_GO_CONFIG_ERROR", msg))
		}
		fmt.Fprintln(stderr, "lea: error:", msg)
		return 1
	}
	l := &launcher.Launcher{Root: root, Core: exe, Cwd: cwd, Cfg: cfg, Environ: os.Environ(),
		Stdin: stdin, Stdout: stdout, Stderr: stderr, Exec: execProcess, Sleep: time.Sleep, Getpid: os.Getpid, Version: version}
	// INT/TERM run the exit traps (locks, an open session), then exit 130. Signals are caught,
	// never ignored: an ignored signal would be inherited by every child across exec.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	l.Run(args, sigs, os.Exit)
	return 0 // not reached: Run exits
}

// execve replaces the process (the deck's tmux attach), as the shell's exec does.
func execve(argv0 string, argv, env []string) error {
	if !strings.Contains(argv0, "/") {
		p, err := exec.LookPath(argv0)
		if err != nil {
			return err
		}
		argv0 = p
	}
	return syscall.Exec(argv0, argv, env)
}
