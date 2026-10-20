package hook

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"time"

	"github.com/DavidGodefroid/lea/internal/identity"
)

// ResumeText is all a LimitWait job ever hands the model: lea's own fixed words, never text from
// an agent, a provider or the daemon.
const ResumeText = "lea: the usage limit that stopped your last turn should have reset. Continue your task where you left off."

var (
	// limitBudget bounds a LimitWait job: the 7-day window and a day, under the hook timeout
	// claude-settings.json gives it (691200 s).
	limitBudget = 8 * 24 * time.Hour
	// limitCall is one limit_wait call, under maxWatch.
	limitCall = 3500
	// limitRetry is the pause after a failed call; limitFailures failed calls in a row end the job.
	limitRetry    = time.Minute
	limitFailures = 60
	// limitGrace lets the StopFailure hook that runs beside this job record the limit first.
	limitGrace = budget
)

// LimitWait is `lea hook claude LimitWait`, the StopFailure hook Claude Code runs in the
// background (asyncRewake): when the failure was a usage limit, the daemon answers it at the
// resume time and the job exits 2 with ResumeText, so Claude Code wakes the model. It exits 0,
// waking nobody, when there is nothing to resume (another failure, auto-resume off, lea gave up,
// the agent moved on), when a newer job of the agent supersedes it, past limitBudget, or when the
// bus stays unreachable.
func LimitWait(ctx context.Context, stdin io.Reader, stderr io.Writer, getenv func(string) string) int {
	c := caller{identity.BusSock(getenv), identity.Agent(getenv)}
	_, _ = io.Copy(io.Discard, io.LimitReader(stdin, maxInput))
	if c.agent == "" {
		return 0
	}
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		return 0
	}
	job := hex.EncodeToString(id[:])
	sleep := func(d time.Duration) bool {
		select {
		case <-time.After(d):
			return true
		case <-ctx.Done():
			return false
		}
	}
	if !sleep(limitGrace) {
		return 0
	}
	deadline := time.Now().Add(limitBudget)
	first, fails := true, 0
	for time.Now().Before(deadline) {
		var r struct {
			Resume     bool `json:"resume"`
			Done       bool `json:"done"`
			Superseded bool `json:"superseded"`
		}
		wctx, cancel := context.WithTimeout(ctx, time.Duration(limitCall)*time.Second+5*time.Second)
		err := c.call(wctx, "limit_wait", map[string]any{"job": job, "first": first, "timeout_s": limitCall}, &r)
		cancel()
		if err != nil {
			if fails++; fails >= limitFailures || !sleep(limitRetry) {
				return 0
			}
			continue
		}
		first, fails = false, 0
		switch {
		case r.Superseded || r.Done:
			return 0
		case r.Resume:
			fmt.Fprintln(stderr, ResumeText)
			return 2
		}
	}
	return 0
}
