package agents

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Lock consoles (issue #18): the user shows a lock tool's console in a pane of the deck. Only the
// user: the methods below are host-only (the daemon's deck methods), no MCP tool reaches them, and
// the pane is never addressed by anything an agent can call. The consoles run hub-attached only,
// under lockhub's `lox up`, each in its own compose service with a terminal (lockhub's
// docs/lox-up.md): lea starts none. The pane attaches to lox up's running console (as `lox attach
// <instance>` does), for the questions the console asks at its terminal: the policy review on a
// first start or after a loosening, a production profile's typed name, the credentials its profile
// does not take from Vault. The approvals of the agents' queries go through the hub (the deck's
// hub view, lox approve), never through this pane. lea never types into the pane, reads it or
// relays an answer. Of the lock tools, only locksql's console is shown here.

const (
	// optLock tags a lock console's pane with its id: "<tool>" or "<tool>/<profile>".
	optLock  = "@lea_lock"
	roleLock = "lock"
	// lockHistory is a lock pane's scrollback: results the user chose to display must not pile
	// up in the host's tmux; the pane and its history go when the attach ends.
	lockHistory = "200"
	// lockOwnerLabel named the lea container whose deck started a console (an earlier lea started
	// its own): lea stop still stops those.
	lockOwnerLabel = "lea.lock-for"
	// lockSlotSize is the lock slot's height, below the agent shown beside the deck.
	lockSlotSize = "40%"
)

var (
	lockProfileRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	lockInstanceRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	lockContainerRe = regexp.MustCompile(`^[0-9a-f]{12,64}$`)
)

// lockSettle is how long LockOpen waits, once the pane runs the attach, before it checks that the
// pane is still there: an attach that fails (the console stopped meanwhile) closes its pane at
// once, and LockOpen then reports the failure instead of "opened". Tests set it to 0.
var lockSettle = 300 * time.Millisecond

// LockTools are the lock tools whose console lea shows in a pane.
var LockTools = []string{"locksql"}

// LockConsole is lox up's running console of a lock: its instance (lockfamily.yaml's name) and
// its container's id, which docker confirmed is lox up's service of that lock for the projects
// directory.
type LockConsole struct {
	Instance  string
	Container string
}

// LockSpec is the host configuration of a lock tool's console: lea's config files, which only
// the user writes (never the projects directory's).
type LockSpec struct {
	Enabled bool
	// EnabledFor, when set, decides Enabled for the deck's projects directory (a tool is on when
	// the user turned it on, or lox up runs a lock of its type: launcher.Config.ToolsOn).
	EnabledFor func(projects string) bool
	// ContainerName is the lea container of a projects directory (launcher.Config.ContainerName
	// of its physical path): a console shows on a deck's own projects directory only.
	ContainerName func(projects string) string
	// AgentProfiles are the profiles an agent may ask the user to open (lock_request): the
	// user's allow-list in lea's own config (LEA_<TOOL>_AGENT_PROFILES), empty by default. An
	// agent only asks; the user opens the console in the deck, or not.
	AgentProfiles []string
	// Projects is the deck's projects directory, which LockOpen takes from the deck's container
	// (its lea.projects label), never from the daemon's environment.
	Projects string
	// Console finds lox up's running console of the tool for projects that serves profile (""
	// for the only lock of the tool), from lox up's state the user owns (.lockfamily/up.json)
	// and docker's labels; an error says why there is none (and to run `lox up`). nil: no
	// console shows.
	Console func(ctx context.Context, projects, profile string) (LockConsole, error)
}

// Consoles reaches the console containers an earlier lea's deck started (the real one is
// Docker's): lea stop stops them. lea starts none.
type Consoles interface {
	// StopConsolesOf stops every console container a deck of owner started.
	StopConsolesOf(ctx context.Context, owner string) error
}

// LockRow is one console of a deck.
type LockRow struct {
	ID    string `json:"id"`    // <tool> or <tool>/<profile>
	Shown bool   `json:"shown"` // in the lock slot, under the agent; else parked in its window
}

func lockTool(tool string) bool {
	for _, t := range LockTools {
		if t == tool {
			return true
		}
	}
	return false
}

// lockID checks tool and profile ("" for the tool's only lock) and returns the pane's id.
func lockID(tool, profile string) (string, error) {
	if !lockTool(tool) {
		return "", fmt.Errorf("no lock console for %q: lea shows %s (lox attach <instance> reaches the others)",
			tool, strings.Join(LockTools, ", "))
	}
	if profile == "" {
		return tool, nil
	}
	if !lockProfileRe.MatchString(profile) {
		return "", fmt.Errorf("invalid profile name %q (lowercase letters, digits, _ and -, at most 32)", profile)
	}
	return tool + "/" + profile, nil
}

// checkSpec refuses a tool that is off and a projects directory that is not absolute and clean.
func checkSpec(tool string, s LockSpec) error {
	if !s.Enabled {
		return fmt.Errorf("%s is off: lea init --%s turns it on", tool, tool)
	}
	if !strings.HasPrefix(s.Projects, "/") || hasControl(s.Projects) {
		return fmt.Errorf("invalid projects directory %q for the %s console", s.Projects, tool)
	}
	return nil
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// lockCmd is a console's pane: docker attach to lox up's console container, as `lox attach`
// does, without passing signals on (closing the pane ends the attach, never the console) and
// with lox attach's detach keys (Ctrl-P Ctrl-Q). Never a console flag: the console runs as lox up
// started it.
func (m *Manager) lockCmd(c LockConsole) []string {
	return []string{m.cfg.Docker, "attach", "--sig-proxy=false", "--detach-keys", "ctrl-p,ctrl-q", c.Container}
}

func byLock(ps []pane, id string) (pane, bool) {
	return findPane(ps, func(p pane) bool { return p.Role == roleLock && p.Lock == id })
}

// lockWindow is the parking window's name of a console.
func lockWindow(id string) string { return "lock-" + strings.ReplaceAll(id, "/", "-") }

// LockOpen shows tool's console for profile in the container's deck: a pane attached to lox up's
// running console, or the one already open, in the lock slot under the agent beside the deck,
// without the keyboard (a console that took it while the user typed into an agent would get stray
// keys). The console shown there before parks in its own window. It returns "opened" or
// "already-open", and an error when no console of lox up runs or the attach ended at once.
func (m *Manager) LockOpen(ctx context.Context, container, tool, profile string) (string, error) {
	if !containerRe.MatchString(container) {
		return "", fmt.Errorf("invalid container name %q", container)
	}
	id, err := lockID(tool, profile)
	if err != nil {
		return "", err
	}
	if m.cfg.LockSpec == nil {
		return "", errors.New("lock consoles are not available in this daemon")
	}
	spec, err := m.cfg.LockSpec(tool)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.deckSession(ctx, container)
	if err != nil {
		return "", err
	}
	// One daemon serves every deck: the console of the deck's own projects directory, and only
	// one that maps back to this deck's container.
	if spec.Projects, err = m.cfg.Containers.Projects(ctx, container); err != nil {
		return "", err
	}
	if spec.ContainerName == nil || spec.ContainerName(spec.Projects) != container {
		return "", fmt.Errorf("the projects directory %s is not the one of %s: no console shows for it", spec.Projects, container)
	}
	if spec.EnabledFor != nil {
		spec.Enabled = spec.EnabledFor(spec.Projects)
	}
	if err := checkSpec(tool, spec); err != nil {
		return "", err
	}
	ps, err := listPanes(ctx, s, container)
	if err != nil {
		return "", err
	}
	status := "already-open"
	p, ok := byLock(ps, id)
	if ok && p.Dead { // defensive: a lock pane closes when its process exits
		if _, err := s.Run(ctx, "kill-pane", "-t", p.ID); err != nil {
			return "", err
		}
		ok = false
	}
	if !ok {
		if spec.Console == nil {
			return "", fmt.Errorf("no way to find lox up's %s console in this daemon", tool)
		}
		c, err := spec.Console(ctx, spec.Projects, profile)
		if err != nil {
			return "", err
		}
		if !lockInstanceRe.MatchString(c.Instance) || !lockContainerRe.MatchString(c.Container) {
			return "", fmt.Errorf("lox up's %s console: invalid instance %q or container %q", tool, c.Instance, c.Container)
		}
		if p, err = m.newLockPane(ctx, s, container, id, m.lockCmd(c)); err != nil {
			return "", err
		}
		// An attach that fails (the console stopped since docker said it runs) ends at once and
		// its pane closes (remain-on-exit off): that is a failure, never "opened".
		if lockSettle > 0 {
			time.Sleep(lockSettle)
		}
		if ps, err = listPanes(ctx, s, container); err != nil {
			return "", err
		}
		if q, alive := findPane(ps, func(q pane) bool { return q.ID == p.ID }); !alive || q.Dead {
			if alive {
				s.Run(context.WithoutCancel(ctx), "kill-pane", "-t", p.ID)
			}
			return "", fmt.Errorf("the %s console of lock %s did not attach (docker attach ended at once): check that `lox up` runs it, then lox attach %s", id, c.Instance, c.Instance)
		}
		status = "opened"
	}
	if err := m.showLock(ctx, s, ps, p); err != nil {
		return "", err
	}
	if ps, err = listPanes(ctx, s, container); err == nil {
		m.publish(container, ps)
	}
	return status, nil
}

// newLockPane starts a console's pane in its parking window: a short history (a session option
// while the pane is made: tmux sizes a pane's history when it makes it; lea's conf sets
// history-limit globally only, so unsetting the session's leaves it as it was), tagged and
// closing when its process exits (remain-on-exit off) before the console runs in it: the pane is
// made running the placeholder, then respawned into the attach. An attach that ends at once leaves
// no dead pane, nor its screen.
func (m *Manager) newLockPane(ctx context.Context, s Session, container, id string, argv []string) (pane, error) {
	target := "=" + container + ":"
	if _, err := s.Run(ctx, "set-option", "-t", target, "history-limit", lockHistory); err != nil {
		return pane{}, err
	}
	out, err := s.Run(ctx, append([]string{"new-window", "-d", "-t", target, "-n", lockWindow(id), "-P", "-F", "#{pane_id}", "--"},
		m.placeholderCmd()...)...)
	if _, uerr := s.Run(ctx, "set-option", "-u", "-t", target, "history-limit"); err == nil {
		err = uerr
	}
	if err == nil && len(out) != 1 {
		err = fmt.Errorf("tmux did not print the pane id of the %s console", id)
	}
	if err == nil {
		for _, o := range [][2]string{{optRole, roleLock}, {optLock, id}, {"remain-on-exit", "off"}} {
			if _, err = s.Run(ctx, "set-option", "-p", "-t", out[0], o[0], o[1]); err != nil {
				break
			}
		}
	}
	if err == nil {
		_, err = s.Run(ctx, append([]string{"respawn-pane", "-k", "-t", out[0], "--"}, argv...)...)
	}
	if err != nil {
		if len(out) == 1 {
			s.Run(context.WithoutCancel(ctx), "kill-pane", "-t", out[0])
		}
		return pane{}, err
	}
	return pane{ID: out[0], Role: roleLock, Lock: id}, nil
}

// showLock puts p in the lock slot, under the slot beside the deck; the console there before
// parks. The keyboard stays where it is.
func (m *Manager) showLock(ctx context.Context, s Session, ps []pane, p pane) error {
	deck, slot, ok := deckAndSlot(ps)
	if !ok {
		return nil // no slot to show it under: it stays in its window
	}
	if p.Window == deck.Window {
		return nil
	}
	for _, q := range ps {
		if q.Role == roleLock && q.Window == deck.Window {
			if _, err := s.Run(ctx, "break-pane", "-d", "-s", q.ID, "-n", lockWindow(q.Lock)); err != nil {
				return err
			}
		}
	}
	if _, err := s.Run(ctx, "join-pane", "-d", "-v", "-l", lockSlotSize, "-s", p.ID, "-t", slot.ID); err != nil {
		return err
	}
	_, err := s.Run(ctx, "resize-pane", "-t", deck.ID, "-x", strconv.Itoa(DeckWidth))
	return err
}

// LockClose closes tool's console pane for profile: the attach ends (no signal reaches the
// console), lox up's console keeps running. It returns "closed", or "not-open" when the deck has
// no such pane.
func (m *Manager) LockClose(ctx context.Context, container, tool, profile string) (string, error) {
	if !containerRe.MatchString(container) {
		return "", fmt.Errorf("invalid container name %q", container)
	}
	id, err := lockID(tool, profile)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.deckSession(ctx, container)
	if err != nil {
		return "", err
	}
	ps, err := listPanes(ctx, s, container)
	if err != nil {
		return "", err
	}
	p, has := byLock(ps, id)
	if !has {
		return "not-open", nil
	}
	if _, err := s.Run(ctx, "kill-pane", "-t", p.ID); err != nil {
		return "", err
	}
	if ps, err = listPanes(ctx, s, container); err == nil {
		m.publish(container, ps)
	}
	return "closed", nil
}

// LockList lists the consoles of the container's deck.
func (m *Manager) LockList(ctx context.Context, container string) ([]LockRow, error) {
	if !containerRe.MatchString(container) {
		return nil, fmt.Errorf("invalid container name %q", container)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.deckSession(ctx, container)
	if err != nil {
		return nil, err
	}
	ps, err := listPanes(ctx, s, container)
	if err != nil {
		return nil, err
	}
	deck, _ := byRole(ps, roleDeck)
	out := []LockRow{}
	for _, p := range ps {
		if p.Role == roleLock && !p.Dead {
			out = append(out, LockRow{ID: p.Lock, Shown: p.Window == deck.Window})
		}
	}
	return out, nil
}

// LockCheckRequest checks an agent's request that the user open tool's console for profile in
// the container's deck (lock_request): a lock tool lea opens, a profile on the user's allow-list
// for agents, and a deck that runs to show the request. It never touches a console nor a pane,
// and says nothing of the consoles that run.
func (m *Manager) LockCheckRequest(ctx context.Context, container, tool, profile string) error {
	if !containerRe.MatchString(container) {
		return fmt.Errorf("invalid container name %q", container)
	}
	if profile == "" {
		return errors.New("a profile is required: an agent asks for one of the profiles the user allowed")
	}
	if _, err := lockID(tool, profile); err != nil {
		return err
	}
	if m.cfg.LockSpec == nil {
		return errors.New("lock consoles are not available in this daemon")
	}
	spec, err := m.cfg.LockSpec(tool)
	if err != nil {
		return err
	}
	if spec.EnabledFor != nil {
		projects, err := m.cfg.Containers.Projects(ctx, container)
		if err != nil {
			return err
		}
		spec.Enabled = spec.EnabledFor(projects)
	}
	if !spec.Enabled {
		return fmt.Errorf("%s is off", tool)
	}
	allowed := false
	for _, p := range spec.AgentProfiles {
		allowed = allowed || p == profile
	}
	if !allowed {
		return fmt.Errorf("profile %q is not one agents may ask for (the user lists them in LEA_%s_AGENT_PROFILES)",
			profile, strings.ToUpper(tool))
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, err = m.deckSession(ctx, container)
	return err
}

// errNoDeck: lock consoles open in a deck that runs, never in a session made for them.
var errNoDeck = errors.New("no deck runs for this projects directory: open it first (lea)")

// deckSession is the container's session, never created: a console opens in a deck the user has.
// Callers hold m.mu.
func (m *Manager) deckSession(ctx context.Context, container string) (Session, error) {
	if _, held := m.conns[container]; !held {
		names, err := m.cfg.Term.Sessions(ctx)
		if err != nil {
			return nil, err
		}
		found := false
		for _, n := range names {
			found = found || n == container
		}
		if !found {
			return nil, errNoDeck
		}
	}
	return m.session(ctx, container)
}

// stopConsoles stops the consoles a deck of container started under an earlier lea (lea stop):
// lox up's consoles are lox up's. Callers hold m.mu.
func (m *Manager) stopConsoles(ctx context.Context, container string) {
	if m.cfg.Consoles == nil {
		return
	}
	if err := m.cfg.Consoles.StopConsolesOf(ctx, container); err != nil {
		log.Printf("lea daemon: stop the lock consoles of %s: %v", container, err)
	}
}
