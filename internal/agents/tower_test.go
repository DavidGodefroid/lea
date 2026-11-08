package agents

import (
	"strings"
	"testing"

	"github.com/DavidGodefroid/lea/internal/providers"
)

// A Control Tower (#339) is a lead labelled Control Tower on Claude, whose first prompt's first
// line runs the control-tower skill (the tower guard turns on from that line only).
func TestNewTowerIsALabelledLeadRunningTheSkill(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	e.m.cfg.DefaultProvider = "codex"
	r, err := e.m.New(bg, NewRequest{Container: C, Tower: true, Focus: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Role != "lead" || r.Label != TowerLabel || r.Profile != "claude" {
		t.Fatalf("row=%+v", r)
	}
	a, err := e.st.AgentByKey(r.Key)
	if err != nil || a.Label != TowerLabel {
		t.Fatalf("stored label %q err=%v", a.Label, err)
	}
	last := lastArg(s, r.Name)
	if !strings.HasPrefix(last, "/lea:control-tower ") || strings.Contains(last, "\n") ||
		!strings.Contains(last, `shown to the user as "Control Tower"`) || !strings.Contains(last, "(role lead)") {
		t.Fatalf("first prompt %q", last)
	}
	// What a new container starts it again with, when its session cannot be resumed.
	if a.Prompt != last {
		t.Fatalf("launch prompt %q, want %q", a.Prompt, last)
	}
}

func TestNewTowerSuffixesTheLabelOfASecondTower(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	want := []string{"Control Tower", "Control Tower 2", "Control Tower 3"}
	for _, w := range want {
		r, err := e.m.New(bg, NewRequest{Container: C, Tower: true, Hidden: true})
		if err != nil || r.Label != w {
			t.Fatalf("row=%+v err=%v, want label %q", r, err, w)
		}
		if _, err := CheckLabel(r.Label); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNewTowerRefusesAnotherProviderOrRoleOrAParent(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	for _, r := range []NewRequest{
		{Container: C, Tower: true, Profile: "codex"},
		{Container: C, Tower: true, Role: "dev"},
		{Container: C, Tower: true, Parent: C + "/lead-1"},
	} {
		if _, err := e.m.New(bg, r); err == nil {
			t.Fatalf("%+v accepted", r)
		}
	}
}

func TestNewTowerWithoutClaudeSaysSo(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	e.m.cfg.Providers = func() (*providers.Registry, error) { return &providers.Registry{}, nil }
	_, err := e.m.New(bg, NewRequest{Container: C, Tower: true})
	if err == nil || !strings.Contains(err.Error(), "Control Tower") || !strings.Contains(err.Error(), "claude") {
		t.Fatalf("err=%v", err)
	}
}

// A plain agent is untouched: no label, no skill.
func TestNewWithoutTowerHasNoLabelNorSkill(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	r, err := e.m.New(bg, NewRequest{Container: C, Brief: "x"})
	if err != nil || r.Label != "" || r.Role != "dev" {
		t.Fatalf("row=%+v err=%v", r, err)
	}
	if last := lastArg(s, r.Name); strings.Contains(last, "control-tower") {
		t.Fatalf("first prompt %q", last)
	}
}

// A tower stopped with its container comes back as a tower (deck-open --fresh): its label and
// role stay, and without a transcript it starts again with the skill's first prompt.
func TestResumeStoppedKeepsATower(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	r, err := e.m.New(bg, NewRequest{Container: C, Tower: true, Hidden: true})
	if err != nil {
		t.Fatal(err)
	}
	e.st.SetState(r.Key, "idle", sid1, 1001)
	if err := e.m.MarkStopped(bg, C, false); err != nil {
		t.Fatal(err)
	}
	re := e.restarted(t)
	re.ctr.noTranscript = map[string]bool{sid1: true}
	if err := re.m.ResumeStopped(bg, C); err != nil {
		t.Fatal(err)
	}
	a, err := re.st.AgentByKey(r.Key)
	if err != nil || a.Label != TowerLabel || a.Role != "lead" || a.Profile != "claude" {
		t.Fatalf("resumed %+v err=%v", a, err)
	}
	if got := agentArgs(t, re.term.get(C), r.Name); len(got) != 1 || !strings.HasPrefix(got[0], TowerCommand+" ") {
		t.Fatalf("restarted with %q", got)
	}
}
