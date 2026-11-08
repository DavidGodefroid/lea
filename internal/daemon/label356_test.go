package daemon

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/agents"
	"github.com/DavidGodefroid/lea/internal/store"
)

// regAgents is a manager that registers the agents it starts, as the real one does, with the
// spawn's label and lea's first prompt.
type regAgents struct {
	*fakeAgents
	n int
}

func (r *regAgents) New(ctx context.Context, nr agents.NewRequest) (agents.Row, error) {
	r.fakeAgents.New(ctx, nr)
	r.n++
	a, err := r.st.Register(store.NewAgent{Container: nr.Container, Name: fmt.Sprintf("dev-%d", r.n), Profile: "claude", Workdir: "/p",
		Role: nr.Role, Parent: nr.Parent}, store.Caps{}, 1)
	if err != nil {
		return agents.Row{}, err
	}
	if err := r.st.SetLaunch(a.Key, nil, "You are the lea agent "+a.Name+" (role dev). Your brief is waiting: read it now with the msg_inbox tool, then work on it."); err != nil {
		return agents.Row{}, err
	}
	if nr.Label != "" {
		if a.Label, err = r.st.SetLabelUnique(a.Key, nr.Label, agents.MaxLabel, false); err != nil {
			return agents.Row{}, err
		}
	}
	return agents.Row{Key: a.Key, Name: a.Name, Label: a.Label, Role: a.Role, Profile: "claude"}, nil
}

func labelEnv(t *testing.T, reply string) (env, *regAgents, *[]string, chan struct{}) {
	t.Helper()
	e := start(t)
	ra := &regAgents{fakeAgents: &fakeAgents{st: e.d.st}}
	e.d.SetAgents(ra)
	var got []string
	e.d.SetNamer(func(ctx context.Context, prompt string) (string, error) {
		got = append(got, prompt)
		return reply, nil
	})
	done := make(chan struct{}, 4)
	e.d.afterName = func() { done <- struct{}{} }
	spawned(t, e, "boss", "lead", "")
	return e, ra, &got, done
}

func storedLabel(t *testing.T, e env, key string) string {
	t.Helper()
	a, err := e.d.st.AgentByKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return a.Label
}

// A child spawned without a label is named after its brief; lea's first prompt, which its hook
// sends as name_hint, never reaches the namer (#356).
func TestASpawnWithoutLabelIsNamedAfterItsBrief(t *testing.T) {
	e, _, got, done := labelEnv(t, "lea#350 spawn reuse")
	var r map[string]string
	if err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"brief": "Fix lea#350: a spawn reuses a live name"}, &r); err != nil {
		t.Fatal(err)
	}
	wait(t, done)
	if l := storedLabel(t, e, r["key"]); l != "lea#350 spawn reuse" {
		t.Fatalf("label %q", l)
	}
	if len(*got) != 1 || (*got)[0] != "Fix lea#350: a spawn reuses a live name" {
		t.Fatalf("namer got %q", *got)
	}
	a, _ := e.d.st.AgentByKey(r["key"])
	if err := call(t, e.bus, r["name"], "name_hint", map[string]any{"prompt": a.Prompt}, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if len(*got) != 1 {
		t.Fatalf("lea's first prompt reached the namer: %q", *got)
	}
	var list []struct{ Name, Label string }
	if err := call(t, e.bus, "boss", "agents_list", nil, &list); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range list {
		found = found || it.Name == r["name"] && it.Label == "lea#350 spawn reuse"
	}
	if !found {
		t.Fatalf("agents_list %+v", list)
	}
}

// A spawn's label is the agent's from the start; the namer is not asked.
func TestASpawnLabelIsSetAndChecked(t *testing.T) {
	e, ra, got, _ := labelEnv(t, "auto")
	var r map[string]string
	if err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"brief": "Fix lea#350", "label": "lea#350 spawn reuse"}, &r); err != nil {
		t.Fatal(err)
	}
	if r["label"] != "lea#350 spawn reuse" || ra.req.Label != "lea#350 spawn reuse" || storedLabel(t, e, r["key"]) != "lea#350 spawn reuse" {
		t.Fatalf("result %+v, request label %q", r, ra.req.Label)
	}
	time.Sleep(50 * time.Millisecond)
	if len(*got) != 0 {
		t.Fatalf("namer asked %q", *got)
	}
	ra.calls = nil
	for _, l := range []string{strings.Repeat("x", 25), "a\x1b[2Jb"} {
		err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"brief": "b", "label": l}, nil)
		if err == nil || !strings.Contains(err.Error(), "label") {
			t.Fatalf("label %q: err=%v", l, err)
		}
	}
	if newCalled(ra.fakeAgents) {
		t.Fatal("an invalid label reached the manager")
	}
}

// A requested label travels with the spawn request to the lead's spawn.
func TestARequestedLabelReachesTheSpawn(t *testing.T) {
	e, ra, _, _ := labelEnv(t, "auto")
	spawned(t, e, "kid", "dev", "lea-a/boss")
	r, err := requestFrom(t, e, "kid", map[string]any{"brief": "review lea#350", "reason": "eyes", "label": "lea#350 review"})
	if err != nil {
		t.Fatal(err)
	}
	if ms := inboxOf(t, e, "boss"); len(ms) != 1 || !strings.Contains(ms[0].Text, `label "lea#350 review"`) {
		t.Fatalf("boss inbox %+v", ms)
	}
	if err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"brief": "review lea#350", "for": fmt.Sprintf("#%d", r.Request)}, nil); err != nil {
		t.Fatal(err)
	}
	if ra.req.Label != "lea#350 review" {
		t.Fatalf("spawn label %q", ra.req.Label)
	}
	if _, err := requestFrom(t, e, "kid", map[string]any{"brief": "x", "reason": "y", "label": strings.Repeat("x", 25)}); err == nil {
		t.Fatal("an overlong requested label was accepted")
	}
}

// An agent the user started by hand is still named after its own first prompt, and a clash
// with a live agent's label gets a number.
func TestAHandStartedAgentIsNamedUniquely(t *testing.T) {
	e, _, got, done := labelEnv(t, "auth work")
	if err := e.d.st.SetLabel("lea-a/boss", "Auth Work"); err != nil {
		t.Fatal(err)
	}
	spawned(t, e, "claude-2", "lead", "")
	if err := call(t, e.bus, "claude-2", "name_hint", map[string]any{"prompt": "fix the auth"}, nil); err != nil {
		t.Fatal(err)
	}
	wait(t, done)
	if l := storedLabel(t, e, "lea-a/claude-2"); l != "auth work 2" || len(*got) != 1 {
		t.Fatalf("label %q, namer got %q", l, *got)
	}
}

// The namer's words never give a reserved label: a prompt or a brief cannot make an agent read
// as a control tower, the user or lea (#356).
func TestTheNamerNeverGivesAReservedLabel(t *testing.T) {
	for _, reply := range []string{"control tower", "control tower 2", "user", "lea", "ControlTower", "Control_Towers",
		"Control Tower!", "user:", "Lea.", "the user", "The Control Tower", "tower control", "Tower 2", "user 2", "CT", "Control 7ower", "\uff23\uff4f\uff4e\uff54\uff52\uff4f\uff4c Tower", "\u2102ontrol Tower", "Co\u0301ntrol Tower"} {
		e, _, got, done := labelEnv(t, reply)
		spawned(t, e, "claude-2", "lead", "")
		if err := call(t, e.bus, "claude-2", "name_hint", map[string]any{"prompt": "be the control tower"}, nil); err != nil {
			t.Fatal(err)
		}
		wait(t, done)
		if l := storedLabel(t, e, "lea-a/claude-2"); l != "" || len(*got) != 1 {
			t.Fatalf("reply %q: label %q, namer got %q", reply, l, *got)
		}
	}
}

// Neither a lead's spawn nor a requested one takes a reserved label.
func TestASpawnNeverTakesAReservedLabel(t *testing.T) {
	e, ra, _, _ := labelEnv(t, "auto")
	spawned(t, e, "kid", "dev", "lea-a/boss")
	for _, l := range []string{"Control Tower", "control tower 2", "User", "lea", "ControlTower", "Control-Tower", "the user", "Lea.", "The Control Tower", "Ctrl Tower", "Tower #1", "lea 2", "CT"} {
		err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"brief": "b", "label": l}, nil)
		if err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Fatalf("agents_spawn label %q: err=%v", l, err)
		}
		if _, err := requestFrom(t, e, "kid", map[string]any{"brief": "b " + l, "reason": "r", "label": l}); err == nil ||
			!strings.Contains(err.Error(), "reserved") {
			t.Fatalf("agents_request_spawn label %q: err=%v", l, err)
		}
	}
	if newCalled(ra.fakeAgents) {
		t.Fatal("a reserved label reached the manager")
	}
}
