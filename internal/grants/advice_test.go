package grants

import (
	"strings"
	"testing"
)

func TestAdviseOneRequest(t *testing.T) {
	a := Advise([]Want{{ID: 5, Action: Merge, Repo: "lea"}})
	if a.Command != "lea allow merge --repo lea" {
		t.Fatalf("command %q", a.Command)
	}
	if !strings.Contains(a.OneOff, "lea approve g5 ") || !strings.Contains(a.OneOff, "lea requests") {
		t.Fatalf("one-off %q", a.OneOff)
	}
	if !strings.Contains(a.Durable, a.Command) || !strings.Contains(a.Durable, "lea allow off N") {
		t.Fatalf("durable %q", a.Durable)
	}
	if !strings.Contains(a.NotCovered, "push, pr, delete-branch, issues, release-tag, release") {
		t.Fatalf("not covered %q", a.NotCovered)
	}
	if n := len(a.Lines()); n != 3 {
		t.Fatalf("%d lines", n)
	}
}

func TestAdviseGroupsRequestsIntoOneCommand(t *testing.T) {
	a := Advise([]Want{
		{ID: 5, Action: PRCreate, Repo: "lea"},
		{ID: 6, Action: ReleaseCreate, Repo: "lea", Tag: "v0.15.*"},
		{ID: 7, Action: CloseIssue, Repo: "lea"},
		{ID: 8, Action: PRCreate, Repo: "site"},
	})
	if a.Command != "lea allow pr issues release --repo lea --repo site --tag 'v0.15.*'" {
		t.Fatalf("command %q", a.Command)
	}
	if !strings.Contains(a.OneOff, "lea approve g5 g6 g7 g8 ") {
		t.Fatalf("one-off %q", a.OneOff)
	}
	if a.Tag != "v0.15.*" || strings.Join(a.Repos, ",") != "lea,site" {
		t.Fatalf("scope %+v", a)
	}
}

func TestAdviseTagGlobs(t *testing.T) {
	a := Advise([]Want{{ID: 1, Action: ReleaseCreate, Repo: "lea", Tag: "v0.15.*"}, {ID: 2, Action: ReleaseCreate, Repo: "lea", Tag: "v0.16.*"}})
	if a.Command != "lea allow release --repo lea" || a.Tag != "" || !strings.Contains(a.Durable, "any semver tag") {
		t.Fatalf("two globs: %q / %q", a.Command, a.Durable)
	}
	a = Advise([]Want{{ID: 1, Action: ReleaseTag, Repo: "lea"}})
	if a.Command != "lea allow release-tag --repo lea" || strings.Contains(a.Durable, "semver") {
		t.Fatalf("release-tag: %q / %q", a.Command, a.Durable)
	}
}

func TestAdviseDeliverSaysWhatItLeavesOut(t *testing.T) {
	var ws []Want
	for i, act := range DeliveryActions {
		ws = append(ws, Want{ID: int64(i + 1), Action: act, Repo: "lea"})
	}
	a := Advise(ws)
	if a.Command != "lea allow deliver --repo lea" {
		t.Fatalf("command %q", a.Command)
	}
	if a.NotCovered != "Not covered by deliver: issues, release-tag, release; each is its own lea allow CAPABILITY --repo R." {
		t.Fatalf("not covered %q", a.NotCovered)
	}
}

func TestAdviseUnknownAction(t *testing.T) {
	a := Advise([]Want{{ID: 3, Action: "teleport", Repo: "lea"}})
	if a.Command != "" || !strings.Contains(a.Durable, "teleport") || a.NotCovered != "" || !strings.Contains(a.OneOff, "g3") {
		t.Fatalf("unknown %+v", a)
	}
	a = Advise([]Want{{ID: 3, Action: "teleport"}, {ID: 4, Action: Merge, Repo: "lea"}})
	if a.Command != "lea allow merge --repo lea" || !strings.Contains(a.Durable, "No capability covers teleport") {
		t.Fatalf("mixed %+v", a)
	}
	if len(Advise(nil).Lines()) != 0 {
		t.Fatal("advice for nothing")
	}
}

func TestAdviseNonForgeCapability(t *testing.T) {
	a := Advise([]Want{{ID: 9, Action: StopFinished}})
	if a.Command != "lea allow stop-finished" || len(a.Repos) != 0 {
		t.Fatalf("stop-finished %q", a.Command)
	}
}
