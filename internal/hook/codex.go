package hook

import (
	"context"
	"encoding/json"
	"regexp"

	"github.com/DavidGodefroid/lea/internal/identity"
)

// maxCodexNotify caps the notify payload lea reads (Codex passes it as one argument).
const maxCodexNotify = 64 << 10

var threadRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// CodexNotify is Codex's notify program (internal/providers sets notify = [lea, hook, codex,
// notify]): Codex runs it with one JSON argument at the end of each turn. The payload is the
// agent's output, so untrusted: only a payload of type agent-turn-complete counts, and only its
// thread id, when it is a UUID (the session to resume), reaches the daemon, as the Stop of a
// Claude agent would. The messages and the last answer it holds are never read or forwarded.
func CodexNotify(ctx context.Context, arg string, getenv func(string) string) {
	c := caller{identity.BusSock(getenv), identity.Agent(getenv)}
	if c.agent == "" || len(arg) > maxCodexNotify {
		return
	}
	var p struct {
		Type     string `json:"type"`
		ThreadID string `json:"thread-id"`
	}
	if json.Unmarshal([]byte(arg), &p) != nil || p.Type != "agent-turn-complete" {
		return
	}
	session := ""
	if threadRe.MatchString(p.ThreadID) {
		session = p.ThreadID
	}
	payload := map[string]any{"hook_event_name": "Stop"}
	if session != "" {
		payload["session_id"] = session
	}
	bctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	_ = c.call(bctx, "hook_event", map[string]any{"event": "Stop", "session_id": session, "payload": payload}, nil)
}
