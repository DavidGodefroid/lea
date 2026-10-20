package daemon

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func subHook(t *testing.T, e env, agent, event string, payload map[string]any) {
	t.Helper()
	if err := call(t, e.bus, agent, "hook_event", map[string]any{"event": event, "session_id": "s", "payload": payload}, nil); err != nil {
		t.Fatalf("%s: %v", event, err)
	}
}

// listed is the caller's own row of agents_list: its state and subagent count.
func listed(t *testing.T, e env, agent string) (string, int) {
	t.Helper()
	var as []agentView
	if err := call(t, e.bus, agent, "agents_list", nil, &as); err != nil {
		t.Fatal(err)
	}
	for _, a := range as {
		if a.You {
			return a.State, a.Subagents
		}
	}
	t.Fatalf("no own row in %+v", as)
	return "", 0
}

func sub(id string) map[string]any { return map[string]any{"agent_id": id} }

func TestSubagentsKeepAnIdleAgentBusy(t *testing.T) {
	e := start(t)
	subHook(t, e, "dev", "UserPromptSubmit", nil)
	subHook(t, e, "dev", "Stop", nil)
	steps := []struct {
		event, id string
		want      int
	}{
		{"SubagentStart", "a1", 1},
		{"SubagentStart", "b2", 2},
		{"SubagentStart", "a1", 2}, // the same subagent counts once
		{"SubagentStop", "zz", 2},  // an unknown one changes nothing
		{"PreToolUse", "", 2},      // a subagent's tool after Stop
		{"SubagentStop", "a1", 1},
		{"SubagentStop", "b2", 0},
	}
	for _, s := range steps {
		var p map[string]any
		if s.id != "" {
			p = sub(s.id)
		}
		subHook(t, e, "dev", s.event, p)
		// Display only: the agent's own state is not touched.
		if st, n := listed(t, e, "dev"); st != "idle" || n != s.want {
			t.Fatalf("after %s %s: state=%s subagents=%d, want idle %d", s.event, s.id, st, n, s.want)
		}
	}
}

func TestSubagentIDsAreValidatedAndCapped(t *testing.T) {
	e := start(t)
	for _, p := range []map[string]any{nil, {"agent_id": ""}, {"agent_id": "a b"}, {"agent_id": "../x"},
		{"agent_id": 7}, {"agent_id": "\x1b[2J"}, {"agent_id": fmt.Sprintf("%065d", 0)}} {
		subHook(t, e, "dev", "SubagentStart", p)
	}
	if _, n := listed(t, e, "dev"); n != 0 {
		t.Fatalf("invalid ids counted: %d", n)
	}
	for i := 0; i < maxSubagents+5; i++ {
		subHook(t, e, "dev", "SubagentStart", sub(fmt.Sprintf("s%d", i)))
	}
	if _, n := listed(t, e, "dev"); n != maxSubagents {
		t.Fatalf("subagents=%d, want the cap %d", n, maxSubagents)
	}
}

func TestSubagentsAreOwnRowOnly(t *testing.T) {
	e := start(t)
	listed(t, e, "rev")
	subHook(t, e, "dev", "SubagentStart", map[string]any{"agent_id": "a1", "agent": "rev"})
	if _, n := listed(t, e, "rev"); n != 0 {
		t.Fatalf("dev's hook counted on rev: %d", n)
	}
	if _, n := listed(t, e, "dev"); n != 1 {
		t.Fatalf("dev subagents=%d", n)
	}
}

func TestSessionEndForgetsSubagentsButSessionStartKeepsThem(t *testing.T) {
	e := start(t)
	subHook(t, e, "dev", "SubagentStart", sub("a1"))
	subHook(t, e, "dev", "SessionStart", nil) // a compaction: background subagents go on
	if _, n := listed(t, e, "dev"); n != 1 {
		t.Fatalf("after SessionStart subagents=%d", n)
	}
	subHook(t, e, "dev", "SessionEnd", nil)
	if _, n := listed(t, e, "dev"); n != 0 {
		t.Fatalf("after SessionEnd subagents=%d", n)
	}
}

func TestSubagentsExpireWithoutHooks(t *testing.T) {
	e := start(t)
	e.d.mu.Lock()
	e.d.subTTL = 300 * time.Millisecond
	e.d.mu.Unlock()
	subHook(t, e, "dev", "SubagentStart", sub("a1"))
	// Any hook of the agent keeps them.
	for i := 0; i < 3; i++ {
		time.Sleep(150 * time.Millisecond)
		subHook(t, e, "dev", "PostToolUse", nil)
	}
	if _, n := listed(t, e, "dev"); n != 1 {
		t.Fatalf("refreshed subagents expired: %d", n)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, n := listed(t, e, "dev"); n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("subagents never expired")
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.d.mu.Lock()
	defer e.d.mu.Unlock()
	if len(e.d.subs) != 0 {
		t.Fatalf("subs=%v", e.d.subs)
	}
}

func TestPruneForgottenDropsSubagents(t *testing.T) {
	e := start(t)
	subHook(t, e, "dev", "SubagentStart", sub("a1"))
	if err := e.d.st.ForgetAgent("lea-a/dev"); err != nil {
		t.Fatal(err)
	}
	e.d.pruneForgotten()
	e.d.mu.Lock()
	defer e.d.mu.Unlock()
	if len(e.d.subs) != 0 {
		t.Fatalf("subs=%v", e.d.subs)
	}
}

func TestNewClearsStaleSubagentCounts(t *testing.T) {
	e := start(t)
	subHook(t, e, "dev", "SubagentStart", sub("a1"))
	New(e.d.st, filepath.Join(t.TempDir(), "run"), func() int64 { return 1 })
	if n, err := e.d.st.Subagents(""); err != nil || len(n) != 0 {
		t.Fatalf("subagents=%v err=%v", n, err)
	}
}

func TestBoardShowsSubagents(t *testing.T) {
	e := start(t)
	spawned(t, e, "dev", "dev", "")
	if err := call(t, e.bus, "dev", "task_update", map[string]any{"status_text": "x"}, nil); err != nil {
		t.Fatal(err)
	}
	subHook(t, e, "dev", "SubagentStart", sub("a1"))
	b, err := e.d.boardList(nil)
	if err != nil || len(b.Items) != 1 || b.Items[0].Subagents != 1 {
		t.Fatalf("board=%+v err=%v", b, err)
	}
}
