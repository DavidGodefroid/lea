package hook

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/DavidGodefroid/lea/internal/identity"
)

// ResumeText is all a LimitWait job ever hands the model: lea's own fixed words, never text from
// an agent, a provider or the daemon.
const ResumeText = "lea: the usage limit that stopped your last turn should have reset. Continue your task where you left off."

var (
	// limitBudget bounds a LimitWait job's calls: the 7-day window and most of a day. With the
	// grace and its last call it stays under the hook timeout claude-settings.json gives it
	// (691200 s, 8 days), so the job ends on its own, never killed.
	limitBudget = 8*24*time.Hour - 2*time.Hour
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
	_, _ = io.Copy(io.Discard, io.LimitReader(stdin, maxInput))
	return limitLoop(ctx, stderr, getenv, false)
}

// LimitResume is `lea hook claude LimitResume`, the SessionStart hook Claude Code runs in the
// background (asyncRewake): when the agent's session resumes (source resume: a container restart
// started it again with --resume) while a resume time is pending, it waits for that time as
// LimitWait would have, the job that held it having ended with the old container. The pending
// time is the daemon's (lea.db), never this payload's: only its source, matched exactly, is read.
// Any other start exits 0 at once.
func LimitResume(ctx context.Context, stdin io.Reader, stderr io.Writer, getenv func(string) string) int {
	var p struct {
		Source string `json:"source"`
	}
	in, _ := io.ReadAll(io.LimitReader(stdin, maxInput))
	if json.Unmarshal(in, &p) != nil || p.Source != "resume" {
		return 0
	}
	return limitLoop(ctx, stderr, getenv, true)
}

// limitLoop waits in limit_wait under limitBudget; restart says the job takes over a limit a
// previous container recorded (LimitResume), so the daemon does not wait for one to be recorded.
func limitLoop(ctx context.Context, stderr io.Writer, getenv func(string) string, restart bool) int {
	c := caller{identity.BusSock(getenv), identity.Agent(getenv)}
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
		params := map[string]any{"job": job, "first": first, "timeout_s": limitCall}
		if restart && first {
			params["restart"] = true
		}
		err := c.call(wctx, "limit_wait", params, &r)
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
