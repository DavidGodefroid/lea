package agents

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/DavidGodefroid/lea/internal/store"
)

// A soft stop ends a suspended agent's process and pane, alone, and keeps its row and session:
// no reconcile takes it for ended, and nothing it started is removed.
func TestSoftStopKeepsTheSessionOfASuspendedAgent(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	e.spawn(t, "kid", "--model", "opus")
	e.spawn(t, "other")
	e.ctr.setLive(C, "kid", "other")
	if err := e.m.SoftStop(bg, C+"/kid"); !errors.Is(err, ErrNotSuspended) {
		t.Fatalf("soft stop of a running agent: %v", err)
	}
	e.st.SetState(C+"/kid", store.StateSuspendedWarm, sid1, 1001)
	if err := e.m.SoftStop(bg, C+"/kid"); err != nil {
		t.Fatal(err)
	}
	e.ctr.mu.Lock()
	stopped, torn := slices.Clone(e.ctr.stopped), slices.Clone(e.ctr.torn)
	e.ctr.mu.Unlock()
	if !reflect.DeepEqual(stopped, []string{C + "/kid"}) || len(torn) != 0 {
		t.Fatalf("stopped %q, torn down %q", stopped, torn)
	}
	if s.byAgent("kid") != nil || s.byAgent("other") == nil {
		t.Fatal("the soft stop did not remove the agent's pane alone")
	}
	a, err := e.st.AgentByKey(C + "/kid")
	if err != nil || a.State != store.StateSuspendedCold || a.SessionID != sid1 {
		t.Fatalf("after the soft stop: %+v err=%v", a, err)
	}
	e.ctr.setLive(C, "other")
	if err := e.m.Ensure(bg, C, ""); err != nil {
		t.Fatal(err)
	}
	if got := e.state(t, "kid"); got != store.StateSuspendedCold {
		t.Fatalf("kid is %s after a reconcile, want %s", got, store.StateSuspendedCold)
	}
	if err := e.m.SoftStop(bg, C+"/kid"); !errors.Is(err, ErrNotSuspended) {
		t.Fatalf("second soft stop: %v", err)
	}
}

// A failed stop leaves the agent warm, for the next try.
func TestAFailedSoftStopLeavesTheAgentWarm(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	e.spawn(t, "kid")
	e.ctr.setLive(C, "kid")
	e.st.SetState(C+"/kid", store.StateSuspendedWarm, sid1, 1001)
	e.ctr.fail(nil, errors.New("docker is gone"))
	if err := e.m.SoftStop(bg, C+"/kid"); err == nil {
		t.Fatal("no error")
	}
	if got := e.state(t, "kid"); got != store.StateSuspendedWarm {
		t.Fatalf("kid is %s, want %s", got, store.StateSuspendedWarm)
	}
}

// ResumeSuspended resumes the agent's session with what woke it as its next turn.
func TestResumeSuspendedResumesTheSessionWithThePrompt(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	e.spawn(t, "kid", "--model", "opus")
	e.st.SetState(C+"/kid", store.StateSuspendedWarm, sid1, 1001)
	if err := e.m.ResumeSuspended(bg, C+"/kid", "wake up"); !errors.Is(err, ErrNotSuspended) {
		t.Fatalf("resume of a warm agent: %v", err)
	}
	e.st.SetState(C+"/kid", store.StateSuspendedCold, "", 1002)
	if err := e.m.ResumeSuspended(bg, C+"/kid", "lea: 1 unread message(s) from lea-0123456789/boss. Read them with the msg_inbox tool."); err != nil {
		t.Fatal(err)
	}
	want := []string{"--resume", sid1, "--model", "opus", "lea: 1 unread message(s) from lea-0123456789/boss. Read them with the msg_inbox tool."}
	if got := agentArgs(t, s, "kid"); !reflect.DeepEqual(got, want) {
		t.Fatalf("kid resumed with %q", got)
	}
	if got := e.state(t, "kid"); got != "unknown" {
		t.Fatalf("kid is %s after the resume, want unknown", got)
	}
}

// Without a session there is nothing to resume: the agent ends.
func TestResumeSuspendedWithoutASessionEndsTheAgent(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	e.spawn(t, "kid")
	e.st.SetState(C+"/kid", store.StateSuspendedCold, "", 1001)
	if err := e.m.ResumeSuspended(bg, C+"/kid", "wake up"); err == nil {
		t.Fatal("no error")
	}
	if got := e.state(t, "kid"); got != "done" {
		t.Fatalf("kid is %s, want done", got)
	}
}

// lea stop keeps a suspended agent suspended (cold), and the next container leaves it for its
// decision or a message to resume.
func TestStopAndRestartLeaveSuspendedAgentsToTheirDecision(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	e.spawn(t, "warm")
	e.spawn(t, "cold")
	e.spawn(t, "lost")
	e.st.SetState(C+"/warm", store.StateSuspendedWarm, sid1, 1001)
	e.st.SetState(C+"/cold", store.StateSuspendedCold, sid2, 1001)
	if err := e.m.MarkStopped(bg, C, false); err != nil {
		t.Fatal(err)
	}
	if got := e.state(t, "warm"); got != store.StateSuspendedCold {
		t.Fatalf("warm is %s after lea stop, want %s", got, store.StateSuspendedCold)
	}
	// A container that ended without lea stop: its warm agent lost its process.
	e.st.SetState(C+"/lost", store.StateSuspendedWarm, sid1, 1001)
	r := e.restarted(t)
	if err := r.m.ResumeStopped(bg, C); err != nil {
		t.Fatal(err)
	}
	s := r.term.get(C) // nil: nothing to start, no session
	for _, n := range []string{"warm", "cold", "lost"} {
		if s != nil && s.byAgent(n) != nil {
			t.Fatalf("%s was started again by the restart", n)
		}
		if got := r.state(t, n); got != store.StateSuspendedCold {
			t.Fatalf("%s is %s after the restart, want %s", n, got, store.StateSuspendedCold)
		}
	}
}
