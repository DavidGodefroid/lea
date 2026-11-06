package launcher

import (
	"io"
	"regexp"
	"strings"
)

// Without the deck (LEA_DECK=0, #135): the user's own tool in the lea container, lea's agents
// joined one by one, and the switches between the two modes.

const attachUsage = "usage: lea attach [--print] [AGENT]"

// agentName is an agent's name as lea gives it (internal/agents' nameRe).
var agentName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,23}$`)

// attach is lea attach [--print] [AGENT]. Without AGENT it runs LEA_TOOL in this projects dir's
// container as `lea` does with LEA_DECK=0 (the container started when needed, the session
// counted); with --print it prints the docker exec command that does it in the running
// container, for an IDE or another terminal (that terminal is no lea session: the container
// stops with the last lea session once no agent runs in it). With AGENT it joins that agent's
// terminal (its abduco session, which outlives the attach), or prints the command that does.
func (l *Launcher) attach(args []string) error {
	printOnly, agent := false, ""
	for _, a := range args {
		switch {
		case a == "--print":
			printOnly = true
		case agent == "" && agentName.MatchString(a):
			agent = a
		default:
			l.logf("error: %s (an agent's name: lower-case letters, digits and -)", attachUsage)
			return exitError{2}
		}
	}
	if !printOnly && agent == "" {
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
		return l.session(proj, l.toolArgs(nil))
	}
	proj, err := l.projectsDir()
	if err != nil {
		return err
	}
	name := l.Cfg.ContainerName(proj)
	if !l.containerRunning(name) {
		return die("%s is not running: start it with lea first (or lea new for an agent)", name)
	}
	argv := []string{"docker", "exec", "-it", "-u", uidgid(), "-e", "TERM", "-w", l.cwdIn(proj), name}
	if agent != "" {
		argv = append(argv, "abduco", "-a", "ag-"+agent)
	} else {
		argv = append(append(argv, "lea-session"), l.toolArgs(nil)...)
	}
	if printOnly {
		_, err := io.WriteString(l.Stdout, shellLine(argv)+"\n")
		return err
	}
	return l.foreground(argv[0], argv[1:]...)
}

// shellLine is argv as one line a POSIX shell runs as is: a word with anything but letters,
// digits and _ . / : = @ % + , - goes in single quotes.
func shellLine(argv []string) string {
	ws := make([]string, len(argv))
	for i, a := range argv {
		if a != "" && toolWord.MatchString(a) {
			ws[i] = a
			continue
		}
		ws[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(ws, " ")
}

// configDeck is lea config deck on|off: LEA_DECK in the user configuration, read by the next lea.
// The daemon restarts with it (its agents keep running); running agents and the container are not
// touched.
func (l *Launcher) configDeck(args []string) error {
	v := map[string]string{"on": "1", "off": "0"}
	if len(args) != 1 || v[args[0]] == "" {
		l.logf("error: usage: lea config deck on|off")
		return exitError{2}
	}
	if err := WriteUserConfig(l.Cfg.UserConfigFile(l.Cwd), "LEA_DECK", v[args[0]]); err != nil {
		return die("%v", err)
	}
	if args[0] == "on" {
		l.logf("LEA_DECK=1: the next lea opens the deck (tmux %s or later); running agents are not touched", TmuxMin)
		return nil
	}
	l.logf("LEA_DECK=0: the next lea runs %s in the lea container, without the deck; running agents are not touched (lea board, lea requests)",
		strings.Join(l.Cfg.Tool(), " "))
	return nil
}

// configTool is lea config tool CMD [ARG...]: LEA_TOOL in the user configuration, checked as
// LoadConfig checks it.
func (l *Launcher) configTool(args []string) error {
	if len(args) == 0 {
		l.logf("error: usage: lea config tool CMD [ARG...]")
		return exitError{2}
	}
	v := strings.Join(args, " ")
	ws, err := ToolArgv(v)
	if err != nil {
		return die("%v", err)
	}
	if err := WriteUserConfig(l.Cfg.UserConfigFile(l.Cwd), "LEA_TOOL", strings.Join(ws, " ")); err != nil {
		return die("%v", err)
	}
	when := "with LEA_DECK=0 (lea config deck off)"
	if l.Cfg.NoDeck() {
		when = "from the next lea"
	}
	l.logf("LEA_TOOL=%s: lea runs it in the lea container %s", strings.Join(ws, " "), when)
	return nil
}
