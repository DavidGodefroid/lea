package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/DavidGodefroid/lea/internal/grants"
	"github.com/DavidGodefroid/lea/internal/store"
)

// Grants (issue #73): the user records on the host which actions every agent of a lea may take
// without asking; the PreToolUse hook asks grant_check before each tool call. grant_add,
// grant_revoke and grant_list are host-only (dispatch): no agent, MCP tool or message can write a
// grant. An agent only reads the grants that apply to it (grants_list).

const (
	// DefaultGrantTTL is a grant's life when the user names none; MaxGrantTTL caps it.
	DefaultGrantTTL = 8 * time.Hour
	MaxGrantTTL     = 30 * 24 * time.Hour
	maxGrantUses    = 50
	maxGrantList    = 200
	// maxLineage bounds the walk up an agent's parents.
	maxLineage = 32
)

var (
	grantRepo   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
	grantBranch = regexp.MustCompile(`^[A-Za-z0-9*][A-Za-z0-9*._/-]{0,127}$`)
	grantGitHub = regexp.MustCompile(`^(github\.com/)?([A-Za-z0-9][A-Za-z0-9-]{0,38})/([A-Za-z0-9._-]{1,100})$`)
)

// grantAdd records a grant: the host's only.
func (d *Daemon) grantAdd(raw json.RawMessage) (any, error) {
	var p struct {
		Container, Action, Repo, Branch, Agent, GitHub string
		TTLS                                           int64 `json:"ttl_s"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if !grants.ValidAction(p.Action) {
		return nil, fmt.Errorf("unknown action %q (actions: %s)", p.Action, strings.Join(grants.Actions, ", "))
	}
	if !containerName.MatchString(p.Container) {
		return nil, errors.New("container: name the lea the grant applies to")
	}
	if p.Repo != "" && !grantRepo.MatchString(p.Repo) {
		return nil, errors.New("repo: a directory name under the projects directory")
	}
	if p.Branch != "" && !grantBranch.MatchString(p.Branch) {
		return nil, errors.New("branch: a branch name or glob (* matches anything)")
	}
	if p.Branch != "" && !strings.Contains(p.Branch, "*") && grants.Protected(p.Branch) {
		return nil, errors.New("branch: no grant covers the default branch")
	}
	if p.Repo == "" && grants.NeedsGitHub(p.Action) {
		return nil, fmt.Errorf("repo: a %s grant names the repository it covers, the one --github is", p.Action)
	}
	switch {
	case p.GitHub == "" && grants.NeedsGitHub(p.Action):
		return nil, fmt.Errorf("github: a %s grant names the GitHub repository its gh and git calls are pinned to (OWNER/NAME)", p.Action)
	case p.GitHub != "":
		gh, err := githubRepo(p.GitHub)
		if err != nil {
			return nil, err
		}
		p.GitHub = gh
	}
	if p.Agent != "" {
		key, err := d.resolveSpawned(p.Agent, p.Container)
		if err != nil {
			return nil, fmt.Errorf("agent: %w", err)
		}
		p.Agent = key
	}
	ttl := DefaultGrantTTL
	if p.TTLS != 0 {
		ttl = time.Duration(p.TTLS) * time.Second
	}
	if ttl < time.Minute || ttl > MaxGrantTTL {
		return nil, fmt.Errorf("expiry must be between 1m and %s", MaxGrantTTL)
	}
	now := d.now()
	g, err := d.st.AddGrant(grants.Grant{Container: p.Container, Action: p.Action, Repo: p.Repo, Branch: p.Branch,
		Agent: p.Agent, GitHub: p.GitHub, Created: now, Expires: now + int64(ttl/time.Second)})
	if err != nil {
		return nil, err
	}
	d.changed()
	return g, nil
}

// githubRepo is s, OWNER/NAME or github.com/OWNER/NAME, as github.com/OWNER/NAME.
func githubRepo(s string) (string, error) {
	m := grantGitHub.FindStringSubmatch(s)
	if m == nil || m[3] == "." || m[3] == ".." {
		return "", errors.New("github: OWNER/NAME of a github.com repository")
	}
	return "github.com/" + m[2] + "/" + m[3], nil
}

// deliverySet is lea delivery on|off (issue #92), the host's only: off revokes the standing
// grants of a lea; on replaces them with the delivery actions on each repository it names, with
// the GitHub repository the host read from that repository's origin. Standing grants never expire;
// grants the user added by hand are left alone.
func (d *Daemon) deliverySet(raw json.RawMessage) (any, error) {
	var p struct {
		Container string
		On        bool
		Repos     []struct{ Repo, GitHub string }
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if !containerName.MatchString(p.Container) {
		return nil, errors.New("container: name the lea delivery applies to")
	}
	if !p.On && len(p.Repos) > 0 {
		return nil, errors.New("repos: delivery off names none")
	}
	if len(p.Repos) > maxGrantList/len(grants.DeliveryActions) {
		return nil, errors.New("repos: too many repositories")
	}
	seen := map[string]bool{}
	for i, r := range p.Repos {
		if !grantRepo.MatchString(r.Repo) || seen[r.Repo] {
			return nil, fmt.Errorf("repo %q: a directory name under the projects directory, once", r.Repo)
		}
		seen[r.Repo] = true
		gh, err := githubRepo(r.GitHub)
		if err != nil {
			return nil, fmt.Errorf("repo %s: %w", r.Repo, err)
		}
		p.Repos[i].GitHub = gh
	}
	now := d.now()
	old, err := d.st.ActiveGrants(p.Container, now)
	if err != nil {
		return nil, err
	}
	for _, g := range old {
		if g.IsStanding() {
			if err := d.st.RevokeGrant(g.ID, now); err != nil && !errors.Is(err, store.ErrDecided) {
				return nil, err
			}
		}
	}
	out := []grants.Grant{}
	for _, r := range p.Repos {
		for _, a := range grants.DeliveryActions {
			g, err := d.st.AddGrant(grants.Grant{Container: p.Container, Action: a, Repo: r.Repo, GitHub: r.GitHub,
				Created: now, Expires: grants.Standing})
			if err != nil {
				return nil, err
			}
			out = append(out, g)
		}
	}
	d.changed()
	return map[string]any{"grants": out}, nil
}

// grantRevoke ends a grant at once: its next matching call takes the normal path again.
func (d *Daemon) grantRevoke(raw json.RawMessage) (any, error) {
	var p struct{ ID int64 }
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	switch err := d.st.RevokeGrant(p.ID, d.now()); {
	case errors.Is(err, store.ErrNotFound):
		return nil, fmt.Errorf("no grant #%d", p.ID)
	case errors.Is(err, store.ErrDecided):
		return nil, fmt.Errorf("grant #%d is already revoked", p.ID)
	case err != nil:
		return nil, err
	}
	d.changed()
	return struct{}{}, nil
}

// grantList is the host's view: active grants, or every recent one with all, and recent uses.
func (d *Daemon) grantList(raw json.RawMessage) (any, error) {
	var p struct{ All bool }
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
	}
	var gs []grants.Grant
	var err error
	if p.All {
		gs, err = d.st.RecentGrants(maxGrantList)
	} else {
		gs, err = d.st.ActiveGrants("", d.now())
	}
	if err != nil {
		return nil, err
	}
	us, err := d.st.GrantUses(maxGrantUses)
	if err != nil {
		return nil, err
	}
	return map[string]any{"grants": gs, "uses": us, "now": d.now()}, nil
}

// grantsMine is an agent's read-only view: the active grants of its lea that cover it.
func (d *Daemon) grantsMine(o Origin, me string) (any, error) {
	c, err := d.grantCaller(o, me, "")
	if err != nil {
		return nil, err
	}
	gs, err := d.st.ActiveGrants(o.Container, d.now())
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, g := range gs {
		if g.Agent != "" && !contains(c.Lineage, g.Agent) {
			continue
		}
		out = append(out, map[string]any{"id": g.ID, "action": g.Action, "repo": g.Repo, "branch": g.Branch,
			"agent": g.Agent, "github": g.GitHub, "expires": g.Expires})
	}
	return out, nil
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// grantCaller is the calling agent as the daemon knows it: its container is the socket's, its
// repo, branch and parents its row's, never anything the agent says. A branch another live agent
// of the lea also works on (same repo) is nobody's own. cwd is where the tool call runs (the
// hook's): it says whether that is the caller's own worktree, and relative paths are read from it.
func (d *Daemon) grantCaller(o Origin, me, cwd string) (grants.Caller, error) {
	if o.Container == "" {
		return grants.Caller{}, errors.New("grants apply to agents only")
	}
	a, err := d.st.AgentByKey(me)
	if err != nil {
		return grants.Caller{}, err
	}
	c := grants.Caller{Key: me, Container: o.Container, Repo: a.Repo, Branch: a.Branch, Lineage: []string{me},
		OwnWorktree: grants.InWorktree(cwd, a.Repo, a.Name), Cwd: cwd}
	if c.OwnWorktree {
		c.Worktree = strings.TrimSuffix(cwd, "/")
	}
	if c.Branch != "" {
		as, err := d.st.Agents()
		if err != nil {
			return grants.Caller{}, err
		}
		for _, x := range as {
			if x.Key != me && x.Container == o.Container && x.Repo == a.Repo && x.Branch == a.Branch && x.State != "done" {
				c.Branch = ""
				break
			}
		}
	}
	for p := a.Parent; p != "" && len(c.Lineage) < maxLineage && !contains(c.Lineage, p); {
		c.Lineage = append(c.Lineage, p)
		pa, err := d.st.AgentByKey(p)
		if err != nil {
			break
		}
		p = pa.Parent
	}
	return c, nil
}

// finishedChild says whether name is one of me's children whose turn is over (idle) or whose
// process ended: what stop-finished covers.
func (d *Daemon) finishedChild(o Origin, me string, input json.RawMessage) bool {
	var in struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(input, &in) != nil || !validTarget(in.Name) {
		return false
	}
	key, err := d.resolveSpawned(in.Name, o.Container)
	if err != nil {
		return false
	}
	t, err := d.st.AgentByKey(key)
	if err != nil || t.Parent != me {
		return false
	}
	return t.State == "idle" || t.State == "done" || t.State == "stopped"
}

// grantCheck answers the PreToolUse hook: allow (a grant covers the call), deny (a hard deny) or
// no verdict. The tool input is read here and never stored; an allowed call leaves an audit row
// naming its target only. Any error is no verdict: the call takes the normal path.
func (d *Daemon) grantCheck(o Origin, me string, raw json.RawMessage) (any, error) {
	var p struct {
		Tool  string            `json:"tool"`
		Input json.RawMessage   `json:"input"`
		Cwd   string            `json:"cwd"`
		Env   map[string]string `json:"env"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	c, err := d.grantCaller(o, me, p.Cwd)
	if err != nil {
		return nil, err
	}
	c.Env = map[string]string{}
	for _, k := range grants.SafeEnv {
		if v, ok := p.Env[k]; ok {
			c.Env[k] = v
		}
	}
	now := d.now()
	gs, err := d.st.ActiveGrants(o.Container, now)
	if err != nil {
		return nil, err
	}
	finished := p.Tool == grants.StopTool && d.finishedChild(o, me, p.Input)
	dec := grants.Decide(p.Tool, p.Input, c, gs, now, finished)
	if dec.Verdict == grants.Allow {
		if err := d.st.AddGrantUse(store.GrantUse{Grant: dec.Grant, Agent: me, Action: dec.Request.Action,
			Target: capText(dec.Request.Target, 200), At: now}); err != nil {
			// No audit row, no allow.
			log.Printf("lea daemon: audit grant #%d: %v", dec.Grant, err)
			return map[string]any{"verdict": ""}, nil
		}
		d.changed()
	}
	return map[string]any{"verdict": dec.Verdict, "reason": dec.Reason, "grant": dec.Grant, "command": dec.Command}, nil
}
