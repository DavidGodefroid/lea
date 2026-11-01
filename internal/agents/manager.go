// Package agents runs the agents lea spawns. Each lea container has one session on lea's tmux
// server, named like the container: a "deck" window with the deck pane on the left and a slot on
// the right, and one parking window per agent whose pane runs `docker exec -it <container>
// abduco ...`. Showing an agent swaps its pane into the slot. Panes are found by their tmux user
// options (@lea_role, @lea_agent, @lea_shell), so a restarted daemon finds them again. A shell
// (the deck's s) is a pane like an agent's, running an interactive shell in the container under
// abduco; it is not an agent: no store row, no LEA_AGENT, no MCP, no hooks. This package creates,
// swaps, respawns and kills panes; it never types into one.
package agents

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DavidGodefroid/lea/internal/bus"
	"github.com/DavidGodefroid/lea/internal/providers"
	"github.com/DavidGodefroid/lea/internal/roles"
	"github.com/DavidGodefroid/lea/internal/store"
	"github.com/DavidGodefroid/lea/internal/tmux"
)

const (
	// DeckWidth is the deck pane's width, its border included (spec §4.1: 32 columns inside).
	DeckWidth = tmux.DeckWidth
	// DefaultProfile is the provider of an agent started without one (internal/providers).
	DefaultProfile = providers.Default
	MaxArgs        = 64
	MaxArgBytes    = 8 << 10

	optRole    = "@lea_role"
	optAgent   = "@lea_agent"
	optWorkdir = "@lea_workdir"
	optShell   = "@lea_shell"
	// optSplit marks a shell opened beside an agent (the deck's split): "<agent>:below" or
	// "<agent>:right". The shell shows with that agent and parks when another one is shown.
	optSplit = "@lea_split"
	// optDeck marks the deck window (a window option): the conf's resize hook and the layout
	// repair find it by this mark, never by its name.
	optDeck    = "@lea_deck"
	roleDeck   = "deck"
	rolePlace  = "placeholder"
	paneFormat = "#{pane_id}|#{window_id}|#{pane_dead}|#{@lea_role}|#{@lea_agent}|#{pane_width}|#{pane_at_left}|#{@lea_shell}|#{@lea_deck}|#{@lea_split}|#{@lea_lock}"
	paneFields = 11
	// subscription reports pane deaths; panesSubscription reports a pane that closed without
	// dying (a shell's: remain-on-exit is off for it), which no %window-close announces when
	// the pane was in the slot.
	subscription      = "lea-dead:%*:#{pane_dead}"
	panesSubscription = "lea-panes:@*:#{window_panes}"

	// KindShell marks a snapshot row that is a shell, not an agent.
	KindShell = "shell"
	// MaxShells caps the shells of one container.
	MaxShells   = 4
	shellPrefix = "shell:"
)

var (
	nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,23}$`)
	// reservedNames are the roles of lea's own panes: no agent takes one as its name.
	reservedNames = map[string]bool{roleDeck: true, rolePlace: true}
	shellRe       = regexp.MustCompile(`^sh-[1-9][0-9]{0,2}$`)
	containerRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	// sessionRe is a Claude session id, as ResumeStopped passes it to --resume.
	sessionRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	// debounce groups the notifications of one change into one reconcile.
	debounce = 200 * time.Millisecond
)

// Session is one control-mode connection to a tmux session (*tmux.Conn).
type Session interface {
	Run(ctx context.Context, args ...string) ([]string, error)
	Events() <-chan tmux.Event
	Close() error
}

// Terminal is lea's tmux server.
type Terminal interface {
	// Open makes sure the session exists (created with deckCmd in its "deck" window) and
	// returns a control-mode connection to it, and whether it created the session.
	Open(ctx context.Context, session string, deckCmd []string) (Session, bool, error)
	Sessions(ctx context.Context) ([]string, error)
	// Popup opens a popup running argv over one of the server's clients.
	Popup(ctx context.Context, client string, argv []string) error
}

// Containers reaches the agents inside a lea container (lea-agent, through docker exec).
type Containers interface {
	Live(ctx context.Context, container string) ([]string, error)
	Stop(ctx context.Context, container, name string) error
	// Projects is the projects directory of the container (where repos live).
	Projects(ctx context.Context, container string) (string, error)
	// Shells lists the shells running in the container; StopShell ends one.
	Shells(ctx context.Context, container string) ([]string, error)
	StopShell(ctx context.Context, container, name string) error
	// Pause freezes an agent's processes (paused) or lets them continue where they stopped.
	Pause(ctx context.Context, container, name string, paused bool) error
	// Teardown removes what an ended agent started in the container's nested Podman (its pods
	// and containers) and its own Podman service. Sweep removes those of every agent but keep
	// and reports what it removed and what no agent started ("" when there is nothing to say).
	Teardown(ctx context.Context, container, name string) error
	// Park stops, without removing them, the containers an idle agent started (#132) and says
	// what it freed ("" when nothing of its own ran).
	Park(ctx context.Context, container, name string) (string, error)
	// Unpark starts again what the agent's last Park stopped.
	Unpark(ctx context.Context, container, name string) (string, error)
	Sweep(ctx context.Context, container string, keep []string) (string, error)
}

type Config struct {
	Store      *store.Store
	Term       Terminal
	Containers Containers
	LeaBin     string // host path of the lea binary: the deck and placeholder panes run it
	Docker     string // docker binary for agent panes; "docker" when empty
	UID, GID   int
	Now        func() int64
	// Approvals is where agents' permission requests are answered: "deck" (default) or "pane"
	// (the agent's own dialog only). ApprovalTimeout (seconds, 1-600; 0: the hook's default) is
	// how long the deck has before the agent's dialog takes over.
	Approvals       string
	ApprovalTimeout int
	// Providers is the registry of the providers an agent may be started on, read at each spawn
	// and restart (the user's providers file may change); nil: the built-in ones.
	// DefaultProvider and DefaultModel (LEA_PROVIDER, LEA_MODEL) apply to an agent started
	// without a provider.
	Providers                     func() (*providers.Registry, error)
	DefaultProvider, DefaultModel string
	// LockSpec reads a lock tool's console configuration from the host's lea config, at each
	// open; Consoles reaches the console containers. Without them no console opens.
	LockSpec func(tool string) (LockSpec, error)
	Consoles Consoles
	// ProxyToken is an agent's LockHTTP proxy password (LEA_LOCKHTTP=1), handed to lea-agent
	// run; nil: LockHTTP is off and the agent keeps its container's proxy.
	ProxyToken func(container, agent string) string
	// RequesterToken is the agent's own token (LEA_AGENT_TOKEN, #160): a Lock's MCP server sends
	// it as the requester of a plan, and the Lock runs a ticket only for the token that asked.
	// Not a security boundary: the agents of a container share one UID. nil: no token.
	RequesterToken func(container, agent, incarnation string) string
}

type Row struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	// Kind is KindShell for a shell, "" for an agent. Label is the agent's display name, set by
	// the user ("" when none).
	Kind    string `json:"kind,omitempty"`
	Label   string `json:"label,omitempty"`
	Profile string `json:"profile"`
	// Model is the model the agent was started with, "" for its CLI's default.
	Model   string `json:"model,omitempty"`
	State   string `json:"state"`
	Workdir string `json:"workdir"`
	Updated int64  `json:"updated"`
	// Created is when the agent was started (unix seconds): the deck lists agents in that order.
	Created int64 `json:"created,omitempty"`
	// Role, Parent (the key of the lead that started it, "" for the user), Repo and Branch are
	// what the agent was started as and on.
	Role   string `json:"role"`
	Parent string `json:"parent"`
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	// Status is the agent's own report on its task (task_update).
	Status string `json:"status"`
	// Unread counts the messages waiting for the agent; ForUser the unread ones it sent the user.
	Unread  int `json:"unread"`
	ForUser int `json:"for_user"`
	// CtxPct and CostUSD come from the agent's status line; -1 is unknown.
	CtxPct   int       `json:"ctx_pct"`
	CostUSD  float64   `json:"cost_usd"`
	Approval *Approval `json:"approval,omitempty"`
	// LockAsk: the agent asks the user to open a lock tool's console (lock_request).
	LockAsk *LockAsk `json:"lock_ask,omitempty"`
	// Paused: the user froze the agent (the deck's p); State is what it was doing.
	Paused bool `json:"paused,omitempty"`
	// Subagents counts the subagents the agent still runs (its Subagent hooks): an idle agent
	// with some is still at work.
	Subagents int `json:"subagents,omitempty"`
	// Waiting and BlockedOn: the agent's open task has a next step, or something blocking it
	// (task_update next, blocked_on). Display only: the deck spins or turns red; never the text.
	Waiting   bool `json:"waiting,omitempty"`
	BlockedOn bool `json:"blocked_on,omitempty"`
	// LimitUntil: the agent stopped on its provider's usage limit (store.Agent.LimitUntil): lea
	// resumes it at that time (unix seconds), or -1: it waits for the user.
	LimitUntil int64 `json:"limit_until,omitempty"`
	// Hub: the hub's pending tickets for this agent (#158), oldest first, set by the daemon.
	Hub []HubCard `json:"hub,omitempty"`
}

type Snapshot struct {
	Version int64  `json:"version"`
	Shown   string `json:"shown"`
	Agents  []Row  `json:"agents"`
	// Sound is the daemon's sound switch ("on", "off", "" when nothing plays), set by the daemon.
	Sound string `json:"sound,omitempty"`
	// Locks is each Lock's approvals state for the container (#153), set by the daemon; none
	// when lea knows of no Lock's approvals.
	Locks []LockView `json:"locks,omitempty"`
	// Pressure is the container's pressure gauge (#132, P2), set by the daemon; nil without a
	// recent sample.
	Pressure *PressureView `json:"pressure,omitempty"`
	// HubState is "down" when lea follows the hub (LEA_HUB=1) but cannot reach its facade, so no
	// agent is suspended on a Lock call; set by the daemon.
	HubState string `json:"hub_state,omitempty"`
	// LocksAdded are the locks lox up runs that the container does not mount (added while it
	// runs): restart lea to use them. Set by the daemon.
	LocksAdded []string `json:"locks_added,omitempty"`
}

// PressureView is what the deck shows of the pressure: a short gauge of the container and the
// worse level of the container and the host (ok, warn, over), with the thresholds passed.
type PressureView struct {
	Short string `json:"short"`
	Level string `json:"level"`
	Over  string `json:"over,omitempty"`
}

// LockView is what the deck shows of one Lock's approvals (D6, #153): its name, whether its
// console runs ("up", "down", "" when unknown) and how many of the container's requests wait
// there. A notification only: requests are approved in the Lock's console (or the hub's facade).
type LockView struct {
	Lock    string `json:"lock"`
	Console string `json:"console"`
	Pending int    `json:"pending"`
}

// HubCard is one ticket of the Lock Family hub's queue (#158) the deck shows on the agent it is
// for: the Lock, its instance, the risk, the production flags and the Lock's masked screen, all
// cleaned by the daemon. Verified: the agent's own wait names the ticket; otherwise the ticket's
// requester (which the agent wrote) claims the agent. Retype is what the user types to approve a
// production ticket: its instance name (set on every production ticket; "" otherwise). Expires is
// unix seconds (0 never).
type HubCard struct {
	ID         string   `json:"id"`
	Instance   string   `json:"instance"`
	Lock       string   `json:"lock"`
	Risk       string   `json:"risk"`
	Production bool     `json:"production,omitempty"`
	Retype     string   `json:"retype,omitempty"`
	Verified   bool     `json:"verified,omitempty"`
	Header     string   `json:"header,omitempty"`
	Note       string   `json:"note,omitempty"`
	Body       []string `json:"body,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
	Expires    int64    `json:"expires,omitempty"`
	// Claimed is the agent name the ticket's requester gives (cleaned); Truncated: lea had to cut
	// the Lock's screen, so the deck cannot show all of it and sends the user to lox approve.
	Claimed   string `json:"claimed,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	// Expired: the ticket expired at the hub (its queue's expired list): the deck offers to ask
	// the Lock again (r), never to decide it. Renews: this ticket asks again for an expired one.
	Expired bool        `json:"expired,omitempty"`
	Renews  *HubRenewal `json:"renews,omitempty"`
}

// HubRenewal is what a renewal ticket says of the expired ticket it asks again for (lockkit's
// ticket.Renewal, cleaned): its id, why it expired (expired, approval-expired), whether the Lock
// compared the plan's inputs (Checked) and what changed since (input: changed, new or gone).
type HubRenewal struct {
	Of      string      `json:"of"`
	Reason  string      `json:"reason,omitempty"`
	Checked bool        `json:"checked,omitempty"`
	Changes []HubChange `json:"changes,omitempty"`
}

// HubChange is one input of a renewal's plan and how it changed.
type HubChange struct {
	Input  string `json:"input"`
	Change string `json:"change"`
}

type NewRequest struct {
	Container, Workdir, Profile, Name string
	// Model is the model to ask the provider for ("" for the default).
	Model string
	Args  []string
	Focus bool
	// Role (roles.Default when empty), Parent (the spawning lead's key), Repo (a directory of the
	// projects directory: the agent starts there) and Branch are recorded on the agent.
	Role, Parent, Repo, Branch string
	// Brief, when set, is posted to the new agent as a request from BriefFrom ("user" when empty),
	// replying to BriefReplyTo, before the agent starts; its first prompt sends it there.
	Brief, BriefFrom string
	BriefReplyTo     int64
	// NamePrefix names an unnamed agent <prefix>-N (the profile when empty).
	NamePrefix string
	// Hidden leaves the agent in its parking window: an agent another agent started never takes
	// the slot or the keyboard.
	Hidden bool
}

type Manager struct {
	cfg   Config
	mu    sync.Mutex // serialises every tmux and container operation
	conns map[string]Session
	smu   sync.Mutex
	shown map[string]string // container → key of the agent (or shell) in the slot
	// shells lists, per container, the shells that have a pane, by name (under smu).
	shells map[string][]string
	// fresh marks a session Open created and no reconcile has laid out yet (under mu).
	fresh map[string]bool
	// recheck marks a new connection whose first reconcile asks the container for its shells: a
	// shell's pane may have closed while no connection (or no daemon) watched (under mu).
	recheck map[string]bool
	feed    feed
	// backoff spaces out the respawns of a pane that keeps dying (by pane id; under mu).
	backoff map[string]*backoff
	// tearing holds, per agent key, the teardown of its stack in flight (closed when it is over;
	// under tmu). A new agent of that name waits for it.
	tmu      sync.Mutex
	tearing  map[string]chan struct{}
	clock    func() time.Time
	schedule func(time.Duration, func())
	// later runs the steps of a redraw (redrawAfterReattach).
	later func(time.Duration, func())
}

func NewManager(cfg Config) *Manager {
	if cfg.Docker == "" {
		cfg.Docker = "docker"
	}
	if cfg.Approvals != "pane" {
		cfg.Approvals = "deck"
	}
	if cfg.Now == nil {
		cfg.Now = func() int64 { return time.Now().Unix() }
	}
	m := &Manager{cfg: cfg, conns: map[string]Session{}, shown: map[string]string{}, fresh: map[string]bool{},
		shells: map[string][]string{}, recheck: map[string]bool{},
		backoff: map[string]*backoff{}, tearing: map[string]chan struct{}{}, clock: time.Now,
		schedule: func(d time.Duration, f func()) { time.AfterFunc(d, f) },
		later:    func(d time.Duration, f func()) { time.AfterFunc(d, f) }}
	m.feed.v = 1 // a first watch (since 0) returns at once
	return m
}

// feed is the change counter decks long-poll on.
type feed struct {
	mu sync.Mutex
	v  int64
	ch chan struct{}
}

func (f *feed) bump() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.v++
	if f.ch != nil {
		close(f.ch)
		f.ch = nil
	}
}

func (f *feed) current() (int64, <-chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ch == nil {
		f.ch = make(chan struct{})
	}
	return f.v, f.ch
}

// Changed tells watching decks that agent facts changed (the daemon calls it on hook events).
func (m *Manager) Changed() { m.feed.bump() }

// Watch returns the container's agents once the change counter differs from since, or when
// timeout passes. A different (also lower: daemon restart) counter returns at once.
func (m *Manager) Watch(ctx context.Context, container string, since int64, timeout time.Duration) (Snapshot, error) {
	t := time.NewTimer(timeout)
	defer t.Stop()
	for {
		v, ch := m.feed.current()
		if v != since {
			return m.snapshot(container, v)
		}
		select {
		case <-ch:
		case <-t.C:
			return m.snapshot(container, v)
		case <-ctx.Done():
			return Snapshot{}, ctx.Err()
		}
	}
}

func (m *Manager) snapshot(container string, v int64) (Snapshot, error) {
	as, err := m.cfg.Store.SpawnedAgents(container)
	if err != nil {
		return Snapshot{}, err
	}
	f, err := loadFacts(m.cfg.Store, container, m.cfg.Now())
	if err != nil {
		return Snapshot{}, err
	}
	rows := make([]Row, 0, len(as))
	for _, a := range as {
		rows = append(rows, f.row(rowOf(a)))
	}
	m.smu.Lock()
	shown := m.shown[container]
	shells := append([]string(nil), m.shells[container]...)
	m.smu.Unlock()
	for _, n := range shells {
		rows = append(rows, shellRow(container, n))
	}
	return Snapshot{Version: v, Shown: shown, Agents: rows}, nil
}

func rowOf(a store.Agent) Row {
	return Row{Key: a.Key, Name: a.Name, Label: a.Label, Profile: a.Profile, Model: a.Model, State: a.State, Workdir: a.Workdir, Updated: a.Updated,
		Created: a.Created, Role: a.Role, Parent: a.Parent, Repo: a.Repo, Branch: a.Branch, CtxPct: -1, CostUSD: -1, Paused: a.Paused,
		LimitUntil: a.LimitUntil}
}

func shellRow(container, name string) Row {
	return Row{Key: ShellKey(container, name), Name: name, Kind: KindShell, State: "running", CtxPct: -1, CostUSD: -1}
}

// ShellKey is the key of a shell's row: never an agent's key, whose container part has no ':'.
func ShellKey(container, name string) string { return shellPrefix + container + "/" + name }

// parseShellKey splits a shell's key; ok is false for any other key.
func parseShellKey(key string) (container, name string, ok bool) {
	rest, ok := strings.CutPrefix(key, shellPrefix)
	if !ok {
		return "", "", false
	}
	container, name, ok = strings.Cut(rest, "/")
	if !ok || !containerRe.MatchString(container) || !shellRe.MatchString(name) {
		return "", "", false
	}
	return container, name, true
}

func (m *Manager) setShown(container, key string) {
	m.smu.Lock()
	old := m.shown[container]
	m.shown[container] = key
	m.smu.Unlock()
	if old != key {
		if _, _, shell := parseShellKey(key); key != "" && !shell {
			// Kept for a rebuilt session (reconcile): "" (placeholder, lost connection) and a
			// shell are not.
			if err := m.cfg.Store.SetLastShown(container, key); err != nil {
				log.Printf("lea daemon: record the shown agent of %s: %v", container, err)
			}
		}
		m.feed.bump()
	}
}

func (m *Manager) deckCmd(container string) []string {
	return []string{m.cfg.LeaBin, "deck", "--container", container}
}

func (m *Manager) placeholderCmd() []string { return []string{m.cfg.LeaBin, "deck", "--placeholder"} }

// paneCmd is an agent's pane: docker exec into the container, then abduco, which keeps the
// agent's terminal alive when this pane, the deck or the tmux server goes away. attach joins
// the agent's existing abduco session and never starts a new agent. FORCE_HYPERLINK: Claude Code
// writes links as hyperlinks (OSC 8), which tmux passes on whole to the user's terminal (lea.tmux.conf).
func (m *Manager) paneCmd(container string, a store.Agent, args []string, attach bool) []string {
	cmd := []string{m.cfg.Docker, "exec", "-it", "-u", fmt.Sprintf("%d:%d", m.cfg.UID, m.cfg.GID),
		"-e", "TERM=xterm-256color", "-e", "COLORTERM=truecolor", "-e", "FORCE_HYPERLINK=1",
		"-e", "LEA_AGENT=" + a.Name, "-e", "LEA_APPROVALS=" + m.cfg.Approvals}
	if m.cfg.ApprovalTimeout > 0 {
		cmd = append(cmd, "-e", "LEA_APPROVAL_TIMEOUT="+strconv.Itoa(m.cfg.ApprovalTimeout))
	}
	if m.cfg.ProxyToken != nil && !attach {
		cmd = append(cmd, "-e", "LEA_PROXY_TOKEN="+m.cfg.ProxyToken(container, a.Name))
	}
	if m.cfg.RequesterToken != nil && !attach {
		cmd = append(cmd, "-e", "LEA_AGENT_TOKEN="+m.cfg.RequesterToken(container, a.Name, a.Incarnation))
	}
	cmd = append(cmd, "-w", a.Workdir, container, "abduco")
	if attach {
		return append(cmd, "-a", "ag-"+a.Name)
	}
	cmd = append(cmd, "-c", "ag-"+a.Name, "lea-agent", "run", a.Name)
	return append(cmd, args...)
}

// shellCmd is a shell's pane: docker exec into the container, then abduco (the shell outlives
// this pane like an agent does), then lea-agent's shell, which records it and runs the user's
// login shell in workdir (the container's own directory when ""). No LEA_AGENT: the shell is the
// user's, not an agent. attach joins the shell's existing abduco session.
func (m *Manager) shellCmd(container, name, workdir string, attach bool) []string {
	cmd := []string{m.cfg.Docker, "exec", "-it", "-u", fmt.Sprintf("%d:%d", m.cfg.UID, m.cfg.GID),
		"-e", "TERM=xterm-256color", "-e", "COLORTERM=truecolor", "-e", "FORCE_HYPERLINK=1"}
	if workdir != "" {
		cmd = append(cmd, "-w", workdir)
	}
	cmd = append(cmd, container, "abduco")
	if attach {
		return append(cmd, "-a", name)
	}
	return append(cmd, "-c", name, "lea-agent", "shell", name)
}

// session returns the container's control connection, opening it (and the session) when needed.
// Callers hold m.mu.
func (m *Manager) session(ctx context.Context, container string) (Session, error) {
	if !containerRe.MatchString(container) {
		return nil, fmt.Errorf("invalid container name %q", container)
	}
	if s, ok := m.conns[container]; ok {
		return s, nil
	}
	s, created, err := m.cfg.Term.Open(ctx, container, m.deckCmd(container))
	if err != nil {
		return nil, err
	}
	if created {
		// The new session's only pane runs the deck: tag it now, so the layout repair never
		// takes it for a pane that is not lea's.
		ps, err := listPanes(ctx, s, container)
		if err == nil && len(ps) == 1 && !ps[0].tagged() {
			if _, err = s.Run(ctx, "set-option", "-p", "-t", ps[0].ID, optRole, roleDeck); err == nil {
				_, err = s.Run(ctx, "set-option", "-w", "-t", ps[0].ID, optDeck, "1")
			}
		}
		if err != nil {
			s.Close()
			return nil, err
		}
		m.fresh[container] = true
	}
	for _, sub := range []string{subscription, panesSubscription} {
		if _, err := s.Run(ctx, "refresh-client", "-B", sub); err != nil {
			s.Close()
			return nil, err
		}
	}
	m.conns[container] = s
	m.recheck[container] = true
	go m.follow(container, s)
	return s, nil
}

// follow turns pane deaths and closed windows into a (debounced) reconcile, and forgets the
// connection when tmux ends it.
func (m *Manager) follow(container string, s Session) {
	var t *time.Timer
	for ev := range s.Events() {
		switch ev.Name {
		case "%subscription-changed", "%window-close", "%unlinked-window-close":
			if t == nil {
				t = time.AfterFunc(debounce, func() { m.refresh(container, s) })
			} else {
				t.Reset(debounce)
			}
		}
	}
	if t != nil {
		t.Stop()
	}
	m.mu.Lock()
	dropped := false
	if m.conns[container] == s {
		delete(m.conns, container)
		// Nothing tracks the shells now: their rows go until a new connection lists them.
		m.smu.Lock()
		dropped = len(m.shells[container]) > 0
		delete(m.shells, container)
		m.smu.Unlock()
	}
	m.mu.Unlock()
	s.Close()
	m.setShown(container, "")
	if dropped {
		m.feed.bump()
	}
}

func (m *Manager) refresh(container string, s Session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conns[container] != s {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := m.reconcile(ctx, container, s); err != nil {
		log.Printf("lea daemon: reconcile %s: %v", container, err)
	}
}

const (
	respawnMin    = time.Second
	respawnMax    = 30 * time.Second
	respawnStable = 10 * time.Second // a pane alive this long after a respawn resets its backoff
)

// backoff is the respawn history of one pane: when it was last respawned, when it was first
// seen dead since, the wait before the next respawn, and the retry already scheduled.
type backoff struct {
	last, seen, due time.Time
	delay           time.Duration
}

// respawnDue reports whether the dead pane id may be respawned now. A pane that died soon after
// its last respawn waits 1 s, then 2 s, 4 s ... up to 30 s from its death; one that lived 10 s
// starts over. When the pane must wait, a reconcile is scheduled for when it may be respawned,
// since no further tmux notification will come. Callers hold m.mu.
func (m *Manager) respawnDue(container, id string) bool {
	now := m.clock()
	b, ok := m.backoff[id]
	if !ok {
		m.backoff[id] = &backoff{last: now, delay: respawnMin}
		return true
	}
	if b.seen.IsZero() {
		b.seen = now
	}
	if b.seen.Sub(b.last) >= respawnStable {
		b.delay = respawnMin
	} else if due := b.seen.Add(b.delay); now.Before(due) {
		if !b.due.Equal(due) {
			b.due = due
			m.schedule(due.Sub(now), func() { m.retry(container) })
		}
		return false
	} else {
		b.delay = min(2*b.delay, respawnMax)
	}
	b.last, b.seen, b.due = now, time.Time{}, time.Time{}
	return true
}

// retry reconciles the container on its current connection, if it still has one.
func (m *Manager) retry(container string) {
	m.mu.Lock()
	s := m.conns[container]
	m.mu.Unlock()
	if s != nil {
		m.refresh(container, s)
	}
}

// Ensure opens (or reopens) the container's session, records workdir as the directory new
// agents start in (when given), and reconciles panes with the live agents. With a workdir (the
// launcher, before it attaches) it also gives the keyboard to the deck.
func (m *Manager) Ensure(ctx context.Context, container, workdir string) error {
	if workdir != "" {
		if err := checkWorkdir(workdir); err != nil {
			return err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.session(ctx, container)
	if err != nil {
		return err
	}
	if workdir != "" {
		if _, err := s.Run(ctx, "set-option", "-t", "="+container+":", optWorkdir, workdir); err != nil {
			return err
		}
		if err := m.unstopLive(ctx, container); err != nil {
			return err
		}
	}
	if err := m.reconcile(ctx, container, s); err != nil {
		return err
	}
	if workdir == "" {
		return nil
	}
	// lea is about to attach: the keyboard starts on the deck, whichever pane had it at the last
	// detach, so a deck key never reaches an agent.
	ps, err := listPanes(ctx, s, container)
	if err != nil {
		return err
	}
	deck, ok := byRole(ps, roleDeck)
	if !ok {
		return nil
	}
	if _, err := s.Run(ctx, "select-window", "-t", deck.ID); err != nil {
		return err
	}
	_, err = s.Run(ctx, "select-pane", "-t", deck.ID)
	return err
}

// Resume reattaches every session of lea's tmux server (daemon start).
func (m *Manager) Resume(ctx context.Context) {
	names, err := m.cfg.Term.Sessions(ctx)
	if err != nil {
		log.Printf("lea daemon: tmux sessions: %v", err)
		return
	}
	for _, n := range names {
		if !containerRe.MatchString(n) {
			continue
		}
		if err := m.Ensure(ctx, n, ""); err != nil {
			log.Printf("lea daemon: reattach %s: %v", n, err)
		}
	}
}

type pane struct {
	ID, Window  string
	Dead        bool
	Role, Agent string
	Width       int
	AtLeft      bool // touches the left edge of its window
	Shell       string
	DeckWin     bool   // its window carries the deck mark (@lea_deck)
	Split       string // a split shell's agent and side (@lea_split), "" for any other pane
	Lock        string // a lock console's id (@lea_lock), "" for any other pane
}

// splitAgent is the agent a split shell belongs to ("" for any other pane), and whether it
// sits below the agent (else at its right).
func (p pane) splitAgent() (agent string, below bool) {
	if p.Shell == "" {
		return "", false
	}
	a, side, ok := strings.Cut(p.Split, ":")
	if !ok || !nameRe.MatchString(a) {
		return "", false
	}
	return a, side == "below"
}

// companion is agent's split shell, if it has one.
func companion(ps []pane, agent string) (pane, bool) {
	return findPane(ps, func(p pane) bool { a, _ := p.splitAgent(); return agent != "" && a == agent })
}

// tagged reports whether lea made the pane: every pane of lea's carries @lea_role, @lea_agent or
// @lea_shell.
func (p pane) tagged() bool { return p.Role != "" || p.Agent != "" || p.Shell != "" }

func listPanes(ctx context.Context, s Session, container string) ([]pane, error) {
	lines, err := s.Run(ctx, "list-panes", "-s", "-t", "="+container, "-F", paneFormat)
	if err != nil {
		return nil, err
	}
	out := make([]pane, 0, len(lines))
	for _, l := range lines {
		f := strings.SplitN(l, "|", paneFields)
		if len(f) != paneFields {
			return nil, fmt.Errorf("unexpected list-panes line %q", l)
		}
		width, err := strconv.Atoi(f[5])
		if err != nil {
			return nil, fmt.Errorf("unexpected list-panes line %q", l)
		}
		out = append(out, pane{ID: f[0], Window: f[1], Dead: f[2] == "1", Role: f[3], Agent: f[4], Width: width,
			AtLeft: f[6] == "1", Shell: f[7], DeckWin: f[8] == "1", Split: f[9], Lock: f[10]})
	}
	return out, nil
}

func findPane(ps []pane, match func(pane) bool) (pane, bool) {
	for _, p := range ps {
		if match(p) {
			return p, true
		}
	}
	return pane{}, false
}

func byRole(ps []pane, role string) (pane, bool) {
	return findPane(ps, func(p pane) bool { return p.Role == role })
}

func byAgent(ps []pane, name string) (pane, bool) {
	return findPane(ps, func(p pane) bool { return p.Agent == name })
}

func byShell(ps []pane, name string) (pane, bool) {
	return findPane(ps, func(p pane) bool { return p.Shell == name })
}

// slotOf is the pane next to deck in the deck window: never a split shell beside the agent shown
// there, unless it is alone, and never the lock console under it.
func slotOf(ps []pane, deck pane) (pane, bool) {
	in := func(p pane) bool { return p.Window == deck.Window && p.ID != deck.ID && p.Role != roleLock }
	if p, ok := findPane(ps, func(p pane) bool { a, _ := p.splitAgent(); return in(p) && a == "" }); ok {
		return p, true
	}
	return findPane(ps, in)
}

func deckAndSlot(ps []pane) (deck, slot pane, ok bool) {
	deck, ok = byRole(ps, roleDeck)
	if !ok {
		return pane{}, pane{}, false
	}
	slot, ok = slotOf(ps, deck)
	return deck, slot, ok
}

// ensureLayout repairs the deck window into what lea wants: one deck pane, live, on the left,
// beside exactly one slot, in the only window marked @lea_deck; a live placeholder; and no pane
// lea did not make. It never refuses a session: a missing deck is made again, a pane without
// lea's tags (a user's split, an old server's leftover) is killed, an extra agent or placeholder
// pane in the deck window goes back to a window of its own, and a deck moved to the right or
// stacked is put back. Window names play no part: an agent's window may be named anything, deck
// included. It reports whether it changed anything and whether the deck is new (a new session,
// or a rebuilt deck), so that the last shown agent takes the slot again.
func (m *Manager) ensureLayout(ctx context.Context, container string, s Session, ps []pane) (changed, created bool, err error) {
	run := func(args ...string) ([]string, error) {
		changed = true
		return s.Run(ctx, args...)
	}
	relist := func() error {
		var err error
		ps, err = listPanes(ctx, s, container)
		return err
	}
	deck, ok := byRole(ps, roleDeck)
	if !ok {
		out, err := run(append([]string{"new-window", "-d", "-t", "=" + container + ":", "-n", roleDeck, "-P", "-F", "#{pane_id}", "--"},
			m.deckCmd(container)...)...)
		if err != nil {
			return false, false, err
		}
		if len(out) != 1 {
			return false, false, errors.New("tmux did not print the deck's pane id")
		}
		if _, err := run("set-option", "-p", "-t", out[0], optRole, roleDeck); err != nil {
			return false, false, err
		}
		created = true
		if err := relist(); err != nil {
			return false, false, err
		}
		if deck, ok = findPane(ps, func(p pane) bool { return p.ID == out[0] }); !ok {
			return false, false, errors.New("the new deck pane is gone")
		}
	}
	// Panes that are not lea's, and decks beyond the first. The deck exists by now, so the
	// session never loses its last pane.
	moved := created // the user's terminal may have been on a window that is gone
	for _, p := range ps {
		// A lock console's pane closes when the console exits; one that died before remain-on-exit
		// was off goes too, its last screen with it.
		if p.ID != deck.ID && (!p.tagged() || p.Role == roleDeck || p.Role == roleLock && p.Dead) {
			if _, err := run("kill-pane", "-t", p.ID); err != nil {
				return false, false, err
			}
			delete(m.backoff, p.ID)
			moved = true
		}
	}
	if changed {
		if err := relist(); err != nil {
			return false, false, err
		}
		deck, _ = byRole(ps, roleDeck)
	}
	// The deck window, and it alone, carries the mark the conf's resize hook follows. A window
	// that held the deck before (the deck was rebuilt elsewhere) loses it.
	unmarked := map[string]bool{}
	for _, p := range ps {
		if p.DeckWin && p.Window != deck.Window && !unmarked[p.Window] {
			if _, err := run("set-option", "-w", "-u", "-t", p.ID, optDeck); err != nil {
				return false, false, err
			}
			unmarked[p.Window] = true
		}
	}
	if !deck.DeckWin {
		if _, err := run("set-option", "-w", "-t", deck.ID, optDeck, "1"); err != nil {
			return false, false, err
		}
	}
	// The deck window keeps the deck and one slot; every other pane in it goes to a window of
	// its own named after it.
	slot, hasSlot := slotOf(ps, deck)
	parked, lockKept := false, false
	for _, p := range ps {
		if p.ID == deck.ID || p.Window != deck.Window || p.ID == slot.ID {
			continue
		}
		if a, _ := p.splitAgent(); a != "" && slot.Agent == a && !p.Dead {
			continue // the shown agent's split shell stays beside it
		}
		if p.Role == roleLock && hasSlot && !lockKept {
			lockKept = true // the lock slot holds one console, under the slot
			continue
		}
		if p.Role == roleLock {
			if _, err := run("break-pane", "-d", "-s", p.ID, "-n", lockWindow(p.Lock)); err != nil {
				return false, false, err
			}
			parked, moved = true, true
			continue
		}
		name := p.Agent
		if name == "" {
			name = p.Shell
		}
		if name == "" {
			name = p.Role
		}
		if _, err := run("break-pane", "-d", "-s", p.ID, "-n", name); err != nil {
			return false, false, err
		}
		parked, moved = true, true
	}
	if parked {
		if err := relist(); err != nil {
			return false, false, err
		}
	}
	if hasSlot {
		slot, _ = slotOf(ps, deck)
		deck, _ = byRole(ps, roleDeck)
		if slot.AtLeft { // stacked top to bottom
			if _, err := run("select-layout", "-t", deck.ID, "even-horizontal"); err != nil {
				return false, false, err
			}
			if err := relist(); err != nil {
				return false, false, err
			}
			deck, _ = byRole(ps, roleDeck)
		}
		if !deck.AtLeft {
			if _, err := run("swap-pane", "-d", "-s", deck.ID, "-t", slot.ID); err != nil {
				return false, false, err
			}
		}
	}
	if deck.Dead && m.respawnDue(container, deck.ID) {
		if _, err := run(append([]string{"respawn-pane", "-k", "-t", deck.ID, "--"}, m.deckCmd(container)...)...); err != nil {
			return false, false, err
		}
	}
	ph, hasPh := byRole(ps, rolePlace)
	switch {
	case !hasPh:
		args := []string{"new-window", "-d", "-t", "=" + container + ":", "-n", rolePlace, "-P", "-F", "#{pane_id}", "--"}
		if !hasSlot {
			args = []string{"split-window", "-h", "-d", "-t", deck.ID, "-P", "-F", "#{pane_id}", "--"}
		}
		out, err := run(append(args, m.placeholderCmd()...)...)
		if err != nil {
			return false, false, err
		}
		if len(out) != 1 {
			return false, false, errors.New("tmux did not print the placeholder's pane id")
		}
		if _, err := run("set-option", "-p", "-t", out[0], optRole, rolePlace); err != nil {
			return false, false, err
		}
	case !hasSlot:
		if _, err := run("join-pane", "-h", "-d", "-s", ph.ID, "-t", deck.ID); err != nil {
			return false, false, err
		}
	case ph.Dead && m.respawnDue(container, ph.ID):
		if _, err := run(append([]string{"respawn-pane", "-k", "-t", ph.ID, "--"}, m.placeholderCmd()...)...); err != nil {
			return false, false, err
		}
	}
	if moved {
		// A window the user's terminal was on may be gone, or the terminal may have followed a
		// pane out: bring it back to the deck (no key of lea's conf leaves a parking window).
		if _, err := run("select-window", "-t", deck.Window); err != nil {
			return false, false, err
		}
	}
	if changed {
		if _, err := s.Run(ctx, "resize-pane", "-t", deck.ID, "-x", strconv.Itoa(DeckWidth)); err != nil {
			return false, false, err
		}
	}
	return changed, created, nil
}

// reconcile brings panes in line with facts: a live agent without a pane gets one (attach); a
// live agent with a dead pane is reattached in place; an agent that is not live, with a dead
// pane or none, is done (its last screen stays until it is stopped); an agent pane whose agent is
// not registered any more is removed. A pane that is alive while its agent is not live yet is
// left alone: abduco is still starting.
func (m *Manager) reconcile(ctx context.Context, container string, s Session) error {
	ps, err := listPanes(ctx, s, container)
	if err != nil {
		return err
	}
	changed, created, err := m.ensureLayout(ctx, container, s, ps)
	if err != nil {
		return err
	}
	if m.fresh[container] {
		delete(m.fresh, container)
		created = true
	}
	if changed {
		if ps, err = listPanes(ctx, s, container); err != nil {
			return err
		}
	}
	// A failed second step of the repaint leaves the deck one column wider.
	if deck, ok := byRole(ps, roleDeck); ok && !changed && deck.Width != DeckWidth {
		if _, err := s.Run(ctx, "resize-pane", "-t", deck.ID, "-x", strconv.Itoa(DeckWidth)); err != nil {
			return err
		}
	}
	live, err := m.cfg.Containers.Live(ctx, container)
	if err != nil {
		return err
	}
	isLive := map[string]bool{}
	for _, n := range live {
		isLive[n] = true
	}
	as, err := m.cfg.Store.SpawnedAgents(container)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	stateChanged := false
	for _, a := range as {
		known[a.Name] = true
		p, has := byAgent(ps, a.Name)
		switch {
		case isLive[a.Name] && !has:
			if _, err := m.newPane(ctx, s, container, a, nil, true); err != nil {
				return err
			}
		case isLive[a.Name] && p.Dead:
			if !m.respawnDue(container, p.ID) {
				continue
			}
			if _, err := s.Run(ctx, append([]string{"respawn-pane", "-k", "-t", p.ID, "--"}, m.paneCmd(container, a, nil, true)...)...); err != nil {
				return err
			}
			id := p.ID
			m.later(redrawDelay, func() { m.redrawAfterReattach(container, id) })
		case !isLive[a.Name] && (!has || p.Dead) && a.State != "done" && a.State != stateStopped && !store.Suspended(a.State):
			// A stopped agent is not done: its container is gone, and the next one starts it again.
			// A suspended one waits for a decision or a message, which resumes it (SoftStop).
			if err := m.cfg.Store.SetState(a.Key, "done", "", m.cfg.Now()); err != nil {
				return err
			}
			stateChanged = true
			m.teardownLater(container, a.Name)
		}
	}
	var orphans []string
	for _, p := range ps {
		if p.Agent != "" && !known[p.Agent] {
			orphans = append(orphans, p.Agent)
		}
	}
	for _, name := range orphans {
		if ps, err = listPanes(ctx, s, container); err != nil {
			return err
		}
		if p, ok := byAgent(ps, name); ok {
			if err := m.removePane(ctx, s, ps, p); err != nil {
				return err
			}
		}
	}
	if ps, err = m.reconcileShells(ctx, container, s, created); err != nil {
		return err
	}
	if created {
		// A rebuilt session (tmux server killed, then lea): the agent last shown, when it still
		// has a terminal, takes the slot and the keyboard again instead of the placeholder.
		if ps, err = m.showLast(ctx, container, s, ps); err != nil {
			return err
		}
	}
	m.publish(container, ps)
	if stateChanged {
		m.feed.bump()
	}
	return nil
}

// reconcileShells removes the pane of a shell that ended (a dead pane: a shell's pane normally
// closes by itself), and gives each shell still running in the container without a pane a pane
// again (attach, parked). The container is asked only when a pane may be missing: in a rebuilt
// session, on a new connection, or when a shell lea last showed has no pane any more (abduco's
// detach key, a killed docker exec, or the shell's end). It returns the panes.
func (m *Manager) reconcileShells(ctx context.Context, container string, s Session, created bool) ([]pane, error) {
	ps, err := listPanes(ctx, s, container)
	if err != nil {
		return nil, err
	}
	// By pane id: a dead pane's name may already be a live shell's.
	var ended []string
	for _, p := range ps {
		if p.Shell != "" && p.Dead {
			ended = append(ended, p.ID)
		}
	}
	for _, id := range ended {
		if ps, err = listPanes(ctx, s, container); err != nil {
			return nil, err
		}
		if p, ok := findPane(ps, func(q pane) bool { return q.ID == id && q.Dead }); ok {
			if err := m.removePane(ctx, s, ps, p); err != nil {
				return nil, err
			}
		}
	}
	if len(ended) > 0 {
		if ps, err = listPanes(ctx, s, container); err != nil {
			return nil, err
		}
	}
	ask := created || m.recheck[container]
	delete(m.recheck, container)
	m.smu.Lock()
	for _, name := range m.shells[container] {
		if p, ok := byShell(ps, name); !ok || p.Dead {
			ask = true
		}
	}
	m.smu.Unlock()
	if ask {
		live, err := m.cfg.Containers.Shells(ctx, container)
		if err != nil { // a container of an older image: it has no shells to give back
			log.Printf("lea daemon: shells of %s: %v", container, err)
		}
		for _, name := range live {
			if _, has := byShell(ps, name); !has && shellRe.MatchString(name) {
				if _, err := m.newShellPane(ctx, s, container, name, "", true); err != nil {
					return nil, err
				}
			}
		}
	}
	return listPanes(ctx, s, container)
}

// publish records what the panes show: the agent or shell in the slot, and the container's
// shells. Watching decks hear of any change.
func (m *Manager) publish(container string, ps []pane) {
	var shells []string
	for _, p := range ps {
		if p.Shell != "" && !p.Dead {
			shells = append(shells, p.Shell)
		}
	}
	slices.SortFunc(shells, func(a, b string) int { return shellNum(a) - shellNum(b) })
	m.smu.Lock()
	changed := !slices.Equal(m.shells[container], shells)
	m.shells[container] = shells
	m.smu.Unlock()
	key := ""
	if _, slot, ok := deckAndSlot(ps); ok && slot.Agent != "" {
		key = container + "/" + slot.Agent
	} else if ok && slot.Shell != "" && !slot.Dead {
		key = ShellKey(container, slot.Shell)
	}
	m.setShown(container, key)
	if changed {
		m.feed.bump()
	}
}

// shellNum is N of a shell named sh-N.
func shellNum(name string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(name, "sh-"))
	return n
}

const (
	// redrawDelay leaves a respawned `docker exec … abduco -a` time to attach.
	redrawDelay = 2 * time.Second
	// redrawStep separates the two resizes, so each reaches the agent as its own size change.
	redrawStep = 200 * time.Millisecond
)

// redrawAfterReattach makes the agent of a reattached pane repaint. abduco reattaches at the
// pane's size; when that is the size the agent already had, the agent gets no SIGWINCH and its
// screen stays blank. When the pane is in the slot, the deck is made one column wider and, a
// moment later, its width again: two real size changes. Nothing is typed. A parked pane needs
// nothing: swapping it into the slot resizes it.
func (m *Manager) redrawAfterReattach(container, id string) {
	if !m.resizeDeckBeside(container, id, DeckWidth+1) {
		return
	}
	m.later(redrawStep, func() { m.resizeDeckBeside(container, "", DeckWidth) })
}

// resizeDeckBeside sets the deck's width when pane id (any pane when id is "") is the live
// agent in the slot, and reports whether it did.
func (m *Manager) resizeDeckBeside(container, id string, width int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.conns[container]
	if s == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ps, err := listPanes(ctx, s, container)
	if err != nil {
		return false
	}
	deck, slot, ok := deckAndSlot(ps)
	if !ok || (id != "" && (slot.ID != id || slot.Dead || slot.Agent == "")) {
		return false
	}
	if _, err := s.Run(ctx, "resize-pane", "-t", deck.ID, "-x", strconv.Itoa(width)); err != nil {
		log.Printf("lea daemon: redraw %s: %v", container, err)
		return false
	}
	return true
}

// showLast shows the container's last shown agent when it has a pane, and returns the panes.
func (m *Manager) showLast(ctx context.Context, container string, s Session, ps []pane) ([]pane, error) {
	last, err := m.cfg.Store.LastShown(container)
	if err != nil || last == "" {
		return ps, err
	}
	name, ok := strings.CutPrefix(last, container+"/")
	if !ok {
		return ps, nil
	}
	p, ok := byAgent(ps, name)
	if !ok {
		return ps, nil
	}
	if err := m.show(ctx, s, ps, p, true); err != nil {
		return ps, err
	}
	return listPanes(ctx, s, container)
}

func (m *Manager) newPane(ctx context.Context, s Session, container string, a store.Agent, args []string, attach bool) (string, error) {
	cmd := append([]string{"new-window", "-d", "-t", "=" + container + ":", "-n", a.Name, "-P", "-F", "#{pane_id}", "--"},
		m.paneCmd(container, a, args, attach)...)
	out, err := s.Run(ctx, cmd...)
	if err != nil {
		return "", err
	}
	if len(out) != 1 {
		return "", fmt.Errorf("tmux did not print the pane id of %s", a.Name)
	}
	if _, err := s.Run(ctx, "set-option", "-p", "-t", out[0], optAgent, a.Name); err != nil {
		return out[0], err
	}
	// The line its dead pane shows (tmux 3.3 and later; -q: older ones keep their own).
	if _, err := s.Run(ctx, "set-option", "-pq", "-t", out[0], "remain-on-exit-format", endedLine); err != nil {
		return out[0], err
	}
	return out[0], nil
}

// endedLine is what an ended agent's pane shows: its last screen stays until x removes it.
const endedLine = "agent ended, x in the deck removes it"

// newShellPane starts a shell's pane in a window of its own: tagged, and closing when its
// process exits (remain-on-exit off), so an ended shell never leaves a dead pane.
func (m *Manager) newShellPane(ctx context.Context, s Session, container, name, workdir string, attach bool) (string, error) {
	cmd := append([]string{"new-window", "-d", "-t", "=" + container + ":", "-n", name, "-P", "-F", "#{pane_id}", "--"},
		m.shellCmd(container, name, workdir, attach)...)
	out, err := s.Run(ctx, cmd...)
	if err != nil {
		return "", err
	}
	if len(out) != 1 {
		return "", fmt.Errorf("tmux did not print the pane id of %s", name)
	}
	if _, err := s.Run(ctx, "set-option", "-p", "-t", out[0], optShell, name); err != nil {
		return out[0], err
	}
	if _, err := s.Run(ctx, "set-option", "-p", "-t", out[0], "remain-on-exit", "off"); err != nil {
		return out[0], err
	}
	return out[0], nil
}

// OpenShell opens a shell in the container (the deck's s), in the directory new agents start
// in, and shows it with the keyboard. A container runs at most MaxShells shells, counting those
// still running without a pane.
func (m *Manager) OpenShell(ctx context.Context, container string) (Row, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.session(ctx, container)
	if err != nil {
		return Row{}, err
	}
	ps, err := listPanes(ctx, s, container)
	if err != nil {
		return Row{}, err
	}
	name, err := m.freeShell(ctx, container, ps)
	if err != nil {
		return Row{}, err
	}
	workdir := ""
	if out, err := s.Run(ctx, "display-message", "-p", "-t", "="+container+":", "#{"+optWorkdir+"}"); err == nil &&
		len(out) == 1 && checkWorkdir(out[0]) == nil {
		workdir = out[0]
	}
	id, err := m.newShellPane(ctx, s, container, name, workdir, false)
	if err == nil {
		if ps, err = listPanes(ctx, s, container); err == nil {
			err = m.show(ctx, s, ps, pane{ID: id}, true)
		}
	}
	if err != nil {
		ctx := context.WithoutCancel(ctx)
		if id != "" {
			s.Run(ctx, "kill-pane", "-t", id)
			if serr := m.cfg.Containers.StopShell(ctx, container, name); serr != nil {
				log.Printf("lea daemon: stop shell %s of %s after a failed open: %v", name, container, serr)
			}
		}
		return Row{}, err
	}
	if ps, err = listPanes(ctx, s, container); err != nil {
		return Row{}, err
	}
	m.publish(container, ps)
	return shellRow(container, name), nil
}

// freeShell is the lowest free sh-N of the container, or errShellLimit at MaxShells shells
// (counting those still running without a pane).
func (m *Manager) freeShell(ctx context.Context, container string, ps []pane) (string, error) {
	live, err := m.cfg.Containers.Shells(ctx, container)
	if err != nil {
		return "", err
	}
	// A dead pane holds its shell's name until the reconcile removes it: never two panes of one name.
	taken := map[string]bool{}
	for _, p := range ps {
		if p.Shell != "" {
			taken[p.Shell] = true
		}
	}
	for _, n := range live {
		taken[n] = true
	}
	if len(taken) >= MaxShells {
		return "", errShellLimit
	}
	for i := 1; ; i++ {
		if n := fmt.Sprintf("sh-%d", i); !taken[n] {
			return n, nil
		}
	}
}

// SplitShell opens a shell beside an agent, as Terminator splits a terminal: below it (below)
// or at its right, in the agent's own directory (its worktree once it exists, else its
// repository, else where it started). The shell shows with the agent from then on, and takes the
// keyboard. An agent has one split shell: asking again shows it.
func (m *Manager) SplitShell(ctx context.Context, key string, below bool) (Row, error) {
	a, err := m.spawned(key)
	if err != nil {
		return Row{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.session(ctx, a.Container)
	if err != nil {
		return Row{}, err
	}
	ps, err := listPanes(ctx, s, a.Container)
	if err != nil {
		return Row{}, err
	}
	ap, ok := byAgent(ps, a.Name)
	if !ok || ap.Dead {
		return Row{}, fmt.Errorf("agent %s has no terminal to split", a.Name)
	}
	name := ""
	id := ""
	if c, ok := companion(ps, a.Name); ok && !c.Dead {
		name, id = c.Shell, c.ID
	} else {
		if name, err = m.freeShell(ctx, a.Container, ps); err != nil {
			return Row{}, err
		}
		side := "right"
		if below {
			side = "below"
		}
		id, err = m.newShellPane(ctx, s, a.Container, name, m.agentDir(ctx, a), false)
		if err == nil {
			_, err = s.Run(ctx, "set-option", "-p", "-t", id, optSplit, a.Name+":"+side)
		}
		if err != nil {
			ctx := context.WithoutCancel(ctx)
			if id != "" {
				s.Run(ctx, "kill-pane", "-t", id)
				if serr := m.cfg.Containers.StopShell(ctx, a.Container, name); serr != nil {
					log.Printf("lea daemon: stop shell %s of %s after a failed split: %v", name, a.Container, serr)
				}
			}
			return Row{}, err
		}
		if ps, err = listPanes(ctx, s, a.Container); err != nil {
			return Row{}, err
		}
	}
	if err := m.show(ctx, s, ps, ap, false); err != nil {
		return Row{}, err
	}
	if deck, ok := byRole(ps, roleDeck); ok {
		if _, err := s.Run(ctx, "select-window", "-t", deck.Window); err != nil {
			return Row{}, err
		}
	}
	if _, err := s.Run(ctx, "select-pane", "-t", id); err != nil {
		return Row{}, err
	}
	if ps, err = listPanes(ctx, s, a.Container); err != nil {
		return Row{}, err
	}
	m.publish(a.Container, ps)
	m.setShown(a.Container, a.Key)
	return shellRow(a.Container, name), nil
}

// agentDir is the directory an agent works in, as its first prompt names it: its worktree once
// the agent has made it, else its repository, else where it started.
func (m *Manager) agentDir(ctx context.Context, a store.Agent) string {
	dir := a.Workdir
	if a.Repo == "" && a.Branch == "" {
		return dir
	}
	projects, err := m.cfg.Containers.Projects(ctx, a.Container)
	if err != nil || projects == "" {
		return dir
	}
	if a.Repo != "" {
		if d, err := repoDir(projects, a.Repo); err == nil {
			dir = d
		}
	}
	if a.Branch != "" {
		if wt := worktreePath(projects, a.Workdir, a); checkWorkdir(wt) == nil {
			if fi, err := os.Stat(wt); err == nil && fi.IsDir() {
				dir = wt
			}
		}
	}
	return dir
}

// errShellLimit refuses a shell over MaxShells. It fits the deck's 31-character status line.
var errShellLimit = limitError(fmt.Sprintf("at most %d shells: x closes one", MaxShells))

// limitError is a store.ErrLimit with a message of its own (no "limit reached: " prefix).
type limitError string

func (e limitError) Error() string { return string(e) }
func (e limitError) Unwrap() error { return store.ErrLimit }

// focusShell shows a shell in the slot; focus also gives it the keyboard.
func (m *Manager) focusShell(ctx context.Context, container, name string, focus bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.session(ctx, container)
	if err != nil {
		return err
	}
	ps, err := listPanes(ctx, s, container)
	if err != nil {
		return err
	}
	p, ok := byShell(ps, name)
	if !ok || p.Dead {
		return fmt.Errorf("shell %s has ended", name)
	}
	if agent, _ := p.splitAgent(); agent != "" {
		if ap, ok := byAgent(ps, agent); ok {
			// Its agent takes the slot, the shell beside it.
			if err := m.show(ctx, s, ps, ap, false); err != nil {
				return err
			}
			if focus {
				if deck, ok := byRole(ps, roleDeck); ok {
					if _, err := s.Run(ctx, "select-window", "-t", deck.Window); err != nil {
						return err
					}
				}
				if _, err := s.Run(ctx, "select-pane", "-t", p.ID); err != nil {
					return err
				}
			}
			if ps, err = listPanes(ctx, s, container); err != nil {
				return err
			}
			m.publish(container, ps)
			return nil
		}
		// Its agent is gone: the shell is an ordinary one from now on.
		if _, err := s.Run(ctx, "set-option", "-p", "-t", p.ID, optSplit, ""); err != nil {
			return err
		}
		p.Split = ""
	}
	if err := m.show(ctx, s, ps, p, focus); err != nil {
		return err
	}
	if ps, err = listPanes(ctx, s, container); err != nil {
		return err
	}
	m.publish(container, ps)
	return nil
}

// stopShell removes a shell's pane and ends the shell (the deck's x: no confirmation). A shell
// whose pane closed while it runs (no reconcile has given it one back yet) is ended all the same.
func (m *Manager) stopShell(ctx context.Context, container, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.session(ctx, container)
	if err != nil {
		return err
	}
	ps, err := listPanes(ctx, s, container)
	if err != nil {
		return err
	}
	if p, ok := byShell(ps, name); ok {
		// The pane goes first: the shell's pane closes by itself once the shell ends.
		if err := m.removePane(ctx, s, ps, p); err != nil {
			return err
		}
	} else if live, err := m.cfg.Containers.Shells(ctx, container); err != nil {
		return err
	} else if !slices.Contains(live, name) {
		return fmt.Errorf("shell %s has ended", name)
	}
	if err := m.cfg.Containers.StopShell(ctx, container, name); err != nil {
		return err
	}
	if ps, err = listPanes(ctx, s, container); err != nil {
		return err
	}
	m.publish(container, ps)
	return nil
}

// SetLabel sets the display label of an agent lea spawned ("" clears it): the user's, from the
// host or the deck only. The agent's name, LEA_AGENT and threads do not change.
func (m *Manager) SetLabel(ctx context.Context, key, label string) error {
	if _, _, ok := parseShellKey(key); ok {
		return errors.New("a shell has no label")
	}
	l, err := CheckLabel(label)
	if err != nil {
		return err
	}
	if _, err := m.spawned(key); err != nil {
		return err
	}
	if err := m.cfg.Store.SetLabel(key, l); err != nil {
		return err
	}
	m.feed.bump()
	return nil
}

// SetPaused freezes an agent lea spawned (the deck's p), or lets a frozen one continue where it
// stopped: its processes get SIGSTOP or SIGCONT in the container, its conversation stays. The
// user's only, from the deck. The row says paused only once the container froze the agent.
func (m *Manager) SetPaused(ctx context.Context, key string, paused bool) error {
	if _, _, ok := parseShellKey(key); ok {
		return errors.New("a shell cannot be paused")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	a, err := m.spawned(key)
	if err != nil {
		return err
	}
	if a.State == "done" || a.State == stateStopped {
		return fmt.Errorf("%s is not running", a.Name)
	}
	if err := m.cfg.Containers.Pause(ctx, a.Container, a.Name, paused); err != nil {
		return err
	}
	if err := m.cfg.Store.SetPaused(key, paused); err != nil {
		return err
	}
	m.feed.bump()
	return nil
}

// removePane kills an agent's pane; when it is in the slot, the placeholder takes its place first.
func (m *Manager) removePane(ctx context.Context, s Session, ps []pane, p pane) error {
	if _, slot, ok := deckAndSlot(ps); ok && slot.ID == p.ID {
		if ph, ok := byRole(ps, rolePlace); ok {
			if _, err := s.Run(ctx, "swap-pane", "-d", "-s", ph.ID, "-t", p.ID); err != nil {
				return err
			}
		}
	}
	_, err := s.Run(ctx, "kill-pane", "-t", p.ID)
	if err == nil {
		delete(m.backoff, p.ID)
	}
	return err
}

// show swaps p into the slot; focus also moves the keyboard to it.
func (m *Manager) show(ctx context.Context, s Session, ps []pane, p pane, focus bool) error {
	deck, slot, ok := deckAndSlot(ps)
	if !ok {
		return errors.New("the deck window has no slot")
	}
	// A split shell shows with its agent only: any other one in the deck window parks first.
	mine := ""
	if p.Agent != "" {
		mine = p.Agent
	}
	relaid := false
	for _, q := range ps {
		if a, _ := q.splitAgent(); a != "" && a != mine && q.Window == deck.Window {
			if _, err := s.Run(ctx, "break-pane", "-d", "-s", q.ID, "-n", q.Shell); err != nil {
				return err
			}
			relaid = true
		}
	}
	if slot.ID != p.ID {
		if _, err := s.Run(ctx, "swap-pane", "-d", "-s", p.ID, "-t", slot.ID); err != nil {
			return err
		}
	}
	if c, ok := companion(ps, mine); ok && !c.Dead && c.Window != deck.Window {
		_, below := c.splitAgent()
		side, size := "-h", "50%"
		if below {
			side, size = "-v", "35%"
		}
		if _, err := s.Run(ctx, "join-pane", "-d", side, "-l", size, "-s", c.ID, "-t", p.ID); err != nil {
			return err
		}
		relaid = true
	}
	if relaid {
		if _, err := s.Run(ctx, "resize-pane", "-t", deck.ID, "-x", strconv.Itoa(DeckWidth)); err != nil {
			return err
		}
	}
	if focus {
		if _, err := s.Run(ctx, "select-window", "-t", deck.Window); err != nil {
			return err
		}
		if _, err := s.Run(ctx, "select-pane", "-t", p.ID); err != nil {
			return err
		}
	}
	return nil
}

// freeName is the lowest free <prefix>-N.
func (m *Manager) freeName(container, prefix string) (string, error) {
	as, err := m.cfg.Store.SpawnedAgents(container)
	if err != nil {
		return "", err
	}
	taken := map[string]bool{}
	for _, a := range as {
		if a.State != "done" {
			taken[a.Name] = true
		}
	}
	for i := 1; ; i++ {
		if n := fmt.Sprintf("%s-%d", prefix, i); !taken[n] {
			return n, nil
		}
	}
}

// New spawns an agent in one atomic step (spec §5.3): check the request, register the agent under
// the caps, open its task, post its brief, start its pane (abduco creates the session and runs
// lea-agent) and show it unless it is Hidden. Any failure removes what was done.
func (m *Manager) New(ctx context.Context, r NewRequest) (Row, error) {
	reg, err := m.registry()
	if err != nil {
		return Row{}, err
	}
	prov, model, err := reg.Resolve(r.Profile, r.Model, m.cfg.DefaultProvider, m.cfg.DefaultModel)
	if err != nil {
		return Row{}, err
	}
	r.Profile, r.Model = prov.Name, model
	if r.Role == "" {
		r.Role = roles.Default
	}
	role, err := roles.Get(r.Role)
	if err != nil {
		return Row{}, err
	}
	if r.Parent != "" && role.CanSpawn {
		return Row{}, fmt.Errorf("a lead's child never spawns: the %s role cannot be given to a child", r.Role)
	}
	if r.Name != "" && !nameRe.MatchString(r.Name) {
		return Row{}, fmt.Errorf("invalid agent name %q: lowercase letters, digits and '-', starting with a letter, at most 24", r.Name)
	}
	if reservedNames[r.Name] {
		return Row{}, fmt.Errorf("agent name %q is reserved: lea's own panes use it", r.Name)
	}
	if err := checkArgs(r.Args); err != nil {
		return Row{}, err
	}
	if err := CheckRepo(r.Repo); err != nil {
		return Row{}, err
	}
	if err := CheckBranch(r.Branch); err != nil {
		return Row{}, err
	}
	if len(r.Brief) > MaxBrief {
		return Row{}, fmt.Errorf("brief is %d bytes, limit %d", len(r.Brief), MaxBrief)
	}
	if r.Brief != "" {
		// The message rules (UTF-8, no control character but newline and tab), not its rate.
		if err := bus.Default.Check(r.Brief, "request", 0, 0); err != nil {
			return Row{}, fmt.Errorf("brief: %w", err)
		}
	}
	if r.BriefFrom == "" {
		r.BriefFrom = "user"
	}
	if r.Brief != "" && r.Parent != "" && r.BriefFrom == "user" {
		return Row{}, errors.New("a spawned agent's brief comes from its parent, not from the user")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	briefHops := 0
	if r.Brief != "" {
		var err error
		if briefHops, err = m.checkBriefSource(r); err != nil {
			return Row{}, err
		}
	}
	s, err := m.session(ctx, r.Container)
	if err != nil {
		return Row{}, err
	}
	if r.Parent != "" {
		if err := m.checkParent(r.Container, r.Parent, r.Role); err != nil {
			return Row{}, err
		}
	}
	projects := ""
	if r.Repo != "" || r.Branch != "" {
		if projects, err = m.cfg.Containers.Projects(ctx, r.Container); err != nil {
			return Row{}, err
		}
	}
	repo := ""
	if r.Repo != "" {
		if repo, err = repoDir(projects, r.Repo); err != nil {
			return Row{}, err
		}
		// Claude trusts a directory, not its subdirectories: started in the repo it would stop
		// on a trust dialog nobody sees when the agent is parked. The projects directory is
		// the one the user trusts and holds both the repo and the worktrees (A14); the first
		// prompt names the repo.
		r.Workdir = projects
	}
	if r.Workdir == "" {
		out, err := s.Run(ctx, "display-message", "-p", "-t", "="+r.Container+":", "#{"+optWorkdir+"}")
		if err != nil {
			return Row{}, err
		}
		if len(out) == 1 {
			r.Workdir = out[0]
		}
	}
	if err := checkWorkdir(r.Workdir); err != nil {
		return Row{}, err
	}
	if r.Name == "" {
		prefix := r.NamePrefix
		if prefix == "" {
			prefix = r.Profile
		}
		if r.Name, err = m.freeName(r.Container, prefix); err != nil {
			return Row{}, err
		}
	}
	var caps store.Caps
	if r.Parent != "" {
		caps.MaxChildren = roles.MaxChildren
	}
	a, err := m.cfg.Store.Register(store.NewAgent{Container: r.Container, Name: r.Name, Profile: r.Profile, Model: r.Model, Workdir: r.Workdir,
		Role: r.Role, Parent: r.Parent, Repo: r.Repo, Branch: r.Branch}, caps, m.cfg.Now())
	if err != nil {
		return Row{}, err
	}
	var args []string
	taskID, err := m.cfg.Store.CreateTask(r.Container, a.Key, taskTitle(r.Brief), m.cfg.Now())
	if err == nil {
		a.TaskID = taskID
	}
	if err == nil && r.Brief != "" {
		_, err = m.cfg.Store.Send(store.Message{From: r.BriefFrom, To: a.Key, Kind: "request", Body: r.Brief,
			ReplyTo: r.BriefReplyTo, Hops: briefHops, At: m.cfg.Now()})
	}
	prompt := ""
	if err == nil && (r.Brief != "" || a.Branch != "" || repo != "") {
		wt := ""
		if a.Branch != "" {
			wt = worktreePath(projects, r.Workdir, a)
		}
		prompt = firstPrompt(a, repo, wt, r.Brief != "")
	}
	if err == nil {
		args, err = launchArgs(prov, a, "", r.Args, prompt)
	}
	if err == nil {
		// What a new container starts the agent again with (ResumeStopped).
		err = m.cfg.Store.SetLaunch(a.Key, r.Args, prompt)
	}
	id := ""
	if err == nil {
		var ps []pane
		if ps, err = listPanes(ctx, s, r.Container); err == nil {
			if old, ok := byAgent(ps, a.Name); ok { // the last screen of an ended agent of that name
				err = m.removePane(ctx, s, ps, old)
			}
		}
	}
	if err == nil {
		err = m.waitTeardown(ctx, a.Key)
	}
	if err == nil {
		id, err = m.newPane(ctx, s, r.Container, a, args, false)
	}
	if err == nil && !r.Hidden {
		var ps []pane
		if ps, err = listPanes(ctx, s, r.Container); err == nil {
			err = m.show(ctx, s, ps, pane{ID: id}, r.Focus)
		}
	}
	if err != nil {
		forget := true
		ctx := context.WithoutCancel(ctx) // a cancelled request must still clean up after itself
		if id != "" {
			// abduco keeps lea-agent running without the pane: end it too, or the agent leaks
			// and a later spawn of the same name lands in it. If it cannot be stopped, its row
			// stays, so the deck shows it and x can remove it.
			s.Run(ctx, "kill-pane", "-t", id)
			delete(m.backoff, id)
			if serr := m.cfg.Containers.Stop(ctx, r.Container, a.Name); serr != nil {
				log.Printf("lea daemon: stop %s after a failed spawn: %v", a.Key, serr)
				forget = false
			} else {
				m.teardown(ctx, r.Container, a.Name)
			}
		}
		if forget {
			if taskID != 0 {
				if derr := m.cfg.Store.DeleteTask(taskID); derr != nil {
					log.Printf("lea daemon: delete the task of %s: %v", a.Key, derr)
				}
			}
			// Forgetting also marks the brief read: a later agent of that name never gets it.
			if ferr := m.cfg.Store.ForgetAgent(a.Key); ferr != nil {
				log.Printf("lea daemon: forget %s: %v", a.Key, ferr)
			}
		}
		m.feed.bump()
		return Row{}, err
	}
	if !r.Hidden {
		m.setShown(r.Container, a.Key)
	}
	m.feed.bump()
	return rowOf(a), nil
}

// checkBriefSource applies the brief's origin rules and returns the hop count the brief carries:
// BriefFrom is the user or a registered agent of the container, and BriefReplyTo, when set, is a
// message sent by or addressed to BriefFrom.
func (m *Manager) checkBriefSource(r NewRequest) (int, error) {
	if r.BriefFrom != "user" {
		a, err := m.cfg.Store.AgentByKey(r.BriefFrom)
		if errors.Is(err, store.ErrNotFound) || (err == nil && a.Container != r.Container) {
			return 0, fmt.Errorf("brief from %q: no such agent in this container", r.BriefFrom)
		}
		if err != nil {
			return 0, err
		}
	}
	if r.BriefReplyTo == 0 {
		return 0, nil
	}
	p, err := m.cfg.Store.Get(r.BriefReplyTo)
	if errors.Is(err, store.ErrNotFound) {
		return 0, fmt.Errorf("reply_to %d: %w", r.BriefReplyTo, err)
	}
	if err != nil {
		return 0, err
	}
	if p.From != r.BriefFrom && p.To != r.BriefFrom {
		return 0, fmt.Errorf("reply_to %d is not a message of %s", r.BriefReplyTo, r.BriefFrom)
	}
	if p.Hops+1 > bus.Default.MaxHops {
		return 0, fmt.Errorf("reply chain exceeds %d hops", bus.Default.MaxHops)
	}
	return p.Hops + 1, nil
}

// checkParent refuses a child whose parent is not a running agent of the same container, or
// whose role may not give role (the store checks the tree, not the roles).
func (m *Manager) checkParent(container, parent, role string) error {
	p, err := m.cfg.Store.AgentByKey(parent)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("unknown parent agent %q", parent)
	}
	if err != nil {
		return err
	}
	if p.Container != container {
		return fmt.Errorf("parent %s runs in another container", p.Name)
	}
	if p.State == "done" {
		return fmt.Errorf("parent %s has ended", p.Name)
	}
	pr, err := roles.Get(p.Role)
	if err != nil {
		return err
	}
	return roles.CanGive(pr, role)
}

// spawned returns an agent lea spawned, by key.
func (m *Manager) spawned(key string) (store.Agent, error) {
	a, err := m.cfg.Store.AgentByKey(key)
	if errors.Is(err, store.ErrNotFound) {
		return store.Agent{}, fmt.Errorf("unknown agent %q", key)
	}
	if err != nil {
		return store.Agent{}, err
	}
	if a.Profile == "" {
		return store.Agent{}, fmt.Errorf("agent %s was not started by lea", a.Name)
	}
	return a, nil
}

// Focus shows the agent (or shell) in the slot; focus also gives it the keyboard.
func (m *Manager) Focus(ctx context.Context, key string, focus bool) error {
	if c, n, ok := parseShellKey(key); ok {
		return m.focusShell(ctx, c, n, focus)
	}
	a, err := m.spawned(key)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.session(ctx, a.Container)
	if err != nil {
		return err
	}
	ps, err := listPanes(ctx, s, a.Container)
	if err != nil {
		return err
	}
	p, ok := byAgent(ps, a.Name)
	if !ok {
		if err := m.reconcile(ctx, a.Container, s); err != nil {
			return err
		}
		if ps, err = listPanes(ctx, s, a.Container); err != nil {
			return err
		}
		if p, ok = byAgent(ps, a.Name); !ok {
			return fmt.Errorf("agent %s has ended and has no terminal left; x removes it", a.Name)
		}
	}
	if err := m.show(ctx, s, ps, p, focus); err != nil {
		return err
	}
	m.setShown(a.Container, a.Key)
	return nil
}

// Stop ends the agent's process, removes its pane and forgets it. A shell's key ends the shell.
func (m *Manager) Stop(ctx context.Context, key string) error {
	if c, n, ok := parseShellKey(key); ok {
		return m.stopShell(ctx, c, n)
	}
	return m.stop(ctx, key, false)
}

// RemoveEnded removes an ended agent: the deck's x on a done row, which asks no confirmation. The
// agent must still be ended: a lead that spawned that name again since the deck drew the row
// reused it, and the new agent is refused, never stopped without the user's y.
func (m *Manager) RemoveEnded(ctx context.Context, key string) error {
	if _, _, ok := parseShellKey(key); ok {
		return errors.New("a shell is not an ended agent")
	}
	return m.stop(ctx, key, true)
}

func (m *Manager) stop(ctx context.Context, key string, endedOnly bool) error {
	a, err := m.spawned(key)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if endedOnly {
		// Read again under the lock New registers under.
		if a, err = m.spawned(key); err != nil {
			return err
		}
		if a.State != "done" {
			return fmt.Errorf("%s is running again", a.Name)
		}
	}
	if err := m.cfg.Containers.Stop(ctx, a.Container, a.Name); err != nil {
		return err
	}
	m.teardown(ctx, a.Container, a.Name)
	if s, err := m.session(ctx, a.Container); err == nil {
		if ps, err := listPanes(ctx, s, a.Container); err == nil {
			if p, ok := byAgent(ps, a.Name); ok {
				if err := m.removePane(ctx, s, ps, p); err != nil {
					return err
				}
			}
		}
	}
	if err := m.cfg.Store.ForgetAgent(key); err != nil {
		return err
	}
	m.smu.Lock()
	if m.shown[a.Container] == key {
		m.shown[a.Container] = ""
	}
	m.smu.Unlock()
	m.feed.bump()
	return nil
}

// stateStopped is the state of an agent whose container lea stop ended: the next container of
// its projects directory starts it again (ResumeStopped).
const stateStopped = "stopped"

// MarkStopped records that the container is about to stop (lea stop): its spawned agents that
// have not ended become stopped, so the next container starts them again; with forget, they are
// forgotten for good instead (lea stop --forget). A suspended agent becomes suspended_cold: its
// decision resumes it. Agents a hook registered only are left alone.
func (m *Manager) MarkStopped(ctx context.Context, container string, forget bool) error {
	if !containerRe.MatchString(container) {
		return fmt.Errorf("invalid container name %q", container)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// The lock consoles its deck opened stop with it: none outlives its pane.
	m.stopConsoles(ctx, container)
	as, err := m.cfg.Store.SpawnedAgents(container)
	if err != nil {
		return err
	}
	for _, a := range as {
		switch {
		case forget:
			err = m.cfg.Store.ForgetAgent(a.Key)
		case store.Suspended(a.State):
			// Still waiting for its decision: the stop ends its process, its session stays.
			err = m.cfg.Store.SetState(a.Key, store.StateSuspendedCold, "", m.cfg.Now())
		case a.State != "done" && a.State != stateStopped:
			err = m.cfg.Store.SetState(a.Key, stateStopped, "", m.cfg.Now())
		}
		if err != nil {
			return err
		}
	}
	m.feed.bump()
	return nil
}

// unstopLive takes back the stop of agents still live in the container lea opens (a lea stop
// cut short: interrupted, or its container never stopped): MarkStopped marked them stopped, and
// no hook of theirs makes a stopped agent live again (store.SetHookState). They become unknown,
// as an agent restart starts, so that their next hook reports their state. Callers hold m.mu.
func (m *Manager) unstopLive(ctx context.Context, container string) error {
	live, err := m.cfg.Containers.Live(ctx, container)
	if err != nil {
		return err
	}
	as, err := m.cfg.Store.SpawnedAgents(container)
	if err != nil {
		return err
	}
	for _, a := range as {
		if a.State == stateStopped && slices.Contains(live, a.Name) {
			if err := m.cfg.Store.SetState(a.Key, "unknown", "", m.cfg.Now()); err != nil {
				return err
			}
			m.feed.bump()
		}
	}
	return nil
}

// ResumeStopped starts again, in the container the launcher has just started, the spawned agents
// of the previous one: each in its parking window, under its name, role and workdir, with `claude
// --resume <session>` and its first arguments when Claude reported its session, else as it was
// first started (its first prompt included). An agent that had ended (done) is forgotten, as a
// new container runs none of it; a live one is left alone. The panes left of them, when the
// session survived, are replaced. A container whose live agents cannot be listed keeps every row.
func (m *Manager) ResumeStopped(ctx context.Context, container string) error {
	if !containerRe.MatchString(container) {
		return fmt.Errorf("invalid container name %q", container)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	live, err := m.cfg.Containers.Live(ctx, container)
	if err != nil {
		return err
	}
	as, err := m.cfg.Store.SpawnedAgents(container)
	if err != nil {
		return err
	}
	var resume []store.Agent
	for _, a := range as {
		switch {
		case slices.Contains(live, a.Name):
		case store.Suspended(a.State):
			// Resumed by its decision or a message (ResumeSuspended), not by the restart. A warm
			// one lost its process with the old container.
			if a.State == store.StateSuspendedWarm {
				if err := m.cfg.Store.SetState(a.Key, store.StateSuspendedCold, "", m.cfg.Now()); err != nil {
					return err
				}
			}
		case a.State == "done":
			if err := m.cfg.Store.ForgetAgent(a.Key); err != nil {
				return err
			}
		default:
			resume = append(resume, a)
		}
	}
	defer m.feed.bump()
	if len(resume) == 0 {
		if s, ok := m.conns[container]; ok {
			return m.reconcile(ctx, container, s)
		}
		return nil
	}
	s, err := m.session(ctx, container)
	if err != nil {
		return err
	}
	var errs []error
	for _, a := range resume {
		if err := m.restart(ctx, s, container, a); err != nil {
			// Its row stays, shown as done: x removes it.
			errs = append(errs, fmt.Errorf("resume %s: %w", a.Name, err))
			if serr := m.cfg.Store.SetState(a.Key, "done", "", m.cfg.Now()); serr != nil {
				errs = append(errs, serr)
			}
		}
	}
	if err := m.reconcile(ctx, container, s); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// restart starts a stopped agent's pane again (ResumeStopped), parked. Callers hold m.mu.
func (m *Manager) restart(ctx context.Context, s Session, container string, a store.Agent) error {
	// The session id comes from the agent's own hooks: only a UUID reaches the command line,
	// never a value that could pass for a flag (--resume takes an optional one).
	session := ""
	if sessionRe.MatchString(a.SessionID) {
		session = a.SessionID
	}
	return m.restartWith(ctx, s, container, a, session, "")
}

// restartWith starts an agent's pane again, parked: with session, it resumes that session, and
// wake, when not empty, is its next turn's prompt; without one, it starts as it first did (its
// first prompt included). Callers hold m.mu.
func (m *Manager) restartWith(ctx context.Context, s Session, container string, a store.Agent, session, wake string) error {
	reg, err := m.registry()
	if err != nil {
		return err
	}
	prov, err := reg.Get(a.Profile)
	if err != nil {
		return err
	}
	args, err := launchArgs(prov, a, session, a.Args, a.Prompt)
	if err != nil {
		return err
	}
	if session != "" && wake != "" {
		// After the user's arguments, as a first prompt would be: Argv never passes the first
		// prompt with a session (the agent had it already).
		args = append(args, wake)
	}
	if err := checkWorkdir(a.Workdir); err != nil {
		return err
	}
	ps, err := listPanes(ctx, s, container)
	if err != nil {
		return err
	}
	if old, ok := byAgent(ps, a.Name); ok {
		if err := m.removePane(ctx, s, ps, old); err != nil {
			return err
		}
	}
	if err := m.waitTeardown(ctx, a.Key); err != nil {
		return err
	}
	if err := m.cfg.Store.SetState(a.Key, "unknown", "", m.cfg.Now()); err != nil {
		return err
	}
	_, err = m.newPane(ctx, s, container, a, args, false)
	return err
}

// teardownTimeout bounds the removal of an ended agent's stack in the background.
const teardownTimeout = 2 * time.Minute

// teardownLater removes, in the background, what an agent that has just ended started in the
// nested Podman (#97): pods and containers left running would pile up until the host runs out of
// memory. A teardown of that agent already in flight is enough.
func (m *Manager) teardownLater(container, name string) {
	key := container + "/" + name
	m.tmu.Lock()
	if _, ok := m.tearing[key]; ok {
		m.tmu.Unlock()
		return
	}
	done := make(chan struct{})
	m.tearing[key] = done
	m.tmu.Unlock()
	go func() {
		defer func() {
			m.tmu.Lock()
			delete(m.tearing, key)
			m.tmu.Unlock()
			close(done)
		}()
		ctx, cancel := context.WithTimeout(context.Background(), teardownTimeout)
		defer cancel()
		if err := m.cfg.Containers.Teardown(ctx, container, name); err != nil {
			log.Printf("lea daemon: tear down the stack of %s: %v", key, err)
		}
	}()
}

// waitTeardown waits for the teardown in flight of the agent key, if any: an agent started under
// that name meanwhile would lose its own Podman service.
func (m *Manager) waitTeardown(ctx context.Context, key string) error {
	m.tmu.Lock()
	done := m.tearing[key]
	m.tmu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// teardown removes a stopped agent's stack now; a failure is logged only: the agent is gone
// either way, and the next container's sweep removes what is left.
func (m *Manager) teardown(ctx context.Context, container, name string) {
	key := container + "/" + name
	if err := m.waitTeardown(ctx, key); err != nil {
		return
	}
	if err := m.cfg.Containers.Teardown(ctx, container, name); err != nil {
		log.Printf("lea daemon: tear down the stack of %s: %v", key, err)
	}
}

// ParkStack stops what an idle agent started in the nested Podman (#132): its containers hold
// memory and CPU while the agent waits, and it starts them again when it needs them. It says
// what it freed. An ended agent is torn down instead, never parked; a teardown in flight goes
// first.
func (m *Manager) ParkStack(ctx context.Context, key string) (string, error) {
	a, err := m.spawned(key)
	if err != nil {
		return "", err
	}
	if a.State == "done" || a.State == stateStopped {
		return "", fmt.Errorf("%s has ended", a.Name)
	}
	if err := m.waitTeardown(ctx, key); err != nil {
		return "", err
	}
	return m.cfg.Containers.Park(ctx, a.Container, a.Name)
}

// UnparkStack starts again what ParkStack stopped: a new turn came while it stopped them.
func (m *Manager) UnparkStack(ctx context.Context, key string) (string, error) {
	a, err := m.spawned(key)
	if err != nil {
		return "", err
	}
	return m.cfg.Containers.Unpark(ctx, a.Container, a.Name)
}

// SweepStacks removes, in a container the launcher has just started, what the agents that are
// gone left in its nested Podman (the agents that run or come back keep theirs), and says what it
// removed and what no agent started (orphans of a shell or of an older lea).
func (m *Manager) SweepStacks(ctx context.Context, container string) (string, error) {
	if !containerRe.MatchString(container) {
		return "", fmt.Errorf("invalid container name %q", container)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	live, err := m.cfg.Containers.Live(ctx, container)
	if err != nil {
		return "", err
	}
	as, err := m.cfg.Store.SpawnedAgents(container)
	if err != nil {
		return "", err
	}
	keep := append([]string(nil), live...)
	for _, a := range as {
		if a.State != "done" && !slices.Contains(keep, a.Name) {
			keep = append(keep, a.Name)
		}
	}
	slices.Sort(keep)
	return m.cfg.Containers.Sweep(ctx, container, keep)
}

// userClients lists the user's terminals attached to the session (control clients, the
// daemon's own, left out), most recently active first. Callers hold m.mu.
func userClients(ctx context.Context, s Session, container string) ([]string, error) {
	lines, err := s.Run(ctx, "list-clients", "-t", "="+container, "-F", "#{client_name}|#{client_control_mode}|#{client_activity}")
	if err != nil {
		return nil, err
	}
	type client struct {
		name string
		act  int64
	}
	var cl []client
	for _, l := range lines {
		f := strings.Split(l, "|")
		if len(f) < 2 || f[1] == "1" {
			continue
		}
		c := client{name: f[0]}
		if len(f) > 2 {
			c.act, _ = strconv.ParseInt(f[2], 10, 64)
		}
		cl = append(cl, c)
	}
	sort.SliceStable(cl, func(i, j int) bool { return cl[i].act > cl[j].act })
	out := make([]string, len(cl))
	for i, c := range cl {
		out[i] = c.name
	}
	return out, nil
}

// Attached reports whether a user's terminal shows the container's deck. It never creates a
// session: with no connection held it reconnects only to a session that already exists (a
// restarted daemon), and a container without a session has no terminal attached.
func (m *Manager) Attached(ctx context.Context, container string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cl, err := m.existingClients(ctx, container)
	return len(cl) > 0, err
}

// existingClients lists the user terminals of a container's session, most recently active
// first, without ever creating the session: with no connection held it reconnects only to a
// session that already exists. Callers hold m.mu.
func (m *Manager) existingClients(ctx context.Context, container string) ([]string, error) {
	if _, held := m.conns[container]; !held {
		if !containerRe.MatchString(container) {
			return nil, fmt.Errorf("invalid container name %q", container)
		}
		names, err := m.cfg.Term.Sessions(ctx)
		if err != nil {
			return nil, err
		}
		found := false
		for _, n := range names {
			found = found || n == container
		}
		if !found {
			return nil, nil
		}
	}
	s, err := m.session(ctx, container)
	if err != nil {
		return nil, err
	}
	return userClients(ctx, s, container)
}

// Popup opens an agent's thread (compose: with the message editor open) in a popup over the
// user's most recently active terminal on the agent's deck. The popup runs lea itself.
func (m *Manager) Popup(ctx context.Context, key string, compose bool) error {
	a, err := m.spawned(key)
	if err != nil {
		return err
	}
	m.mu.Lock()
	cl, err := m.existingClients(ctx, a.Container) // never creates a deck nobody sees
	m.mu.Unlock()
	if err != nil {
		return err
	}
	if len(cl) == 0 {
		return fmt.Errorf("no terminal shows the deck of %s", a.Container)
	}
	argv := []string{m.cfg.LeaBin, "deck", "--thread", a.Key}
	if compose {
		argv = append(argv, "--compose")
	}
	return m.cfg.Term.Popup(ctx, cl[0], argv)
}

// Detach detaches the user's terminals from the session; agents and the deck keep running.
// Control clients (the daemon's own) stay.
func (m *Manager) Detach(ctx context.Context, container string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.session(ctx, container)
	if err != nil {
		return err
	}
	cl, err := userClients(ctx, s, container)
	if err != nil {
		return err
	}
	for _, name := range cl {
		if _, err := s.Run(ctx, "detach-client", "-t", name); err != nil {
			return err
		}
	}
	return nil
}
