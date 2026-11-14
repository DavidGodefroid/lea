package grants

import (
	"fmt"
	"sort"
	"strings"
)

// Want is one grant request (gN) as the durable route sees it (#412, #415): its action, the
// repository it names and, for a release, its tag glob.
type Want struct {
	ID                int64
	Action, Repo, Tag string
}

// Advice is what lea tells the user about one or several grant requests, in three lines at
// most: the one-off answer, the lea allow command that covers them all for every agent of the lea
// and how to undo it, and what that command leaves out. Every part comes from Capabilities and
// Presets: no prompt writes its own table.
type Advice struct {
	OneOff     string `json:"one_off"`
	Durable    string `json:"durable"`
	NotCovered string `json:"not_covered"`
	// Command is the lea allow command alone ("" when no request maps to a capability), with
	// what it records: the capability names (a preset's when exactly one), the repositories and
	// the release tag glob ("" any semver tag).
	Command      string   `json:"command"`
	Capabilities []string `json:"capabilities"`
	Repos        []string `json:"repos"`
	Tag          string   `json:"tag"`
}

// Lines are the advice's non-empty lines, in order.
func (a Advice) Lines() []string {
	var out []string
	for _, l := range []string{a.OneOff, a.Durable, a.NotCovered} {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// Advise groups ws into one advice: one lea approve for all of them, one lea allow covering
// each repository and capability they name.
func Advise(ws []Want) Advice {
	var a Advice
	if len(ws) == 0 {
		return a
	}
	ids := make([]string, 0, len(ws))
	have := map[string]bool{}
	repos := map[string]bool{}
	var unmapped []string
	tags := map[string]bool{}
	release := false
	for _, w := range ws {
		if w.ID != 0 {
			ids = append(ids, fmt.Sprintf("g%d", w.ID))
		}
		c, ok := CapabilityOf(w.Action)
		if !ok {
			unmapped = append(unmapped, w.Action)
			continue
		}
		have[c.Name] = true
		if w.Repo != "" && c.Forge() {
			repos[w.Repo] = true
		}
		if c.Action == ReleaseCreate {
			release = true
			tags[w.Tag] = true
		}
	}
	if len(ids) > 0 {
		a.OneOff = fmt.Sprintf("One-off: lea approve %s (or lea deny gN [reason]) covers the lead and its children for the time it asked; lea requests says what each one is.",
			strings.Join(ids, " "))
	}
	for _, c := range Capabilities {
		if have[c.Name] {
			a.Capabilities = append(a.Capabilities, c.Name)
		}
	}
	for r := range repos {
		a.Repos = append(a.Repos, r)
	}
	sort.Strings(a.Repos)
	if len(a.Capabilities) == 0 {
		a.Durable = "Durable: none: no lea allow capability covers " + strings.Join(unmapped, ", ") + "; answer it one-off."
		return a
	}
	if release && len(tags) == 1 {
		for t := range tags {
			a.Tag = t
		}
	}
	a.Capabilities = strings.Fields(Short(a.Capabilities))
	cmd := "lea allow " + strings.Join(a.Capabilities, " ")
	for _, r := range a.Repos {
		cmd += " --repo " + r
	}
	if a.Tag != "" {
		cmd += " --tag '" + a.Tag + "'"
	}
	a.Command = cmd
	a.Durable = "Durable: " + cmd + " lets every agent of this lea do it until you undo it with lea allow off N (lea allow lists N)."
	if release && a.Tag == "" {
		a.Durable += " With no --tag it covers any semver tag."
	}
	if len(unmapped) > 0 {
		a.Durable += " No capability covers " + strings.Join(unmapped, ", ") + ": answer it one-off."
	}
	covered := map[string]bool{}
	if cs, err := Expand(a.Capabilities); err == nil {
		for _, c := range cs {
			covered[c.Name] = true
		}
	}
	var left []string
	for _, c := range Capabilities {
		if c.Forge() && !covered[c.Name] {
			left = append(left, c.Name)
		}
	}
	if len(left) > 0 {
		what := strings.Join(a.Capabilities, " ")
		a.NotCovered = fmt.Sprintf("Not covered by %s: %s; each is its own lea allow CAPABILITY --repo R.", what, strings.Join(left, ", "))
	}
	return a
}
