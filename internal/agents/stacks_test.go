package agents

import (
	"reflect"
	"testing"
	"time"
)

// waitTorn waits until the async teardowns reach want.
func waitTorn(t *testing.T, c *fakeContainers, want []string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !reflect.DeepEqual(c.tornDown(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("torn down %q, want %q", c.tornDown(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// An agent stopped from the deck (or by its lead) leaves no stack in the nested Podman (#97).
func TestStopTearsDownTheAgentsStack(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	r := e.spawn(t, "dev-1")
	e.ctr.setLive(C, "dev-1")
	if err := e.m.Stop(bg, r.Key); err != nil {
		t.Fatal(err)
	}
	if got := e.ctr.tornDown(); !reflect.DeepEqual(got, []string{C + "/dev-1"}) {
		t.Fatalf("torn down %q", got)
	}
}

// An agent that ends by itself (its CLI exits) has its stack removed once it is seen done.
func TestReconcileTearsDownTheStackOfAnEndedAgent(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	e.spawn(t, "dev-1")
	e.spawn(t, "dev-2")
	e.ctr.setLive(C, "dev-2")
	s.locked(func() { s.byAgent("dev-1").dead = true })
	e.m.Ensure(bg, C, "")
	waitTorn(t, e.ctr, []string{C + "/dev-1"})
	// Seen done again: torn down once only.
	e.m.Ensure(bg, C, "")
	time.Sleep(20 * time.Millisecond)
	waitTorn(t, e.ctr, []string{C + "/dev-1"})
}

// lea stop keeps the stacks of the agents it stops: the next container resumes them (#112), and
// its sweep removes only those of agents that are gone.
func TestMarkStoppedKeepsTheStacks(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	e.spawn(t, "dev-1")
	if err := e.m.MarkStopped(bg, C, false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if got := e.ctr.tornDown(); len(got) != 0 {
		t.Fatalf("torn down %q", got)
	}
}

// A new container sweeps the stacks of every agent but those that run or come back, and reports
// what it found.
func TestSweepStacksKeepsTheAgentsThatRunOrComeBack(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	e.spawn(t, "lead-1")
	e.spawn(t, "dev-1")
	e.spawn(t, "dev-2")
	e.st.SetState(C+"/dev-1", stateStopped, "", 1001)
	e.st.SetState(C+"/dev-2", "done", "", 1001)
	e.ctr.setLive(C, "lead-1", "other")
	e.ctr.sweepOut = "removed 2 containers and 1 pod of ended agents (dev-2)"
	got, err := e.m.SweepStacks(bg, C)
	if err != nil || got != e.ctr.sweepOut {
		t.Fatalf("SweepStacks = %q, %v", got, err)
	}
	if !reflect.DeepEqual(e.ctr.sweeps, []string{C + ":dev-1,lead-1,other"}) {
		t.Fatalf("sweeps %q", e.ctr.sweeps)
	}
	if _, err := e.m.SweepStacks(bg, "bad name"); err == nil {
		t.Fatal("an invalid container name is swept")
	}
}

// A new agent that takes the name of an ended one starts only once the ended one's teardown is
// over: the teardown would otherwise stop the new agent's Podman service.
func TestNewWaitsForTheTeardownOfTheEndedAgentOfThatName(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	e.spawn(t, "dev-1")
	hold := make(chan struct{})
	e.ctr.mu.Lock()
	e.ctr.tearHold = hold
	e.ctr.mu.Unlock()
	s.locked(func() { s.byAgent("dev-1").dead = true })
	e.m.Ensure(bg, C, "")
	started := make(chan struct{})
	go func() { e.spawn(t, "dev-1"); close(started) }()
	select {
	case <-started:
		t.Fatal("dev-1 started again while its stack was being torn down")
	case <-time.After(50 * time.Millisecond):
	}
	close(hold)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("dev-1 never started")
	}
	waitTorn(t, e.ctr, []string{C + "/dev-1"})
}
