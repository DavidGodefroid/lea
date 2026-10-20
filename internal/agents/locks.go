package agents

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
)

// Lock consoles (issue #18, phases 0 and 1): the user opens a lock tool's console in a pane of
// the deck. Only the user: the methods below are host-only (the daemon's deck methods), no MCP
// tool reaches them, and the pane is never addressed by anything an agent can call. The console
// runs in its own container, from a fixed argv template filled with the host configuration
// (LockSpec) and a validated profile name; lea never types into it, reads it or relays an answer.
// Of the lock tools, only locksql has a container console: lockmq and lockfile consoles run in
// their own account's session on the host, which lockkit's isolation checks require (a pane of
// the user's tmux is refused there by design).

const (
	// optLock tags a lock console's pane with its id: "<tool>" or "<tool>/<profile>".
	optLock  = "@lea_lock"
	roleLock = "lock"
	// lockHistory is a lock pane's scrollback: results the user chose to display must not pile
	// up in the host's tmux; the pane and its history go when the console exits.
	lockHistory = "200"
	// lockOwnerLabel names the lea container whose deck opened a console: lea stop stops them.
	lockOwnerLabel = "lea.lock-for"
	// lockSlotSize is the lock slot's height, below the agent shown beside the deck.
	lockSlotSize = "40%"
)

var (
	lockProfileRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	lockImageRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]{0,254}$`)
	lockVaultRe   = regexp.MustCompile(`^https?://[A-Za-z0-9._~:/?#\[\]@!$&'()*+,;=%-]{1,512}$`)
)

// LockTools are the lock tools whose console lea opens in a pane.
var LockTools = []string{"locksql"}

// LockSpec is the host configuration of a lock tool's console: lea's config files, which only
// the user writes (never the projects directory's).
type LockSpec struct {
	Enabled   bool
	Image     string // the console image
	Projects  string // the projects directory: the console's --project
	VaultAddr string // VAULT_ADDR for the console, "" for none
}

// ConsoleState is what the container runtime knows of a console container.
type ConsoleState struct {
	Exists, Running bool
	Owner           string // its lea.lock-for label: the lea container whose deck opened it
}

// Consoles reaches the console containers (the real one is Docker's).
type Consoles interface {
	ConsoleState(ctx context.Context, name string) (ConsoleState, error)
	// RemoveConsole removes a stopped console container that is in the way of a new one.
	RemoveConsole(ctx context.Context, name string) error
	// StopConsole stops a console container; one that does not exist is not an error.
	StopConsole(ctx context.Context, name string) error
	// StopConsolesOf stops every console container a deck of owner opened.
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

// lockID checks tool and profile ("" for the console's own choice) and returns the pane's id
// and the console container's name.
func lockID(tool, profile string) (id, name string, err error) {
	if !lockTool(tool) {
		return "", "", fmt.Errorf("no lock console for %q: lea opens %s (lockmq and lockfile consoles run in their own account's session)",
			tool, strings.Join(LockTools, ", "))
	}
	if profile == "" {
		return tool, tool + "-console", nil
	}
	if !lockProfileRe.MatchString(profile) {
		return "", "", fmt.Errorf("invalid profile name %q (lowercase letters, digits, _ and -, at most 32)", profile)
	}
	return tool + "/" + profile, tool + "-console-" + profile, nil
}

// checkSpec refuses a configuration value that would change the argv's meaning: an option where
// the image goes, a mount separator in the projects directory.
func checkSpec(tool string, s LockSpec) error {
	if !s.Enabled {
		return fmt.Errorf("%s is off: lea init --%s turns it on", tool, tool)
	}
	if !lockImageRe.MatchString(s.Image) {
		return fmt.Errorf("invalid %s console image %q", tool, s.Image)
	}
	if !strings.HasPrefix(s.Projects, "/") || strings.ContainsAny(s.Projects, ":,") || hasControl(s.Projects) {
		return fmt.Errorf("invalid projects directory %q for the %s console", s.Projects, tool)
	}
	if s.VaultAddr != "" && !lockVaultRe.MatchString(s.VaultAddr) {
		return fmt.Errorf("invalid vault address %q for the %s console", s.VaultAddr, tool)
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

// lockCmd is a console's pane: the fixed template of `lea locksql`, with no log driver (with a
// tty, docker's json-file log would keep every screen for docker logs), labelled with the lea
// container whose deck opened it. Never a console flag: --auto, --allow-unmask, --show-results
// and the like stay the user's to type in a terminal of their own (lea locksql).
func (m *Manager) lockCmd(container, tool, profile, name string, s LockSpec) []string {
	cmd := []string{m.cfg.Docker, "run", "-it", "--rm", "--log-driver", "none", "--user", tool, "--name", name,
		"--label", lockOwnerLabel + "=" + container, "--add-host=host.docker.internal:host-gateway",
		"-v", s.Projects + ":" + s.Projects, "-v", tool + "-run:/run/" + tool, "-v", tool + "-home:/home/" + tool}
	if s.VaultAddr != "" {
		cmd = append(cmd, "-e", "VAULT_ADDR="+s.VaultAddr)
	}
	cmd = append(cmd, s.Image, "console", "--project", s.Projects)
	if profile != "" {
		cmd = append(cmd, "--profile", profile)
	}
	return cmd
}

func byLock(ps []pane, id string) (pane, bool) {
	return findPane(ps, func(p pane) bool { return p.Role == roleLock && p.Lock == id })
}

// lockWindow is the parking window's name of a console.
func lockWindow(id string) string { return "lock-" + strings.ReplaceAll(id, "/", "-") }

// LockOpen opens tool's console for profile in the container's deck, or shows the one already
// open: in the lock slot, under the agent beside the deck, without the keyboard (a console that
// took it while the user typed into an agent would get stray keys). The console shown there
// before parks in its own window. It returns "opened" or "already-open".
func (m *Manager) LockOpen(ctx context.Context, container, tool, profile string) (string, error) {
	if !containerRe.MatchString(container) {
		return "", fmt.Errorf("invalid container name %q", container)
	}
	id, name, err := lockID(tool, profile)
	if err != nil {
		return "", err
	}
	if m.cfg.LockSpec == nil || m.cfg.Consoles == nil {
		return "", errors.New("lock consoles are not available in this daemon")
	}
	spec, err := m.cfg.LockSpec(tool)
	if err != nil {
		return "", err
	}
	if err := checkSpec(tool, spec); err != nil {
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
	status := "already-open"
	p, ok := byLock(ps, id)
	if ok && p.Dead { // remain-on-exit was not off yet when it exited
		if _, err := s.Run(ctx, "kill-pane", "-t", p.ID); err != nil {
			return "", err
		}
		ok = false
	}
	if !ok {
		st, err := m.cfg.Consoles.ConsoleState(ctx, name)
		if err != nil {
			return "", err
		}
		if st.Running {
			// Not this deck's (no pane here): lea locksql in a terminal, another lea's deck, or
			// a console left without its pane. lea neither takes it over nor stops it.
			where := "outside lea's decks (lea locksql in a terminal?)"
			if st.Owner == container {
				where = "without its pane (lea lock close " + strings.TrimSpace(tool+" "+profile) + " stops it)"
			} else if st.Owner != "" {
				where = "in the deck of " + st.Owner
			}
			return "", fmt.Errorf("the %s console %s already runs %s", id, name, where)
		}
		if st.Exists {
			if err := m.cfg.Consoles.RemoveConsole(ctx, name); err != nil {
				return "", err
			}
		}
		if p, err = m.newLockPane(ctx, s, container, id, m.lockCmd(container, tool, profile, name, spec)); err != nil {
			return "", err
		}
		status = "opened"
		if ps, err = listPanes(ctx, s, container); err != nil {
			return "", err
		}
	}
	if err := m.showLock(ctx, s, ps, p); err != nil {
		return "", err
	}
	if ps, err = listPanes(ctx, s, container); err == nil {
		m.publish(container, ps)
	}
	return status, nil
}

// newLockPane starts a console's pane in its parking window: tagged, a short history (a session
// option while the pane is made: tmux sizes a pane's history when it makes it), and closing when
// the console exits (remain-on-exit off), its history with it.
func (m *Manager) newLockPane(ctx context.Context, s Session, container, id string, argv []string) (pane, error) {
	target := "=" + container + ":"
	if _, err := s.Run(ctx, "set-option", "-t", target, "history-limit", lockHistory); err != nil {
		return pane{}, err
	}
	out, err := s.Run(ctx, append([]string{"new-window", "-d", "-t", target, "-n", lockWindow(id), "-P", "-F", "#{pane_id}", "--"}, argv...)...)
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

// LockClose stops tool's console for profile and closes its pane. It returns "closed", or
// "not-open" when the deck has no such console and none runs.
func (m *Manager) LockClose(ctx context.Context, container, tool, profile string) (string, error) {
	if !containerRe.MatchString(container) {
		return "", fmt.Errorf("invalid container name %q", container)
	}
	id, name, err := lockID(tool, profile)
	if err != nil {
		return "", err
	}
	if m.cfg.Consoles == nil {
		return "", errors.New("lock consoles are not available in this daemon")
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
	st, err := m.cfg.Consoles.ConsoleState(ctx, name)
	if err != nil {
		return "", err
	}
	if st.Exists && st.Owner != container && !has {
		return "", fmt.Errorf("the %s console %s is not this deck's: stop it where it runs", id, name)
	}
	if !has && !st.Exists {
		return "not-open", nil
	}
	if st.Exists {
		if err := m.cfg.Consoles.StopConsole(ctx, name); err != nil {
			return "", err
		}
	}
	if has {
		if ps, err = listPanes(ctx, s, container); err != nil {
			return "", err
		}
		if p, has = findPane(ps, func(q pane) bool { return q.ID == p.ID }); has {
			if _, err := s.Run(ctx, "kill-pane", "-t", p.ID); err != nil {
				return "", err
			}
		}
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

// stopConsoles stops the consoles a deck of container opened (lea stop). Callers hold m.mu.
func (m *Manager) stopConsoles(ctx context.Context, container string) {
	if m.cfg.Consoles == nil {
		return
	}
	if err := m.cfg.Consoles.StopConsolesOf(ctx, container); err != nil {
		log.Printf("lea daemon: stop the lock consoles of %s: %v", container, err)
	}
}
