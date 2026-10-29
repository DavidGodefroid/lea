package providers

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// LeaBin is the lea binary in the container: Codex runs it as its MCP server and notify program.
const LeaBin = "/usr/local/bin/lea"

var (
	// sessionRe is a session id as both CLIs write it (a UUID): the only value that reaches
	// --resume or codex resume, never one that could pass for a flag or a session name.
	sessionRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	agentRe   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,23}$`)
)

// ValidSession reports whether s is a session id lea passes to a CLI.
func ValidSession(s string) bool { return sessionRe.MatchString(s) }

// Resolve picks an agent's provider and model: the requested provider, else defProvider (the
// configuration's LEA_PROVIDER), else Default; the requested model, else (for the default
// provider only) defModel (LEA_MODEL), else the provider's own default. A provider of the
// providers file needs a model.
func (r *Registry) Resolve(provider, model, defProvider, defModel string) (Provider, string, error) {
	if err := CheckModel(model); err != nil {
		return Provider{}, "", err
	}
	if provider == "" {
		provider = defProvider
		if model == "" {
			model = defModel
		}
	}
	if provider == "" {
		provider = Default
	}
	p, err := r.Get(provider)
	if err != nil {
		return Provider{}, "", err
	}
	if model == "" {
		model = p.Model
	}
	if err := CheckModel(model); err != nil {
		return Provider{}, "", err
	}
	if p.Endpoint != nil && model == "" {
		return Provider{}, "", fmt.Errorf("provider %s needs a model: give one, or model=<model> in the providers file", p.Name)
	}
	return p, model, nil
}

// LeaApprovedTools are lea's own MCP tools Codex runs without asking: lea's control plane
// (messages, board, leases, requests the user or a lead answers). LeaPromptedTools start or stop
// agents: they keep Codex's prompt, as a Claude agent keeps its own (a grant may answer it).
var (
	LeaApprovedTools = []string{"agents_list", "agents_request_spawn", "ask", "board_read", "grants_list",
		"lease_acquire", "lease_release", "lock_request", "msg_inbox", "msg_reply", "msg_send", "msg_wait",
		"providers_list", "task_update"}
	LeaPromptedTools = []string{"agents_spawn", "agents_stop"}
)

// Argv is what an agent's CLI runs with, after `lea-agent run <agent> --cli <CLI> --`: the
// provider's own flags, model, the user's arguments (lea new -- ...), then the session to resume
// (a UUID, else nothing) or else the first prompt. Every value lea puts into a Codex -c option is
// its own (the agent's checked name, the provider's checked name, fixed strings): the model goes
// in as a separate argument, never into TOML.
//
// workdir is the agent's working directory (the projects directory, which every agent can write):
// Codex is told it is untrusted. That answers its trust dialog, which would otherwise wait, unseen,
// in a parked pane, and it never loads a project configuration (.codex/) an agent could have
// written there: with one present, Codex asks again, in the pane. A directory whose name holds a
// quote or a backslash is left out (Codex asks).
func (p Provider) Argv(agent, workdir, model, session string, user []string, prompt string) ([]string, error) {
	if !agentRe.MatchString(agent) {
		return nil, errors.New("invalid agent name")
	}
	if err := CheckModel(model); err != nil {
		return nil, err
	}
	if session != "" && !ValidSession(session) {
		session = ""
	}
	var a []string
	switch p.CLI {
	case Claude:
		if session != "" {
			a = append(a, "--resume", session)
		}
		if model != "" {
			a = append(a, "--model", model)
		}
	case Codex:
		if session != "" {
			a = append(a, "resume", session)
		}
		// The sandbox and approvals of Codex itself, in the agent's pane: writes in the
		// workspace, asks for anything else (lea's container stays the outer boundary).
		a = append(a, "--sandbox", "workspace-write", "--ask-for-approval", "on-request",
			"-c", "check_for_update_on_startup=false",
			"-c", `mcp_servers.lea.command="`+LeaBin+`"`,
			"-c", `mcp_servers.lea.args=["mcp"]`,
			"-c", `mcp_servers.lea.env.LEA_AGENT="`+agent+`"`,
			"-c", `notify=["`+LeaBin+`","hook","codex","notify"]`)
		for _, t := range LeaApprovedTools {
			a = append(a, "-c", `mcp_servers.lea.tools.`+t+`.approval_mode="approve"`)
		}
		if trustable(workdir) {
			a = append(a, "-c", `projects."`+workdir+`".trust_level="untrusted"`)
		}
		// Each enabled Lock's MCP server, whole: a Lock call waits for a human, longer than
		// Codex's default tool timeout. An entry with the timeout alone (a server the user did not
		// declare) would be no valid server, so lea declares it, with the image's own client. Its
		// tools need no approval in Codex: the human approves each call at the Lock, and Codex's
		// own prompt would hold the call before it reaches the Lock.
		for _, l := range p.Locks {
			if !slices.Contains(LockNames, l.Name) || l.TimeoutSec < 1 {
				continue
			}
			a = append(a,
				"-c", `mcp_servers.`+l.Name+`.command="/usr/local/bin/`+l.Name+`"`,
				"-c", `mcp_servers.`+l.Name+`.args=["mcp"]`,
				"-c", `mcp_servers.`+l.Name+`.tool_timeout_sec=`+strconv.Itoa(l.TimeoutSec),
				"-c", `mcp_servers.`+l.Name+`.default_tools_approval_mode="approve"`)
		}
		if p.Endpoint != nil {
			if !nameRe.MatchString(p.Name) || reserved[p.Name] {
				return nil, errors.New("invalid provider name")
			}
			id := "lea-" + p.Name
			a = append(a,
				"-c", `model_provider="`+id+`"`,
				"-c", `model_providers.`+id+`.name="`+p.Name+`"`,
				"-c", `model_providers.`+id+`.base_url="`+ForwarderBase(p.Name)+`"`,
				"-c", `model_providers.`+id+`.wire_api="responses"`)
		}
		if model != "" {
			a = append(a, "--model", model)
		}
	default:
		return nil, fmt.Errorf("provider %s: unknown CLI", p.Name)
	}
	a = append(a, user...)
	if session == "" && prompt != "" {
		a = append(a, prompt)
	}
	return a, nil
}

// trustable reports whether dir can be written as a quoted TOML key: a clean absolute path
// without a quote, a backslash or a control character.
func trustable(dir string) bool {
	if !strings.HasPrefix(dir, "/") || cleanPath(dir) != dir || strings.ContainsAny(dir, "\"\\") {
		return false
	}
	for _, c := range dir {
		if c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}

// UsageLimit says whether a CLI's turn-failure class means the provider's usage or rate limit
// stopped the agent: the adapter seam of auto-resume (issue #20). kind is the CLI's own
// structured error class, never text the model or the provider wrote. Claude Code reports
// rate_limit in its StopFailure hook. Codex has no structured signal for it yet (its limit is
// only a line of its terminal), so it never reports one.
func UsageLimit(cli, kind string) bool {
	switch cli {
	case Claude:
		return kind == "rate_limit"
	}
	return false
}
