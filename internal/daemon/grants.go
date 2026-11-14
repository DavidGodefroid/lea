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
// without asking (lea allow, #319: allow.go); the PreToolUse hook asks grant_check before each
// tool call. allow_set, allow_off, allow_list and grant_revoke are host-only (dispatch): no agent,
// MCP tool or message can write a grant. An agent only reads the grants that apply to it
// (grants_list).

const (
	// DefaultGrantTTL is the life of a lead's grant request when it names none; MaxGrantTTL caps
	// every timed grant.
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

// grantParams is a grant as the host (grant_add) or a lead's request (grants_request) names it.
type grantParams struct {
	Container, Action, Repo, Branch, Agent, GitHub string
	// Tag is a release-create grant's tag glob (#353), "" any semver tag.
	Tag  string
	TTLS int64 `json:"ttl_s"`
}

// checkGrant validates p as grant_add records it (the agent aside), normalises its GitHub
// repository and returns its life.
func checkGrant(p *grantParams) (time.Duration, error) {
	if !grants.ValidAction(p.Action) {
		return 0, fmt.Errorf("unknown action %q (actions: %s)", p.Action, strings.Join(grants.Actions, ", "))
	}
	if !containerName.MatchString(p.Container) {
		return 0, errors.New("container: name the lea the grant applies to")
	}
	if p.Repo != "" && !grantRepo.MatchString(p.Repo) {
		return 0, errors.New("repo: a directory name under the projects directory")
	}
	if p.Branch != "" && !grantBranch.MatchString(p.Branch) {
		return 0, errors.New("branch: a branch name or glob (* matches anything)")
	}
	if p.Branch != "" && !strings.Contains(p.Branch, "*") && grants.Protected(p.Branch) {
		return 0, errors.New("branch: no grant covers the default branch")
	}
	if p.Tag != "" && p.Action != grants.ReleaseCreate {
		return 0, fmt.Errorf("tag: only a %s grant names a tag glob", grants.ReleaseCreate)
	}
	if p.Tag != "" && !grants.ValidTagGlob(p.Tag) {
		return 0, errors.New("tag: a glob on a semver tag, starting with v (v0.15.*)")
	}
	if p.Repo == "" && grants.NeedsGitHub(p.Action) {
		return 0, fmt.Errorf("repo: a %s grant names the repository it covers, the one --github is", p.Action)
	}
	switch {
	case p.GitHub == "" && grants.NeedsGitHub(p.Action):
		return 0, fmt.Errorf("github: a %s grant names the GitHub repository its gh and git calls are pinned to (OWNER/NAME)", p.Action)
	case p.GitHub != "":
		gh, err := githubRepo(p.GitHub)
		if err != nil {
			return 0, err
		}
		p.GitHub = gh
	}
	ttl := DefaultGrantTTL
	if p.TTLS != 0 {
		ttl = time.Duration(p.TTLS) * time.Second
	}
	if ttl < time.Minute || ttl > MaxGrantTTL {
		return 0, fmt.Errorf("expiry must be between 1m and %s", MaxGrantTTL)
	}
	return ttl, nil
}

// recordGrant records a checked grant, its agent a key.
func (d *Daemon) recordGrant(p grantParams, ttl time.Duration) (grants.Grant, error) {
	now := d.now()
	g, err := d.st.AddGrant(grants.Grant{Container: p.Container, Action: p.Action, Repo: p.Repo, Branch: p.Branch,
		Agent: p.Agent, Pin: p.GitHub, Tag: p.Tag, Created: now, Expires: now + int64(ttl/time.Second)})
	if err != nil {
		return grants.Grant{}, err
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

// grantRevoke ends one grant at once (lea grant revoke): its next matching call takes the normal
// path again, and a set left with no active row ends with it.
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
		out = append(out, map[string]any{"id": g.ID, "set": g.SetID, "action": g.Action, "repo": g.Repo, "branch": g.Branch,
			"agent": g.Agent, "github": g.Pin, "expires": g.Expires})
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

// SetGrantProxy names the proxy of the pinned calls of an agent (nil: grants.LeaProxy, the
// default). Under LockHTTP squid is on the proxy's loopback: the calls go through the gate, with
// the agent's own proxy password.
func (d *Daemon) SetGrantProxy(f func(container, agent string) string) {
	d.mu.Lock()
	d.grantProxy = f
	d.mu.Unlock()
}

func (d *Daemon) grantProxyFor(container, agent string) string {
	d.mu.Lock()
	f := d.grantProxy
	d.mu.Unlock()
	if f == nil {
		return grants.LeaProxy
	}
	return f(container, agent)
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
		OwnWorktree: grants.InWorktree(cwd, a.Repo, a.Name), Cwd: cwd, Proxy: d.grantProxyFor(o.Container, a.Name)}
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
	if deny := d.leadGuard(o, me, p.Tool); deny != nil {
		return deny, nil
	}
	if p.Tool == askQuestionTool && o.Container != "" {
		// One question at a time to the user (#95).
		if why := d.askGuard(me); why != "" {
			return map[string]any{"verdict": grants.Deny, "reason": "lea: " + why}, nil
		}
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
	if dec.Verdict == "" {
		// A push no grant rewrote runs as the agent wrote it: its bare branch must not be a tag.
		if why := grants.RawPush(p.Tool, p.Input, c); why != "" {
			dec = grants.Decision{Verdict: grants.Deny, Reason: "lea hard deny: " + why}
		}
	}
	if dec.Verdict == grants.Allow {
		if err := d.st.AddGrantUse(store.GrantUse{Grant: dec.Grant, Agent: me, Action: dec.Request.Action,
			Target: capText(dec.Request.Target, 200), At: now}); err != nil {
			// No audit row, no allow.
			log.Printf("lea daemon: audit grant #%d: %v", dec.Grant, err)
			return map[string]any{"verdict": ""}, nil
		}
		d.changed()
	}
	return map[string]any{"verdict": dec.Verdict, "reason": dec.Reason, "grant": dec.Grant, "command": dec.Command, "hint": dec.Hint}, nil
}
