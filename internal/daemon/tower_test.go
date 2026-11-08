package daemon

import (
	"testing"
)

// agent_new's tower option (#339) reaches the manager as a lead's request; the role it implies
// is never the default dev.
func TestAgentNewPassesTheTowerOption(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	if err := call(t, e.api, "", "agent_new", map[string]any{"container": "lea-a", "workdir": "/p", "tower": true, "focus": true}, nil); err != nil {
		t.Fatal(err)
	}
	if !fa.req.Tower || fa.req.Role != "lead" || !fa.req.Focus || fa.req.Profile != "" {
		t.Fatalf("request=%+v", fa.req)
	}
	if err := call(t, e.api, "", "agent_new", map[string]any{"container": "lea-a", "workdir": "/p"}, nil); err != nil {
		t.Fatal(err)
	}
	if fa.req.Tower || fa.req.Role != "dev" {
		t.Fatalf("request=%+v", fa.req)
	}
}

// An agent never gets a tower: agent_new is host only, and agents_spawn drops the option.
func TestAgentsCannotStartATower(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	spawned(t, e, "boss", "lead", "")
	if err := call(t, e.bus, "boss", "agent_new", map[string]any{"container": "lea-a", "tower": true}, nil); err == nil {
		t.Fatal("agent_new from a container was accepted")
	}
	if err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"brief": "x", "tower": true, "role": "lead"}, nil); err == nil && fa.req.Tower {
		t.Fatalf("request=%+v", fa.req)
	}
	if err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"brief": "x", "tower": true}, nil); err != nil {
		t.Fatal(err)
	}
	if fa.req.Tower || fa.req.Role != "dev" {
		t.Fatalf("request=%+v", fa.req)
	}
}
