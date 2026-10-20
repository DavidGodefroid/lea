package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"regexp"
	"time"

	"github.com/DavidGodefroid/lea/internal/providers"
	"github.com/DavidGodefroid/lea/internal/store"
)

// Auto-resume after a usage limit (issue #20).
//
// Detection is structured: Claude Code's StopFailure hook names the error class of the failed
// turn, and providers.UsageLimit tells which class is a usage limit. The reset time comes from the
// rate-limit windows the agent's status line reports (numbers only). No text the model or the
// provider wrote is read.
//
// Resuming types nothing into a terminal: the StopFailure hook starts a background job (lea hook
// claude LimitWait, an asyncRewake hook) that waits in limit_wait. At the resume time the daemon
// answers it, and the job exits 2 with lea's fixed resume text, which Claude Code hands to the
// model as it wakes it. Every resume is bounded: at most maxResumeTries without progress, never
// sooner than minResumeWait after the limit, never later than maxResumeHorizon, each one logged
// and recorded as an event. LEA_AUTO_RESUME=0 turns it off: the agent then shows as limited and
// waits for the user.

var (
	// maxResumeTries caps the resumes of one agent without progress (a tool used or a turn that
	// ended well): past it lea gives up and the agent waits for the user.
	maxResumeTries = 6
	// minResumeWait is the shortest wait after a limit, whatever the reset time says.
	minResumeWait int64 = 60
	// retryBase and retryMax are the backoff without a known reset time: 5 min, doubling, at
	// most an hour.
	retryBase int64 = 300
	retryMax  int64 = 3600
	// maxResumeHorizon is the latest reset time believed: the 7-day window and a day of slack.
	maxResumeHorizon int64 = 8 * 24 * 3600
	// resumeJitter spreads the agents of one account over this many seconds after the reset.
	resumeJitter int64 = 30
	// pausedRecheck is how often a waiting job looks again at an agent the user paused.
	pausedRecheck = time.Minute
	// limitSettle is how long a new job waits for its limit to be recorded: the StopFailure hook
	// that records it runs beside the job, and may land after the job's first call.
	limitSettle = 30 * time.Second
)

// DefaultResumeDelay is the default wait after the reset time (LEA_RESUME_DELAY), in seconds.
const DefaultResumeDelay = 60

// MaxResumeDelay caps LEA_RESUME_DELAY.
const MaxResumeDelay = 3600

// limitJob is a LimitWait job's id: the newest job of an agent supersedes the older ones.
var limitJob = regexp.MustCompile(`^[0-9a-f]{16,32}$`)

// limitWindow is one rate-limit window the agent's status line reported.
type limitWindow struct {
	pct      int
	resetsAt int64
}

// SetResume is the user's auto-resume setting: on (LEA_AUTO_RESUME, on unless 0) and the wait
// after the reset time, in seconds (LEA_RESUME_DELAY, 0 to MaxResumeDelay).
func (d *Daemon) SetResume(on bool, delay int64) {
	if delay < 0 || delay > MaxResumeDelay {
		delay = DefaultResumeDelay
	}
	d.mu.Lock()
	d.resumeOff, d.resumeDelay = !on, delay
	d.mu.Unlock()
}

// recordLimits keeps the windows of an agent's last status report.
func (d *Daemon) recordLimits(me string, ws []limitWindow) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(ws) == 0 {
		delete(d.windows, me)
		return
	}
	d.windows[me] = ws
}

// resetAt is when the agent's limit resets, 0 when unknown: the latest reset of a full window,
// else of a window at 90% or more (the report may predate the last request). A reset in the past
// or beyond maxResumeHorizon is not believed.
func (d *Daemon) resetAt(me string, now int64) int64 {
	d.mu.Lock()
	ws := d.windows[me]
	d.mu.Unlock()
	for _, full := range []int{100, 90} {
		best := int64(0)
		for _, w := range ws {
			if w.pct >= full && w.resetsAt > now && w.resetsAt <= now+maxResumeHorizon && w.resetsAt > best {
				best = w.resetsAt
			}
		}
		if best > 0 {
			return best
		}
	}
	return 0
}

// agentCLI is the CLI an agent runs: its provider's, Claude for an agent lea did not start.
func (d *Daemon) agentCLI(a store.Agent) (string, error) {
	if a.Profile == "" {
		return providers.Claude, nil
	}
	reg, err := d.registry()
	if err != nil {
		return "", err
	}
	p, err := reg.Get(a.Profile)
	if err != nil {
		return "", err
	}
	return p.CLI, nil
}

// turnFailed handles a StopFailure: when its error class is the agent's provider's usage limit,
// the agent is marked limited and, auto-resume on, given a resume time.
func (d *Daemon) turnFailed(me string, payload json.RawMessage) {
	var p struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(payload, &p) != nil || p.Error == "" {
		return
	}
	a, err := d.st.AgentByKey(me)
	if err != nil {
		log.Printf("lea daemon: read %s: %v", me, err)
		return
	}
	cli, err := d.agentCLI(a)
	if err != nil || !providers.UsageLimit(cli, p.Error) {
		return
	}
	now := d.now()
	reset := d.resetAt(me, now)
	d.mu.Lock()
	off, delay := d.resumeOff, d.resumeDelay
	d.mu.Unlock()
	until, why := int64(0), ""
	switch {
	case off:
		until, why = -1, "auto-resume is off"
	case a.LimitTries >= maxResumeTries:
		until, why = -1, fmt.Sprintf("gave up after %d resumes", a.LimitTries)
	case reset > 0:
		until, why = reset+delay+jitter(), "after the reset"
	default:
		backoff := retryBase << min(a.LimitTries, 8)
		until, why = now+min(backoff, retryMax)+jitter(), "backing off, no reset time known"
	}
	if until > 0 && until < now+minResumeWait {
		until = now + minResumeWait
	}
	if err := d.st.SetLimit(me, until, a.LimitTries); err != nil {
		log.Printf("lea daemon: limit of %s: %v", me, err)
		return
	}
	b, _ := json.Marshal(map[string]any{"until": until, "reset": reset, "tries": a.LimitTries})
	if err := d.st.AddEvent(me, "limit", "hit", string(b), now); err != nil {
		log.Printf("lea daemon: limit event of %s: %v", me, err)
	}
	if until > 0 {
		log.Printf("lea daemon: %s hit its usage limit; resume in %d s, %s (try %d of %d)", me, until-now, why, a.LimitTries+1, maxResumeTries)
	} else {
		log.Printf("lea daemon: %s hit its usage limit; %s, it waits for the user", me, why)
	}
	d.changed()
	d.wake(limitKey(me))
}

// jitter is a random wait of 0 to resumeJitter seconds.
func jitter() int64 {
	if resumeJitter <= 0 {
		return 0
	}
	return rand.Int64N(resumeJitter + 1)
}

func limitKey(me string) string { return "limit:" + me }

// limitProgress clears an agent's limit when it is past it: it used a tool or ended a turn well
// (progress: the tries start over), or a session started or ended. A prompt (the user's, or the
// turn a resume started) ends the wait but keeps the tries: only progress shows the limit is gone.
func (d *Daemon) limitProgress(me, event string) {
	a, err := d.st.AgentByKey(me)
	if err != nil || (a.LimitUntil == 0 && a.LimitTries == 0) {
		return
	}
	tries := 0
	switch event {
	case "PreToolUse", "PostToolUse", "Stop", "SessionStart", "SessionEnd":
	case "UserPromptSubmit":
		if a.LimitUntil == 0 {
			return
		}
		tries = a.LimitTries
	default:
		return
	}
	if err := d.st.SetLimit(me, 0, tries); err != nil {
		log.Printf("lea daemon: clear the limit of %s: %v", me, err)
		return
	}
	if a.LimitUntil != 0 {
		log.Printf("lea daemon: %s is past its usage limit (%s)", me, event)
		d.changed()
	}
	d.wake(limitKey(me))
}

// limitWait is the LimitWait job's call: it answers resume once the agent's resume time has
// come (and records the try), done when there is nothing to wait for (no limit, auto-resume off,
// lea gave up), superseded when a newer job of the agent took over, or nothing at the timeout
// (the job calls again). first registers the job as the agent's newest. Agents only.
func (d *Daemon) limitWait(ctx context.Context, o Origin, me string, raw json.RawMessage) (any, error) {
	if o.Container == "" {
		return nil, errors.New("limit_wait: agents only")
	}
	var p struct {
		Job      string `json:"job"`
		First    bool   `json:"first"`
		TimeoutS int    `json:"timeout_s"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if !limitJob.MatchString(p.Job) {
		return nil, errors.New("limit_wait: invalid job id")
	}
	if p.TimeoutS < 1 || p.TimeoutS > maxWatch {
		return nil, fmt.Errorf("timeout_s must be between 1 and %d", maxWatch)
	}
	d.mu.Lock()
	if p.First || d.limitJobs[me] == "" {
		// A daemon restarted meanwhile knows no job: the first to call again is the newest.
		d.limitJobs[me] = p.Job
	}
	d.mu.Unlock()
	if p.First {
		d.wake(limitKey(me)) // the older job sees it is superseded
	}
	deadline := time.Now().Add(time.Duration(p.TimeoutS) * time.Second)
	// A new job finding no limit to wait for (none, or a stale -1 of an earlier limit) waits a
	// while for turnFailed (which wakes it) before it is done: answered done too early, it would
	// leave a limit recorded later without a waiter.
	var settle time.Time
	if p.First {
		settle = time.Now().Add(limitSettle)
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ch := d.waitChan(limitKey(me))
		d.mu.Lock()
		cur := d.limitJobs[me]
		d.mu.Unlock()
		if cur != p.Job {
			return map[string]any{"superseded": true}, nil
		}
		res, wait, idle, err := d.limitDue(me)
		switch {
		case !idle:
			settle = time.Time{} // a resume time was seen: from now on, none means it is over
		case time.Now().Before(settle):
			res, wait = nil, time.Until(settle)
		}
		if err != nil || res != nil {
			return res, err
		}
		left := time.Until(deadline)
		if left <= 0 {
			return map[string]any{}, nil
		}
		t := time.NewTimer(min(wait, left))
		select {
		case <-ch:
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		}
		t.Stop()
	}
}

// limitDue checks an agent's limit: resume (claimed: the time is cleared and the try counted)
// when it is due, done when there is no resume to wait for (idle: no resume time is recorded),
// or how long to wait.
func (d *Daemon) limitDue(me string) (res map[string]any, wait time.Duration, idle bool, err error) {
	defer d.lockKey(limitKey(me))()
	a, err := d.st.AgentByKey(me)
	if err != nil {
		return nil, 0, false, err
	}
	if a.LimitUntil <= 0 {
		return map[string]any{"done": true}, 0, true, nil
	}
	now := d.now()
	if a.Paused {
		return nil, pausedRecheck, false, nil
	}
	if now < a.LimitUntil {
		return nil, time.Duration(a.LimitUntil-now) * time.Second, false, nil
	}
	if err := d.st.SetLimit(me, 0, a.LimitTries+1); err != nil {
		return nil, 0, false, err
	}
	b, _ := json.Marshal(map[string]any{"try": a.LimitTries + 1})
	if err := d.st.AddEvent(me, "limit", "resume", string(b), now); err != nil {
		log.Printf("lea daemon: resume event of %s: %v", me, err)
	}
	log.Printf("lea daemon: resuming %s after its usage limit (try %d of %d)", me, a.LimitTries+1, maxResumeTries)
	d.changed()
	return map[string]any{"resume": true}, 0, false, nil
}
