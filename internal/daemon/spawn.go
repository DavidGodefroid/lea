package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/DavidGodefroid/lea/internal/agents"
	"github.com/DavidGodefroid/lea/internal/bus"
	"github.com/DavidGodefroid/lea/internal/roles"
	"github.com/DavidGodefroid/lea/internal/store"
)

// checkBrief applies the message rules to a brief (valid UTF-8, no control character but newline
// and tab) with its own cap. A required brief may not be empty.
func checkBrief(b string, required bool) error {
	if b == "" {
		if required {
			return errors.New("brief is empty: say what the new agent must do")
		}
		return nil
	}
	if len(b) > agents.MaxBrief {
		return fmt.Errorf("brief is %d bytes, limit %d", len(b), agents.MaxBrief)
	}
	if err := bus.CheckText(b); err != nil {
		return fmt.Errorf("brief %w", err)
	}
	return nil
}

// manager is the agent manager; nil in a daemon without one (phase 0, tests).
func (d *Daemon) manager() Agents {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.agents
}

// shortName is the agent name of a key (container/name).
func shortName(key string) string {
	if i := strings.LastIndexByte(key, '/'); i >= 0 {
		return key[i+1:]
	}
	return key
}

// spawnedCaller is the record of the calling agent when lea started it and it still runs: only
// such agents use the agent-tree methods. The host API, a name only a hook registered and an
// ended agent are refused. Within a container the name is what LEA_AGENT claims (spec §7.2).
func (d *Daemon) spawnedCaller(o Origin, me, method string) (store.Agent, error) {
	if o.Container == "" {
		return store.Agent{}, fmt.Errorf("%s: agents only", method)
	}
	a, err := d.st.AgentByKey(me)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.Agent{}, err
	}
	if err != nil || a.Profile == "" || a.State == "done" {
		return store.Agent{}, fmt.Errorf("%s: only running agents lea started may call it", method)
	}
	return a, nil
}

type spawnParams struct {
	Role    string `json:"role"`
	Profile string `json:"profile"`
	Model   string `json:"model"`
	Repo    string `json:"repo"`
	Branch  string `json:"branch"`
	Brief   string `json:"brief"`
	For     string `json:"for"`
	// Queue: under pressure, start the agent once it falls instead of refusing it (#132).
	Queue bool `json:"queue"`
}

// Caps on what an agent names before the daemon uses or echoes it in an error (the manager
// checks the values themselves).
const (
	maxRoleLen    = 32
	maxProfileLen = 32
	maxRepoLen    = 100
	maxBranchLen  = 200
	maxForLen     = 2*64 + 1
)

// checkLens refuses an overlong role, profile, repo or branch without echoing it.
func checkLens(role, profile, repo, branch string) error {
	switch {
	case len(role) > maxRoleLen:
		return fmt.Errorf("role is %d bytes, limit %d", len(role), maxRoleLen)
	case len(profile) > maxProfileLen:
		return fmt.Errorf("profile is %d bytes, limit %d", len(profile), maxProfileLen)
	case len(repo) > maxRepoLen:
		return fmt.Errorf("repo is %d bytes, limit %d", len(repo), maxRepoLen)
	case len(branch) > maxBranchLen:
		return fmt.Errorf("branch is %d bytes, limit %d", len(branch), maxBranchLen)
	}
	return nil
}

// agentsSpawn starts a child of the calling lead (spec §5.2, §6.1), or, with for, the agent a spawn
// request asked for. The daemon checks the role it may give, the brief, the request and the rate;
// the manager checks the profile, repo, branch and the caps, runs tmux and Docker, and parks the
// new agent: it never takes the user's slot or keyboard.
func (d *Daemon) agentsSpawn(ctx context.Context, o Origin, me string, raw json.RawMessage) (any, error) {
	caller, err := d.spawnedCaller(o, me, "agents_spawn")
	if err != nil {
		return nil, err
	}
	var p spawnParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if err := checkLens(p.Role, p.Profile, p.Repo, p.Branch); err != nil {
		return nil, err
	}
	if p.Role == "" {
		p.Role = roles.Default
	}
	cr, err := roles.Get(caller.Role)
	if err != nil {
		return nil, err
	}
	if err := roles.CanGive(cr, p.Role); err != nil {
		return nil, err
	}
	if err := d.checkProvider(p.Profile, p.Model); err != nil {
		return nil, err
	}
	if err := checkBrief(p.Brief, true); err != nil {
		return nil, err
	}
	a := d.manager()
	if a == nil {
		return nil, errors.New("agents are not available in this daemon")
	}
	var req *store.SpawnRequest
	if p.For != "" {
		r, err := d.findRequest(p.For, me, o.Container)
		if err != nil {
			return nil, err
		}
		req = &r
	}
	d.mu.Lock()
	admit := d.admitLocked(o.Container)
	d.mu.Unlock()
	if admit != nil && !p.Queue {
		return nil, fmt.Errorf("agents_spawn: %w", admit)
	}
	if err := d.rates.allow("agents_spawn", me, d.now()); err != nil {
		return nil, err
	}
	if req != nil && p.Profile == "" && p.Model == "" {
		// The provider the requester asked for, unless the lead names one.
		p.Profile, p.Model = req.Profile, req.Model
	}
	nr := agents.NewRequest{Container: o.Container, Profile: p.Profile, Model: p.Model, Role: p.Role, Parent: me,
		Repo: p.Repo, Branch: p.Branch, Brief: p.Brief, BriefFrom: me, NamePrefix: p.Role, Hidden: true}
	if admit != nil {
		id, err := d.queueSpawn(me, nr, req)
		if err != nil {
			return nil, err
		}
		d.changed()
		return map[string]any{"queued": id, "reason": admit.Error(),
			"note": fmt.Sprintf("queued: it starts once the pressure falls (you get a message), or is dropped after %s", queuedSpawnTTL)}, nil
	}
	row, err := d.startSpawn(ctx, a, me, nr, req)
	if err != nil {
		return nil, err
	}
	d.changed()
	return map[string]string{"name": row.Name, "key": row.Key, "role": row.Role, "provider": row.Profile, "model": row.Model}, nil
}

// startSpawn starts the agent nr asks for, accepting the spawn request req (nil: none) on lead's
// behalf.
func (d *Daemon) startSpawn(ctx context.Context, a Agents, lead string, nr agents.NewRequest, req *store.SpawnRequest) (agents.Row, error) {
	if req != nil {
		if err := d.st.MoveSpawnRequest(req.ID, "pending", "accepting", "", d.now()); err != nil {
			return agents.Row{}, fmt.Errorf("spawn request #%d: %w", req.ID, err)
		}
		nr.BriefReplyTo = req.Message
	}
	row, err := a.New(ctx, nr)
	if err != nil {
		if req != nil {
			d.reopenRequest(req.ID)
		}
		return agents.Row{}, err
	}
	if req != nil {
		d.acceptRequest(*req, lead, nr.Role, row)
	}
	return row, nil
}

// agentsStop stops one of the calling lead's own children (spec §5.2: several leads may run; each
// stops only its own children).
func (d *Daemon) agentsStop(ctx context.Context, o Origin, me string, raw json.RawMessage) (any, error) {
	caller, err := d.spawnedCaller(o, me, "agents_stop")
	if err != nil {
		return nil, err
	}
	var p struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if cr, err := roles.Get(caller.Role); err != nil || !cr.CanSpawn {
		return nil, fmt.Errorf("agents_stop: a %s stops no agent (only a lead stops its own children)", caller.Role)
	}
	if !validTarget(p.Name) {
		return nil, errors.New("agents_stop: invalid agent name (a name or container/name, at most 64 characters each)")
	}
	key, err := d.resolveSpawned(p.Name, o.Container)
	if err != nil {
		return nil, err
	}
	t, err := d.st.AgentByKey(key)
	if err != nil {
		return nil, err
	}
	if t.Parent != me {
		return nil, fmt.Errorf("agents_stop: %s is not one of your children", t.Name)
	}
	a := d.manager()
	if a == nil {
		return nil, errors.New("agents are not available in this daemon")
	}
	if err := d.rates.allow("agents_stop", me, d.now()); err != nil {
		return nil, err
	}
	if err := a.Stop(ctx, key); err != nil {
		return nil, err
	}
	d.cancelDone(key)
	d.pruneForgotten()
	d.changed()
	return map[string]string{"stopped": t.Name}, nil
}

// validTarget is an agent name or a container/name key as an agent may name another: the name is
// never echoed back or looked up when it breaks the name rules.
func validTarget(s string) bool {
	if c, n, ok := strings.Cut(s, "/"); ok {
		return agentName.MatchString(c) && agentName.MatchString(n)
	}
	return agentName.MatchString(s)
}
