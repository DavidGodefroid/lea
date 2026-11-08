package agents

import (
	"strings"
	"testing"
)

// Only the user or lea names a control tower, the user or lea itself (#356): an agent's label
// never reads as one, whatever its case or spacing.
func TestReservedLabel(t *testing.T) {
	for _, l := range []string{"user", "USER", " lea ", "Lea", "Control Tower", "control tower", "Control  Tower 2",
		"CONTROL TOWER of lea", "control\ttower"} {
		if !ReservedLabel(l) {
			t.Errorf("%q is not reserved", l)
		}
	}
	for _, l := range []string{"", "users", "lea#350 spawn reuse", "leap", "control", "tower control", "my control tower", "user 2"} {
		if ReservedLabel(l) {
			t.Errorf("%q is reserved", l)
		}
	}
	if l, err := CheckAgentLabel("  lea#350 fix "); err != nil || l != "lea#350 fix" {
		t.Fatalf("CheckAgentLabel %q err=%v", l, err)
	}
	if _, err := CheckAgentLabel("Control Tower"); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("CheckAgentLabel reserved: err=%v", err)
	}
	if _, err := CheckAgentLabel("bad\x07"); err == nil {
		t.Fatal("CheckAgentLabel took a control character")
	}
}

// A spawn an agent asks for (its brief from another agent) never takes a reserved label; the
// user's spawn may.
func TestNewRefusesAReservedLabelFromAnAgent(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	lead := e.spawn(t, "")
	for _, l := range []string{"Control Tower", "control tower 2", "user", "LEA"} {
		_, err := e.m.New(bg, NewRequest{Container: C, Brief: "b", BriefFrom: lead.Key, Parent: lead.Key, Label: l})
		if err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Fatalf("label %q: err=%v", l, err)
		}
	}
	r, err := e.m.New(bg, NewRequest{Container: C, Brief: "b", Label: "Control Tower"})
	if err != nil || r.Label != "Control Tower" {
		t.Fatalf("the user's tower %q err=%v", r.Label, err)
	}
}

// An automatic label (the namer's words, from a brief or a prompt) is never a reserved one.
func TestAutoLabelRefusesAReservedLabel(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	a := e.spawn(t, "")
	if _, err := e.m.AutoLabel(bg, a.Key, "control tower"); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("err=%v", err)
	}
	if b, _ := e.st.AgentByKey(a.Key); b.Label != "" {
		t.Fatalf("label %q", b.Label)
	}
}
