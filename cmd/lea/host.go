package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/DavidGodefroid/lea/internal/acp"
	"github.com/DavidGodefroid/lea/internal/agents"
	"github.com/DavidGodefroid/lea/internal/client"
	"github.com/DavidGodefroid/lea/internal/daemon"
	"github.com/DavidGodefroid/lea/internal/deck"
	"github.com/DavidGodefroid/lea/internal/identity"
	"github.com/DavidGodefroid/lea/internal/roles"
	"github.com/DavidGodefroid/lea/internal/store"
	"github.com/DavidGodefroid/lea/internal/textsafe"
	"github.com/DavidGodefroid/lea/internal/tmux"
	"github.com/DavidGodefroid/lea/internal/wire"
)

// hostCall sends a request to the host API; agentCall to the container bus.
func hostCall(method string, params, result any) error {
	return hostCallT(method, params, result, defaultTimeout)
}

// hostCallT is hostCall with a timeout: agent methods may wait on docker and tmux.
func hostCallT(method string, params, result any, timeout time.Duration) error {
	return callSock(identity.DaemonSock(os.Getenv), "", method, params, result, timeout)
}

const agentTimeout = 60 * time.Second

func agentCall(method string, params, result any) error {
	return callSock(identity.BusSock(os.Getenv), identity.Agent(os.Getenv), method, params, result, defaultTimeout)
}

const defaultTimeout = 10 * time.Second

// callSock sends one request. Blocking methods (msg_wait, approval_request) must pass a timeout
// longer than the timeout_s they request, or the client gives up first.
func callSock(sock, agent, method string, params, result any, timeout time.Duration) error {
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		raw = b
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err := client.Call(ctx, sock, wire.Request{Method: method, Agent: agent, Params: raw}, result)
	if err != nil && (errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED)) {
		return fmt.Errorf("lea daemon is not running (no answer on %s)", sock)
	}
	return err
}

// ensureHostDir creates the host data dir, refusing a symlink or non-directory and tightening
// any mode wider than 0700.
func ensureHostDir(host string) error {
	if err := os.MkdirAll(host, 0o700); err != nil {
		return err
	}
	fi, err := os.Lstat(host)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return fmt.Errorf("%s is a symlink or not a directory, refused", host)
	}
	if fi.Mode().Perm()&^0o700 != 0 {
		return os.Chmod(host, 0o700)
	}
	return nil
}

// clean makes agent-controlled text safe to print on a terminal: control characters, DEL, C1
// and ESC become visible \xNN / \uNNNN escapes. keepNL leaves newlines alone.
func clean(s string, keepNL bool) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' && keepNL:
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, "\\x%02x", r)
		case textsafe.Unsafe(r) && r > 0xffff:
			fmt.Fprintf(&b, "\\U%08x", r)
		case textsafe.Unsafe(r):
			fmt.Fprintf(&b, "\\u%04x", r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// firstLine is s up to its first newline.
func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}

func oneLine(s string, max int) string {
	if rs := []rune(s); len(rs) > max {
		s = string(rs[:max]) + "…"
	}
	return clean(s, false)
}

func runDaemon(stderr io.Writer) int {
	host := identity.HostDir(os.Getenv)
	if err := ensureHostDir(host); err != nil {
		fmt.Fprintln(stderr, "lea:", clean(err.Error(), false))
		return 1
	}
	st, err := store.Open(filepath.Join(host, "lea.db"))
	if err != nil {
		fmt.Fprintln(stderr, "lea:", clean(err.Error(), false))
		return 1
	}
	defer st.Close()
	// No hook waits across a daemon restart: pending cards left behind would be zombies.
	if err := st.ExpirePendingApprovals(); err != nil {
		fmt.Fprintln(stderr, "lea:", clean(err.Error(), false))
		return 1
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Chmod(filepath.Join(host, "lea.db"+suffix), 0o600); err != nil && suffix == "" {
			fmt.Fprintln(stderr, "lea:", clean(err.Error(), false))
			return 1
		}
	}
	l, err := daemon.Listen(identity.DaemonSock(os.Getenv))
	if err != nil {
		fmt.Fprintln(stderr, "lea:", clean(err.Error(), false))
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	d := daemon.New(st, filepath.Join(host, "run"), func() int64 { return time.Now().Unix() })
	conf := filepath.Join(host, "tmux.conf")
	if err := tmux.WriteConf(conf); err != nil {
		fmt.Fprintln(stderr, "lea:", clean(err.Error(), false))
		return 1
	}
	// The launcher compares this with the tmux it resolves and restarts a daemon that differs.
	_ = os.Remove(filepath.Join(host, "daemon.tmux"))
	if err := os.WriteFile(filepath.Join(host, "daemon.tmux"), []byte(os.Getenv("LEA_TMUX_BIN")), 0o600); err != nil {
		fmt.Fprintln(stderr, "lea:", clean(err.Error(), false))
		return 1
	}
	// Same pattern for the build: the launcher restarts a daemon of another build of this binary.
	_ = os.Remove(filepath.Join(host, "daemon.version"))
	if err := os.WriteFile(filepath.Join(host, "daemon.version"), []byte(version), 0o600); err != nil {
		fmt.Fprintln(stderr, "lea:", clean(err.Error(), false))
		return 1
	}
	// Same pattern for the approval settings: the launcher restarts a daemon that recorded others.
	at := approvalTimeout(os.Getenv("LEA_APPROVAL_TIMEOUT"))
	if at == 0 {
		at = 60 // the hook's default, as the launcher exports it
	}
	appr := approvalsMode(os.Getenv("LEA_APPROVALS")) + " " + strconv.Itoa(at)
	_ = os.Remove(filepath.Join(host, "daemon.approvals"))
	if err := os.WriteFile(filepath.Join(host, "daemon.approvals"), []byte(appr), 0o600); err != nil {
		fmt.Fprintln(stderr, "lea:", clean(err.Error(), false))
		return 1
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, "lea:", clean(err.Error(), false))
		return 1
	}
	m := agents.NewManager(agents.Config{
		Store:      st,
		Term:       agents.Tmux(tmux.Server{Socket: tmux.DefaultSocket, Conf: conf, Bin: os.Getenv("LEA_TMUX_BIN")}),
		Containers: agents.Docker{UID: os.Getuid(), GID: os.Getgid()},
		LeaBin:     exe,
		UID:        os.Getuid(),
		GID:        os.Getgid(),

		Approvals:       approvalsMode(os.Getenv("LEA_APPROVALS")),
		ApprovalTimeout: approvalTimeout(os.Getenv("LEA_APPROVAL_TIMEOUT")),
	})
	d.SetAgents(m)
	// A sound when an agent waits for the user and when a turn ends, while a deck shows its
	// container, unless LEA_SOUND=0.
	if os.Getenv("LEA_SOUND") != "0" {
		files := map[string]string{
			daemon.SoundAttention: "/usr/share/sounds/freedesktop/stereo/message-new-instant.oga",
			daemon.SoundDone:      "/usr/share/sounds/freedesktop/stereo/complete.oga",
		}
		if f := os.Getenv("LEA_SOUND_ATTENTION"); f != "" {
			files[daemon.SoundAttention] = f
		}
		if f := os.Getenv("LEA_SOUND_DONE"); f != "" {
			files[daemon.SoundDone] = f
		}
		if s := daemon.HostSounder(files); s != nil {
			d.SetSounder(s)
		}
	}
	// Agents are named after their first prompt unless LEA_AUTONAME=0 or claude is missing.
	if os.Getenv("LEA_AUTONAME") != "0" {
		if bin, err := exec.LookPath("claude"); err == nil {
			model := os.Getenv("LEA_AUTONAME_MODEL")
			if model == "" {
				model = "haiku"
			}
			d.SetNamer(daemon.ClaudeNamer(bin, model))
		}
	}
	go m.Resume(ctx)
	d.Serve(ctx, l, daemon.Origin{})
	return 0
}

func runHost(args []string, stdout, stderr io.Writer) int {
	fail := func(err error) int { fmt.Fprintln(stderr, "lea:", clean(err.Error(), false)); return 1 }
	switch args[0] {
	case "agents":
		var as []struct {
			Key, State, Label string
			Updated           int64
		}
		if err := hostCall("agents_list", nil, &as); err != nil {
			return fail(err)
		}
		tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "AGENT\tSTATE\tUPDATED")
		for _, a := range as {
			name := clean(a.Key, false)
			if a.Label != "" { // the user's label, then the agent's identity
				name = clean(a.Label, false) + " (" + name + ")"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\n", name, clean(a.State, false), time.Unix(a.Updated, 0).Format(time.TimeOnly))
		}
		tw.Flush()
		return 0
	case "msg":
		if len(args) < 3 {
			fmt.Fprintln(stderr, "usage: lea msg <agent> <text...>")
			return 2
		}
		var r struct{ ID, Thread int64 }
		if err := hostCall("msg_send", map[string]any{"to": args[1], "body": strings.Join(args[2:], " "), "kind": "note"}, &r); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "sent #%d (thread %d)\n", r.ID, r.Thread)
		return 0
	case "inbox":
		call := hostCall
		if len(args) > 1 && args[1] == "--agent" {
			call = agentCall
		}
		if len(args) > 2 && args[1] == "--agent" && args[2] == "--wait" {
			return runInboxWait(args[3:], stdout, stderr)
		}
		var ms []struct{ Text string }
		if err := call("msg_inbox", nil, &ms); err != nil {
			return fail(err)
		}
		for _, m := range ms {
			fmt.Fprintln(stdout, clean(m.Text, true))
		}
		return 0
	case "role":
		if len(args) != 3 {
			fmt.Fprintf(stderr, "usage: lea role <agent> <role>   (roles: %s)\n", strings.Join(roles.Names(), ", "))
			return 2
		}
		var r struct{ Key, Role string }
		if err := hostCall("agent_role", map[string]any{"agent": args[1], "role": args[2]}, &r); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "%s is now %s\n", clean(r.Key, false), clean(r.Role, false))
		return 0
	case "approvals":
		var as []struct {
			ID                 int64
			Agent, Tool, Input string
		}
		if err := hostCall("approval_list", nil, &as); err != nil {
			return fail(err)
		}
		for _, a := range as {
			fmt.Fprintf(stdout, "#%d %s %s %s\n", a.ID, oneLine(a.Agent, 64), oneLine(a.Tool, 64), oneLine(a.Input, 200))
		}
		return 0
	case "approve":
		if len(args) != 3 || (args[2] != "allow" && args[2] != "deny" && args[2] != "always") {
			fmt.Fprintln(stderr, "usage: lea approve <id> allow|deny|always")
			return 2
		}
		id, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			fmt.Fprintln(stderr, "usage: lea approve <id> allow|deny|always")
			return 2
		}
		if err := hostCall("approval_decide", map[string]any{"id": id, "verdict": args[2]}, nil); err != nil {
			return fail(err)
		}
		return 0
	case "board":
		container := ""
		for i := 1; i < len(args); i++ {
			switch {
			case args[i] == "--md":
			case args[i] == "--container" && i+1 < len(args):
				container = args[i+1]
				i++
			default:
				fmt.Fprintln(stderr, "usage: lea board [--md] [--container NAME]")
				return 2
			}
		}
		var b board
		if err := hostCall("board_list", map[string]any{"container": container}, &b); err != nil {
			return fail(err)
		}
		renderBoard(stdout, b)
		return 0
	case "requests":
		var rs []struct {
			ID                                  int64
			Container, From, Role, Repo, Branch string
			Brief, Reason                       string
			Size                                int
		}
		if err := hostCall("request_list", nil, &rs); err != nil {
			return fail(err)
		}
		if len(rs) == 0 {
			fmt.Fprintln(stdout, "no spawn request waits for you")
			return 0
		}
		for _, r := range rs {
			where := ""
			if r.Repo != "" {
				where += " repo " + r.Repo
			}
			if r.Branch != "" {
				where += " branch " + r.Branch
			}
			fmt.Fprintf(stdout, "#%d %s asks for a %s%s: %s (%d bytes)\n", r.ID, oneLine(r.From, 64), oneLine(r.Role, 16), oneLine(where, 200),
				oneLine(firstLine(r.Brief), 120), r.Size)
			if r.Reason != "" {
				fmt.Fprintf(stdout, "    reason: %s\n", oneLine(r.Reason, 200))
			}
		}
		return 0
	case "request":
		const use = "usage: lea request accept <id> | lea request decline <id> [reason...]"
		if len(args) < 3 || (args[1] != "accept" && args[1] != "decline") || (args[1] == "accept" && len(args) != 3) {
			fmt.Fprintln(stderr, use)
			return 2
		}
		id, err := strconv.ParseInt(args[2], 10, 64)
		if err != nil || id < 1 {
			fmt.Fprintln(stderr, use)
			return 2
		}
		var r struct{ Name string }
		if err := hostCallT("request_decide", map[string]any{"id": id, "verdict": args[1], "reason": strings.Join(args[3:], " ")},
			&r, agentTimeout); err != nil {
			return fail(err)
		}
		if args[1] == "accept" {
			fmt.Fprintf(stdout, "started %s for request #%d\n", clean(r.Name, false), id)
		} else {
			fmt.Fprintf(stdout, "declined request #%d\n", id)
		}
		return 0
	case "attach-container":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "usage: lea attach-container <name>")
			return 2
		}
		var r struct{ Socket string }
		if err := hostCall("container_attach", map[string]any{"name": args[1]}, &r); err != nil {
			return fail(err)
		}
		fmt.Fprintln(stdout, r.Socket)
		return 0
	}
	return 2
}

func runProbe(args []string, stdout, stderr io.Writer) int {
	cwd, prompt := ".", "Reply with the single word pong."
	for len(args) > 0 && args[0] != "--" {
		if len(args) < 2 {
			break
		}
		switch args[0] {
		case "--cwd":
			cwd = args[1]
		case "--prompt":
			prompt = args[1]
		}
		args = args[2:]
	}
	if len(args) < 2 || args[0] != "--" {
		fmt.Fprintln(stderr, "usage: lea acp-probe [--cwd DIR] [--prompt TEXT] -- CMD...")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	stop, err := acp.Probe(ctx, args[1:], cwd, prompt, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "lea acp-probe:", err)
		return 1
	}
	fmt.Fprintln(stdout, "stopReason:", stop)
	return 0
}

func runDeck(args []string, stderr io.Writer) int {
	ascii := os.Getenv("LEA_ASCII") == "1"
	var err error
	switch {
	case len(args) == 1 && args[0] == "--placeholder":
		err = deck.RunPlaceholder()
	case len(args) == 2 && args[0] == "--container":
		err = deck.Run(deck.Client{Sock: identity.DaemonSock(os.Getenv)}, args[1], ascii)
	case len(args) >= 2 && args[0] == "--thread" && (len(args) == 2 || (len(args) == 3 && args[2] == "--compose")):
		err = deck.RunThread(deck.Client{Sock: identity.DaemonSock(os.Getenv)}, args[1], len(args) == 3, ascii)
	default:
		fmt.Fprintln(stderr, "usage: lea deck --container NAME | --placeholder | --thread KEY [--compose]")
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "lea deck:", err)
		return 1
	}
	return 0
}

type newOpts struct {
	Container, Workdir, Profile, Name string
	Role, Repo, Branch, BriefFile     string
	Args                              []string
	workdirs                          int // how many --workdir were given
}

// parseNew reads `lea new` arguments: options, at most one agent name, then `--` and the
// agent's own arguments.
func parseNew(args []string) (newOpts, error) {
	var o newOpts
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--":
			o.Args = append([]string{}, args[i+1:]...)
			return checkNewPlace(o)
		case "--strict":
			return o, errors.New("lea new: --strict is not available yet")
		case "--container", "--workdir", "-p", "--profile", "-r", "--role", "--repo", "--branch", "--brief":
			if i+1 >= len(args) {
				return o, fmt.Errorf("lea new: %s needs a value", a)
			}
			i++
			switch v := args[i]; a {
			case "--container":
				o.Container = v
			case "--workdir":
				o.Workdir = v
				o.workdirs++
			case "-p", "--profile":
				o.Profile = v
			case "-r", "--role":
				o.Role = v
			case "--repo":
				o.Repo = v
			case "--branch":
				o.Branch = v
			default:
				o.BriefFile = v
			}
		default:
			if strings.HasPrefix(a, "-") {
				return o, fmt.Errorf("lea new: unknown option %s (agent arguments go after --)", a)
			}
			if o.Name != "" {
				return o, fmt.Errorf("lea new: one agent name only (got %s and %s)", o.Name, a)
			}
			o.Name = a
		}
	}
	return checkNewPlace(o)
}

// checkNewPlace settles where an agent with a repo starts: always in the projects directory. The
// launchers pass the session's --workdir first in every call, so that one is dropped; a second
// --workdir is the user's own and is refused rather than dropped without a word.
func checkNewPlace(o newOpts) (newOpts, error) {
	if o.Repo == "" {
		return o, nil
	}
	if o.workdirs > 1 {
		return o, errors.New("lea new: --repo and --workdir cannot be combined (an agent in a repo starts in the projects directory)")
	}
	o.Workdir = ""
	return o, nil
}

// readBrief reads a brief file on the host: the user's own file, at most agents.MaxBrief bytes.
func readBrief(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, agents.MaxBrief+1))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(b)) == "" {
		return "", fmt.Errorf("brief %s is empty: say what the new agent must do", path)
	}
	if len(b) > agents.MaxBrief {
		return "", fmt.Errorf("brief %s is larger than %d bytes", path, agents.MaxBrief)
	}
	return string(b), nil
}

func runNew(args []string, stdout, stderr io.Writer) int {
	o, err := parseNew(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if o.Container == "" {
		fmt.Fprintln(stderr, "lea new: run it as `lea new`: the launcher names the container")
		return 2
	}
	brief := ""
	if o.BriefFile != "" {
		if brief, err = readBrief(o.BriefFile); err != nil {
			fmt.Fprintln(stderr, "lea new:", clean(err.Error(), false))
			return 1
		}
	}
	var r struct{ Name string }
	if err := hostCallT("agent_new", map[string]any{"container": o.Container, "workdir": o.Workdir,
		"profile": o.Profile, "name": o.Name, "args": o.Args, "focus": false, "role": o.Role, "repo": o.Repo,
		"branch": o.Branch, "brief": brief}, &r, agentTimeout); err != nil {
		fmt.Fprintln(stderr, "lea:", clean(err.Error(), false))
		return 1
	}
	// The hint to open the deck is for when no terminal shows it (or the daemon cannot tell).
	var att struct{ Attached bool }
	if err := hostCall("deck_attached", map[string]any{"container": o.Container}, &att); err == nil && att.Attached {
		fmt.Fprintf(stdout, "started %s; shown in the deck\n", clean(r.Name, false))
		return 0
	}
	fmt.Fprintf(stdout, "started %s; open the deck with: lea\n", clean(r.Name, false))
	return 0
}

func runDeckOpen(args []string, stderr io.Writer) int {
	const use = "usage: lea deck-open --container NAME [--workdir DIR] [--no-default] [--fresh]"
	var container, workdir string
	spawn, fresh := true, false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--container", "--workdir":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, use)
				return 2
			}
			if args[i] == "--container" {
				container = args[i+1]
			} else {
				workdir = args[i+1]
			}
			i++
		case "--no-default":
			spawn = false
		case "--fresh":
			fresh = true
		default:
			fmt.Fprintln(stderr, use)
			return 2
		}
	}
	if container == "" {
		fmt.Fprintln(stderr, use)
		return 2
	}
	fail := func(err error) int { fmt.Fprintln(stderr, "lea:", clean(err.Error(), false)); return 1 }
	// --fresh: the launcher has just started the container. The previous container's agents
	// start again first (their conversations resumed), and its ended ones are forgotten; a
	// failure leaves them shown as done.
	if fresh {
		if err := hostCallT("agents_resume_stopped", map[string]any{"container": container}, nil, agentTimeout); err != nil {
			fmt.Fprintln(stderr, "lea: warning: the agents of the previous container were not all resumed:", clean(err.Error(), false))
		}
	}
	if err := hostCallT("deck_ensure", map[string]any{"container": container, "workdir": workdir}, nil, agentTimeout); err != nil {
		return fail(err)
	}
	if !spawn {
		return 0
	}
	var snap struct{ Agents []struct{ State string } }
	// since -1 never equals the daemon's counter: the snapshot comes back at once.
	if err := hostCallT("agents_watch", map[string]any{"container": container, "since": -1, "timeout_s": 1}, &snap, defaultTimeout); err != nil {
		return fail(err)
	}
	for _, a := range snap.Agents {
		if a.State != "done" {
			return 0
		}
	}
	if err := hostCallT("agent_new", map[string]any{"container": container, "workdir": workdir,
		"profile": "claude", "role": "lead", "focus": true}, nil, agentTimeout); err != nil {
		return fail(err)
	}
	return 0
}

// runDeckStop is deck-stop: the launcher is about to stop the container. Its agents are recorded
// as stopped, so that the next container resumes them (deck-open --fresh), or forgotten with
// --forget. Exit 1 with a warning when the daemon cannot record them: the stop goes on.
func runDeckStop(args []string, stderr io.Writer) int {
	const use = "usage: lea deck-stop --container NAME [--forget]"
	container, forget := "", false
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--container" && i+1 < len(args):
			container = args[i+1]
			i++
		case args[i] == "--forget":
			forget = true
		default:
			fmt.Fprintln(stderr, use)
			return 2
		}
	}
	if container == "" {
		fmt.Fprintln(stderr, use)
		return 2
	}
	if err := hostCallT("agents_mark_stopped", map[string]any{"container": container, "forget": forget}, nil, agentTimeout); err != nil {
		fmt.Fprintf(stderr, "lea: warning: the agents of %s were not recorded as stopped: %s\n", clean(container, false), clean(err.Error(), false))
		return 1
	}
	return 0
}

// approvalsMode is where deck agents' permission requests are answered: "pane" (the agent's own
// dialog only) when asked for, else "deck".
func approvalsMode(v string) string {
	if v == "pane" {
		return "pane"
	}
	return "deck"
}

// approvalTimeout is LEA_APPROVAL_TIMEOUT in seconds (1-600), 0 (the hook's default) otherwise.
func approvalTimeout(v string) int {
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 600 {
		return 0
	}
	return n
}

// runInboxWait is lea inbox --agent --wait [SECONDS] (inside lea, an hour by default): it returns
// once the agent has an unread message, printing how many and from whom, and leaves them unread.
// Run as a background job, it wakes an agent whose turn is over: Claude Code starts a turn when
// the job ends. Exit 0 on a message, 1 when the time runs out with none.
func runInboxWait(args []string, stdout, stderr io.Writer) int {
	secs := 3600
	if len(args) > 1 {
		fmt.Fprintln(stderr, "usage: lea inbox --agent --wait [SECONDS]")
		return 2
	}
	if len(args) == 1 {
		n, err := strconv.Atoi(args[0])
		if err != nil || n < 1 || n > 3600 {
			fmt.Fprintln(stderr, "usage: lea inbox --agent --wait [SECONDS]   (1 to 3600)")
			return 2
		}
		secs = n
	}
	var r struct {
		Count int
		Text  string
	}
	err := callSock(identity.BusSock(os.Getenv), identity.Agent(os.Getenv), "msg_watch", map[string]any{"timeout_s": secs}, &r,
		time.Duration(secs)*time.Second+5*time.Second)
	if err != nil {
		fmt.Fprintln(stderr, "lea:", clean(err.Error(), false))
		return 1
	}
	if r.Count == 0 {
		fmt.Fprintln(stdout, "lea: no message")
		return 1
	}
	fmt.Fprintln(stdout, clean(r.Text, false))
	return 0
}
