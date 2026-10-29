package hook

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/DavidGodefroid/lea/internal/identity"
)

// rewakeWatch is the rewake job's msg_watch, under the hook timeout of an hour that
// claude-settings.json gives it.
const rewakeWatch = 3500

// rewakeGrace lets the Stop hook that runs beside this job finish first: when it blocks the stop,
// it delivers the unread messages in the turn, and the job then has nothing to wake for.
var rewakeGrace = budget

// Rewake is `lea hook claude Rewake`, the Stop hook Claude Code runs in the background
// (asyncRewake): it waits for a message to the agent while its turn is over, then exits 2 with the
// unread notice, and Claude Code wakes the model with it. A lead hears its children, a child the
// answer to its question, without the user typing. It also runs after each Lock MCP call
// (PostToolUse): a turn suspended on a pending Lock decision ends with no Stop (#161), and this
// job is what a message or the decision wakes. It exits 0, waking nobody, when it times out,
// when a newer rewake job of the same agent supersedes it, or when the bus is unreachable.
func Rewake(ctx context.Context, stdin io.Reader, stderr io.Writer, getenv func(string) string) int {
	c := caller{identity.BusSock(getenv), identity.Agent(getenv)}
	_, _ = io.Copy(io.Discard, io.LimitReader(stdin, maxInput))
	if c.agent == "" {
		return 0
	}
	select {
	case <-time.After(rewakeGrace):
	case <-ctx.Done():
		return 0
	}
	var r struct {
		Count      int    `json:"count"`
		Text       string `json:"text"`
		Superseded bool   `json:"superseded"`
	}
	wctx, cancel := context.WithTimeout(ctx, rewakeWatch*time.Second+5*time.Second)
	defer cancel()
	if c.call(wctx, "msg_watch", map[string]any{"timeout_s": rewakeWatch, "rewake": true}, &r) != nil ||
		r.Superseded || r.Count == 0 {
		return 0
	}
	fmt.Fprintln(stderr, r.Text)
	return 2
}
