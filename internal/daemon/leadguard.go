package daemon

import (
	"encoding/json"
	"errors"

	"github.com/DavidGodefroid/lea/internal/grants"
	"github.com/DavidGodefroid/lea/internal/store"
)

// builtinAgentTools are the provider's built-in sub-agent tool: Claude Code's Agent, named Task
// before it was renamed (#409).
var builtinAgentTools = map[string]bool{"Agent": true, "Task": true}

// leadAgentToolReason is why a lead may not use the built-in sub-agent tool: its child would be
// invisible to lea (no board, no ctx_pct, no child limits, no host-pressure queue).
const leadAgentToolReason = "lea: you are a lead: start work only with agents_spawn, never with the built-in Agent tool. " +
	"An Agent sub-agent is invisible to lea and the user: it is not on the board or in the deck, its context is not tracked, " +
	"and it bypasses your child limits and the host-pressure queue."

// leadReminder is the tower reminder lea adds to a lead's context after a compaction (#409).
const leadReminder = "lea: you are a lead (control tower). Start work only with agents_spawn (agents_list and board_read to follow it), " +
	"never with the built-in Agent tool: lea denies it to leads. If you were running as a control tower, keep doing so " +
	"(the lea:control-tower skill) and do not do the work yourself."

// leadGuard denies a lead's call of the built-in sub-agent tool (#409); nil lets the call through.
// An agent lea cannot read is let through: the rest of grant_check still applies.
func (d *Daemon) leadGuard(o Origin, me, tool string) map[string]any {
	if o.Container == "" || !builtinAgentTools[tool] {
		return nil
	}
	a, err := d.st.AgentByKey(me)
	if err != nil || !canSpawn(a.Role) {
		return nil
	}
	return map[string]any{"verdict": grants.Deny, "reason": leadAgentToolReason}
}

// sessionContext answers the SessionStart hook: the context lea adds to a session that starts.
// After a compaction a lead gets the tower reminder, as the summary may have lost it (#409).
func (d *Daemon) sessionContext(o Origin, me string, raw json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		Source    string `json:"source"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	none := map[string]any{"text": ""}
	if o.Container == "" || p.Source != "compact" || d.foreignSession(me, p.SessionID, "SessionStart", p.Source) {
		return none, nil
	}
	a, err := d.st.AgentByKey(me)
	if errors.Is(err, store.ErrNotFound) {
		return none, nil
	}
	if err != nil {
		return nil, err
	}
	if !canSpawn(a.Role) {
		return none, nil
	}
	return map[string]any{"text": leadReminder}, nil
}
