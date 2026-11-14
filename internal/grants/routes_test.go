package grants

import (
	"strings"
	"testing"
)

// TestRoutes: the one-off route, the durable lea allow route and what it leaves out, in at most
// three lines (#412): a single request, grouped requests on one repository, a tag glob, the
// deliver subset, several repositories and branches, and an action no capability names.
func TestRoutes(t *testing.T) {
	for _, tc := range []struct {
		name string
		asks []Ask
		want []string
	}{
		{"single", []Ask{{ID: 5, Action: Merge, Repo: "lea"}}, []string{
			"one-off: lea approve g5 or lea deny g5 [reason]; lea requests says what it asks",
			"durable, for every agent of this lea until lea allow off N (add --for 8h to limit it): lea allow merge --repo lea",
			"it covers only merge on lea, not push, pr, delete-branch, issues, release-tag, release, nor other repositories"}},
		{"grouped with tag glob", []Ask{{ID: 5, Action: PRCreate, Repo: "lea"}, {ID: 6, Action: ReleaseCreate, Repo: "lea", Tag: "v0.15.*"},
			{ID: 7, Action: CloseIssue, Repo: "lea"}}, []string{
			"one-off: lea approve g5 g6 g7 (or lea deny gN [reason]); lea requests says what each asks",
			"durable, for every agent of this lea until lea allow off N (add --for 8h to limit it): lea allow pr issues release --repo lea --tag 'v0.15.*'",
			"it covers only pr, issues, release on lea, not push, merge, delete-branch, release-tag, nor other repositories"}},
		{"deliver subset", []Ask{{ID: 1, Action: CommitIdentity, Repo: "web"}, {ID: 2, Action: PRCreate, Repo: "web"},
			{ID: 3, Action: Merge, Repo: "web"}, {ID: 4, Action: DeleteOwnBranch, Repo: "web"}}, []string{
			"one-off: lea approve g1 g2 g3 g4 (or lea deny gN [reason]); lea requests says what each asks",
			"durable, for every agent of this lea until lea allow off N (add --for 8h to limit it): lea allow deliver --repo web",
			"it covers only deliver (push, pr, merge, delete-branch) on web, not issues, release-tag, release, nor other repositories"}},
		{"repositories and branches", []Ask{{ID: 1, Action: Merge, Repo: "a"}, {ID: 2, Action: Merge, Repo: "b", Branch: "*"},
			{ID: 3, Action: Merge, Repo: "c", Branch: "fix/*"}}, []string{
			"one-off: lea approve g1 g2 g3 (or lea deny gN [reason]); lea requests says what each asks",
			"durable, for every agent of this lea until lea allow off N (add --for 8h to limit it): lea allow merge --repo a --repo b; lea allow merge --repo c --branch 'fix/*'",
			"it covers only merge on a, b, c, not push, pr, delete-branch, issues, release-tag, release, nor other repositories"}},
		{"unknown action", []Ask{{ID: 9, Action: "rm-rf", Repo: "lea"}}, []string{
			"one-off: lea approve g9 or lea deny g9 [reason]; lea requests says what it asks",
			"no lea allow form covers g9 (rm-rf): approve it once"}},
		{"unknown among known", []Ask{{ID: 1, Action: ReleaseTag, Repo: "lea"}, {ID: 2, Action: "rm-rf", Repo: "lea"}}, []string{
			"one-off: lea approve g1 g2 (or lea deny gN [reason]); lea requests says what each asks",
			"durable, for every agent of this lea until lea allow off N (add --for 8h to limit it): lea allow release-tag --repo lea",
			"it covers only release-tag on lea, not push, pr, merge, delete-branch, issues, release, nor other repositories; no lea allow form covers g2 (rm-rf): approve it once"}},
	} {
		got := Routes(tc.asks)
		if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
		if len(got) > 3 {
			t.Errorf("%s: %d lines, at most 3", tc.name, len(got))
		}
	}
	if Routes(nil) != nil {
		t.Error("no ask: no route")
	}
}
