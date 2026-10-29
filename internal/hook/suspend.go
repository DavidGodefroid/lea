package hook

import (
	"context"
	"encoding/json"
	"io"
	"regexp"
)

// The suspend hook (#160): a Lock's MCP run that waits for a human's decision on the approval hub
// answers pending (lockkit mcpkit.PendingResult: status, ticket, lock, instance). lea records the
// wait, suspends the agent (hub_wait_register) and ends its turn with continue:false: no Stop
// follows (spike #154), the decision or a message resumes it.

var (
	// lockTool is a Lock's MCP tool, as image/claude-settings.json matches it.
	lockTool = regexp.MustCompile(`^mcp__lock[a-z]*__`)
	// ticketID is a hub ticket (a ULID), hubName a Lock or an instance (store.AddHubWait checks
	// them again).
	ticketID = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)
	hubName  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
)

type pendingWait struct {
	Ticket   string `json:"ticket"`
	Lock     string `json:"lock"`
	Instance string `json:"instance"`
}

// pendingOf is the wait a Lock tool's response asks for. Claude Code hands the structured content
// as a JSON string (spike #154); a response without one comes as content blocks, and a call Claude
// Code moved to the background (over 2 minutes, spike #157) comes as its plain-text notice: neither
// is a pending answer, the hook leaves the turn alone.
func pendingOf(raw json.RawMessage) (pendingWait, bool) {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return pendingWait{}, false
	}
	var r struct {
		Status string `json:"status"`
		pendingWait
	}
	if json.Unmarshal([]byte(s), &r) != nil || r.Status != "pending" ||
		!ticketID.MatchString(r.Ticket) || !hubName.MatchString(r.Lock) || !hubName.MatchString(r.Instance) {
		return pendingWait{}, false
	}
	return r.pendingWait, true
}

// suspend ends the turn of an agent whose Lock call answered pending, once lea recorded the wait;
// false leaves the PostToolUse hook to its usual work.
func suspend(ctx context.Context, c caller, tool string, resp json.RawMessage, stdout io.Writer) bool {
	if !lockTool.MatchString(tool) {
		return false
	}
	w, ok := pendingOf(resp)
	if !ok || c.call(ctx, "hub_wait_register", w, nil) != nil {
		return false
	}
	json.NewEncoder(stdout).Encode(map[string]any{"continue": false,
		"stopReason": "Waiting for the approval of ticket " + w.Ticket + " on " + w.Lock + " (" + w.Instance + ")"})
	return true
}
