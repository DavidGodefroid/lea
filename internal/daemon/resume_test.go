package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/store"
)

const job1, job2 = "0123456789abcdef", "fedcba9876543210"

// quickSettle shortens the settle window of a new job.
func quickSettle(t *testing.T) {
	t.Helper()
	s := limitSettle
	limitSettle = 100 * time.Millisecond
	t.Cleanup(func() { limitSettle = s })
}

// noJitter makes resume times exact for the test.
func noJitter(t *testing.T) {
	t.Helper()
	j := resumeJitter
	resumeJitter = 0
	t.Cleanup(func() { resumeJitter = j })
}

func agentRow(t *testing.T, e env, key string) store.Agent {
	t.Helper()
	a, err := e.d.st.AgentByKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func limitHit(t *testing.T, e env, agent, class string) {
	t.Helper()
	if err := call(t, e.bus, agent, "hook_event", map[string]any{"event": "StopFailure", "session_id": "s1",
		"payload": map[string]any{"hook_event_name": "StopFailure", "error": class}}, nil); err != nil {
		t.Fatal(err)
	}
}

type waitAnswer struct {
	Resume     bool `json:"resume"`
	Done       bool `json:"done"`
	Superseded bool `json:"superseded"`
}

func limitWait(t *testing.T, e env, agent, job string, first bool, timeoutS int) waitAnswer {
	t.Helper()
	var r waitAnswer
	if err := call(t, e.bus, agent, "limit_wait", map[string]any{"job": job, "first": first, "timeout_s": timeoutS}, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func reportLimits(t *testing.T, e env, agent string, limits ...map[string]any) {
	t.Helper()
	if err := call(t, e.bus, agent, "status_report", map[string]any{"ctx_pct": 10, "cost_usd": -1, "limits": limits}, nil); err != nil {
		t.Fatal(err)
	}
}

// A usage limit (Claude's StopFailure rate_limit) ends the turn, marks the agent limited until
// the reset its status line announced plus the delay, and its LimitWait job is answered resume
// then, once: the try is counted and the limit cleared.
func TestUsageLimitResumesAtTheReset(t *testing.T) {
	noJitter(t)
	e := start(t)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "UserPromptSubmit", "session_id": "s1"}, nil)
	reportLimits(t, e, "dev", map[string]any{"window": "5h", "pct": 100, "resets_at": 1600},
		map[string]any{"window": "7d", "pct": 40, "resets_at": 90000})
	limitHit(t, e, "dev", "rate_limit")
	a := agentRow(t, e, "lea-a/dev")
	if a.State != "idle" || a.LimitUntil != 1600+DefaultResumeDelay || a.LimitTries != 0 {
		t.Fatalf("agent=%+v", a)
	}
	if ps, _ := e.d.st.EventPayloads("lea-a/dev", "hit"); len(ps) != 1 || !strings.Contains(ps[0], `"reset":1600`) {
		t.Fatalf("events=%v", ps)
	}
	// Not due yet: the job is told nothing and calls again.
	if r := limitWait(t, e, "dev", job1, true, 1); r != (waitAnswer{}) {
		t.Fatalf("early answer %+v", r)
	}
	e.clock.Store(1660)
	if r := limitWait(t, e, "dev", job1, false, 1); !r.Resume {
		t.Fatalf("not resumed: %+v", r)
	}
	if a := agentRow(t, e, "lea-a/dev"); a.LimitUntil != 0 || a.LimitTries != 1 {
		t.Fatalf("agent=%+v", a)
	}
	if ps, _ := e.d.st.EventPayloads("lea-a/dev", "resume"); len(ps) != 1 || ps[0] != `{"try":1}` {
		t.Fatalf("events=%v", ps)
	}
	// Resumed once: a job calling again has nothing left to wait for.
	if r := limitWait(t, e, "dev", job1, false, 1); !r.Done {
		t.Fatalf("resumed twice: %+v", r)
	}
}

// A job waiting for a reset hours away is answered at that time, not at its call's timeout: the
// waiter sleeps until the resume time.
func TestLimitWaitWakesAtTheResumeTime(t *testing.T) {
	noJitter(t)
	m := minResumeWait
	minResumeWait = 1
	t.Cleanup(func() { minResumeWait = m })
	e := start(t)
	e.d.SetResume(true, 0)
	reportLimits(t, e, "dev", map[string]any{"window": "5h", "pct": 100, "resets_at": 1001})
	limitHit(t, e, "dev", "rate_limit")
	go func() { time.Sleep(300 * time.Millisecond); e.clock.Store(1001) }()
	start := time.Now()
	if r := limitWait(t, e, "dev", job1, true, 4); !r.Resume {
		t.Fatalf("answer %+v", r)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("answered after %v", d)
	}
}

// Without a known reset, lea backs off: 5 minutes, doubling per try, at most an hour; past
// maxResumeTries it gives up and the agent waits for the user.
func TestUsageLimitBacksOffThenGivesUp(t *testing.T) {
	quickSettle(t)
	noJitter(t)
	e := start(t)
	call(t, e.bus, "dev", "agents_list", nil, nil)
	for tries, want := range []int64{1300, 1600, 2200, 3400, 4600, 4600, -1} {
		if err := e.d.st.SetLimit("lea-a/dev", 0, tries); err != nil {
			t.Fatal(err)
		}
		limitHit(t, e, "dev", "rate_limit")
		if a := agentRow(t, e, "lea-a/dev"); a.LimitUntil != want || a.LimitTries != tries {
			t.Fatalf("tries %d: until=%d want %d", tries, a.LimitUntil, want)
		}
	}
	if r := limitWait(t, e, "dev", job1, true, 1); !r.Done {
		t.Fatalf("a job waited for an agent lea gave up on: %+v", r)
	}
}

// A reset in the past, too far ahead, or too close is not taken as is.
func TestUsageLimitResetIsBounded(t *testing.T) {
	noJitter(t)
	e := start(t)
	e.d.SetResume(true, 0)
	for _, c := range []struct {
		name        string
		pct         int
		reset, want int64
	}{
		{"past", 100, 999, 1300},
		{"beyond the horizon", 100, 1000 + maxResumeHorizon + 1, 1300},
		{"in ten seconds", 100, 1010, 1000 + minResumeWait},
		{"a window not full", 50, 2000, 1300},
		{"a window nearly full", 95, 2000, 2000},
	} {
		reportLimits(t, e, "dev", map[string]any{"window": "7d", "pct": c.pct, "resets_at": c.reset})
		e.d.st.SetLimit("lea-a/dev", 0, 0)
		limitHit(t, e, "dev", "rate_limit")
		if a := agentRow(t, e, "lea-a/dev"); a.LimitUntil != c.want {
			t.Errorf("%s: until=%d want %d", c.name, a.LimitUntil, c.want)
		}
	}
}

// Auto-resume off: the agent shows limited and waits for the user; its job ends at once.
func TestUsageLimitWithAutoResumeOff(t *testing.T) {
	quickSettle(t)
	e := start(t)
	e.d.SetResume(false, 60)
	limitHit(t, e, "dev", "rate_limit")
	if a := agentRow(t, e, "lea-a/dev"); a.LimitUntil != -1 {
		t.Fatalf("agent=%+v", a)
	}
	if r := limitWait(t, e, "dev", job1, true, 1); !r.Done {
		t.Fatalf("answer %+v", r)
	}
}

// Other failures end the turn without a limit, and Codex reports no limit through this hook.
func TestOtherFailuresAreNotUsageLimits(t *testing.T) {
	e := start(t)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "UserPromptSubmit"}, nil)
	for _, class := range []string{"overloaded", "authentication_failed", "", "unknown"} {
		limitHit(t, e, "dev", class)
		if a := agentRow(t, e, "lea-a/dev"); a.State != "idle" || a.LimitUntil != 0 {
			t.Fatalf("%q: agent=%+v", class, a)
		}
	}
	if _, err := e.d.st.RegisterAgent("lea-a", "cx", "codex", "/p", 1); err != nil {
		t.Fatal(err)
	}
	limitHit(t, e, "cx", "rate_limit")
	if a := agentRow(t, e, "lea-a/cx"); a.LimitUntil != 0 {
		t.Fatalf("codex agent limited: %+v", a)
	}
}

// Progress (a tool, a turn that ends well, a new session) clears the limit and its tries; a
// prompt ends the wait (its waiting job is done at once) but keeps the tries.
func TestProgressClearsTheLimit(t *testing.T) {
	e := start(t)
	for _, ev := range []string{"PreToolUse", "PostToolUse", "Stop", "SessionStart", "SessionEnd"} {
		e.d.st.SetLimit("lea-a/dev", 0, 2)
		limitHit(t, e, "dev", "rate_limit")
		call(t, e.bus, "dev", "hook_event", map[string]any{"event": ev}, nil)
		if a := agentRow(t, e, "lea-a/dev"); a.LimitUntil != 0 || a.LimitTries != 0 {
			t.Fatalf("%s: agent=%+v", ev, a)
		}
	}
	e.d.st.SetLimit("lea-a/dev", 0, 2)
	limitHit(t, e, "dev", "rate_limit")
	done := make(chan waitAnswer, 1)
	go func() { done <- limitWait(t, e, "dev", job1, true, 5) }()
	time.Sleep(200 * time.Millisecond)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "UserPromptSubmit"}, nil)
	select {
	case r := <-done:
		if !r.Done {
			t.Fatalf("answer %+v", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the job kept waiting after the user's prompt")
	}
	if a := agentRow(t, e, "lea-a/dev"); a.LimitUntil != 0 || a.LimitTries != 2 {
		t.Fatalf("agent=%+v", a)
	}
}

// The newest job of an agent supersedes the older one, which ends without waking the model.
func TestANewerLimitJobSupersedes(t *testing.T) {
	e := start(t)
	limitHit(t, e, "dev", "rate_limit")
	done := make(chan waitAnswer, 1)
	go func() { done <- limitWait(t, e, "dev", job1, true, 5) }()
	time.Sleep(200 * time.Millisecond)
	newer := make(chan waitAnswer, 1)
	go func() { newer <- limitWait(t, e, "dev", job2, true, 1) }()
	defer func() { <-newer }()
	select {
	case r := <-done:
		if !r.Superseded {
			t.Fatalf("answer %+v", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the older job was not superseded")
	}
	// The older job calling again stays superseded.
	if r := limitWait(t, e, "dev", job1, false, 1); !r.Superseded {
		t.Fatalf("answer %+v", r)
	}
}

// A paused agent is not resumed: its job waits until the user lets it continue.
func TestAPausedAgentIsNotResumed(t *testing.T) {
	noJitter(t)
	e := start(t)
	if _, err := e.d.st.RegisterAgent("lea-a", "dev", "claude", "/p", 1); err != nil {
		t.Fatal(err)
	}
	limitHit(t, e, "dev", "rate_limit")
	e.d.st.SetPaused("lea-a/dev", true)
	e.clock.Store(5000)
	if r := limitWait(t, e, "dev", job1, true, 1); r != (waitAnswer{}) {
		t.Fatalf("paused agent answered %+v", r)
	}
	e.d.st.SetPaused("lea-a/dev", false)
	if r := limitWait(t, e, "dev", job1, false, 1); !r.Resume {
		t.Fatalf("answer %+v", r)
	}
}

func TestLimitWaitRefusesBadCalls(t *testing.T) {
	e := start(t)
	if err := call(t, e.api, "", "limit_wait", map[string]any{"job": job1, "first": true, "timeout_s": 1}, nil); err == nil {
		t.Fatal("the host API waited on a limit")
	}
	for _, p := range []map[string]any{
		{"job": "x", "first": true, "timeout_s": 1},
		{"job": job1 + "\n", "first": true, "timeout_s": 1},
		{"job": job1, "first": true, "timeout_s": 0},
		{"job": job1, "first": true, "timeout_s": maxWatch + 1},
	} {
		if err := call(t, e.bus, "dev", "limit_wait", p, nil); err == nil {
			t.Errorf("accepted %v", p)
		}
	}
}

// The status line's windows are checked: an agent cannot report what is not a window.
func TestStatusReportChecksLimits(t *testing.T) {
	e := start(t)
	for _, l := range []map[string]any{
		{"window": "1h", "pct": 50, "resets_at": 2000},
		{"window": "5h", "pct": 101, "resets_at": 2000},
		{"window": "5h", "pct": -1, "resets_at": 2000},
		{"window": "5h", "pct": 50, "resets_at": -5},
		{"window": "5h", "pct": 50, "resets_at": 1e12},
	} {
		if err := call(t, e.bus, "dev", "status_report", map[string]any{"ctx_pct": 1, "cost_usd": -1, "limits": []any{l}}, nil); err == nil {
			t.Errorf("accepted %v", l)
		}
	}
	three := []any{map[string]any{"window": "5h"}, map[string]any{"window": "7d"}, map[string]any{"window": "5h"}}
	if err := call(t, e.bus, "dev", "status_report", map[string]any{"ctx_pct": 1, "cost_usd": -1, "limits": three}, nil); err == nil {
		t.Error("accepted three windows")
	}
}

// The StopFailure hook that records the limit runs beside the job and may land after the job's
// first call: the job waits for it (turnFailed wakes it) instead of ending, so a limit is never
// left without a waiter, shown "resumes at" forever.
func TestALimitRecordedAfterTheJobsFirstCallIsStillResumed(t *testing.T) {
	noJitter(t)
	e := start(t)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "UserPromptSubmit"}, nil)
	done := make(chan waitAnswer, 1)
	go func() { done <- limitWait(t, e, "dev", job1, true, 4) }()
	time.Sleep(300 * time.Millisecond)
	limitHit(t, e, "dev", "rate_limit")
	e.clock.Add(10_000)
	e.d.wake(limitKey("lea-a/dev"))
	select {
	case r := <-done:
		if !r.Resume {
			t.Fatalf("a late limit was lost: %+v", r)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("the job never answered")
	}
}

// A job for a failure that is not a limit ends once the settle window passes, and a job that saw
// its limit end (the user took over) ends at once.
func TestAJobWithoutALimitEndsAfterTheSettleWindow(t *testing.T) {
	s := limitSettle
	limitSettle = 300 * time.Millisecond
	t.Cleanup(func() { limitSettle = s })
	e := start(t)
	limitHit(t, e, "dev", "overloaded")
	start := time.Now()
	if r := limitWait(t, e, "dev", job1, true, 4); !r.Done {
		t.Fatalf("answer %+v", r)
	}
	if d := time.Since(start); d < 250*time.Millisecond || d > 3*time.Second {
		t.Fatalf("done after %v", d)
	}
	// A job that is not new (first=false) never settles: no limit is done at once.
	start = time.Now()
	if r := limitWait(t, e, "dev", job1, false, 4); !r.Done || time.Since(start) > 200*time.Millisecond {
		t.Fatalf("answer %+v after %v", r, time.Since(start))
	}
}

// A stale -1 (an earlier limit lea gave up on, or auto-resume off) does not end a new job: the
// new limit, recorded after the job's first call, is still resumed.
func TestAStaleGiveUpDoesNotLoseANewLimit(t *testing.T) {
	noJitter(t)
	e := start(t)
	if err := e.d.st.SetLimit("lea-a/dev", -1, 0); err != nil {
		call(t, e.bus, "dev", "agents_list", nil, nil)
		e.d.st.SetLimit("lea-a/dev", -1, 0)
	}
	done := make(chan waitAnswer, 1)
	go func() { done <- limitWait(t, e, "dev", job1, true, 4) }()
	time.Sleep(300 * time.Millisecond)
	limitHit(t, e, "dev", "rate_limit")
	e.clock.Add(10_000)
	e.d.wake(limitKey("lea-a/dev"))
	select {
	case r := <-done:
		if !r.Resume {
			t.Fatalf("a new limit after a stale -1 was lost: %+v", r)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("the job never answered")
	}
}

func restartWait(t *testing.T, e env, agent, job string, timeoutS int) waitAnswer {
	t.Helper()
	var r waitAnswer
	if err := call(t, e.bus, agent, "limit_wait", map[string]any{"job": job, "first": true, "restart": true, "timeout_s": timeoutS}, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// restartContainer does to an agent what lea stop and the next container do: stopped, then
// started again (unknown), then its resumed session's SessionStart.
func restartContainer(t *testing.T, e env, agent string) {
	t.Helper()
	for _, st := range []string{"stopped", "unknown"} {
		if err := e.d.st.SetState("lea-a/"+agent, st, "", e.clock.Load()); err != nil {
			t.Fatal(err)
		}
	}
	if err := call(t, e.bus, agent, "hook_event", map[string]any{"event": "SessionStart", "session_id": "s1",
		"payload": map[string]any{"hook_event_name": "SessionStart", "source": "resume"}}, nil); err != nil {
		t.Fatal(err)
	}
}

// A container restart keeps a pending resume (#83): the resumed session's SessionStart keeps the
// limit, and its LimitResume job (restart) is answered resume at the recorded time, once.
func TestAPendingResumeSurvivesAContainerRestart(t *testing.T) {
	noJitter(t)
	e := start(t)
	reportLimits(t, e, "dev", map[string]any{"window": "5h", "pct": 100, "resets_at": 1600})
	limitHit(t, e, "dev", "rate_limit")
	e.d.st.SetLimit("lea-a/dev", 1660, 2)
	restartContainer(t, e, "dev")
	if a := agentRow(t, e, "lea-a/dev"); a.LimitUntil != 1660 || a.LimitTries != 2 || a.State != "idle" {
		t.Fatalf("the restart lost the limit: %+v", a)
	}
	if r := restartWait(t, e, "dev", job1, 1); r != (waitAnswer{}) {
		t.Fatalf("early answer %+v", r)
	}
	e.clock.Store(1660)
	if r := limitWait(t, e, "dev", job1, false, 1); !r.Resume {
		t.Fatalf("not resumed: %+v", r)
	}
	if a := agentRow(t, e, "lea-a/dev"); a.LimitUntil != 0 || a.LimitTries != 3 {
		t.Fatalf("agent=%+v", a)
	}
}

// Any other session start still clears the limit: a new or cleared session is not the one that
// hit it. A source outside Claude Code's four counts as none.
func TestOnlyAResumedSessionKeepsTheLimit(t *testing.T) {
	e := start(t)
	for _, src := range []string{"startup", "clear", "compact", "", "resume "} {
		limitHit(t, e, "dev", "rate_limit")
		call(t, e.bus, "dev", "hook_event", map[string]any{"event": "SessionStart",
			"payload": map[string]any{"hook_event_name": "SessionStart", "source": src}}, nil)
		if a := agentRow(t, e, "lea-a/dev"); a.LimitUntil != 0 {
			t.Fatalf("source %q kept the limit: %+v", src, a)
		}
	}
}

// A restart job finds nothing to settle for: without a pending limit it is done at once, not
// after the settle window a StopFailure's job waits.
func TestARestartJobWithoutALimitIsDoneAtOnce(t *testing.T) {
	e := start(t)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "UserPromptSubmit"}, nil)
	start := time.Now()
	if r := restartWait(t, e, "dev", job1, 4); !r.Done || time.Since(start) > time.Second {
		t.Fatalf("answer %+v after %v", r, time.Since(start))
	}
}

// The resume time a restart takes over is bounded again by the daemon: one already due waits
// minResumeWait (a restart never resumes at once), one past beyond maxResumeHorizon or with
// auto-resume turned off meanwhile is given up, and the agent waits for the user.
func TestARestartedResumeIsBounded(t *testing.T) {
	e := start(t)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "UserPromptSubmit"}, nil)
	e.d.st.SetLimit("lea-a/dev", 900, 1)
	if r := restartWait(t, e, "dev", job1, 1); r != (waitAnswer{}) {
		t.Fatalf("a due resume after a restart: %+v", r)
	}
	if a := agentRow(t, e, "lea-a/dev"); a.LimitUntil != 1000+minResumeWait || a.LimitTries != 1 {
		t.Fatalf("agent=%+v", a)
	}

	e.clock.Store(2 * maxResumeHorizon)
	e.d.st.SetLimit("lea-a/dev", e.clock.Load()-maxResumeHorizon-1, 1)
	if r := restartWait(t, e, "dev", job2, 1); !r.Done {
		t.Fatalf("a stale resume was kept: %+v", r)
	}
	if a := agentRow(t, e, "lea-a/dev"); a.LimitUntil != -1 {
		t.Fatalf("agent=%+v", a)
	}

	// A time beyond the horizon ahead (a clock set back between containers) is not believed.
	e.d.st.SetLimit("lea-a/dev", e.clock.Load()+maxResumeHorizon+1, 1)
	if r := restartWait(t, e, "dev", job2, 1); !r.Done {
		t.Fatalf("a resume beyond the horizon was kept: %+v", r)
	}
	if a := agentRow(t, e, "lea-a/dev"); a.LimitUntil != -1 {
		t.Fatalf("agent=%+v", a)
	}

	e.d.SetResume(false, 0)
	e.d.st.SetLimit("lea-a/dev", e.clock.Load()+5000, 1)
	if r := restartWait(t, e, "dev", job1, 1); !r.Done {
		t.Fatalf("auto-resume off, still resumed: %+v", r)
	}
	if a := agentRow(t, e, "lea-a/dev"); a.LimitUntil != -1 {
		t.Fatalf("agent=%+v", a)
	}
}

// The SessionEnd of an agent ending with its container (lea stop marked it stopped first) neither
// ends it nor clears its pending resume: the next container resumes both.
func TestTheSessionEndOfAStoppingContainerKeepsTheResume(t *testing.T) {
	e := start(t)
	limitHit(t, e, "dev", "rate_limit")
	e.d.st.SetLimit("lea-a/dev", 1660, 1)
	e.d.st.SetState("lea-a/dev", "stopped", "", 1000)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "SessionEnd", "payload": map[string]any{"hook_event_name": "SessionEnd"}}, nil)
	if a := agentRow(t, e, "lea-a/dev"); a.State != "stopped" || a.LimitUntil != 1660 || a.LimitTries != 1 {
		t.Fatalf("agent=%+v", a)
	}
	// A live agent's SessionEnd still ends it.
	e.d.st.SetState("lea-a/dev", "idle", "", 1000)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "SessionEnd", "payload": map[string]any{"hook_event_name": "SessionEnd"}}, nil)
	if a := agentRow(t, e, "lea-a/dev"); a.State != "done" || a.LimitUntil != 0 {
		t.Fatalf("agent=%+v", a)
	}
}

// lea stop can mark an agent stopped while its turn fails on the usage limit: the StopFailure (and
// the SessionEnd after it) of the ending container then land on a stopped agent. They record the
// limit but leave the agent stopped, so the next container resumes it and its pending resume
// (before, StopFailure set it idle again, its SessionEnd then ended it, and the next container
// forgot it). The hook's state is written only while the agent is not stopped (SetHookState), so
// a stop landing between the limit and that write is covered the same way.
func TestALateUsageLimitOfAStoppingContainerKeepsTheResume(t *testing.T) {
	noJitter(t)
	e := start(t)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "UserPromptSubmit", "session_id": "s1"}, nil)
	reportLimits(t, e, "dev", map[string]any{"window": "5h", "pct": 100, "resets_at": 1600})
	e.d.st.SetState("lea-a/dev", "stopped", "", 1000)
	limitHit(t, e, "dev", "rate_limit")
	if a := agentRow(t, e, "lea-a/dev"); a.State != "stopped" || a.LimitUntil != 1600+DefaultResumeDelay {
		t.Fatalf("after the StopFailure: %+v", a)
	}
	for _, ev := range []string{"Notification", "SessionEnd"} {
		call(t, e.bus, "dev", "hook_event", map[string]any{"event": ev, "session_id": "s1",
			"payload": map[string]any{"hook_event_name": ev, "notification_type": "idle_prompt"}}, nil)
	}
	if a := agentRow(t, e, "lea-a/dev"); a.State != "stopped" || a.LimitUntil != 1600+DefaultResumeDelay || a.LimitTries != 0 {
		t.Fatalf("the stop lost the resume: %+v", a)
	}
	// The next container takes the agent and its resume over.
	if err := e.d.st.SetState("lea-a/dev", "unknown", "", 1000); err != nil {
		t.Fatal(err)
	}
	if err := call(t, e.bus, "dev", "hook_event", map[string]any{"event": "SessionStart", "session_id": "s1",
		"payload": map[string]any{"hook_event_name": "SessionStart", "source": "resume"}}, nil); err != nil {
		t.Fatal(err)
	}
	if a := agentRow(t, e, "lea-a/dev"); a.State != "idle" || a.LimitUntil != 1600+DefaultResumeDelay {
		t.Fatalf("after the restart: %+v", a)
	}
}

// A resume falling due while the container stops (lea stop marked the agent stopped, its old job
// still runs) is not claimed: the old job would wake a model about to die. The next container's
// LimitResume job claims it.
func TestAStoppedAgentsResumeIsLeftToTheNextContainer(t *testing.T) {
	e := start(t)
	limitHit(t, e, "dev", "rate_limit")
	e.d.st.SetLimit("lea-a/dev", 1500, 1)
	e.d.st.SetState("lea-a/dev", "stopped", "", 1000)
	e.clock.Store(2000)
	if r := limitWait(t, e, "dev", job1, false, 1); r != (waitAnswer{}) {
		t.Fatalf("a stopped agent was resumed: %+v", r)
	}
	if a := agentRow(t, e, "lea-a/dev"); a.LimitUntil != 1500 || a.LimitTries != 1 {
		t.Fatalf("agent=%+v", a)
	}
	restartContainer(t, e, "dev")
	if r := restartWait(t, e, "dev", job2, 1); r != (waitAnswer{}) {
		t.Fatalf("resumed at once after the restart: %+v", r)
	}
	e.clock.Add(minResumeWait)
	if r := limitWait(t, e, "dev", job2, false, 1); !r.Resume {
		t.Fatalf("the next container did not resume it: %+v", r)
	}
}

// A user's pause outlives the restart that clears it, for the resume: an agent paused while it
// waited on its limit is not resumed by the next container, and waits for the user.
func TestAPausedLimitedAgentIsNotResumedAfterARestart(t *testing.T) {
	e := start(t)
	if _, err := e.d.st.RegisterAgent("lea-a", "dev", "claude", "/p", 1000); err != nil {
		t.Fatal(err)
	}
	limitHit(t, e, "dev", "rate_limit")
	e.d.st.SetLimit("lea-a/dev", 1660, 1)
	if err := e.d.st.SetPaused("lea-a/dev", true); err != nil {
		t.Fatal(err)
	}
	restartContainer(t, e, "dev")
	if a := agentRow(t, e, "lea-a/dev"); a.Paused || a.LimitUntil != -1 {
		t.Fatalf("agent=%+v", a)
	}
	e.clock.Store(5000)
	if r := restartWait(t, e, "dev", job1, 1); !r.Done {
		t.Fatalf("a paused agent was resumed after the restart: %+v", r)
	}
}
