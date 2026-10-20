// Package deck is the lea deck: the agent column drawn in the left pane of a lea tmux session.
// It keeps no facts of its own: agents and their states come from the daemon (agents_watch), and
// every action is a daemon call. The only local state is the cursor, which done agents this
// deck has shown (spec §3.1: "seen" is per deck client), the rename field and the key list.
package deck

import (
	"context"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DavidGodefroid/lea/internal/settings"
	"github.com/DavidGodefroid/lea/internal/textsafe"
)

// KindShell marks a row that is a shell in the container (s), not an agent (agents.KindShell).
const KindShell = "shell"

// maxLabel is the longest label the rename field takes (agents.MaxLabel).
const maxLabel = 24

// Agent is one row: an agent, or a shell (Kind KindShell). Label is the user's display name for
// an agent ("" when none): the row shows it instead of Name.
type Agent struct {
	Key      string    `json:"key"`
	Name     string    `json:"name"`
	Kind     string    `json:"kind,omitempty"`
	Label    string    `json:"label,omitempty"`
	Profile  string    `json:"profile"`
	State    string    `json:"state"`
	Workdir  string    `json:"workdir"`
	Updated  int64     `json:"updated"`
	Created  int64     `json:"created,omitempty"`
	Role     string    `json:"role"`
	Parent   string    `json:"parent"`
	Repo     string    `json:"repo"`
	Branch   string    `json:"branch"`
	Status   string    `json:"status"`
	Unread   int       `json:"unread"`
	ForUser  int       `json:"for_user"`
	CtxPct   int       `json:"ctx_pct"`
	CostUSD  float64   `json:"cost_usd"`
	Approval *Approval `json:"approval,omitempty"`
	// Paused: the user froze the agent (p); it continues where it stopped on resume.
	Paused bool `json:"paused,omitempty"`
	// Subagents counts the subagents the agent still runs (agents.Row.Subagents).
	Subagents int `json:"subagents,omitempty"`
}

// shownState is the state a row shows: an idle agent whose subagents still run is working.
func shownState(a Agent) string {
	if a.State == "idle" && a.Subagents > 0 {
		return "working"
	}
	return a.State
}

// Approval is the permission request an agent waits on (agents.Approval).
type Approval struct {
	ID      int64  `json:"id"`
	Tool    string `json:"tool"`
	Summary string `json:"summary"`
	Always  bool   `json:"always"`
}

type Snapshot struct {
	Version int64   `json:"version"`
	Shown   string  `json:"shown"`
	Agents  []Agent `json:"agents"`
	// Sound is the daemon's sound switch: "on", "off", or "" when no sound plays on the host.
	Sound string `json:"sound,omitempty"`
}

// API is what the deck asks of the daemon.
type API interface {
	Watch(ctx context.Context, container string, since int64) (Snapshot, error)
	New(ctx context.Context, container string) (Agent, error)
	Show(ctx context.Context, key string, focus bool) error
	Stop(ctx context.Context, key string) error
	// RemoveEnded removes an agent only while it is ended (x on a done row: no y).
	RemoveEnded(ctx context.Context, key string) error
	SetRole(ctx context.Context, key, role string) error
	Decide(ctx context.Context, id int64, verdict string) error
	Detach(ctx context.Context, container string) error
	Popup(ctx context.Context, key string, compose bool) error
	SetLabel(ctx context.Context, key, label string) error
	// SetPaused freezes an agent (the deck's p) or lets a frozen one continue.
	SetPaused(ctx context.Context, key string, paused bool) error
	OpenShell(ctx context.Context, container string) (Agent, error)
	// SplitShell opens a shell beside an agent, below it or at its right.
	SplitShell(ctx context.Context, key string, below bool) (Agent, error)
	// SetSound switches the daemon's sounds and answers the new state.
	SetSound(ctx context.Context, on bool) (string, error)
}

// isShell reports whether a row is a shell.
func isShell(a Agent) bool { return a.Kind == KindShell }

// pausable reports whether p applies to a row: an agent whose process runs (a shell is no agent;
// an ended or stopped agent has no process to freeze).
func pausable(a Agent) bool { return !isShell(a) && a.State != "done" && a.State != "stopped" }

// display is the name a row shows: the label when the user gave one.
func display(a Agent) string {
	if a.Label != "" {
		return a.Label
	}
	return a.Name
}

const (
	showDelay   = 150 * time.Millisecond
	frameDelay  = 100 * time.Millisecond
	retryDelay  = time.Second
	callTimeout = 60 * time.Second
	watchWait   = 25 // seconds the daemon holds one watch

	nothingNeedsYou = "nothing needs you"
)

type (
	snapshotMsg struct {
		snap Snapshot
		err  error
	}
	retryMsg struct{}
	frameMsg struct{}
	showMsg  struct{ seq int }
	newMsg   struct {
		agent Agent
		err   error
	}
	doneMsg  struct{ err error }
	soundMsg struct {
		sound string
		err   error
	}
)

type Model struct {
	api       API
	container string
	ascii     bool
	now       func() time.Time

	agents        []Agent // in launch order, shells last
	version       int64
	shown         string
	selected      string
	seen          map[string]int64 // key → Updated of a done agent this deck has shown
	confirm       string           // key the stop modal asks about (confirm.go)
	confirmYes    bool             // the modal's focus is on Confirm, not Cancel
	roleFor       string           // key waiting for a role key (l, d, r)
	editFor       string           // key whose label the rename field (e) edits
	draft         []rune           // the rename field's text
	help          bool             // the key list (?) is shown
	asked         map[string]bool  // agents this deck asked the daemon to show, not yet seen shown
	lastAsked     string           // the latest of them
	status        string
	notice        func() string // the update notice (WithNotice)
	noticeText    string        // its latest text, shown while there is no status
	sound         string        // the daemon's sound switch (Snapshot.Sound)
	seq           int           // the latest selection; older delayed shows are dropped
	frame         int           // the working spinner's frame
	intro         int           // frames left of the logo's start animation (WithIntro)
	focused       bool          // the deck's pane has the keyboard (focus events)
	menuFor       string        // key whose menu (right click) is open
	menuSel       int           // the menu's highlighted item
	settings      SettingsStore // the settings screen's store (WithSettings), nil when none
	settingsOff   string        // why there is none
	setOpen       bool          // the settings screen (,) is shown
	setList       []settings.Setting
	setSel        int    // its selected setting
	setEditing    bool   // the selected text setting's field is open
	setDraft      []rune // the field's text
	hoverFor      string // key of the row under the pointer, "" when none
	hoverSeq      int    // the latest pointer move; older fades are dropped
	ticking       bool   // a frame tick is pending
	width, height int
}

func New(api API, container string, ascii bool, now func() time.Time) Model {
	return Model{api: api, container: container, ascii: ascii, now: now, seen: map[string]int64{}}
}

// The logo's animation, in frames of frameDelay: the start lights a dot, then the name, every
// introStep frames; the working wave moves every waveStep frames.
const (
	introStep   = 3
	introFrames = 5 * introStep
	waveStep    = 3
)

// WithIntro plays the logo's start animation (the deck run by lea; tests see the logo still).
func (m Model) WithIntro() Model {
	m.intro, m.ticking = introFrames, true
	return m
}

func (m Model) Init() tea.Cmd {
	if m.intro > 0 {
		return tea.Batch(m.watch(), tea.Tick(frameDelay, func(time.Time) tea.Msg { return frameMsg{} }))
	}
	return m.watch()
}

// working reports whether an agent works: the spinner and the logo's wave then move.
func (m Model) working() bool {
	for _, a := range m.agents {
		if shownState(a) == "working" && !isShell(a) && !a.Paused {
			return true
		}
	}
	return false
}

func (m Model) watch() tea.Cmd {
	api, c, v := m.api, m.container, m.version
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), (watchWait+10)*time.Second)
		defer cancel()
		snap, err := api.Watch(ctx, c, v)
		return snapshotMsg{snap, err}
	}
}

// animate schedules the next frame (spinner, logo) while an agent works or the logo's start
// animation runs, and none is pending. It sets
// m.ticking through the pointer: Update returns the updated model.
func (m *Model) animate() tea.Cmd {
	if m.ticking || (m.intro == 0 && !m.working()) {
		return nil
	}
	m.ticking = true
	return tea.Tick(frameDelay, func(time.Time) tea.Msg { return frameMsg{} })
}

func (m Model) call(fn func(ctx context.Context) error) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		return doneMsg{fn(ctx)}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.FocusMsg:
		m.focused = true
		return m, nil
	case tea.BlurMsg:
		m.focused = false
		return m, nil
	case snapshotMsg:
		if msg.err != nil {
			m.status = "daemon unreachable, retrying: " + oneLine(msg.err.Error())
			return m, tea.Tick(retryDelay, func(time.Time) tea.Msg { return retryMsg{} })
		}
		if strings.HasPrefix(m.status, "daemon unreachable") {
			m.status = ""
		}
		if m.notice != nil {
			m.noticeText = m.notice()
		}
		var anim tea.Cmd
		if m.apply(msg.snap) {
			// The row in the slot went (x, or a shell that exited): the placeholder holds the
			// slot, and the row now selected follows into it, as after a move.
			m.seq++
			seq := m.seq
			anim = m.animate()
			return m, tea.Batch(m.watch(), tea.Tick(showDelay, func(time.Time) tea.Msg { return showMsg{seq} }), anim)
		}
		anim = m.animate()
		return m, tea.Batch(m.watch(), anim)
	case frameMsg:
		m.ticking = false
		m.frame++
		if m.intro > 0 {
			m.intro--
		}
		return m, m.animate()
	case retryMsg:
		return m, m.watch()
	case showMsg:
		if msg.seq != m.seq || m.selected == "" {
			return m, nil
		}
		key, api := m.selected, m.api
		m.ask(key)
		return m, m.call(func(ctx context.Context) error { return api.Show(ctx, key, false) })
	case newMsg:
		if msg.err != nil {
			m.status = oneLine(msg.err.Error())
			return m, nil
		}
		m.selected, m.status = msg.agent.Key, ""
		m.ask(msg.agent.Key)
		return m, nil
	case doneMsg:
		if msg.err != nil {
			m.status = oneLine(msg.err.Error())
		}
		return m, nil
	case soundMsg:
		if msg.err != nil {
			m.status = oneLine(msg.err.Error())
			return m, nil
		}
		m.sound, m.status = msg.sound, "sounds "+msg.sound
		return m, nil
	case tea.KeyMsg:
		if m.help { // any key closes the key list, and does nothing else
			m.help = false
			return m, nil
		}
		if m.setOpen {
			return m.settingsKey(msg)
		}
		if m.editFor != "" {
			return m.editKey(msg)
		}
		if m.menuFor != "" {
			return m.menuKey(msg.String())
		}
		return m.key(msg.String())
	case tea.MouseMsg:
		return m.mouse(msg)
	case hoverEndMsg:
		if msg.seq == m.hoverSeq {
			m.hoverFor = ""
		}
		return m, nil
	}
	return m, nil
}

// editKey is a key typed in the rename field: enter saves (an empty field clears the label), esc
// cancels; every other key is text. Control and invisible characters are dropped, and the field
// holds at most maxLabel characters (the daemon checks again).
func (m Model) editKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch s := k.String(); {
	case s == "esc" || s == "ctrl+c":
		m.editFor, m.draft = "", nil
	case s == "enter":
		key, label, api := m.editFor, string(m.draft), m.api
		m.editFor, m.draft = "", nil
		return m, m.call(func(ctx context.Context) error { return api.SetLabel(ctx, key, label) })
	case s == "backspace":
		if len(m.draft) > 0 {
			m.draft = m.draft[:len(m.draft)-1]
		}
	case s == "ctrl+u":
		m.draft = nil
	case k.Type == tea.KeySpace:
		m.typeRune(' ')
	case k.Type == tea.KeyRunes:
		for _, r := range k.Runes {
			m.typeRune(r)
		}
	}
	return m, nil
}

func (m *Model) typeRune(r rune) {
	if !textsafe.Unsafe(r) && len(m.draft) < maxLabel {
		m.draft = append(m.draft, r)
	}
}

// ask records a show this deck requested. A snapshot may still report an earlier one: only the
// latest request ends the bookkeeping.
func (m *Model) ask(key string) {
	if m.asked == nil {
		m.asked = map[string]bool{}
	}
	m.asked[key], m.lastAsked = true, key
}

// apply takes a snapshot in. It reports whether the selected row went while the placeholder holds
// the slot, so that the row selected in its place is to be shown.
func (m *Model) apply(s Snapshot) bool {
	before, was := m.shown, m.selected
	m.version, m.shown, m.sound = s.Version, s.Shown, s.Sound
	for _, a := range s.Agents {
		if a.Key == s.Shown && a.State == "done" {
			m.seen[a.Key] = a.Updated
		}
	}
	prev, at := m.agents, m.index(m.selected)
	m.agents = sortAgents(s.Agents)
	// An agent shown from elsewhere (lea new in another terminal, another deck client) takes
	// the selection, so the row under the cursor is the one in the slot. A show this deck asked
	// for does not: the cursor may already have moved on.
	if s.Shown != before && s.Shown != "" && !m.asked[s.Shown] && m.index(s.Shown) >= 0 {
		m.selected = s.Shown
	}
	if s.Shown == m.lastAsked {
		m.asked, m.lastAsked = nil, ""
	}
	if m.index(m.selected) < 0 {
		m.selected = m.neighbour(prev, at)
		if m.selected == "" && m.index(s.Shown) >= 0 {
			m.selected = s.Shown
		} else if m.selected == "" && len(m.agents) > 0 {
			m.selected = m.agents[0].Key
		}
	}
	if m.confirm != "" && m.index(m.confirm) < 0 {
		m.confirm, m.confirmYes = "", false
	}
	if m.roleFor != "" && m.index(m.roleFor) < 0 {
		m.roleFor, m.status = "", ""
	}
	if m.menuFor != "" && m.menuFor != deckMenu && m.index(m.menuFor) < 0 {
		m.menuFor = ""
	}
	if m.editFor != "" && m.index(m.editFor) < 0 {
		m.editFor, m.draft = "", nil
	}
	return was != "" && m.index(was) < 0 && m.selected != "" && s.Shown == ""
}

// neighbour is the row that takes the place of prev[at] when it disappears: the next row of the
// previous display order that is still listed, else the previous one ("" when none is left, or
// when nothing was selected).
func (m Model) neighbour(prev []Agent, at int) string {
	if at < 0 {
		return ""
	}
	for i := at + 1; i < len(prev); i++ {
		if m.index(prev[i].Key) >= 0 {
			return prev[i].Key
		}
	}
	for i := at - 1; i >= 0; i-- {
		if m.index(prev[i].Key) >= 0 {
			return prev[i].Key
		}
	}
	return ""
}

func (m Model) index(key string) int {
	for i, a := range m.agents {
		if a.Key == key {
			return i
		}
	}
	return -1
}

func (m Model) key(k string) (tea.Model, tea.Cmd) {
	if m.confirm != "" {
		return m.confirmKey(k)
	}
	if m.roleFor != "" {
		key, api := m.roleFor, m.api
		m.roleFor, m.status = "", ""
		if role, ok := roleKeys[k]; ok {
			return m, m.call(func(ctx context.Context) error { return api.SetRole(ctx, key, role) })
		}
		return m, nil
	}
	api, c := m.api, m.container
	switch k {
	case "j", "down":
		return m.move(m.index(m.selected) + 1)
	case "k", "up":
		return m.move(m.index(m.selected) - 1)
	case "enter":
		if m.selected == "" {
			return m, nil
		}
		m.seq++ // drops a pending delayed show
		key := m.selected
		m.ask(key)
		return m, m.call(func(ctx context.Context) error { return api.Show(ctx, key, true) })
	case "n":
		m.status = "starting a new agent…"
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
			defer cancel()
			a, err := api.New(ctx, c)
			return newMsg{a, err}
		}
	case "s":
		m.status = "opening a shell…"
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
			defer cancel()
			a, err := api.OpenShell(ctx, c)
			return newMsg{a, err}
		}
	case "-", "|":
		// Split, as Terminator does: - puts a shell below the agent, | at its right.
		a := m.selectedAgent()
		if a == nil || isShell(*a) {
			return m, nil
		}
		key, below := a.Key, k == "-"
		m.status = "opening a shell beside " + display(*a) + "…"
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
			defer cancel()
			sh, err := api.SplitShell(ctx, key, below)
			return newMsg{sh, err}
		}
	case "S":
		if m.sound == "" {
			m.status = "no sound player on this host"
			return m, nil
		}
		on := m.sound == "off"
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
			defer cancel()
			s, err := api.SetSound(ctx, on)
			return soundMsg{s, err}
		}
	case "?":
		m.help = true
		return m, nil
	case ",":
		return m.openSettings()
	case "e":
		if a := m.selectedAgent(); a != nil && !isShell(*a) {
			m.editFor, m.draft, m.status = a.Key, []rune(a.Label), ""
		}
		return m, nil
	case "m", "t":
		if a := m.selectedAgent(); a == nil || isShell(*a) {
			return m, nil
		}
		key, compose := m.selected, k == "m"
		return m, m.call(func(ctx context.Context) error { return api.Popup(ctx, key, compose) })
	case "x":
		i := m.index(m.selected)
		if i < 0 {
			return m, nil
		}
		key := m.selected
		// A shell is the user's own, and an ended agent has nothing left to stop: the row goes at
		// once. The ended agent only while it is still ended (a lead may reuse its name).
		if isShell(m.agents[i]) {
			return m, m.call(func(ctx context.Context) error { return api.Stop(ctx, key) })
		}
		if m.agents[i].State == "done" {
			return m, m.call(func(ctx context.Context) error { return api.RemoveEnded(ctx, key) })
		}
		m.confirm, m.confirmYes, m.status = key, false, ""
		return m, nil
	case "p":
		a := m.selectedAgent()
		if a == nil || !pausable(*a) {
			return m, nil
		}
		key, pause := a.Key, !a.Paused
		return m, m.call(func(ctx context.Context) error { return api.SetPaused(ctx, key, pause) })
	case "r":
		if a := m.selectedAgent(); a != nil && !isShell(*a) {
			m.roleFor = a.Key
			m.status = "role of " + display(*a) + ": l lead, d dev, r reviewer"
		}
		return m, nil
	case "a", "d", "A":
		a := m.selectedAgent()
		if a == nil || a.Approval == nil {
			return m, nil
		}
		if k == "A" && !a.Approval.Always {
			m.status = `no "always" rule offered: a allows once`
			return m, nil
		}
		id, verdict := a.Approval.ID, verdicts[k]
		return m, m.call(func(ctx context.Context) error { return api.Decide(ctx, id, verdict) })
	case " ":
		if i := m.nextNeedingYou(); i >= 0 {
			return m.move(i)
		}
		m.status = nothingNeedsYou
		return m, nil
	case "q", "ctrl+c":
		return m, m.call(func(ctx context.Context) error { return api.Detach(ctx, c) })
	}
	if len(k) == 1 && k[0] >= '1' && k[0] <= '9' {
		return m.move(int(k[0] - '1'))
	}
	return m, nil
}

// selectedAgent is the selected row, nil when none.
func (m Model) selectedAgent() *Agent {
	if i := m.index(m.selected); i >= 0 {
		return &m.agents[i]
	}
	return nil
}

// roleKeys are the answers to r (spec §4.2: change the selected agent's role; the user only).
var roleKeys = map[string]string{"l": "lead", "d": "dev", "r": "reviewer"}

var verdicts = map[string]string{"a": "allow", "d": "deny", "A": "always"}

// move selects row i; the slot follows after showDelay without further movement.
func (m Model) move(i int) (tea.Model, tea.Cmd) {
	if i < 0 || i >= len(m.agents) {
		return m, nil
	}
	if m.status == nothingNeedsYou {
		m.status = ""
	}
	m.selected = m.agents[i].Key
	m.seq++
	seq := m.seq
	return m, tea.Tick(showDelay, func(time.Time) tea.Msg { return showMsg{seq} })
}

// rank is the attention order of spec §4.1: blocked, done not yet seen, a message waiting for
// the user, working, idle (a seen done agent sorts with them), unknown; shells come last.
func rank(a Agent, seen map[string]int64) int {
	switch {
	case isShell(a):
		return 6
	case a.Paused: // frozen by the user: it neither needs them nor works
		return 4
	case a.State == "blocked":
		return 0
	case a.State == "done" && seen[a.Key] != a.Updated:
		return 1
	case a.ForUser > 0:
		return 2
	case shownState(a) == "working":
		return 3
	case a.State == "idle" || a.State == "done":
		return 4
	}
	return 5
}

// sortAgents lists the agents in launch order, so a row keeps its place while its state changes;
// shells come last. Agents started in the same second keep the daemon's order. space still finds
// the agents that need the user (rank).
func sortAgents(in []Agent) []Agent {
	out := append([]Agent(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		if si, sj := isShell(out[i]), isShell(out[j]); si != sj {
			return sj
		}
		return out[i].Created < out[j].Created
	})
	return out
}

// label is a row's short status.
func (m Model) label(a Agent) string {
	switch r := rank(a, m.seen); {
	case isShell(a):
		return "shell"
	case a.Paused:
		return "paused"
	case a.State == "blocked" && a.Approval == nil:
		return "see terminal" // the agent's own dialog asks: the deck has no card for it
	case a.State == "blocked":
		return "needs you"
	case r == 1:
		return "done, unseen"
	case r == 2:
		return "message for you"
	case a.State == "done":
		return "done"
	case a.State == "stopped": // its container stopped; the next one resumes it
		return "stopped"
	case a.State == "working" || a.State == "idle":
		return shownState(a)
	}
	return "unknown"
}

// nextNeedingYou is the first row after the selection (wrapping) that needs the user: blocked,
// done and not yet seen, or holding a message for the user (spec §4.2 space). -1 when none.
func (m Model) nextNeedingYou() int {
	n, at := len(m.agents), m.index(m.selected)
	for i := 1; i <= n; i++ {
		j := ((at+i)%n + n) % n
		if rank(m.agents[j], m.seen) <= 2 {
			return j
		}
	}
	return -1
}
