package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/DavidGodefroid/lea/internal/agents"
	"github.com/DavidGodefroid/lea/internal/roles"
	"github.com/DavidGodefroid/lea/internal/wire"
)

// Agents is the deck side of the daemon (package agents): tmux sessions and the agents lea
// spawned. Its methods are host-only: no container may spawn, show, stop or watch agents.
type Agents interface {
	Ensure(ctx context.Context, container, workdir string) error
	New(ctx context.Context, r agents.NewRequest) (agents.Row, error)
	Focus(ctx context.Context, key string, focus bool) error
	Stop(ctx context.Context, key string) error
	// RemoveEnded removes an agent only while it is ended (the deck's x on a done row).
	RemoveEnded(ctx context.Context, key string) error
	Detach(ctx context.Context, container string) error
	Watch(ctx context.Context, container string, since int64, timeout time.Duration) (agents.Snapshot, error)
	// MarkStopped records that the container is about to stop (lea stop): its agents become
	// stopped, or are forgotten with forget. ResumeStopped starts them again in the container the
	// launcher has just started.
	MarkStopped(ctx context.Context, container string, forget bool) error
	ResumeStopped(ctx context.Context, container string) error
	// SweepStacks removes, in a container the launcher has just started, what the agents that
	// are gone left in its nested Podman, and says what it removed and what no agent started.
	SweepStacks(ctx context.Context, container string) (string, error)
	// ParkStack stops, without removing them, the containers an idle agent started (#132), and
	// says what it freed.
	ParkStack(ctx context.Context, key string) (string, error)
	// UnparkStack starts again what ParkStack stopped (a new turn came meanwhile).
	UnparkStack(ctx context.Context, key string) (string, error)
	// SoftStop stops a suspended agent whose keepalive ended, keeping its session;
	// ResumeSuspended starts it again with prompt as its next turn (#161).
	SoftStop(ctx context.Context, key string) error
	ResumeSuspended(ctx context.Context, key, prompt string) error
	Attached(ctx context.Context, container string) (bool, error)
	Popup(ctx context.Context, key string, compose bool) error
	// SetLabel renames an agent for display (the deck's e); OpenShell opens a shell in the
	// container (the deck's s). Both are the user's only.
	SetLabel(ctx context.Context, key, label string) error
	// SetPaused freezes an agent (the deck's p) or lets it continue; the user's only.
	SetPaused(ctx context.Context, key string, paused bool) error
	OpenShell(ctx context.Context, container string) (agents.Row, error)
	// SplitShell opens a shell beside an agent (the deck's split), below it or at its right.
	SplitShell(ctx context.Context, key string, below bool) (agents.Row, error)
	// LockOpen, LockClose and LockList manage the lock consoles of a deck (lea lock, the deck's
	// L): the user's only. No agent may open, show, close or list a console.
	LockOpen(ctx context.Context, container, tool, profile string) (string, error)
	LockClose(ctx context.Context, container, tool, profile string) (string, error)
	LockList(ctx context.Context, container string) ([]agents.LockRow, error)
	// LockCheckRequest checks an agent's request that the user open a console (lock_request):
	// it opens nothing and says nothing of the consoles that run.
	LockCheckRequest(ctx context.Context, container, tool, profile string) error
	Changed()
}

// deckMethods are host-only: dispatchDeck refuses them on a container's bus socket, which is
// all an agent (its MCP tools, lea inside the container) can reach.
var deckMethods = map[string]bool{
	"deck_ensure": true, "agent_new": true, "agent_focus": true,
	"agent_stop": true, "deck_detach": true, "agents_watch": true, "agents_mark_stopped": true,
	"agents_resume_stopped": true, "agents_sweep_stacks": true,
	"deck_attached": true, "deck_popup": true, "agent_label": true, "shell_open": true,
	"shell_split": true, "agent_pause": true,
	"lock_open": true, "lock_close": true, "lock_list": true, "lock_request_decide": true,
}

func (d *Daemon) SetAgents(a Agents) {
	d.mu.Lock()
	d.agents = a
	d.mu.Unlock()
}

// changed tells watching decks that agent facts changed.
func (d *Daemon) changed() {
	d.mu.Lock()
	a := d.agents
	d.mu.Unlock()
	if a != nil {
		a.Changed()
	}
}

func (d *Daemon) dispatchDeck(ctx context.Context, o Origin, req wire.Request) (any, error) {
	if o.Container != "" {
		return nil, fmt.Errorf("%s: host only", req.Method)
	}
	d.mu.Lock()
	a := d.agents
	d.mu.Unlock()
	if a == nil {
		return nil, errors.New("agents are not available in this daemon")
	}
	ok := struct{}{}
	switch req.Method {
	case "deck_ensure":
		var p struct{ Container, Workdir string }
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		return ok, a.Ensure(ctx, p.Container, p.Workdir)
	case "agent_new":
		var p struct {
			Container, Workdir, Profile, Name string
			Model                             string
			Args                              []string
			Focus                             bool
			Role, Repo, Branch, Brief         string
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		if p.Role == "" {
			p.Role = roles.Default
		}
		if _, err := roles.Get(p.Role); err != nil {
			return nil, err
		}
		if err := checkBrief(p.Brief, false); err != nil {
			return nil, err
		}
		// Host-only method: the brief is the user's own, whatever the request says.
		from := ""
		if p.Brief != "" {
			from = "user"
		}
		return a.New(ctx, agents.NewRequest{Container: p.Container, Workdir: p.Workdir, Profile: p.Profile, Model: p.Model,
			Name: p.Name, Args: p.Args, Focus: p.Focus, Role: p.Role, Repo: p.Repo, Branch: p.Branch,
			Brief: p.Brief, BriefFrom: from})
	case "agent_focus":
		var p struct {
			Key   string
			Focus bool
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		return ok, a.Focus(ctx, p.Key, p.Focus)
	case "agent_stop":
		var p struct {
			Key   string
			Ended bool // only while the agent is ended: x without y on a done row
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		stop := a.Stop
		if p.Ended {
			stop = a.RemoveEnded
		}
		err := stop(ctx, p.Key)
		// A stopped agent's last turn does not ring.
		d.cancelDone(p.Key)
		d.pruneForgotten()
		return ok, err
	case "agents_mark_stopped":
		var p struct {
			Container string
			Forget    bool
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		err := a.MarkStopped(ctx, p.Container, p.Forget)
		d.pruneForgotten()
		return ok, err
	case "agents_resume_stopped":
		var p struct{ Container string }
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		err := a.ResumeStopped(ctx, p.Container)
		d.pruneForgotten()
		return ok, err
	case "agents_sweep_stacks":
		var p struct{ Container string }
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		n, err := a.SweepStacks(ctx, p.Container)
		if err != nil {
			return map[string]string{"notice": n}, err
		}
		// What agents idle for long still run (a restarted daemon lost its timers) stops too.
		lines := []string{}
		if n != "" {
			lines = append(lines, n)
		}
		lines = append(lines, d.parkIdleSince(p.Container)...)
		d.suspendedSince(p.Container)
		return map[string]string{"notice": strings.Join(lines, "\n")}, nil
	case "deck_attached":
		var p struct{ Container string }
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		att, err := a.Attached(ctx, p.Container)
		return map[string]bool{"attached": att}, err
	case "deck_detach":
		var p struct{ Container string }
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		return ok, a.Detach(ctx, p.Container)
	case "deck_popup":
		var p struct {
			Key     string
			Compose bool
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		return ok, a.Popup(ctx, p.Key, p.Compose)
	case "agent_label":
		var p struct{ Key, Label string }
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		return ok, a.SetLabel(ctx, p.Key, p.Label)
	case "agent_pause":
		var p struct {
			Key    string
			Paused bool
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		return ok, a.SetPaused(ctx, p.Key, p.Paused)
	case "shell_open":
		var p struct{ Container string }
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		return a.OpenShell(ctx, p.Container)
	case "shell_split":
		var p struct {
			Key   string
			Below bool
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		return a.SplitShell(ctx, p.Key, p.Below)
	case "lock_open", "lock_close":
		var p struct{ Container, Tool, Profile string }
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		do := a.LockOpen
		if req.Method == "lock_close" {
			do = a.LockClose
		}
		st, err := do(ctx, p.Container, p.Tool, p.Profile)
		return map[string]string{"status": st}, err
	case "lock_request_decide":
		return d.lockRequestDecide(ctx, a, req.Params)
	case "lock_list":
		var p struct{ Container string }
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		return a.LockList(ctx, p.Container)
	case "agents_watch":
		var p struct {
			Container string
			Since     int64
			TimeoutS  int `json:"timeout_s"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		if p.TimeoutS < 1 || p.TimeoutS > 60 {
			return nil, errors.New("timeout_s must be between 1 and 60")
		}
		snap, err := a.Watch(ctx, p.Container, p.Since, time.Duration(p.TimeoutS)*time.Second)
		// The deck shows the user's sound switch beside the agents.
		snap.Sound = d.soundState()
		snap.Locks = d.lockViews(p.Container)
		snap.Pressure = d.deckPressure(p.Container)
		return snap, err
	}
	return nil, fmt.Errorf("unknown method %q", req.Method)
}

// deckAttached reports whether someone can answer an approval in the container's deck. Without
// an agent manager (phase 0 daemons, tests) the host CLI answers, as before.
func (d *Daemon) deckAttached(ctx context.Context, container string) bool {
	d.mu.Lock()
	a := d.agents
	d.mu.Unlock()
	if a == nil {
		return true
	}
	ok, err := a.Attached(ctx, container)
	if err != nil {
		log.Printf("lea daemon: clients of %s: %v", container, err)
		return false
	}
	return ok
}
