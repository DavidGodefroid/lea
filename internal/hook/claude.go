// Package hook implements `lea hook <vendor> <event>`: it reports agent state to the daemon and
// returns the vendor's hook output. It never fails the agent: when the bus is unreachable it
// prints nothing and exits 0. Claude's PreToolUse is the exception: lea's guard (hard denies,
// grants) fails closed, and denies the call it could not check (failClosed).
package hook

import (
	"context"
	"encoding/json"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/DavidGodefroid/lea/internal/client"
	"github.com/DavidGodefroid/lea/internal/grants"
	"github.com/DavidGodefroid/lea/internal/identity"
	"github.com/DavidGodefroid/lea/internal/wire"
)

const (
	maxInput = 1 << 20
	// budget bounds all non-approval daemon calls of one hook invocation together.
	budget = 2 * time.Second
	// maxNotification caps the Notification message forwarded to the daemon.
	maxNotification = 1 << 10
)

// subagentID is a subagent's agent_id as lea forwards it (daemon.subagentID).
var subagentID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// errorClass is StopFailure's error as lea forwards it: Claude Code's error class (rate_limit,
// overloaded...), a plain identifier.
var errorClass = regexp.MustCompile(`^[a-z_]{1,40}$`)

// sessionSources are SessionStart's sources as lea forwards them.
var sessionSources = map[string]bool{"startup": true, "resume": true, "clear": true, "compact": true}

type caller struct {
	sock, agent string
}

func (c caller) call(ctx context.Context, method string, params, result any) error {
	var raw json.RawMessage
	if params != nil {
		raw, _ = json.Marshal(params)
	}
	return client.Call(ctx, c.sock, wire.Request{Method: method, Agent: c.agent, Params: raw}, result)
}

// truncate cuts s to at most n bytes without splitting a UTF-8 sequence.
// maxNamePrompt is the part of the first prompt sent for naming: daemon.MaxNamePrompt.
const maxNamePrompt = 2000

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func Claude(ctx context.Context, event string, stdin io.Reader, stdout io.Writer, getenv func(string) string) {
	c := caller{identity.BusSock(getenv), identity.Agent(getenv)}
	if c.agent == "" {
		// A Claude without lea's identity runs lea's hooks only in a lea container (the image's
		// managed settings): an agent's own claude, or one whose settings unset LEA_AGENT. Its
		// tool calls cannot be checked: refused.
		if event == "PreToolUse" {
			failClosed(stdout, "this Claude Code has no lea agent identity (LEA_AGENT): only lea's agents (lea-agent run, the deck) run tools in lea")
		}
		return
	}
	in, _ := io.ReadAll(io.LimitReader(stdin, maxInput))
	var p struct {
		HookEventName  string          `json:"hook_event_name"`
		SessionID      string          `json:"session_id"`
		StopHookActive bool            `json:"stop_hook_active"`
		ToolName       string          `json:"tool_name"`
		ToolInput      json.RawMessage `json:"tool_input"`
		Suggestions    json.RawMessage `json:"permission_suggestions"`
		Message        string          `json:"message"`
		NotifType      string          `json:"notification_type"`
		Prompt         string          `json:"prompt"`
		AgentID        string          `json:"agent_id"`
		Error          string          `json:"error"`
		Source         string          `json:"source"`
		Cwd            string          `json:"cwd"`
		ToolResponse   json.RawMessage `json:"tool_response"`
	}
	_ = json.Unmarshal(in, &p)
	// Forward only what phase 0 needs: never tool_input or tool_response (file contents,
	// secrets), so lea.db stays small and free of tool data.
	payload := map[string]any{}
	if p.HookEventName != "" {
		payload["hook_event_name"] = p.HookEventName
	}
	if p.SessionID != "" {
		payload["session_id"] = p.SessionID
	}
	if p.ToolName != "" {
		payload["tool_name"] = p.ToolName
	}
	if event == "Notification" && p.Message != "" {
		payload["message"] = truncate(p.Message, maxNotification)
	}
	if event == "Notification" && p.NotifType != "" {
		payload["notification_type"] = truncate(p.NotifType, 64)
	}
	// A failed turn is known by its error class only: never error_details or
	// last_assistant_message, which hold the provider's and the model's text.
	if event == "StopFailure" && errorClass.MatchString(p.Error) {
		payload["error"] = p.Error
	}
	// A session start is known by its source, one of Claude Code's four: a resumed session keeps
	// a usage limit a previous container recorded (daemon.limitProgress).
	if event == "SessionStart" && sessionSources[p.Source] {
		payload["source"] = p.Source
	}
	// A subagent is known by its id only: never its prompt, type or transcript.
	if (event == "SubagentStart" || event == "SubagentStop") && subagentID.MatchString(p.AgentID) {
		payload["agent_id"] = p.AgentID
	}
	// A tool event says who runs the tool, by id only: the agent itself (an empty id) or a
	// subagent, an agent-type Stop hook's among them (the daemon tells a blocked stop by it).
	if (event == "PreToolUse" || event == "PostToolUse") && (p.AgentID == "" || subagentID.MatchString(p.AgentID)) {
		payload["agent_id"] = p.AgentID
	}
	bctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	if err := c.call(bctx, "hook_event",
		map[string]any{"event": event, "session_id": p.SessionID, "payload": payload}, nil); err != nil {
		if event == "PreToolUse" {
			failClosed(stdout, "lea's bus did not answer ("+truncate(err.Error(), 200)+"); retry in a moment")
		}
		return
	}
	switch event {
	case "Stop":
		var r struct {
			Block  bool   `json:"block"`
			Reason string `json:"reason"`
		}
		if c.call(bctx, "stop_check", nil, &r) == nil && r.Block {
			json.NewEncoder(stdout).Encode(map[string]any{"decision": "block", "reason": r.Reason})
		}
	case "UserPromptSubmit", "PostToolUse":
		if event == "PostToolUse" && suspend(bctx, c, p.ToolName, p.ToolResponse, stdout) {
			return
		}
		if event == "UserPromptSubmit" && p.Prompt != "" {
			// The daemon names an unlabelled agent after its first prompt; the prompt is not
			// stored (hook_event above never carries it).
			_ = c.call(bctx, "name_hint", map[string]any{"prompt": truncate(p.Prompt, maxNamePrompt)}, nil)
		}
		var r struct {
			Count int    `json:"count"`
			Text  string `json:"text"`
		}
		if c.call(bctx, "context_unread", nil, &r) == nil && r.Count > 0 {
			json.NewEncoder(stdout).Encode(map[string]any{"hookSpecificOutput": map[string]any{
				"hookEventName": event, "additionalContext": r.Text}})
		}
	case "PreToolUse":
		// The user's grants (lea grant add) and lea's hard denies: the daemon reads the tool input
		// and the working directory in memory and never stores them. No verdict prints nothing:
		// the call takes the normal path (Claude's checks, the deck). No answer or an error is a
		// deny: the hard denies were not checked.
		if p.ToolName == "" {
			failClosed(stdout, "the hook input names no tool")
			return
		}
		input := p.ToolInput
		if len(input) == 0 {
			input = json.RawMessage(`{}`)
		}
		var r struct {
			Verdict string `json:"verdict"`
			Reason  string `json:"reason"`
			Command string `json:"command"`
			Hint    string `json:"hint"`
		}
		// The environment a pinned command keeps comes from this process (Claude's), never from
		// the agent's shell: the daemon checks each value and drops the rest.
		env := map[string]string{}
		for _, k := range grants.SafeEnv {
			if v := getenv(k); v != "" {
				env[k] = truncate(v, 256)
			}
		}
		if err := c.call(bctx, "grant_check", map[string]any{"tool": p.ToolName, "input": input, "cwd": truncate(p.Cwd, 4096), "env": env}, &r); err != nil {
			failClosed(stdout, "lea could not check the call ("+truncate(err.Error(), 200)+"); retry in a moment")
			return
		}
		if r.Verdict != "allow" && r.Verdict != "deny" {
			// No verdict: the normal path, with the forms the agent's grant covers (#271).
			if r.Verdict == "" && r.Hint != "" {
				json.NewEncoder(stdout).Encode(map[string]any{"hookSpecificOutput": map[string]any{
					"hookEventName": "PreToolUse", "additionalContext": truncate(r.Hint, 1000)}})
			}
			return
		}
		out := map[string]any{"hookEventName": "PreToolUse", "permissionDecision": r.Verdict,
			"permissionDecisionReason": truncate(r.Reason, 500)}
		if r.Verdict == "allow" && p.ToolName == "Bash" {
			// An allowed shell call runs the daemon's pinned command, never the agent's text: no
			// pinned command, no allow.
			var in map[string]any
			if r.Command == "" || json.Unmarshal(input, &in) != nil || in == nil {
				return
			}
			in["command"] = r.Command
			out["updatedInput"] = in
		}
		json.NewEncoder(stdout).Encode(map[string]any{"hookSpecificOutput": out})
	case "PermissionRequest":
		if getenv("LEA_APPROVALS") != "deck" {
			return
		}
		timeout := 60
		if n, err := strconv.Atoi(getenv("LEA_APPROVAL_TIMEOUT")); err == nil && n >= 1 && n <= 600 {
			timeout = n
		}
		input := p.ToolInput
		if len(input) == 0 {
			input = json.RawMessage(`{}`)
		}
		rules := alwaysRules(p.Suggestions)
		var r struct {
			Verdict string `json:"verdict"`
		}
		actx, acancel := context.WithTimeout(ctx, time.Duration(timeout+5)*time.Second)
		defer acancel()
		if c.call(actx, "approval_request",
			map[string]any{"tool": p.ToolName, "input": input, "timeout_s": timeout, "always": rules != nil}, &r) != nil {
			return
		}
		var decision map[string]any
		switch r.Verdict {
		case "allow":
			decision = map[string]any{"behavior": "allow"}
		case "always":
			decision = map[string]any{"behavior": "allow"}
			if rules != nil {
				decision["updatedPermissions"] = rules
			}
		case "deny":
			decision = map[string]any{"behavior": "deny", "message": "Denied from lea"}
		default:
			return
		}
		json.NewEncoder(stdout).Encode(map[string]any{"hookSpecificOutput": map[string]any{
			"hookEventName": "PermissionRequest", "decision": decision}})
	}
}

// failClosed denies the PreToolUse call lea's guard could not check: a missing identity, an
// unreachable bus, an error. Claude Code shows the reason to the model.
func failClosed(stdout io.Writer, why string) {
	json.NewEncoder(stdout).Encode(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": "PreToolUse", "permissionDecision": "deny",
		"permissionDecisionReason": "lea refused the call, its guard fails closed: " + why}})
}

// alwaysRules returns Claude's own "don't ask again" suggestions for this request when it sent
// any (a non-empty JSON array, at most 16 KiB): `A` in the deck applies exactly them, as the
// native dialog's "always" would. They are never stored or forwarded to the daemon.
func alwaysRules(raw json.RawMessage) json.RawMessage {
	var a []json.RawMessage
	if len(raw) == 0 || len(raw) > 16<<10 || json.Unmarshal(raw, &a) != nil || len(a) == 0 {
		return nil
	}
	return raw
}

// MergeBlocked is `lea hook lea merge-blocked <pr>`: the pinned merge of an agent of a tower's
// tree runs it when it refuses an irreversible migration of PR pr, with the migration's lines
// on stdin (at most 2 KiB read). The daemon sends the tower its warning (#110). It never fails.
func MergeBlocked(ctx context.Context, pr string, stdin io.Reader, getenv func(string) string) {
	b, _ := io.ReadAll(io.LimitReader(stdin, 2048))
	c := caller{sock: identity.BusSock(getenv), agent: identity.Agent(getenv)}
	if c.agent == "" {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	_ = c.call(cctx, "merge_blocked", map[string]string{"pr": pr, "lines": strings.ToValidUTF8(string(b), "?")}, nil)
}
