package hook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/DavidGodefroid/lea/internal/client"
	"github.com/DavidGodefroid/lea/internal/identity"
)

// rewakeWatch is one msg_watch of the rewake job, under the daemon's longest watch (maxWatch);
// the job renews it until rewakeLife ends.
const rewakeWatch = 3500

var (
	// rewakeGrace lets the Stop hook that runs beside this job finish first: when it blocks the
	// stop, it delivers the unread messages in the turn, and the job then has nothing to wake for.
	rewakeGrace = budget
	// rewakeStartGrace is the job's first wait at a session start: a session resumed with a
	// prompt (lea's resume of a suspended agent) submits it first, and that turn reads the inbox.
	rewakeStartGrace = 10 * time.Second
	// rewakeLife is how long one job waits for a message, its watches renewed: under the hook
	// timeout of 8 days (691200 s) that claude-settings.json gives it. An agent idle for longer
	// hears of no message until the user wakes it (#297).
	rewakeLife = 691200*time.Second - 10*time.Minute
	// rewakeRetry and rewakeRetryMax are the waits between two tries while the bus is
	// unreachable (the daemon restarts): doubling from the first to the second.
	rewakeRetry, rewakeRetryMax = time.Second, 30 * time.Second
)

// Rewake is `lea hook claude Rewake`, the hook Claude Code runs in the background (asyncRewake)
// at each turn end (Stop), at each session start (SessionStart: a session resumed after a
// container restart is idle without a Stop) and after each Lock MCP call (PostToolUse: a turn
// suspended on a pending Lock decision ends with no Stop, #161). It waits for a message to the
// agent while its turn is over, then exits 2 with the unread notice, and Claude Code wakes the
// model with it: a lead hears its children, a child the answer to its question, without the user
// typing.
//
// The daemon decides when the turn is over and holds a wake while the agent is paused, limited
// or woken too often; the job only waits. It renews its watch until rewakeLife ends, and while
// the bus is unreachable (a daemon that restarts) it tries again until the bus is back (#297).
// It exits 0, waking nobody, when its life ends, when a newer rewake job of the same agent
// supersedes it, or when the daemon refuses the watch.
func Rewake(ctx context.Context, stdin io.Reader, stderr io.Writer, getenv func(string) string) int {
	c := caller{identity.BusSock(getenv), identity.Agent(getenv)}
	in, _ := io.ReadAll(io.LimitReader(stdin, maxInput))
	if c.agent == "" {
		return 0
	}
	grace := rewakeGrace
	var p struct {
		Event string `json:"hook_event_name"`
	}
	if json.Unmarshal(in, &p) == nil && p.Event == "SessionStart" {
		grace = rewakeStartGrace
	}
	ctx, cancel := context.WithTimeout(ctx, rewakeLife)
	defer cancel()
	sleep := func(d time.Duration) bool {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
			return true
		case <-ctx.Done():
			return false
		}
	}
	if !sleep(grace) {
		return 0
	}
	retry := rewakeRetry
	for {
		var r struct {
			Count      int    `json:"count"`
			Text       string `json:"text"`
			Superseded bool   `json:"superseded"`
		}
		began := time.Now()
		wctx, wcancel := context.WithTimeout(ctx, rewakeWatch*time.Second+5*time.Second)
		err := c.call(wctx, "msg_watch", map[string]any{"timeout_s": rewakeWatch, "rewake": true}, &r)
		wcancel()
		var refused *client.RemoteError
		switch {
		case ctx.Err() != nil, errors.As(err, &refused):
			return 0
		case err != nil:
			// The bus is unreachable: the daemon restarts, or the connection broke.
			if !sleep(retry) {
				return 0
			}
			retry = min(2*retry, rewakeRetryMax)
			continue
		case r.Superseded:
			return 0
		case r.Count > 0:
			fmt.Fprintln(stderr, r.Text)
			return 2
		}
		// The watch ended without a message: renew it (never in a tight loop).
		if time.Since(began) < time.Second && !sleep(rewakeRetry) {
			return 0
		}
		retry = rewakeRetry
	}
}
