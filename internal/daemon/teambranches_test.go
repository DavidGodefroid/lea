package daemon

import (
	"reflect"
	"testing"

	"github.com/DavidGodefroid/lea/internal/grants"
	"github.com/DavidGodefroid/lea/internal/store"
)

// TestTeamBranches: a lead may push only the branches of its own direct children whose work is
// over, and that no other agent of the lea still works on (#406).
func TestTeamBranches(t *testing.T) {
	ag := func(name, parent, branch, state string) store.Agent {
		return store.Agent{Key: "lea-a/" + name, Container: "lea-a", Name: name, Parent: parent, Repo: "lea", Branch: branch, State: state}
	}
	const lead = "lea-a/claude-1"
	as := []store.Agent{
		{Key: lead, Container: "lea-a", Name: "claude-1", State: "working"},
		ag("dev-1", lead, "feat/a", "done"),
		ag("dev-2", lead, "feat/b", "idle"),
		ag("dev-3", lead, "feat/c", "stopped"),
		ag("dev-4", lead, "feat/d", "working"),          // still working: its own to push
		ag("dev-5", "lea-a/claude-2", "feat/e", "done"), // another lead's child
		ag("dev-6", lead, "main", "done"),               // protected
		ag("dev-7", lead, "feat/f", "done"),
		ag("dev-8", "lea-a/claude-2", "feat/f", "working"), // a live agent owns feat/f now
		ag("dev-9", lead, "", "done"),                      // no branch
		{Key: "lea-b/dev-1", Container: "lea-b", Name: "dev-1", Parent: lead, Repo: "lea", Branch: "feat/g", State: "done"}, // another lea
	}
	got := teamBranches(as, "lea-a", lead)
	want := []grants.TeamBranch{
		{Key: "lea-a/dev-1", Name: "dev-1", Repo: "lea", Branch: "feat/a"},
		{Key: "lea-a/dev-2", Name: "dev-2", Repo: "lea", Branch: "feat/b"},
		{Key: "lea-a/dev-3", Name: "dev-3", Repo: "lea", Branch: "feat/c"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("team branches %+v, want %+v", got, want)
	}
	if got := teamBranches(as, "lea-a", "lea-a/dev-1"); got != nil {
		t.Fatalf("a child has no team: %+v", got)
	}
}
