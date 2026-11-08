package grants

import (
	"strings"
	"testing"
)

// TestCapabilities: each capability maps to its stored action (#319), every action has exactly
// one, and the deliver preset expands to the four delivery actions.
func TestCapabilities(t *testing.T) {
	for name, action := range map[string]string{"push": CommitIdentity, "pr": PRCreate, "merge": Merge, "delete-branch": DeleteOwnBranch,
		"issues": CloseIssue, "stop-finished": StopFinished, "release-tag": ReleaseTag} {
		c, ok := CapabilityNamed(name)
		if !ok || c.Action != action {
			t.Errorf("%s: %+v %v, want action %s", name, c, ok, action)
		}
		if back, ok := CapabilityOf(action); !ok || back.Name != name {
			t.Errorf("%s: CapabilityOf gives %+v", action, back)
		}
	}
	if len(Capabilities) != len(Actions) {
		t.Fatalf("%d capabilities for %d actions", len(Capabilities), len(Actions))
	}
	for _, a := range Actions {
		if _, ok := CapabilityOf(a); !ok {
			t.Errorf("action %s has no capability", a)
		}
	}
	if strings.Join(DeliveryActions, " ") != "commit-identity pr-create merge delete-own-branch" {
		t.Fatalf("deliver: %v", DeliveryActions)
	}
	caps, err := Expand([]string{"merge", "deliver", "issues", "push"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range caps {
		got = append(got, c.Name)
	}
	if strings.Join(got, " ") != "push pr merge delete-branch issues" {
		t.Fatalf("expand: %v", got)
	}
	for _, bad := range [][]string{nil, {"rm-rf"}, {"merge", "commit-identity"}} {
		if _, err := Expand(bad); err == nil {
			t.Errorf("%v: no error", bad)
		}
	}
	if s := Short([]string{"delete-branch", "merge", "pr", "push"}); s != "deliver" {
		t.Fatalf("short deliver: %q", s)
	}
	if s := Short([]string{"issues", "merge"}); s != "merge issues" {
		t.Fatalf("short: %q", s)
	}
	for _, c := range Capabilities {
		if c.Forge() != (len(c.GH) > 0) {
			t.Errorf("%s: forge %v but GitHub forms %v", c.Name, c.Forge(), c.GH)
		}
	}
}

// TestSetState: a set is active while it is neither revoked nor expired and one of its rows is
// active; its capabilities and repositories are those of its active rows.
func TestSetState(t *testing.T) {
	g := func(action, repo string, revoked int64) Grant {
		return Grant{Container: "lea-a", Action: action, Repo: repo, Pin: "github.com/o/" + repo, Expires: Standing, Revoked: revoked}
	}
	s := Set{ID: 1, Container: "lea-a", Capabilities: []string{"push", "merge"}, Expires: Standing,
		Grants: []Grant{g(CommitIdentity, "web", 0), g(Merge, "web", 5), g(CommitIdentity, "api", 0)}}
	if !s.Active(now) || !s.UntilRevoked() {
		t.Fatal("active set")
	}
	if c := s.ActiveCapabilities(now); strings.Join(c, " ") != "push" {
		t.Fatalf("capabilities: %v", c)
	}
	if r := s.Repos(now); len(r) != 2 || r[0][0] != "api" || r[1][1] != "github.com/o/web" {
		t.Fatalf("repos: %v", r)
	}
	s.Grants[0].Revoked, s.Grants[2].Revoked = 6, 6
	if s.Active(now) {
		t.Fatal("a set whose rows are all revoked")
	}
	s = Set{Expires: now - 1, Grants: []Grant{{Expires: now + 10}}}
	if s.Active(now) {
		t.Fatal("an expired set")
	}
}
