package agents

import (
	"slices"
	"strings"
	"testing"
)

// childEnv is the LEA_CHILD_* variables of an agent's pane command.
func childEnv(s *fakeSession, name string) []string {
	var out []string
	if p := s.byAgent(name); p != nil {
		for _, a := range p.cmd {
			if strings.HasPrefix(a, "LEA_CHILD_") {
				out = append(out, a)
			}
		}
	}
	return out
}

// A lead's child gets the deny-lists (#423), read at its spawn: an edit applies to the next
// child, the running one keeps its command; the lead and the user's sessions never get them.
func TestChildDenyListsGoToALeadsChildrenOnly(t *testing.T) {
	e := newEnv(t)
	plugins, mcp := "foo", "claude.ai Gmail"
	e.m.cfg.ChildDeny = func() (string, string) { return plugins, mcp }
	s := e.ensure(t, "/p")
	e.spawn(t, "mine")
	lead, err := e.m.New(bg, NewRequest{Container: C, Name: "boss", Role: "lead"})
	if err != nil {
		t.Fatal(err)
	}
	kid := func(name string) {
		t.Helper()
		if _, err := e.m.New(bg, NewRequest{Container: C, Name: name, Role: "dev", Parent: lead.Key, Hidden: true}); err != nil {
			t.Fatal(err)
		}
	}
	kid("dev-1")
	if got := childEnv(s, "dev-1"); !slices.Equal(got, []string{"LEA_CHILD_PLUGINS=foo", "LEA_CHILD_MCP=claude.ai Gmail"}) {
		t.Fatalf("child env %q", got)
	}
	for _, n := range []string{"mine", "boss"} {
		if got := childEnv(s, n); got != nil {
			t.Fatalf("%s got %q", n, got)
		}
	}
	plugins, mcp = "bar", ""
	kid("dev-2")
	if got := childEnv(s, "dev-2"); !slices.Equal(got, []string{"LEA_CHILD_PLUGINS=bar"}) {
		t.Fatalf("next child env %q", got)
	}
	if got := childEnv(s, "dev-1"); !slices.Equal(got, []string{"LEA_CHILD_PLUGINS=foo", "LEA_CHILD_MCP=claude.ai Gmail"}) {
		t.Fatalf("the running child's command changed: %q", got)
	}
	plugins = ""
	kid("dev-3")
	if got := childEnv(s, "dev-3"); got != nil {
		t.Fatalf("empty lists gave %q", got)
	}
}
