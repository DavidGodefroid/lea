package daemon

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/agents"
)

// parks are the ParkStack calls the fake manager saw.
func (f *fakeAgents) parks() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var p []string
	for _, c := range f.calls {
		if strings.HasPrefix(c, "park ") {
			p = append(p, c)
		}
	}
	return p
}

func waitParks(t *testing.T, fa *fakeAgents, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for len(fa.parks()) < n {
		if time.Now().After(deadline) {
			t.Fatalf("parks %q, want %d", fa.parks(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func boardStack(t *testing.T, e env, agent string) string {
	t.Helper()
	var b struct {
		Items []struct{ Agent, Stack string }
	}
	if err := call(t, e.api, "", "board_list", map[string]any{"container": "lea-a"}, &b); err != nil {
		t.Fatal(err)
	}
	for _, it := range b.Items {
		if it.Agent == agent {
			return it.Stack
		}
	}
	t.Fatalf("no board item for %s: %+v", agent, b)
	return ""
}

// The containers an agent started are stopped once it has been idle LEA_STACK_IDLE (#132); a
// new turn before that keeps them; the board says what was freed until the agent works again.
func TestAnIdleAgentsStackIsParked(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{parkOut: "kid: stopped 2 containers and 1 pod (freed 812 MiB)"}
	e.d.SetAgents(fa)
	spawned(t, e, "kid", "dev", "")
	if err := call(t, e.bus, "kid", "task_update", map[string]any{"status_text": "testing"}, nil); err != nil {
		t.Fatal(err)
	}
	e.d.SetStackIdle(80 * time.Millisecond)
	hookEvent(t, e, "kid", "UserPromptSubmit")
	hookEvent(t, e, "kid", "Stop")
	hookEvent(t, e, "kid", "UserPromptSubmit") // a new turn before the delay
	time.Sleep(250 * time.Millisecond)
	if p := fa.parks(); len(p) != 0 {
		t.Fatalf("a working agent's stack is parked: %q", p)
	}
	hookEvent(t, e, "kid", "Stop")
	waitParks(t, fa, 1)
	if p := fa.parks(); p[0] != "park lea-a/kid" {
		t.Fatalf("parks %q", p)
	}
	if s := boardStack(t, e, "kid"); !strings.Contains(s, "stopped 2 containers and 1 pod (freed 812 MiB)") {
		t.Fatalf("board stack %q", s)
	}
	time.Sleep(200 * time.Millisecond)
	if p := fa.parks(); len(p) != 1 {
		t.Fatalf("parked again: %q", p)
	}
	hookEvent(t, e, "kid", "UserPromptSubmit")
	if s := boardStack(t, e, "kid"); s != "" {
		t.Fatalf("board stack %q after a new turn", s)
	}
}

// LEA_STACK_IDLE=0 turns it off; an agent lea did not start (a hook registered it) is never
// parked.
func TestNoParkWhenOffOrForeignAgents(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{parkOut: "x"}
	e.d.SetAgents(fa)
	spawned(t, e, "kid", "dev", "")
	e.d.SetStackIdle(0)
	hookEvent(t, e, "kid", "Stop")
	e.d.SetStackIdle(50 * time.Millisecond)
	hookEvent(t, e, "other", "Stop")
	time.Sleep(250 * time.Millisecond)
	if p := fa.parks(); len(p) != 0 {
		t.Fatalf("parks %q", p)
	}
}

// The sweep at each lea parks the stacks of agents idle for LEA_STACK_IDLE already (a restarted
// daemon lost its timers) and reports them after what it removed.
func TestSweepParksAgentsIdleForLong(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{parkOut: "kid: stopped 1 container and 0 pods (freed 40 MiB)"}
	e.d.SetAgents(fa)
	e.d.SetStackIdle(10 * time.Minute)
	kid := spawned(t, e, "kid", "dev", "")
	busy := spawned(t, e, "busy", "dev", "")
	fresh := spawned(t, e, "fresh", "dev", "")
	*e.clock = 5000
	e.d.st.SetState(kid, "idle", "", 5000-601)
	e.d.st.SetState(busy, "working", "", 1000)
	e.d.st.SetState(fresh, "idle", "", 5000-60)
	var sw struct{ Notice string }
	if err := call(t, e.api, "", "agents_sweep_stacks", map[string]any{"container": "lea-a"}, &sw); err != nil {
		t.Fatal(err)
	}
	if p := fa.parks(); !slices.Equal(p, []string{"park lea-a/kid"}) {
		t.Fatalf("parks %q", p)
	}
	if sw.Notice != "removed 1 container and 0 pods of ended agents (dev-2)\nkid: stopped 1 container and 0 pods (freed 40 MiB)" {
		t.Fatalf("notice %q", sw.Notice)
	}
}

// idle_prompt after a Stop keeps the pending stop: the wait counts from the end of the turn.
func TestIdlePromptKeepsThePendingPark(t *testing.T) {
	e := start(t)
	e.d.SetAgents(&fakeAgents{})
	key := spawned(t, e, "kid", "dev", "")
	e.d.SetStackIdle(time.Hour)
	hookEvent(t, e, "kid", "Stop")
	e.d.mu.Lock()
	before := e.d.parkTimers[key]
	e.d.mu.Unlock()
	if before == nil {
		t.Fatal("no pending stop after Stop")
	}
	if err := call(t, e.bus, "kid", "hook_event", map[string]any{"event": "Notification", "session_id": "s1",
		"payload": map[string]any{"notification_type": "idle_prompt"}}, nil); err != nil {
		t.Fatal(err)
	}
	e.d.mu.Lock()
	after := e.d.parkTimers[key]
	e.d.mu.Unlock()
	if after != before {
		t.Fatal("idle_prompt restarted the wait")
	}
	hookEvent(t, e, "kid", "UserPromptSubmit")
	e.d.mu.Lock()
	n := len(e.d.parkTimers)
	e.d.mu.Unlock()
	if n != 0 {
		t.Fatal("a new turn left the stop pending")
	}
}

func (f *fakeAgents) has(call string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.calls, call)
}

// An agent idle at its prompt while its subagents (a workflow, a background agent) still run is
// not parked: they may use the stack. Once they end, it is.
func TestNoParkWhileSubagentsRun(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{parkOut: "kid: stopped 1 container and 0 pods (freed 40 MiB)"}
	e.d.SetAgents(fa)
	spawned(t, e, "kid", "dev", "")
	e.d.SetStackIdle(80 * time.Millisecond)
	sub := func(event string) {
		t.Helper()
		if err := call(t, e.bus, "kid", "hook_event", map[string]any{"event": event, "session_id": "s1",
			"payload": map[string]any{"agent_id": "a1"}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	sub("SubagentStart")
	hookEvent(t, e, "kid", "Stop")
	time.Sleep(300 * time.Millisecond)
	if p := fa.parks(); len(p) != 0 {
		t.Fatalf("parked while a subagent runs: %q", p)
	}
	sub("SubagentStop")
	waitParks(t, fa, 1)
}

// A stack still at work (lea-agent park: busy) is not stopped and nothing is recorded; lea looks
// again one idle delay later.
func TestABusyStackIsTriedAgain(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{parkErr: fmt.Errorf("kid: %w", agents.ErrStackBusy)}
	e.d.SetAgents(fa)
	spawned(t, e, "kid", "dev", "")
	if err := call(t, e.bus, "kid", "task_update", map[string]any{"status_text": "testing"}, nil); err != nil {
		t.Fatal(err)
	}
	e.d.SetStackIdle(60 * time.Millisecond)
	hookEvent(t, e, "kid", "Stop")
	waitParks(t, fa, 2)
	if s := boardStack(t, e, "kid"); s != "" {
		t.Fatalf("board stack %q for a busy stack", s)
	}
}

// A new turn that comes while the stack is being stopped starts it again at once: the agent is
// at work and counts on it.
func TestANewTurnDuringTheParkStartsTheStackAgain(t *testing.T) {
	e := start(t)
	hold := make(chan struct{})
	fa := &fakeAgents{parkOut: "kid: stopped 1 container and 0 pods (freed 40 MiB)", parkHold: hold}
	e.d.SetAgents(fa)
	spawned(t, e, "kid", "dev", "")
	if err := call(t, e.bus, "kid", "task_update", map[string]any{"status_text": "testing"}, nil); err != nil {
		t.Fatal(err)
	}
	e.d.SetStackIdle(50 * time.Millisecond)
	hookEvent(t, e, "kid", "Stop")
	waitParks(t, fa, 1)
	hookEvent(t, e, "kid", "UserPromptSubmit")
	close(hold)
	deadline := time.Now().Add(3 * time.Second)
	for !fa.has("unpark lea-a/kid") {
		if time.Now().After(deadline) {
			t.Fatalf("not started again: %q", fa.calls)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if s := boardStack(t, e, "kid"); s != "" {
		t.Fatalf("board stack %q after the stack started again", s)
	}
}
