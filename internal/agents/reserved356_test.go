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
	for _, l := range []string{"", "users", "lea#350 spawn reuse", "leap", "control", "tower control", "my control tower", "user 2", "towers"} {
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

// An agent's label is plain ASCII, so no look-alike, punctuation or spelling of a reserved name
// gets past ReservedLabel (#356 review).
func TestCheckAgentLabelRefusesLookAlikes(t *testing.T) {
	for _, l := range []string{"ControlTower", "Control-Tower", "Control_Tower", "Control.Tower", "Control Tower2",
		"Control Towers", "control tower of lea", "user.", "Lea.", "the user", "The_User", "human", "Tower",
		"system", "lea-proxy", "lea daemon", "host",
		"C0ntrol T0wer", "Contro1 Tower", "CONTROI TOWER", "1ea", "Iea", "us3r", "U5ER", "Contr0l-Towers"} {
		if _, err := CheckAgentLabel(l); err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Errorf("CheckAgentLabel(%q): err=%v", l, err)
		}
	}
	for _, l := range []string{"Control Tower!", "user:", "\u0421\u043entrol Tower", "\uff23\uff4f\uff4e\uff54\uff52\uff4f\uff4c Tower",
		"\u2102ontrol Tower", "Co\u0301ntrol Tower", "re\u0301parer bug", "caf\u00e9", "a/b", "a@b", "a'b"} {
		if _, err := CheckAgentLabel(l); err == nil {
			t.Errorf("CheckAgentLabel(%q) took a character outside [A-Za-z0-9 _.#-]", l)
		}
	}
	for _, l := range []string{"auth work", "auth work 2", "lea#350 spawn reuse", "fix_ci-v1.2", "users", "leap",
		"control", "tower control", "my control tower", "user 2", "humane", "towers"} {
		if got, err := CheckAgentLabel(l); err != nil || got != l {
			t.Errorf("CheckAgentLabel(%q) = %q, %v", l, got, err)
		}
	}
	// The user's own labels keep the full CheckLabel rules.
	for _, l := range []string{"Control Tower 2", "caf\u00e9", "the user"} {
		if got, err := CheckLabel(l); err != nil || got != l {
			t.Errorf("CheckLabel(%q) = %q, %v", l, got, err)
		}
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
