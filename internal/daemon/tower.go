package daemon

import (
	"encoding/json"
	"strings"
)

// The control tower's guard (skill control-tower, #383): once an agent runs the skill, every tool
// but the team's is denied, so the tower delegates the work instead of doing it. The mark is the
// agent's own row in lea's store (store.SetTower), so a tower resumed in a new container or on a
// new session is held to it from its first tool call. Only the user's prompt
// "/lea:control-tower off" (tower_prompt, from the agent's UserPromptSubmit hook) turns it off:
// no tool the tower may call does, and lea's PreToolUse hook (grant_check) fails closed.

// towerTools are the tools a tower keeps: the team's, questions to the user, and loading a
// skill (its text only: /check among them).
var towerTools = map[string]bool{
	"mcp__lea__agents_spawn": true, "mcp__lea__agents_list": true, "mcp__lea__agents_stop": true, "mcp__lea__agents_request_spawn": true,
	"mcp__lea__msg_send": true, "mcp__lea__msg_reply": true, "mcp__lea__msg_inbox": true, "mcp__lea__msg_wait": true,
	"mcp__lea__board_read": true, "mcp__lea__task_update": true, "mcp__lea__grants_list": true, "mcp__lea__grants_request": true,
	"AskUserQuestion": true, "ToolSearch": true, "TodoWrite": true, "Skill": true,
}

// towerDeny is the reason a tower's other tool calls are denied with.
const towerDeny = "control-tower: you are the control tower, you do not act yourself. Give this to an agent: agents_spawn a new one, or msg_send the agent already on this subject. Your tools: agents_spawn, agents_list, agents_stop, msg_send, msg_reply, msg_inbox, msg_wait, board_read, task_update, grants_list, grants_request, questions to the user, and the Bash job 'lea inbox --agent --wait'. The user leaves tower mode with '/lea:control-tower off'."

// towerCommand reads the first line of a prompt: on is true for "/lea:control-tower [...]", false
// for "/lea:control-tower off" (and the short names /control-tower); set is false for any other
// prompt, one that only mentions the skill among them.
func towerCommand(prompt string) (on, set bool) {
	line, _, _ := strings.Cut(prompt, "\n")
	line = strings.TrimSpace(line)
	for _, name := range []string{"/lea:control-tower", "/control-tower"} {
		rest, ok := strings.CutPrefix(line, name)
		switch {
		case !ok:
		case rest == " off":
			return false, true
		case rest == "" || rest[0] == ' ' || rest[0] == '\t':
			return true, true
		}
	}
	return false, false
}

// towerAllows says whether a tower may make the call: one of towerTools, or the one Bash command
// that wakes it when an agent writes to it.
func towerAllows(tool string, input json.RawMessage) bool {
	if towerTools[tool] {
		return true
	}
	if tool != "Bash" {
		return false
	}
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(input, &in) != nil {
		return false
	}
	const wait = "lea inbox --agent --wait"
	if in.Command == wait {
		return true
	}
	n, ok := strings.CutPrefix(in.Command, wait+" ")
	if !ok || n == "" {
		return false
	}
	for _, r := range n {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// towerPrompt is tower_prompt: the agent's prompt turns its tower mark on or off. A nested
// Claude under the agent's identity, on its own session, changes nothing.
func (d *Daemon) towerPrompt(me string, raw json.RawMessage) (any, error) {
	var p struct {
		Prompt    string `json:"prompt"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	on, set := towerCommand(p.Prompt)
	if !set || d.foreignSession(me, p.SessionID, "", "") {
		return struct{}{}, nil
	}
	if err := d.st.SetTower(me, on); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// towerCheck is the tower guard's part of grant_check: loading the skill with the Skill tool
// turns the mark on; while it is on, a call the tower may not make is denied (deny true). An
// error is grant_check's: the hook denies the call it could not check.
func (d *Daemon) towerCheck(me, tool string, input json.RawMessage) (deny bool, err error) {
	if tool == "Skill" {
		var in struct {
			Skill string `json:"skill"`
		}
		if json.Unmarshal(input, &in) == nil && (in.Skill == "lea:control-tower" || in.Skill == "control-tower") {
			return false, d.st.SetTower(me, true)
		}
	}
	on, err := d.st.Tower(me)
	if err != nil || !on {
		return false, err
	}
	return !towerAllows(tool, input), nil
}
