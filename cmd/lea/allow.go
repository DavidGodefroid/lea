package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/DavidGodefroid/lea/internal/grants"
	"github.com/DavidGodefroid/lea/internal/store"
)

const allowUse = `usage: lea allow                       what agents of this lea may do without asking
       lea allow log                   every recorded use
       lea allow CAPABILITY... [--repo R]... [--branch GLOB] [--agent NAME] [--tag GLOB] [--for DURATION] [--gitlab HOST]... [--yes DIGEST]
       lea allow off [N|CAPABILITY|deliver]
capabilities: ` + "push, pr, merge, delete-branch, issues, stop-finished, release-tag, release (--tag: its tags); preset: deliver (push pr merge delete-branch)"

// allowView is allow_list's answer.
type allowView struct {
	Sets    []grants.Set
	Uses    []store.GrantUse
	LastUse map[string]int64 `json:"last_use"`
	Pins    map[string]string
	Now     int64
}

// runAllow is the core form of lea allow (lea allow --container NAME --projects DIR [ARGS]),
// #319: the launcher names the container and the projects directory. It records a set of grants
// (allow_set), lists the lea's sets and uses (allow_list) and revokes them (allow_off), through
// the host API only. The pins of the forge capabilities are always read from each repository's
// origin, on the host and at that moment; a pin the user has not seen yet (new, or changed since
// the last one recorded for that repository) is recorded only once the user confirms it.
func runAllow(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 4 || args[0] != "--container" || args[2] != "--projects" || !filepath.IsAbs(args[3]) {
		fmt.Fprintln(stderr, allowUse)
		return 2
	}
	container, proj, rest := args[1], args[3], args[4:]
	bad := func() int { fmt.Fprintln(stderr, allowUse); return 2 }
	if len(rest) == 0 {
		return allowShow(container, false, stdout, stderr)
	}
	switch rest[0] {
	case "log":
		if len(rest) != 1 {
			return bad()
		}
		return allowShow(container, true, stdout, stderr)
	case "off":
		if len(rest) > 2 {
			return bad()
		}
		return allowOff(container, rest[1:], stdout, stderr)
	case "check":
		if len(rest) != 3 || rest[1] != "--state" || !filepath.IsAbs(rest[2]) {
			return bad()
		}
		return allowCheck(container, proj, rest[2], stdout)
	}
	return allowRecord(container, proj, rest, stdin, stdout, stderr)
}

func failf(stderr io.Writer, err error) int {
	fmt.Fprintln(stderr, "lea:", clean(err.Error(), false))
	return 1
}

// allowRequest is what lea allow CAPABILITY... was asked for.
type allowRequest struct {
	caps               []grants.Capability
	names              []string
	repos              []string
	branch, agent, pin string
	tag                string
	ttl                time.Duration
	gitlab             map[string]bool
	yes                bool
	digest             string
	words              []string // the command's words, for the --yes it asks for
}

func parseAllow(rest []string) (allowRequest, bool) {
	r := allowRequest{gitlab: map[string]bool{"gitlab.com": true}}
	i := 0
	for ; i < len(rest) && !strings.HasPrefix(rest[i], "-"); i++ {
		r.names = append(r.names, rest[i])
	}
	if len(r.names) == 0 {
		return r, false
	}
	seen := map[string]bool{}
	for ; i < len(rest); i += 2 {
		if i+1 >= len(rest) || rest[i+1] == "" {
			return r, false
		}
		f, v := rest[i], rest[i+1]
		if f != "--repo" && f != "--gitlab" && seen[f] {
			return r, false
		}
		seen[f] = true
		switch f {
		case "--repo":
			r.repos = append(r.repos, v)
		case "--branch":
			r.branch = v
		case "--agent":
			r.agent = v
		case "--tag":
			r.tag = v
		case "--for":
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 {
				return r, false
			}
			r.ttl = d
		case "--gitlab":
			if !gitlabHostRe.MatchString(strings.ToLower(v)) {
				return r, false
			}
			r.gitlab[strings.ToLower(v)] = true
		case "--yes":
			if !digestRe.MatchString(v) {
				return r, false
			}
			r.yes, r.digest = true, v
			continue
		case "--check-pin":
			// lea grant add --github X (the alias): X must be the repository's origin.
			r.pin = v
			continue
		default:
			return r, false
		}
		r.words = append(r.words, f, v)
	}
	r.words = append(append([]string{}, r.names...), r.words...)
	return r, true
}

// forgeRepo is a repository of the projects directory and the pin its origin reads as.
type forgeRepo struct {
	Repo string `json:"repo"`
	Pin  string `json:"pin"`
}

// readRepos reads the pins of the repositories r names, or of every repository of proj with a
// forge origin: what was skipped, and why, comes second; an error is a named repository that has
// none.
func readRepos(proj string, r allowRequest) ([]forgeRepo, []string, error) {
	var repos []forgeRepo
	var skipped []string
	if len(r.repos) > 0 {
		for _, name := range r.repos {
			if strings.HasPrefix(name, ".") || strings.ContainsRune(name, '/') {
				return nil, nil, fmt.Errorf("--repo %s: a directory of the projects directory", oneLine(name, 100))
			}
			url := repoOrigin(filepath.Join(proj, name))
			pin := originPin(url, r.gitlab)
			if pin == "" {
				return nil, nil, fmt.Errorf("--repo %s: %s", oneLine(name, 100), noForge(url))
			}
			repos = append(repos, forgeRepo{name, pin})
		}
		return repos, nil, nil
	}
	ents, err := os.ReadDir(proj)
	if err != nil {
		return nil, nil, err
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".") || !e.IsDir() {
			continue
		}
		url := repoOrigin(filepath.Join(proj, e.Name()))
		if pin := originPin(url, r.gitlab); pin != "" {
			repos = append(repos, forgeRepo{e.Name(), pin})
		} else {
			skipped = append(skipped, e.Name()+"  skipped: "+noForge(url))
		}
	}
	return repos, skipped, nil
}

func noForge(url string) string {
	if url == "" {
		return "no origin"
	}
	return "origin is not on github.com, gitlab.com or a --gitlab host"
}

// pinsDigest names a list of pins: --yes DIGEST records exactly the pins the user was shown.
func pinsDigest(repos []forgeRepo) string {
	sum := sha256.New()
	for _, x := range repos {
		fmt.Fprintf(sum, "%s\x00%s\n", x.Repo, x.Pin)
	}
	return hex.EncodeToString(sum.Sum(nil))[:16]
}

func allowRecord(container, proj string, rest []string, stdin io.Reader, stdout, stderr io.Writer) int {
	r, ok := parseAllow(rest)
	if !ok {
		fmt.Fprintln(stderr, allowUse)
		return 2
	}
	caps, err := grants.Expand(r.names)
	if err != nil {
		fmt.Fprintln(stderr, "lea:", clean(err.Error(), false))
		return 2
	}
	r.caps = caps
	forge := false
	for _, c := range caps {
		forge = forge || c.Forge()
	}
	var cur allowView
	if err := hostCall("allow_list", map[string]any{"container": container}, &cur); err != nil {
		return failf(stderr, err)
	}
	// The GitLab hosts the lea's pins already name are remembered (--gitlab once).
	for _, pin := range cur.Pins {
		if h, ok := strings.CutPrefix(pin, "gitlab:"); ok {
			h, _, _ = strings.Cut(h, "/")
			r.gitlab[h] = true
		}
	}
	var repos []forgeRepo
	var skipped []string
	if forge {
		if repos, skipped, err = readRepos(proj, r); err != nil {
			return failf(stderr, err)
		}
		if r.pin != "" {
			want := r.pin
			if !strings.Contains(want, ":") && !strings.HasPrefix(want, "github.com/") {
				want = "github.com/" + want
			}
			for _, x := range repos {
				if x.Pin != want {
					return failf(stderr, fmt.Errorf("--github %s is not the origin of %s, which is %s: lea pins the origin; check it, then run lea allow %s --repo %s",
						oneLine(r.pin, 200), oneLine(x.Repo, 100), oneLine(x.Pin, 200), r.names[0], oneLine(x.Repo, 100)))
				}
			}
		}
		if len(repos) == 0 {
			for _, sk := range skipped {
				fmt.Fprintf(stderr, "lea: %s\n", oneLine(sk, 200))
			}
			return failf(stderr, fmt.Errorf("nothing recorded: no repository of %s has an origin on a forge", oneLine(proj, 200)))
		}
	} else if len(r.repos) > 0 {
		fmt.Fprintln(stderr, "lea: "+caps[0].Name+" covers agents, not a repository: name no --repo")
		return 2
	}
	// An agent can write a repository's .git/config (or add a repository): a pin is recorded only
	// once the user has seen it, unless it is the last one recorded for that repository.
	unseen := false
	for _, x := range repos {
		switch w, ok := cur.Pins[x.Repo]; {
		case !ok:
			fmt.Fprintf(stderr, "lea: %s: new pin %s\n", oneLine(x.Repo, 100), oneLine(x.Pin, 200))
			unseen = true
		case w != x.Pin:
			fmt.Fprintf(stderr, "lea: %s: origin changed from %s to %s\n", oneLine(x.Repo, 100), oneLine(w, 200), oneLine(x.Pin, 200))
			unseen = true
		}
	}
	digest := pinsDigest(repos)
	if r.yes && r.digest != digest {
		return failf(stderr, fmt.Errorf("nothing recorded: the pins changed since they were listed; run lea allow %s again and check them", oneLine(strings.Join(r.names, " "), 200)))
	}
	if unseen && !r.yes {
		for _, sk := range skipped {
			fmt.Fprintf(stderr, "lea: %s\n", oneLine(sk, 200))
		}
		if !stdinIsTerminal(stdin) {
			fmt.Fprintf(stderr, "lea: nothing recorded: check those repositories (agents can rewrite .git/config), then run lea allow %s --yes %s\n",
				oneLine(strings.Join(r.words, " "), 400), digest)
			return 1
		}
		fmt.Fprint(stderr, "lea: agents can rewrite .git/config: record these? [y/N] ")
		line, _ := bufio.NewReader(stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Fprintln(stderr, "lea: nothing recorded")
			return 1
		}
		// Read again: an origin rewritten while the question waited is not what the user saw.
		again, _, err := readRepos(proj, r)
		if err != nil || pinsDigest(again) != digest {
			return failf(stderr, fmt.Errorf("nothing recorded: the pins changed since they were listed; run lea allow %s again and check them", oneLine(strings.Join(r.names, " "), 200)))
		}
	}
	p := map[string]any{"container": container, "capabilities": r.names, "repos": repos, "all": len(r.repos) == 0,
		"branch": r.branch, "agent": r.agent, "tag": r.tag, "ttl_s": int64(r.ttl / time.Second)}
	var res struct {
		Set       grants.Set
		Unchanged bool
	}
	if err := hostCall("allow_set", p, &res); err != nil {
		return failf(stderr, err)
	}
	now := time.Now().Unix()
	head := fmt.Sprintf("allowance #%d", res.Set.ID)
	if res.Unchanged {
		head += " (unchanged)"
	}
	fmt.Fprintf(stdout, "%s: %s\n", head, describeSet(res.Set, now))
	for _, s := range skipped {
		fmt.Fprintf(stdout, "  %s\n", oneLine(s, 200))
	}
	if forge && len(r.repos) == 0 {
		fmt.Fprintln(stdout, "run it again after adding a repository or changing an origin")
	}
	return 0
}

// describeSet is a set as one sentence: who may do what, where, and until when.
func describeSet(s grants.Set, now int64) string {
	caps := s.ActiveCapabilities(now)
	var does []string
	for _, n := range caps {
		c, _ := grants.CapabilityNamed(n)
		does = append(does, c.Does)
	}
	who := "agents of " + oneLine(s.Container, 64)
	var branch, agent string
	for _, g := range s.Grants {
		if g.Active(now) {
			branch, agent = g.Branch, g.Agent
			break
		}
	}
	if agent != "" {
		who = oneLine(agent, 129) + " and its children"
	}
	b := who + " may " + joinAnd(does)
	if repos := s.Repos(now); len(repos) > 0 {
		var on []string
		for _, r := range repos {
			on = append(on, oneLine(r[0], 100)+" ("+oneLine(r[1], 200)+")")
		}
		b += " on " + strings.Join(on, ", ")
	}
	if branch != "" && branch != "*" {
		b += ", branches " + oneLine(branch, 128)
	}
	for _, g := range s.Grants {
		if g.Active(now) && g.Action == grants.ReleaseCreate && g.Tag != "" {
			b += ", tags " + oneLine(g.Tag, 64)
			break
		}
	}
	if s.UntilRevoked() {
		return b + fmt.Sprintf(", until lea allow off %d", s.ID)
	}
	return b + fmt.Sprintf(", until %s (%s left; lea allow off %d ends it sooner)", stamp(s.Expires), left(s.Expires-now), s.ID)
}

func joinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return "nothing"
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

// left is a time left, to the minute.
func left(s int64) string {
	d := (time.Duration(s) * time.Second).Round(time.Minute)
	if d < time.Minute {
		return "<1m"
	}
	return strings.TrimSuffix(d.String(), "0s")
}

// allowShow is lea allow (each active set, one line, and the last uses) or lea allow log (every
// recorded use, newest first), for this lea only.
func allowShow(container string, log bool, stdout, stderr io.Writer) int {
	n := 10
	if log {
		n = 200
	}
	var v allowView
	if err := hostCall("allow_list", map[string]any{"container": container, "uses": n}, &v); err != nil {
		return failf(stderr, err)
	}
	if log {
		if len(v.Uses) == 0 {
			fmt.Fprintf(stdout, "no allowance of %s was used yet\n", oneLine(container, 64))
			return 0
		}
		writeUses(stdout, v.Uses)
		return 0
	}
	if len(v.Sets) == 0 {
		fmt.Fprintf(stdout, "no allowance: agents of %s ask as usual (lea allow deliver lets them push, open and merge their own PRs)\n", oneLine(container, 64))
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tALLOWS\tWHERE\tUNTIL\tLAST USE")
	for _, s := range v.Sets {
		var where []string
		for _, r := range s.Repos(v.Now) {
			where = append(where, oneLine(r[0], 100)+" ("+oneLine(r[1], 200)+")")
		}
		var branch, agent string
		for _, g := range s.Grants {
			if g.Active(v.Now) {
				branch, agent = g.Branch, g.Agent
				break
			}
		}
		if branch != "" && branch != "*" {
			where = append(where, "branches "+oneLine(branch, 128))
		}
		if agent != "" {
			where = append(where, "for "+oneLine(agent, 129)+" and its children")
		}
		if len(where) == 0 {
			where = []string{"every agent"}
		}
		until := "revoked"
		if !s.UntilRevoked() {
			until = stamp(s.Expires) + " (" + left(s.Expires-v.Now) + " left)"
		}
		fmt.Fprintf(tw, "#%d\t%s\t%s\t%s\t%s\n", s.ID, grants.Short(s.ActiveCapabilities(v.Now)), strings.Join(where, ", "), until,
			stamp(v.LastUse[strconv.FormatInt(s.ID, 10)]))
	}
	tw.Flush()
	if len(v.Uses) > 0 {
		fmt.Fprintln(stdout, "\nlast uses (lea allow log: all):")
		writeUses(stdout, v.Uses)
	}
	return 0
}

func writeUses(w io.Writer, us []store.GrantUse) {
	for _, u := range us {
		name := u.Action
		if c, ok := grants.CapabilityOf(u.Action); ok {
			name = c.Name
		}
		fmt.Fprintf(w, "  %s #%d %s %s: %s\n", stamp(u.At), u.Set, oneLine(u.Agent, 64), oneLine(name, 32), oneLine(u.Target, 200))
	}
}

var setRe = regexp.MustCompile(`^#?([1-9][0-9]{0,17})$`)

// allowOff is lea allow off [N|CAPABILITY|deliver].
func allowOff(container string, rest []string, stdout, stderr io.Writer) int {
	p := map[string]any{"container": container}
	what := "every allowance of " + oneLine(container, 64)
	if len(rest) == 1 {
		if m := setRe.FindStringSubmatch(rest[0]); m != nil {
			id, _ := strconv.ParseInt(m[1], 10, 64)
			p["set"], what = id, fmt.Sprintf("allowance #%d", id)
		} else {
			p["capability"], what = rest[0], rest[0]+" in every allowance of "+oneLine(container, 64)
		}
	}
	var r struct{ Sets []int64 }
	if err := hostCall("allow_off", p, &r); err != nil {
		return failf(stderr, err)
	}
	if len(r.Sets) == 0 && len(rest) == 1 && p["set"] == nil {
		fmt.Fprintf(stdout, "nothing to revoke: no allowance of %s covers %s\n", oneLine(container, 64), oneLine(rest[0], 64))
		return 0
	}
	fmt.Fprintf(stdout, "revoked %s: its agents ask as usual\n", what)
	return 0
}

// allowCheck is lea doctor's part on allowances (lea allow --container NAME --projects DIR check
// --state DIR), issue #92, in doctor's format: none is fine; otherwise each pinned repository's
// origin still matches its pin, the repositories on a forge no allowance covers are named, and
// each forge host the lea pushes to has credentials: GitHub's in the proxy (lea github login,
// #103), GitLab's in lea's state (STATE/glab-cli; git's store STATE/git/credentials covers pushes
// only). It only reads, and fails only on missing credentials.
func allowCheck(container, proj, state string, stdout io.Writer) int {
	var v allowView
	if err := hostCall("allow_list", map[string]any{"container": container}, &v); err != nil {
		fmt.Fprintf(stdout, "warn  allowances: not checked: %s\n", oneLine(clean(err.Error(), false), 200))
		return 0
	}
	gitlab := map[string]bool{"gitlab.com": true}
	pins := map[string]string{}
	for _, s := range v.Sets {
		for _, r := range s.Repos(v.Now) {
			pins[r[0]] = r[1]
			if h, ok := strings.CutPrefix(r[1], "gitlab:"); ok {
				h, _, _ = strings.Cut(h, "/")
				gitlab[h] = true
			}
		}
	}
	if len(pins) == 0 {
		if len(v.Sets) == 0 {
			fmt.Fprintln(stdout, "ok    allowances: none (lea allow deliver lets agents push, open and merge their own PRs)")
		} else {
			fmt.Fprintln(stdout, "ok    allowances: none on a forge repository")
		}
		return 0
	}
	repos := make([]string, 0, len(pins))
	for repo := range pins {
		repos = append(repos, repo)
	}
	sort.Strings(repos)
	hosts := map[string]bool{} // forge host -> GitLab
	for _, repo := range repos {
		pin, name := pins[repo], oneLine(repo, 100)
		url := repoOrigin(filepath.Join(proj, repo))
		switch now := originPin(url, gitlab); {
		case url == "":
			fmt.Fprintf(stdout, "warn  allowances %s: no origin any more, still pinned to %s: run lea allow off, then lea allow again\n", name, oneLine(pin, 200))
		case now != pin:
			if now == "" {
				now = "off any known forge"
			}
			fmt.Fprintf(stdout, "warn  allowances %s: origin is now %s, still pinned to %s: check it, then run lea allow deliver again\n", name, oneLine(now, 200), oneLine(pin, 200))
		default:
			fmt.Fprintf(stdout, "ok    allowances %s (%s)\n", name, oneLine(pin, 200))
		}
		if h, ok := strings.CutPrefix(pin, "gitlab:"); ok {
			h, _, _ = strings.Cut(h, "/")
			hosts[h] = true
		} else {
			hosts["github.com"] = false
		}
	}
	if ents, err := os.ReadDir(proj); err == nil {
		for _, e := range ents {
			if _, covered := pins[e.Name()]; covered || strings.HasPrefix(e.Name(), ".") || !e.IsDir() {
				continue
			}
			if pin := originPin(repoOrigin(filepath.Join(proj, e.Name())), gitlab); pin != "" {
				fmt.Fprintf(stdout, "warn  allowances %s: not covered (%s): run lea allow deliver\n", oneLine(e.Name(), 100), oneLine(pin, 200))
			}
		}
	}
	names := make([]string, 0, len(hosts))
	for h := range hosts {
		names = append(names, h)
	}
	sort.Strings(names)
	// Pinned gh and glab calls take the token of their own config only; pushes take git's store too.
	rc := 0
	for _, h := range names {
		tool, cfg, login, what := "gh", filepath.Join(state, "gh", "hosts.yml"), "gh auth login", "PRs"
		if hosts[h] {
			tool, cfg, login, what = "glab", filepath.Join(state, "glab-cli", "config.yml"), "glab auth login --hostname "+h, "merge requests"
		}
		switch {
		case !hosts[h] && h == "github.com" && forgeTokenIn(state):
			// The proxy's forge forwarder holds it (#103): lea github login.
			fmt.Fprintf(stdout, "ok    allowances credentials %s (lea github login: in the proxy)\n", h)
		case !hosts[h] && h == "github.com":
			fmt.Fprintf(stdout, "FAIL  allowances credentials %s: none in lea's proxy: on the host, gh auth token | lea github login\n", h)
			rc = 1
		case yamlKey(cfg, h):
			fmt.Fprintf(stdout, "ok    allowances credentials %s (%s)\n", h, tool)
		case storeHas(filepath.Join(state, "git", "credentials"), h):
			fmt.Fprintf(stdout, "warn  allowances credentials %s: git's store only: pushes work, %s need %s\n", h, what, login)
		default:
			fmt.Fprintf(stdout, "FAIL  allowances credentials %s: none in lea's state: log in from lea (lea shell): %s\n", h, login)
			rc = 1
		}
	}
	return rc
}
