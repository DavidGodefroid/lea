// Command lea is the lea core binary: the host daemon and CLI, and the hook, MCP and
// status-line shims that run inside lea containers.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/DavidGodefroid/lea/internal/hook"
	"github.com/DavidGodefroid/lea/internal/identity"
	"github.com/DavidGodefroid/lea/internal/mcpserver"
	"github.com/DavidGodefroid/lea/internal/statusline"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var version = "dev"

// statusBudget bounds the status line's report to the daemon: Claude waits for the line.
const statusBudget = 500 * time.Millisecond

const usage = `usage: lea <command>
  (no command), init, shell, stop, update, doctor, kube refresh, new, config
                             the launcher (see lea --help)
  launcher-check             print "go launcher"
  version                    print the version
  daemon                     run the host daemon (started by the launcher)
  agents                     list agents
  msg <agent> <text...>      send a message as the user
  inbox                      read the user's messages
  inbox --agent [--wait [S]] read the agent's messages, or wait up to S seconds (3600) for one
                             and leave it unread (inside lea)
  approvals                  list pending approvals
  approve <id> allow|deny|always   answer an approval
  board [--md] [--container NAME]   the tasks of every agent, as markdown
  requests                   list the spawn requests that wait for you
  request accept <id> | request decline <id> [reason]
                             answer a spawn request
  attach-container <name>    bind a container's bus socket (used by the launcher)
  deck --container NAME      the agent column (run by lea's tmux session)
  deck --placeholder         the empty slot (run by lea's tmux session)
  deck --thread KEY [--compose]
                             an agent's messages (run in a deck popup)
  deck-open --container NAME [--workdir DIR] [--no-default] [--fresh]
                             open a container's deck session (used by the launcher;
                             --fresh: the container was just started)
  deck-stop --container NAME [--forget]
                             record a container's agents as stopped before it stops, so the
                             next one resumes them; --forget drops them (used by the launcher)
  new --container NAME --workdir DIR [-p claude] [-r ROLE] [--branch B] [--brief FILE] [NAME] [-- ARGS]
                             start an agent (used by the launcher, in this form only;
                             any other new is the launcher's lea new)
  role <agent> <role>        change an agent's role (lead, dev, reviewer)
  sound [on|off]             show or switch lea's sounds (until the daemon restarts)
  hook claude <event>        Claude Code hook (inside lea)
  mcp                        MCP server for agents (inside lea)
  statusline [--dump FILE]   Claude status line (inside lea)
  acp-probe -- CMD...        check an ACP agent`

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if launcherCommand(args) || typedOnTheHost(args, stdin) {
		return runLauncher(args, stdin, stdout, stderr)
	}
	switch args[0] {
	case "launcher-check":
		fmt.Fprintln(stdout, "go launcher")
		return 0
	case "version":
		fmt.Fprintln(stdout, version)
		return 0
	case "daemon":
		return runDaemon(stderr)
	case "agents", "msg", "inbox", "approvals", "approve", "attach-container", "role", "requests", "request", "board", "sound":
		return runHost(args, stdout, stderr)
	case "deck":
		return runDeck(args[1:], stderr)
	case "deck-open":
		return runDeckOpen(args[1:], stderr)
	case "deck-stop":
		return runDeckStop(args[1:], stderr)
	case "new":
		return runNew(args[1:], stdout, stderr)
	case "lock":
		return runLock(args[1:], stdout, stderr)
	case "hook":
		if len(args) == 4 && args[1] == "codex" && args[2] == "notify" {
			hook.CodexNotify(context.Background(), args[3], os.Getenv)
			return 0
		}
		if len(args) != 3 || args[1] != "claude" {
			return 0 // unknown vendor or arity: never fail an agent's hook
		}
		if args[2] == "Rewake" {
			return hook.Rewake(context.Background(), stdin, stderr, os.Getenv)
		}
		hook.Claude(context.Background(), args[2], stdin, stdout, os.Getenv)
		return 0
	case "mcp":
		s := mcpserver.New(identity.BusSock(os.Getenv), identity.Agent(os.Getenv), version)
		if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			fmt.Fprintln(stderr, "lea mcp:", err)
			return 1
		}
		return 0
	case "statusline":
		dump := ""
		if len(args) == 3 && args[1] == "--dump" {
			dump = args[2]
		}
		post := func(r statusline.Report) {
			ctx, cancel := context.WithTimeout(context.Background(), statusBudget)
			defer cancel()
			_ = statusline.Post(ctx, identity.BusSock(os.Getenv), identity.Agent(os.Getenv), r)
		}
		if err := statusline.Run(stdin, stdout, dump, post); err != nil {
			fmt.Fprintln(stderr, "lea statusline:", err)
		}
		return 0
	case "acp-probe":
		return runProbe(args[1:], stdout, stderr)
	}
	// Not reached: launcherCommand sends every word that is not a core command to the launcher.
	fmt.Fprintf(stderr, "lea: unknown command %q\n%s\n", args[0], usage)
	return 2
}
