package grants

import (
	"fmt"
	"sort"
	"strings"
)

// Capability is an action as the user names it (lea allow, #319): the grant rows still store
// Action, the hook's unit, and every covered form and hard deny stays the action's own.
type Capability struct {
	// Name is what the user types (lea allow push), Action the action stored in a grant row.
	Name, Action string
	// Does says what the capability lets agents do, as a sentence part ("push their branch").
	Does string
	// GH and GLab are the forms a grant of the capability covers on GitHub and on GitLab, as an
	// agent writes them: <branch> stands for its own branch. Empty for a call that is not a forge
	// command (stop-finished).
	GH, GLab []string
}

// Forge says whether a capability's calls go to a forge repository (gh, glab or git against its
// pin): every capability but stop-finished.
func (c Capability) Forge() bool { return NeedsGitHub(c.Action) }

// Capabilities is every capability, in display order. The CLI, the deck, the hint, the first
// prompt and the docs read it.
var Capabilities = []Capability{
	{Name: "push", Action: CommitIdentity, Does: "push their branch",
		GH:   []string{"git commit -m M", "git push [-u] origin <branch>"},
		GLab: []string{"git commit -m M", "git push [-u] origin <branch>"}},
	{Name: "pr", Action: PRCreate, Does: "open PRs",
		GH:   []string{"gh pr create --head <branch> --title T (--body B | --body-file F) [--base B] [--draft]"},
		GLab: []string{"glab mr create --source-branch <branch> --title T [--description D] [--target-branch B] [--draft] [--remove-source-branch] [--yes]"}},
	{Name: "merge", Action: Merge, Does: "merge PRs",
		GH:   []string{"gh pr merge <number|<branch>> [--squash|--merge|--rebase] [--delete-branch]"},
		GLab: []string{"glab mr merge <number|<branch>> [--squash] [--remove-source-branch] [--yes]"}},
	{Name: "delete-branch", Action: DeleteOwnBranch, Does: "delete their branch",
		GH:   []string{"git push origin --delete <branch>"},
		GLab: []string{"git push origin --delete <branch>"}},
	{Name: "issues", Action: CloseIssue, Does: "close, reopen and comment on issues",
		GH: []string{"gh issue close|reopen|comment N"}},
	{Name: "stop-finished", Action: StopFinished, Does: "stop their finished children"},
	{Name: "release-tag", Action: ReleaseTag, Does: "push a release tag on the default branch",
		GH:   []string{"git push origin vX.Y.Z"},
		GLab: []string{"git push origin vX.Y.Z"}},
	{Name: "release", Action: ReleaseCreate, Does: "publish GitHub releases of pushed tags",
		GH: []string{"gh release create <tag> --verify-tag [--title T] [--notes-file F | --generate-notes] [--prerelease] [--latest]"}},
}

// Deliver is the preset lea allow deliver records: push the agent's own branch, open its PR,
// merge it, delete the branch (#92).
const Deliver = "deliver"

// Presets name sets of capabilities.
var Presets = map[string][]string{Deliver: {"push", "pr", "merge", "delete-branch"}}

// DeliveryActions are the actions of the deliver preset.
var DeliveryActions = actionsOf(Presets[Deliver])

func actionsOf(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		c, _ := CapabilityNamed(n)
		out = append(out, c.Action)
	}
	return out
}

// CapabilityNamed is the capability the user calls name.
func CapabilityNamed(name string) (Capability, bool) {
	for _, c := range Capabilities {
		if c.Name == name {
			return c, true
		}
	}
	return Capability{}, false
}

// CapabilityOf is the capability whose stored action is action.
func CapabilityOf(action string) (Capability, bool) {
	for _, c := range Capabilities {
		if c.Action == action {
			return c, true
		}
	}
	return Capability{}, false
}

// CapabilityNames is every capability's name, in display order.
func CapabilityNames() []string {
	out := make([]string, len(Capabilities))
	for i, c := range Capabilities {
		out[i] = c.Name
	}
	return out
}

// Expand reads capability and preset names into capabilities, each once, in display order.
func Expand(names []string) ([]Capability, error) {
	want := map[string]bool{}
	for _, n := range names {
		if p, ok := Presets[n]; ok {
			for _, x := range p {
				want[x] = true
			}
			continue
		}
		if _, ok := CapabilityNamed(n); !ok {
			return nil, fmt.Errorf("unknown capability %q (capabilities: %s; preset: %s)", n, strings.Join(CapabilityNames(), ", "), Deliver)
		}
		want[n] = true
	}
	if len(want) == 0 {
		return nil, fmt.Errorf("name a capability (%s) or the %s preset", strings.Join(CapabilityNames(), ", "), Deliver)
	}
	out := []Capability{}
	for _, c := range Capabilities {
		if want[c.Name] {
			out = append(out, c)
		}
	}
	return out, nil
}

// Short is how a set of capability names reads: the preset's name when it is exactly one, the
// names in display order otherwise.
func Short(names []string) string {
	caps, err := Expand(names)
	if err != nil {
		return strings.Join(names, " ")
	}
	got := make([]string, len(caps))
	for i, c := range caps {
		got[i] = c.Name
	}
	for p, xs := range Presets {
		if strings.Join(xs, " ") == strings.Join(got, " ") {
			return p
		}
	}
	return strings.Join(got, " ")
}

// Set is the unit lea allow records and revokes (#319): the rows of one call, one per action and
// repository. Expires is Standing for a set that holds until revoked.
type Set struct {
	ID        int64
	Container string
	// Capabilities are the names the user allowed, in display order.
	Capabilities              []string
	Created, Expires, Revoked int64
	Grants                    []Grant
}

// UntilRevoked says whether s holds until lea allow off revokes it.
func (s Set) UntilRevoked() bool { return s.Expires == Standing }

// Active says whether s still applies at now: not revoked, not expired, one row still active.
func (s Set) Active(now int64) bool {
	if s.Revoked != 0 || s.Expires <= now {
		return false
	}
	for _, g := range s.Grants {
		if g.Active(now) {
			return true
		}
	}
	return false
}

// ActiveCapabilities are the names of the capabilities s still covers at now (a row revoked on
// its own drops its capability), in display order.
func (s Set) ActiveCapabilities(now int64) []string {
	have := map[string]bool{}
	for _, g := range s.Grants {
		if g.Active(now) {
			if c, ok := CapabilityOf(g.Action); ok {
				have[c.Name] = true
			}
		}
	}
	out := []string{}
	for _, c := range Capabilities {
		if have[c.Name] {
			out = append(out, c.Name)
		}
	}
	return out
}

// Repos are the repositories s's active rows cover, with their pins, sorted by repository.
func (s Set) Repos(now int64) [][2]string {
	seen := map[string]string{}
	for _, g := range s.Grants {
		if g.Active(now) && g.Repo != "" {
			seen[g.Repo] = g.Pin
		}
	}
	out := make([][2]string, 0, len(seen))
	for r, p := range seen {
		out = append(out, [2]string{r, p})
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

// Covers says whether an active grant of gs covers action for c, the forge call's pinning aside:
// what the first prompt tells an agent it may run without asking.
func Covers(action string, c Caller, gs []Grant, now int64) bool {
	_, ok := heldGrant(action, c, gs, now)
	return ok
}
