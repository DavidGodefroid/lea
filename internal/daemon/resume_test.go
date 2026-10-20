package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/store"
)

const job1, job2 = "0123456789abcdef", "fedcba9876543210"

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
	*e.clock = 1660
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
	go func() { time.Sleep(300 * time.Millisecond); *e.clock = 1001 }()
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
	*e.clock = 5000
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
