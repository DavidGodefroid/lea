package agents

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSetPausedFreezesAndResumesTheAgent(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	r := e.spawn(t, "claude-1")
	e.ctr.setLive(C, "claude-1")
	e.st.SetState(r.Key, "working", "", 1001)
	if err := e.m.SetPaused(bg, r.Key, true); err != nil {
		t.Fatal(err)
	}
	snap, _ := e.m.Watch(bg, C, 0, time.Second)
	if len(snap.Agents) != 1 || !snap.Agents[0].Paused || snap.Agents[0].State != "working" {
		t.Fatalf("snapshot=%+v", snap.Agents)
	}
	if err := e.m.SetPaused(bg, r.Key, false); err != nil {
		t.Fatal(err)
	}
	if a, _ := e.st.AgentByKey(r.Key); a.Paused {
		t.Fatalf("still paused: %+v", a)
	}
	if !reflect.DeepEqual(e.ctr.paused, []string{C + "/claude-1:pause", C + "/claude-1:resume"}) {
		t.Fatalf("paused=%q", e.ctr.paused)
	}
}

// The row says paused only when the container froze the process.
func TestSetPausedKeepsTheRowWhenTheContainerFails(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	r := e.spawn(t, "claude-1")
	e.ctr.pauseErr = errors.New("lea-agent pause claude-1 failed")
	if err := e.m.SetPaused(bg, r.Key, true); err == nil {
		t.Fatal("no error")
	}
	if a, _ := e.st.AgentByKey(r.Key); a.Paused {
		t.Fatalf("paused without a frozen process: %+v", a)
	}
}

func TestSetPausedRefusesShellsAndEndedAgents(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	r := e.spawn(t, "claude-1")
	if err := e.m.SetPaused(bg, ShellKey(C, "sh-1"), true); err == nil {
		t.Fatal("a shell was paused")
	}
	for _, st := range []string{"done", "stopped"} {
		e.st.SetState(r.Key, st, "", 1001)
		if err := e.m.SetPaused(bg, r.Key, true); err == nil || !strings.Contains(err.Error(), "not running") {
			t.Fatalf("%s agent: err=%v", st, err)
		}
	}
	if err := e.m.SetPaused(bg, C+"/nobody", true); err == nil {
		t.Fatal("an unknown agent was paused")
	}
	if len(e.ctr.paused) != 0 {
		t.Fatalf("paused=%q", e.ctr.paused)
	}
}

// The deck's row carries the agent's usage limit (#20): when lea resumes it.
func TestTheRowCarriesTheUsageLimit(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	r := e.spawn(t, "claude-1")
	e.st.SetState(r.Key, "idle", "", 1001)
	if err := e.st.SetLimit(r.Key, 5000, 1); err != nil {
		t.Fatal(err)
	}
	e.m.Changed()
	snap, _ := e.m.Watch(bg, C, 0, time.Second)
	if len(snap.Agents) != 1 || snap.Agents[0].LimitUntil != 5000 {
		t.Fatalf("snapshot=%+v", snap.Agents)
	}
}
