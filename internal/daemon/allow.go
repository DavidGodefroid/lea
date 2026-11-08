package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/DavidGodefroid/lea/internal/grants"
	"github.com/DavidGodefroid/lea/internal/store"
)

// Allowances (#319): lea allow records a set of grant rows in one call, one per capability and
// repository, and the set is what the user sees (allow_list) and revokes (allow_off). The three
// methods are host-only (dispatch), as grant_revoke: no agent, MCP tool or message reaches them.
// The pins come from the host, which reads each repository's origin (cmd/lea allow): this method
// only validates them.

const (
	// maxAllowUses bounds the uses allow_list returns (lea allow log).
	maxAllowUses = 200
	// allowListUses: what lea allow shows by default.
	allowListUses = 10
)

// allowParams is a lea allow call, as the host's CLI sends it.
type allowParams struct {
	Container    string   `json:"container"`
	Capabilities []string `json:"capabilities"`
	Repos        []struct {
		Repo string `json:"repo"`
		Pin  string `json:"pin"`
	} `json:"repos"`
	// All says Repos are every forge repository of the projects directory: an until-revoked set
	// recorded again drops the repositories that are no longer there.
	All    bool   `json:"all"`
	Branch string `json:"branch"`
	Agent  string `json:"agent"`
	// Tag is the release capability's tag glob (#353), "" any semver tag.
	Tag string `json:"tag"`
	// TTLS is the set's life; 0 holds until revoked.
	TTLS int64 `json:"ttl_s"`
}

// normalPin is a repository's pin as a grant stores it: github.com/OWNER/NAME (OWNER/NAME
// accepted) or gitlab:HOST/GROUP/.../NAME.
func normalPin(repo, pin string) (string, error) {
	if strings.HasPrefix(pin, "gitlab:") {
		if !grants.ValidPin(pin) {
			return "", fmt.Errorf("repo %s: gitlab:HOST/GROUP/NAME of a GitLab project", repo)
		}
		return pin, nil
	}
	gh, err := githubRepo(pin)
	if err != nil {
		return "", fmt.Errorf("repo %s: %w", repo, err)
	}
	return gh, nil
}

// allowRows checks p and returns the set's capabilities and rows (container, created and expiry
// aside), and its expiry.
func (d *Daemon) allowRows(p *allowParams, now int64) ([]grants.Capability, []grants.Grant, int64, error) {
	if !containerName.MatchString(p.Container) {
		return nil, nil, 0, errors.New("container: name the lea the allowance applies to")
	}
	caps, err := grants.Expand(p.Capabilities)
	if err != nil {
		return nil, nil, 0, err
	}
	forge := false
	for _, c := range caps {
		forge = forge || c.Forge()
	}
	switch {
	case forge && len(p.Repos) == 0:
		return nil, nil, 0, errors.New("repos: no repository of the projects directory has an origin on a forge: name one with --repo")
	case !forge && len(p.Repos) > 0:
		return nil, nil, 0, errors.New("repos: " + caps[0].Name + " covers agents, not a repository: name no --repo")
	case len(p.Repos)*len(caps) > maxGrantList:
		return nil, nil, 0, errors.New("repos: too many repositories")
	}
	if p.Branch != "" && !grantBranch.MatchString(p.Branch) {
		return nil, nil, 0, errors.New("branch: a branch name or glob (* matches anything)")
	}
	if p.Branch != "" && !strings.Contains(p.Branch, "*") && grants.Protected(p.Branch) {
		return nil, nil, 0, errors.New("branch: no allowance covers the default branch")
	}
	release := false
	for _, c := range caps {
		release = release || c.Action == grants.ReleaseCreate
	}
	if p.Tag != "" && !release {
		return nil, nil, 0, errors.New("tag: only the release capability names a tag glob")
	}
	if p.Tag != "" && !grants.ValidTagGlob(p.Tag) {
		return nil, nil, 0, errors.New("tag: a glob on a semver tag, starting with v (v0.15.*)")
	}
	expires := grants.Standing
	if p.TTLS != 0 {
		ttl := time.Duration(p.TTLS) * time.Second
		if ttl < time.Minute || ttl > MaxGrantTTL {
			return nil, nil, 0, fmt.Errorf("--for must be between 1m and %s", MaxGrantTTL)
		}
		expires = now + p.TTLS
	}
	if p.Agent != "" {
		key, err := d.resolveSpawned(p.Agent, p.Container)
		if err != nil {
			return nil, nil, 0, fmt.Errorf("agent: %w", err)
		}
		p.Agent = key
	}
	seen := map[string]bool{}
	for i, r := range p.Repos {
		if !grantRepo.MatchString(r.Repo) || seen[r.Repo] {
			return nil, nil, 0, fmt.Errorf("repo %q: a directory name under the projects directory, once", r.Repo)
		}
		seen[r.Repo] = true
		if p.Repos[i].Pin, err = normalPin(r.Repo, r.Pin); err != nil {
			return nil, nil, 0, err
		}
	}
	var rows []grants.Grant
	for _, c := range caps {
		if !c.Forge() {
			rows = append(rows, grants.Grant{Action: c.Action, Branch: p.Branch, Agent: p.Agent})
			continue
		}
		for _, r := range p.Repos {
			g := grants.Grant{Action: c.Action, Repo: r.Repo, Pin: r.Pin, Branch: p.Branch, Agent: p.Agent}
			if c.Action == grants.ReleaseCreate {
				g.Tag = p.Tag
			}
			rows = append(rows, g)
		}
	}
	return caps, rows, expires, nil
}

func capNames(caps []grants.Capability) string {
	out := make([]string, len(caps))
	for i, c := range caps {
		out[i] = c.Name
	}
	return strings.Join(out, " ")
}

// allowSet is lea allow <capabilities> (host only). A set that holds until revoked, recorded
// again with the same capabilities, branch and agent, is updated in place: its unchanged rows keep
// their ids, a repository whose pin changed gets new rows, and with all the repositories no
// longer named are dropped. Any other call records a new set.
func (d *Daemon) allowSet(raw json.RawMessage) (any, error) {
	var p allowParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	now := d.now()
	caps, rows, expires, err := d.allowRows(&p, now)
	if err != nil {
		return nil, err
	}
	if expires == grants.Standing {
		sets, err := d.st.GrantSets(p.Container, now)
		if err != nil {
			return nil, err
		}
		for _, s := range sets {
			if !s.UntilRevoked() || strings.Join(s.Capabilities, " ") != capNames(caps) || !sameScope(s, p.Branch, p.Agent, p.Tag, now) {
				continue
			}
			return d.updateSet(s, rows, p.All, now)
		}
	}
	names := strings.Fields(capNames(caps))
	s, err := d.st.AddGrantSet(grants.Set{Container: p.Container, Capabilities: names, Created: now, Expires: expires}, rows)
	if err != nil {
		return nil, err
	}
	d.changed()
	return map[string]any{"set": s, "unchanged": false}, nil
}

// sameScope says whether the active rows of s all name branch and agent, and its release rows tag.
func sameScope(s grants.Set, branch, agent, tag string, now int64) bool {
	for _, g := range s.Grants {
		if g.Active(now) && (g.Branch != branch || g.Agent != agent || (g.Action == grants.ReleaseCreate && g.Tag != tag)) {
			return false
		}
	}
	return true
}

// updateSet records want in s: rows already there with the same pin stay as they are.
func (d *Daemon) updateSet(s grants.Set, want []grants.Grant, all bool, now int64) (any, error) {
	type key struct{ action, repo string }
	have := map[key]grants.Grant{}
	for _, g := range s.Grants {
		if g.Active(now) {
			have[key{g.Action, g.Repo}] = g
		}
	}
	var add []grants.Grant
	var revoke []int64
	wanted := map[key]bool{}
	for _, g := range want {
		k := key{g.Action, g.Repo}
		wanted[k] = true
		switch old, ok := have[k]; {
		case ok && old.Pin == g.Pin:
		case ok:
			revoke = append(revoke, old.ID)
			add = append(add, g)
		default:
			add = append(add, g)
		}
	}
	if all {
		for k, g := range have {
			if !wanted[k] {
				revoke = append(revoke, g.ID)
			}
		}
	}
	if len(add)+len(revoke) == 0 {
		return map[string]any{"set": s, "unchanged": true}, nil
	}
	if err := d.st.ChangeGrantSet(s.ID, add, revoke, now); err != nil {
		return nil, err
	}
	d.changed()
	s, err := d.st.GrantSetByID(s.ID)
	if err != nil {
		return nil, err
	}
	if !s.Active(now) {
		// Every repository it covered is gone: nothing is left to allow.
		if err := d.st.RevokeGrantSet(s.ID, now); err != nil && !errors.Is(err, store.ErrDecided) {
			return nil, err
		}
	}
	return map[string]any{"set": s, "unchanged": false}, nil
}

// allowOff is lea allow off [N|capability|deliver] (host only): set N, the deliver sets, one
// capability in every set, or, with neither, everything of the lea. The next matching call takes
// the normal path.
func (d *Daemon) allowOff(raw json.RawMessage) (any, error) {
	var p struct {
		Container  string `json:"container"`
		Set        int64  `json:"set"`
		Capability string `json:"capability"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if !containerName.MatchString(p.Container) {
		return nil, errors.New("container: name the lea")
	}
	now := d.now()
	revoked := []int64{}
	if p.Set != 0 {
		s, err := d.st.GrantSetByID(p.Set)
		if errors.Is(err, store.ErrNotFound) || (err == nil && s.Container != p.Container) {
			return nil, fmt.Errorf("no allowance #%d in %s", p.Set, p.Container)
		}
		if err != nil {
			return nil, err
		}
		if !s.Active(now) {
			return nil, fmt.Errorf("allowance #%d is already off", p.Set)
		}
		if err := d.st.RevokeGrantSet(s.ID, now); err != nil {
			return nil, err
		}
		d.changed()
		return map[string]any{"sets": []int64{s.ID}}, nil
	}
	var only string
	if p.Capability != "" && p.Capability != grants.Deliver {
		c, ok := grants.CapabilityNamed(p.Capability)
		if !ok {
			return nil, fmt.Errorf("unknown capability %q (capabilities: %s; preset: %s)", p.Capability, strings.Join(grants.CapabilityNames(), ", "), grants.Deliver)
		}
		only = c.Action
	}
	sets, err := d.st.GrantSets(p.Container, now)
	if err != nil {
		return nil, err
	}
	for _, s := range sets {
		switch {
		case p.Capability == grants.Deliver && grants.Short(s.Capabilities) != grants.Deliver:
			continue
		case only != "" && strings.Join(s.ActiveCapabilities(now), " ") != p.Capability:
			// The set covers more than that capability: only its rows of it end.
			var ids []int64
			for _, g := range s.Grants {
				if g.Active(now) && g.Action == only {
					ids = append(ids, g.ID)
				}
			}
			if len(ids) == 0 {
				continue
			}
			if err := d.st.ChangeGrantSet(s.ID, nil, ids, now); err != nil {
				return nil, err
			}
			revoked = append(revoked, s.ID)
			continue
		}
		if err := d.st.RevokeGrantSet(s.ID, now); err != nil && !errors.Is(err, store.ErrDecided) {
			return nil, err
		}
		revoked = append(revoked, s.ID)
	}
	if p.Capability == "" {
		// A row an older lea recorded with no set.
		gs, err := d.st.ActiveGrants(p.Container, now)
		if err != nil {
			return nil, err
		}
		for _, g := range gs {
			if g.SetID == 0 {
				if err := d.st.RevokeGrant(g.ID, now); err != nil && !errors.Is(err, store.ErrDecided) {
					return nil, err
				}
			}
		}
	}
	d.changed()
	return map[string]any{"sets": revoked}, nil
}

// allowList is lea allow and lea allow log (host only): the active sets of one lea, each with its
// last use, its recent uses (uses of them, at most maxAllowUses), and the last pin recorded for
// each of its repositories (what lea allow compares the origins with).
func (d *Daemon) allowList(raw json.RawMessage) (any, error) {
	var p struct {
		Container string `json:"container"`
		Uses      int    `json:"uses"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if !containerName.MatchString(p.Container) {
		return nil, errors.New("container: name the lea")
	}
	if p.Uses <= 0 {
		p.Uses = allowListUses
	}
	p.Uses = min(p.Uses, maxAllowUses)
	now := d.now()
	sets, err := d.st.GrantSets(p.Container, now)
	if err != nil {
		return nil, err
	}
	uses, err := d.st.GrantUses(p.Container, p.Uses)
	if err != nil {
		return nil, err
	}
	last, err := d.st.LastGrantSetUses(p.Container)
	if err != nil {
		return nil, err
	}
	pins, err := d.st.LastPins(p.Container)
	if err != nil {
		return nil, err
	}
	lastUse := map[string]int64{}
	for id, at := range last {
		lastUse[fmt.Sprint(id)] = at
	}
	return map[string]any{"sets": sets, "uses": uses, "last_use": lastUse, "pins": pins, "now": now}, nil
}
