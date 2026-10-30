package hook

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
)

const ticket = "01JB8Z5Q3W4X7Y9ABCDEFGHJKM"

func postToolUse(tool string, resp any) string {
	in, _ := json.Marshal(map[string]any{"hook_event_name": "PostToolUse", "session_id": "s1", "tool_name": tool,
		"tool_response": resp})
	return string(in)
}

func pendingJSON(instance string) string {
	b, _ := json.Marshal(map[string]string{"status": "pending", "ticket": ticket, "lock": "locksql", "instance": instance})
	return string(b)
}

func hubWaits(t *testing.T, db string) []string {
	t.Helper()
	h, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	rows, err := h.Query(`SELECT agent_key || ' ' || ticket || ' ' || lock || ' ' || instance FROM hub_waits ORDER BY agent_key`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

// A pending answer of a Lock tool (its structured content as a JSON string) records the agent's
// wait and ends its turn.
func TestPendingLockResultEndsTheTurn(t *testing.T) {
	_, bus, db := startDB(t)
	runHook(bus, "SessionStart", `{"session_id":"s1"}`, nil)
	out := runHook(bus, "PostToolUse", postToolUse("mcp__locksql__query_run", pendingJSON("crm-prod")), nil)
	var r struct {
		Continue   *bool  `json:"continue"`
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil || r.Continue == nil || *r.Continue ||
		!strings.Contains(r.StopReason, ticket) {
		t.Fatalf("out=%q", out)
	}
	if ws := hubWaits(t, db); len(ws) != 1 || ws[0] != "lea-a/dev "+ticket+" locksql crm-prod" {
		t.Fatalf("waits %q", ws)
	}
	// tool_response never reaches lea.db's events.
	for _, p := range storedPayloads(t, db, "PostToolUse") {
		if strings.Contains(p, ticket) {
			t.Fatalf("stored payload %s", p)
		}
	}
}

// What is not a pending answer of a Lock tool leaves the turn alone: another tool, a result, the
// notice of a call moved to the background (spike #157), content blocks, an answer without its
// instance, a malformed ticket.
func TestOtherResultsDoNotSuspend(t *testing.T) {
	_, bus, db := startDB(t)
	runHook(bus, "SessionStart", `{"session_id":"s1"}`, nil)
	bg := `MCP tool "locksql/query_run" is still running after 120s. It was moved to the background as task kn8pa8cdu and keeps running; you'll receive a notification with the result when it completes. You can keep working in the meantime. To stop it, use TaskStop with task_id "kn8pa8cdu". Note: it does not survive exiting this session.`
	cases := map[string]string{
		"other tool":     postToolUse("mcp__github__issue", pendingJSON("crm-prod")),
		"result":         postToolUse("mcp__locksql__query_run", `{"rows":[[1]]}`),
		"background":     postToolUse("mcp__locksql__query_run", bg),
		"content blocks": postToolUse("mcp__locksql__query_run", []map[string]string{{"type": "text", "text": "Pending human approval: ticket " + ticket + " on locksql."}}),
		"object":         postToolUse("mcp__locksql__query_run", map[string]string{"status": "pending", "ticket": ticket, "lock": "locksql", "instance": "crm-prod"}),
		"no instance":    postToolUse("mcp__locksql__query_run", pendingJSON("")),
		"bad ticket":     postToolUse("mcp__locksql__query_run", strings.Replace(pendingJSON("crm-prod"), ticket, "42", 1)),
		"status ok":      postToolUse("mcp__locksql__query_run", strings.Replace(pendingJSON("crm-prod"), `"pending"`, `"ok"`, 1)),
	}
	for name, in := range cases {
		if out := runHook(bus, "PostToolUse", in, nil); strings.Contains(out, "continue") {
			t.Fatalf("%s: out=%q", name, out)
		}
	}
	if ws := hubWaits(t, db); len(ws) != 0 {
		t.Fatalf("waits %q", ws)
	}
}
