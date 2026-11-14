package grants

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Ask is a grant request as Routes reads it: what a lead asked the user for (grants_request).
type Ask struct {
	ID                        int64
	Action, Repo, Branch, Tag string
}

// Routes is how the user answers asks (#412), in at most three lines: the one-off route (lea
// approve the requests, lea requests to read them), the durable one (the lea allow commands that
// cover them all, for every agent of the lea, until lea allow off) and what the durable one leaves
// out. The action of each ask is read into its capability from Capabilities only. Empty for no ask.
func Routes(asks []Ask) []string {
	if len(asks) == 0 {
		return nil
	}
	ids := make([]string, len(asks))
	for i, a := range asks {
		ids[i] = fmt.Sprintf("g%d", a.ID)
	}
	one := "one-off: lea approve " + strings.Join(ids, " ") + " or lea deny " + ids[0] + " [reason]; lea requests says what it asks"
	if len(asks) > 1 {
		one = "one-off: lea approve " + strings.Join(ids, " ") + " (or lea deny gN [reason]); lea requests says what each asks"
	}
	cmds, caps, repos, unknown := durable(asks)
	if len(cmds) == 0 {
		return []string{one, "no lea allow form covers " + strings.Join(unknown, ", ") + ": approve it once"}
	}
	two := "durable, for every agent of this lea until lea allow off N (add --for 8h to limit it): " + strings.Join(cmds, "; ")
	var out []string
	for _, c := range Capabilities {
		if c.Forge() && !slices.Contains(caps, c.Name) {
			out = append(out, c.Name)
		}
	}
	covered := strings.Join(caps, ", ")
	if p := Short(caps); p != strings.Join(caps, " ") {
		covered = p + " (" + covered + ")"
	}
	three := "it covers only " + covered + " on " + strings.Join(repos, ", ")
	if len(out) > 0 {
		three += ", not " + strings.Join(out, ", ") + ", nor other repositories"
	} else {
		three += ", not other repositories"
	}
	if len(unknown) > 0 {
		three += "; no lea allow form covers " + strings.Join(unknown, ", ") + ": approve it once"
	}
	return []string{one, two, three}
}

// AllowCommands is the lea allow commands that cover asks, for every agent of the lea: the
// durable route of Routes alone, for a notification too short for all three lines.
func AllowCommands(asks []Ask) []string {
	cmds, _, _, _ := durable(asks)
	return cmds
}

// durable is the lea allow commands that cover asks, one per scope (repository, branch glob, tag
// glob), the repositories of the same capabilities and scope folded into one command; with the
// capabilities they cover in display order, the repositories in the order asked, and the asks no
// capability covers.
func durable(asks []Ask) (cmds, caps, repos, unknown []string) {
	type scope struct{ repo, branch, tag string }
	var order []scope
	want := map[scope]map[string]bool{}
	all := map[string]bool{}
	for _, a := range asks {
		c, ok := CapabilityOf(a.Action)
		if !ok || !c.Forge() {
			unknown = append(unknown, fmt.Sprintf("g%d (%s)", a.ID, a.Action))
			continue
		}
		s := scope{repo: a.Repo, branch: a.Branch}
		if s.branch == "*" {
			s.branch = ""
		}
		if a.Action == ReleaseCreate {
			s.tag = a.Tag
		}
		if want[s] == nil {
			want[s] = map[string]bool{}
			order = append(order, s)
		}
		want[s][c.Name] = true
		all[c.Name] = true
		if !slices.Contains(repos, a.Repo) {
			repos = append(repos, a.Repo)
		}
	}
	// A repository asked for release with one tag and for pr with no tag is one scope once the tag
	// is set: lea allow --tag only scopes the release rows.
	for i, s := range order {
		if s.tag != "" {
			continue
		}
		for _, t := range order {
			if t.tag != "" && t.repo == s.repo && t.branch == s.branch {
				for n := range want[s] {
					want[t][n] = true
				}
				order[i].repo = ""
				break
			}
		}
	}
	type folded struct {
		names, branch, tag string
		repos              []string
	}
	var fs []*folded
	for _, s := range order {
		if s.repo == "" {
			continue
		}
		var names []string
		for _, c := range Capabilities {
			if want[s][c.Name] {
				names = append(names, c.Name)
			}
		}
		n := Short(names)
		i := slices.IndexFunc(fs, func(f *folded) bool { return f.names == n && f.branch == s.branch && f.tag == s.tag })
		if i < 0 {
			fs = append(fs, &folded{names: n, branch: s.branch, tag: s.tag})
			i = len(fs) - 1
		}
		fs[i].repos = append(fs[i].repos, s.repo)
	}
	for _, f := range fs {
		cmd := "lea allow " + f.names
		for _, r := range f.repos {
			cmd += " --repo " + shellWord(r)
		}
		if f.branch != "" {
			cmd += " --branch " + shellWord(f.branch)
		}
		if f.tag != "" {
			cmd += " --tag " + shellWord(f.tag)
		}
		cmds = append(cmds, cmd)
	}
	for _, c := range Capabilities {
		if all[c.Name] {
			caps = append(caps, c.Name)
		}
	}
	return cmds, caps, repos, unknown
}

var plainWord = regexp.MustCompile(`^[A-Za-z0-9._/@:+=-]+$`)

// shellWord is s as the user pastes it in a shell: quoted when it holds a glob or a space.
func shellWord(s string) string {
	if plainWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
