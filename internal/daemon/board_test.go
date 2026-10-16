package daemon

import (
	"strings"
	"testing"
)

func TestTaskUpdateFeedsTheBoardAndAgentsList(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	boss := spawned(t, e, "boss", "lead", "")
	spawned(t, e, "kid", "dev", boss)
	upd := map[string]any{"status_text": "writing tests", "next": "open the review", "blocked_on": ""}
	for i := 0; i < 2; i++ {
		if err := call(t, e.bus, "kid", "task_update", upd, nil); err != nil {
			t.Fatal(err)
		}
	}
	fa.mu.Lock()
	n := fa.changed
	fa.mu.Unlock()
	if n != 1 {
		t.Fatalf("changed=%d, want 1 (the same report twice)", n)
	}
	var b struct {
		Items []struct {
			Agent, Role, Parent, State, Status, Next string
			Ended                                    int64
		}
	}
	if err := call(t, e.api, "", "board_list", map[string]any{"container": "lea-a"}, &b); err != nil {
		t.Fatal(err)
	}
	if len(b.Items) != 1 || b.Items[0].Agent != "kid" || b.Items[0].Status != "writing tests" || b.Items[0].Next != "open the review" ||
		b.Items[0].Parent != "boss" || b.Items[0].Role != "dev" || b.Items[0].State != "unknown" {
		t.Fatalf("board=%+v", b)
	}
	if err := call(t, e.bus, "kid", "board_list", nil, nil); err == nil || !strings.Contains(err.Error(), "host only") {
		t.Fatalf("board_list from an agent: err=%v", err)
	}
	if err := call(t, e.api, "", "task_update", upd, nil); err == nil || !strings.Contains(err.Error(), "agents only") {
		t.Fatalf("task_update from the host: err=%v", err)
	}
	if err := call(t, e.api, "", "board_list", map[string]any{"container": "Bad Name"}, nil); err == nil {
		t.Fatal("bad container name accepted")
	}
	var as []struct{ Name, Task string }
	if err := call(t, e.bus, "boss", "agents_list", nil, &as); err != nil {
		t.Fatal(err)
	}
	for _, a := range as {
		if a.Name == "kid" && a.Task != "untrusted: writing tests" {
			t.Fatalf("agents_list kid task=%q", a.Task)
		}
	}
}

func TestTaskUpdateRefusesBadText(t *testing.T) {
	e := start(t)
	spawned(t, e, "kid", "dev", "")
	for i, p := range []map[string]any{
		{"status_text": "two\nlines"},
		{"status_text": "esc\x1b[2J"},
		{"status_text": "c1 \u0085"},
		{"status_text": "bidi \u202e"},
		{"status_text": strings.Repeat("é", 201)},
		{"next": strings.Repeat("n", 501)},
		{"blocked_on": "tab\there"},
	} {
		if err := call(t, e.bus, "kid", "task_update", p, nil); err == nil {
			t.Fatalf("params %d accepted", i)
		}
	}
	if ts, _ := e.d.st.Tasks("lea-a", 0); len(ts) != 0 {
		t.Fatalf("refused reports left tasks: %+v", ts)
	}
}

func TestTaskUpdateIsRateLimited(t *testing.T) {
	e := start(t)
	spawned(t, e, "kid", "dev", "")
	for i := 0; i < 30; i++ {
		if err := call(t, e.bus, "kid", "task_update", map[string]any{"status_text": "s"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := call(t, e.bus, "kid", "task_update", map[string]any{"status_text": "s"}, nil); err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("31st update: err=%v", err)
	}
}

func TestAgentsListHidesTaskTextFromOtherContainers(t *testing.T) {
	e := start(t)
	spawned(t, e, "kid", "dev", "")
	if err := call(t, e.bus, "kid", "task_update", map[string]any{"status_text": "secret plan"}, nil); err != nil {
		t.Fatal(err)
	}
	var as []struct{ Name, Task string }
	if err := call(t, e.bus, "kid", "agents_list", nil, &as); err != nil {
		t.Fatal(err)
	}
	if len(as) == 0 || as[0].Task != "untrusted: secret plan" {
		t.Fatalf("same container: %+v", as)
	}
	if _, err := e.d.st.RegisterAgent("lea-b", "x", "claude", "/p", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.st.UpdateTask("lea-b/x", "other container text", "", "", 2); err != nil {
		t.Fatal(err)
	}
	if err := call(t, e.bus, "kid", "agents_list", nil, &as); err != nil {
		t.Fatal(err)
	}
	for _, a := range as {
		if a.Name == "x" && a.Task != "" {
			t.Fatalf("task text of another container leaked: %q", a.Task)
		}
	}
}
