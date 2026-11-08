package agents

import (
	"strings"
	"testing"
)

// A spawn's label (#356) is checked, set before the agent starts (its first prompt says it) and
// made unique among the container's live agents.
func TestNewSetsTheSpawnLabel(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	r, err := e.m.New(bg, NewRequest{Container: C, Brief: "Fix lea#350", Label: "  lea#350 spawn reuse "})
	if err != nil {
		t.Fatal(err)
	}
	if r.Label != "lea#350 spawn reuse" {
		t.Fatalf("row label %q", r.Label)
	}
	a, err := e.st.AgentByKey(r.Key)
	if err != nil || a.Label != "lea#350 spawn reuse" {
		t.Fatalf("stored label %q err=%v", a.Label, err)
	}
	if !strings.Contains(a.Prompt, `shown to the user as "lea#350 spawn reuse"`) {
		t.Fatalf("first prompt %q", a.Prompt)
	}
	r2, err := e.m.New(bg, NewRequest{Container: C, Brief: "Fix lea#350 again", Label: "LEA#350 spawn reuse"})
	if err != nil || r2.Label != "LEA#350 spawn reuse 2" {
		t.Fatalf("clash label %q err=%v", r2.Label, err)
	}
}

func TestNewRefusesAnInvalidLabel(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	for _, l := range []string{strings.Repeat("a", 25), "bad\x1b]0;x\x07"} {
		_, err := e.m.New(bg, NewRequest{Container: C, Brief: "b", Label: l})
		if err == nil || !strings.Contains(err.Error(), "label") {
			t.Fatalf("label %q: err=%v", l, err)
		}
	}
	if as, _ := e.st.SpawnedAgents(C); len(as) != 0 {
		t.Fatalf("a refused spawn registered %d agents", len(as))
	}
}

// The user's rename to a label a live agent holds is refused with the reason; a done agent's
// label is free.
func TestSetLabelRefusesALiveAgentsLabel(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	a, b := e.spawn(t, ""), e.spawn(t, "")
	if err := e.m.SetLabel(bg, a.Key, "Control Tower"); err != nil {
		t.Fatal(err)
	}
	err := e.m.SetLabel(bg, b.Key, "control tower")
	if err == nil || !strings.Contains(err.Error(), a.Name) {
		t.Fatalf("rename to a live agent's label: err=%v", err)
	}
	if err := e.m.SetLabel(bg, a.Key, "Control Tower"); err != nil {
		t.Fatalf("its own label again: %v", err)
	}
	if err := e.st.SetState(a.Key, "done", "", 2); err != nil {
		t.Fatal(err)
	}
	if err := e.m.SetLabel(bg, b.Key, "Control Tower"); err != nil {
		t.Fatalf("a done agent's label: %v", err)
	}
}

// An automatic label never replaces one already set and is suffixed on a clash.
func TestAutoLabel(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	a, b := e.spawn(t, ""), e.spawn(t, "")
	if got, err := e.m.AutoLabel(bg, a.Key, "Auth Work"); err != nil || got != "Auth Work" {
		t.Fatalf("first %q err=%v", got, err)
	}
	if got, err := e.m.AutoLabel(bg, b.Key, "Auth Work"); err != nil || got != "Auth Work 2" {
		t.Fatalf("second %q err=%v", got, err)
	}
	if got, err := e.m.AutoLabel(bg, a.Key, "else"); err != nil || got != "Auth Work" {
		t.Fatalf("kept %q err=%v", got, err)
	}
	if _, err := e.m.AutoLabel(bg, a.Key, "bad\x07"); err == nil {
		t.Fatal("an invalid automatic label was accepted")
	}
}
