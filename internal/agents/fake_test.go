package agents

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DavidGodefroid/lea/internal/tmux"
)

// fakeSession models the part of a tmux session the manager uses: panes with a window, a dead
// flag, user options and a command, and windows with a name. Within a window, panes sit left to
// right in the order of f.panes, unless the window is stacked (top to bottom: every pane is at
// the left). Window order is not modelled: the manager finds panes by their options.
type fpane struct {
	id, win     string
	dead        bool
	role, agent string
	shell       string
	split       string // @lea_split
	lock        string // @lea_lock
	cmd         []string
	width       int
	// remainOff: the pane's remain-on-exit is off (a shell): it closes, never dies, on exit.
	remainOff bool
	// remainFormat is the pane's own remain-on-exit-format ("": the global one).
	remainFormat string
}

type fakeSession struct {
	mu      sync.Mutex
	panes   []*fpane
	opts    map[string]string
	log     []string
	n       int
	events  chan tmux.Event
	clients []string
	names   map[string]string // window id → name
	marked  map[string]bool   // window id → its @lea_deck mark is set
	stacked map[string]bool   // window id → panes split top to bottom
	fail    string            // a command name that fails
	onFail  func()            // called when fail triggers (e.g. to cancel the request)
}

func newFakeSession(deckCmd []string) *fakeSession {
	return &fakeSession{opts: map[string]string{}, events: make(chan tmux.Event, 16), n: 1,
		panes: []*fpane{{id: "%0", win: "@0", cmd: deckCmd, width: DeckWidth}},
		names: map[string]string{"@0": "deck"}, stacked: map[string]bool{}, marked: map[string]bool{}}
}

// atLeft reports whether p touches the left edge of its window.
func (f *fakeSession) atLeft(p *fpane) bool {
	return f.stacked[p.win] || f.find(func(q *fpane) bool { return q.win == p.win }) == p
}

// index is p's position in f.panes.
func (f *fakeSession) index(p *fpane) int {
	for i, q := range f.panes {
		if q == p {
			return i
		}
	}
	return -1
}

// move puts p right after q (at the end when q is nil).
func (f *fakeSession) move(p, q *fpane) {
	f.panes = append(f.panes[:f.index(p)], f.panes[f.index(p)+1:]...)
	if q == nil {
		f.panes = append(f.panes, p)
		return
	}
	i := f.index(q) + 1
	f.panes = append(f.panes[:i], append([]*fpane{p}, f.panes[i:]...)...)
}

// windows counts the windows named name.
func (f *fakeSession) windows(name string) int {
	n := 0
	for w, wn := range f.names {
		if wn == name && f.find(func(p *fpane) bool { return p.win == w }) != nil {
			n++
		}
	}
	return n
}

// markedWindows lists the windows holding a pane whose @lea_deck mark is set.
func (f *fakeSession) markedWindows() []string {
	var out []string
	for w, on := range f.marked {
		if on && f.find(func(p *fpane) bool { return p.win == w }) != nil {
			out = append(out, w)
		}
	}
	return out
}

func (f *fakeSession) Events() <-chan tmux.Event { f.mu.Lock(); defer f.mu.Unlock(); return f.events }
func (f *fakeSession) Close() error              { return nil }
func (f *fakeSession) emit(ev tmux.Event)        { f.mu.Lock(); ch := f.events; f.mu.Unlock(); ch <- ev }
func (f *fakeSession) closeEvents()              { f.mu.Lock(); close(f.events); f.mu.Unlock() }

func (f *fakeSession) next() int { f.n++; return f.n - 1 }

func (f *fakeSession) find(match func(*fpane) bool) *fpane {
	for _, p := range f.panes {
		if match(p) {
			return p
		}
	}
	return nil
}
func (f *fakeSession) pane(id string) *fpane {
	return f.find(func(p *fpane) bool { return p.id == id })
}
func (f *fakeSession) byRole(r string) *fpane {
	return f.find(func(p *fpane) bool { return p.role == r })
}
func (f *fakeSession) byAgent(n string) *fpane {
	return f.find(func(p *fpane) bool { return p.agent == n })
}
func (f *fakeSession) byShell(n string) *fpane {
	return f.find(func(p *fpane) bool { return p.shell == n })
}

// exit ends the process of pane p as tmux would: a pane with remain-on-exit off closes, any
// other stays dead.
func (f *fakeSession) exit(p *fpane) {
	if !p.remainOff {
		p.dead = true
		return
	}
	f.panes = append(f.panes[:f.index(p)], f.panes[f.index(p)+1:]...)
}

// slot is the pane next to the deck in the deck window.
func (f *fakeSession) slot() *fpane {
	d := f.byRole(roleDeck)
	return f.find(func(p *fpane) bool { return d != nil && p.win == d.win && p != d })
}

// locked runs fn with the session locked (tests mutate panes through it).
func (f *fakeSession) locked(fn func()) { f.mu.Lock(); defer f.mu.Unlock(); fn() }

func (f *fakeSession) logged(line string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, l := range f.log {
		if l == line {
			return true
		}
	}
	return false
}

func (f *fakeSession) Run(ctx context.Context, args ...string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.log = append(f.log, strings.Join(args, " "))
	if f.fail != "" && args[0] == f.fail {
		if f.onFail != nil {
			f.onFail()
		}
		return nil, errors.New("fake tmux failure")
	}
	flag := func(name string) string {
		for i, a := range args {
			if a == "--" {
				break
			}
			if a == name && i+1 < len(args) {
				return args[i+1]
			}
		}
		return ""
	}
	argv := func() []string {
		for i, a := range args {
			if a == "--" {
				return append([]string(nil), args[i+1:]...)
			}
		}
		return nil
	}
	add := func(win string) *fpane {
		p := &fpane{id: fmt.Sprintf("%%%d", f.next()), win: win, cmd: argv()}
		f.panes = append(f.panes, p)
		return p
	}
	must := func(id string) (*fpane, error) {
		if p := f.pane(id); p != nil {
			return p, nil
		}
		return nil, fmt.Errorf("can't find pane: %s", id)
	}
	switch args[0] {
	case "list-panes":
		var out []string
		for _, p := range f.panes {
			d := "0"
			if p.dead {
				d = "1"
			}
			l := "0"
			if f.atLeft(p) {
				l = "1"
			}
			m := ""
			if f.marked[p.win] {
				m = "1"
			}
			out = append(out, strings.Join([]string{p.id, p.win, d, p.role, p.agent, fmt.Sprint(p.width), l, p.shell, m, p.split, p.lock}, "|"))
		}
		return out, nil
	case "set-option":
		if args[1] == "-w" { // set-option -w [-u] -t PANE @lea_deck [1]: the pane's window
			unset := args[2] == "-u"
			p, err := must(flag("-t"))
			if err != nil {
				return nil, err
			}
			if args[len(args)-1] != optDeck && args[len(args)-2] != optDeck {
				return nil, fmt.Errorf("fake tmux: unexpected window option %q", args)
			}
			f.marked[p.win] = !unset
			return nil, nil
		}
		if args[1] == "-pq" { // set-option -pq -t PANE NAME VALUE: an option older tmux lacks
			args = append([]string{args[0], "-p"}, args[2:]...)
		}
		if args[1] == "-p" { // set-option -p -t PANE NAME VALUE
			p, err := must(args[3])
			if err != nil {
				return nil, err
			}
			switch args[4] {
			case optRole:
				p.role = args[5]
			case optAgent:
				p.agent = args[5]
			case optShell:
				p.shell = args[5]
			case optSplit:
				p.split = args[5]
			case optLock:
				p.lock = args[5]
			case "remain-on-exit":
				p.remainOff = args[5] == "off"
			case "remain-on-exit-format":
				p.remainFormat = args[5]
			}
			return nil, nil
		}
		if args[1] == "-u" { // set-option -u -t =SESSION: NAME
			if err := sessionTarget(args[3]); err != nil {
				return nil, err
			}
			delete(f.opts, args[4])
			return nil, nil
		}
		// set-option -t =SESSION: NAME VALUE (tmux 3.5a needs the trailing colon for session options)
		if err := sessionTarget(args[2]); err != nil {
			return nil, err
		}
		f.opts[args[3]] = args[4]
		return nil, nil
	case "display-message": // display-message -p -t =SESSION: '#{NAME}'
		if err := sessionTarget(flag("-t")); err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(strings.TrimPrefix(args[len(args)-1], "#{"), "}")
		return []string{f.opts[name]}, nil
	case "split-window":
		t, err := must(flag("-t"))
		if err != nil {
			return nil, err
		}
		p := add(t.win)
		f.move(p, t)
		return []string{p.id}, nil
	case "new-window":
		w := fmt.Sprintf("@%d", f.next())
		f.names[w] = flag("-n")
		return []string{add(w).id}, nil
	case "break-pane": // break-pane -d -s PANE -n NAME
		p, err := must(flag("-s"))
		if err != nil {
			return nil, err
		}
		p.win = fmt.Sprintf("@%d", f.next())
		f.names[p.win] = flag("-n")
		f.move(p, nil)
		return nil, nil
	case "select-layout": // select-layout -t PANE even-horizontal
		p, err := must(flag("-t"))
		if err != nil {
			return nil, err
		}
		if args[len(args)-1] != "even-horizontal" {
			return nil, fmt.Errorf("fake tmux: unexpected layout %q", args)
		}
		f.stacked[p.win] = false
		return nil, nil
	case "swap-pane":
		a, err := must(flag("-s"))
		if err != nil {
			return nil, err
		}
		b, err := must(flag("-t"))
		if err != nil {
			return nil, err
		}
		a.win, b.win = b.win, a.win
		i, j := f.index(a), f.index(b)
		f.panes[i], f.panes[j] = b, a
		return nil, nil
	case "join-pane":
		a, err := must(flag("-s"))
		if err != nil {
			return nil, err
		}
		b, err := must(flag("-t"))
		if err != nil {
			return nil, err
		}
		a.win = b.win
		f.move(a, b)
		return nil, nil
	case "kill-pane":
		id := flag("-t")
		for i, p := range f.panes {
			if p.id == id {
				f.panes = append(f.panes[:i], f.panes[i+1:]...)
				return nil, nil
			}
		}
		return nil, fmt.Errorf("can't find pane: %s", id)
	case "respawn-pane":
		p, err := must(flag("-t"))
		if err != nil {
			return nil, err
		}
		p.dead, p.cmd = false, argv()
		return nil, nil
	case "list-clients":
		return append([]string(nil), f.clients...), nil
	case "resize-pane":
		if p := f.pane(flag("-t")); p != nil {
			p.width, _ = strconv.Atoi(flag("-x"))
		}
		return nil, nil
	case "select-pane", "select-window", "refresh-client", "detach-client":
		return nil, nil
	}
	return nil, fmt.Errorf("fake tmux: unexpected command %q", args)
}

// sessionTarget checks the "=SESSION:" form real tmux needs for set-option and display-message:
// both take a target pane, and a bare "=SESSION" fails or prints nothing on tmux 3.5a.
func sessionTarget(t string) error {
	if !strings.HasPrefix(t, "=") || !strings.HasSuffix(t, ":") || len(t) < 3 {
		return fmt.Errorf("fake tmux: session target %q is not of the form =SESSION:", t)
	}
	return nil
}

// fakeTerm is the tmux server: sessions keep their panes across connections.
type fakeTerm struct {
	mu       sync.Mutex
	sessions map[string]*fakeSession
	names    []string
	opens    int
	popups   []string
	popupErr error
}

func (t *fakeTerm) Popup(ctx context.Context, client string, argv []string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.popups = append(t.popups, client+" "+strings.Join(argv, " "))
	return t.popupErr
}

func (t *fakeTerm) Open(ctx context.Context, session string, deckCmd []string) (Session, bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.opens++
	s, ok := t.sessions[session]
	if !ok {
		s = newFakeSession(deckCmd)
		t.sessions[session] = s
	} else {
		s.locked(func() { s.events = make(chan tmux.Event, 16) }) // a new control client
	}
	return s, !ok, nil
}

// put installs a session as an older daemon or the user left it: panes in the order given,
// window names by window id.
func (t *fakeTerm) put(session string, names map[string]string, panes ...*fpane) *fakeSession {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := &fakeSession{opts: map[string]string{}, events: make(chan tmux.Event, 16), n: 50,
		panes: panes, names: names, stacked: map[string]bool{}, marked: map[string]bool{}}
	t.sessions[session] = s
	return s
}

func (t *fakeTerm) Sessions(ctx context.Context) ([]string, error) { return t.names, nil }

func (t *fakeTerm) get(session string) *fakeSession {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sessions[session]
}

type fakeContainers struct {
	mu       sync.Mutex
	live     map[string][]string
	shells   map[string][]string // container → live shells
	shellErr error               // Shells fails with it (an image without lea-agent shells)
	stopped  []string
	lives    int
	err      error // Live fails with it (docker unreachable)
	stopErr  error // Stop fails with it
	projects map[string]string
	paused   []string      // container/name:pause|resume, in call order
	pauseErr error         // Pause fails with it
	torn     []string      // container/name of each Teardown, in call order
	tearHold chan struct{} // when set, Teardown waits for it to close
	sweeps   []string      // container:keep,... of each Sweep
	sweepOut string        // what Sweep reports
	parks    []string      // container/name of each Park
	parkOut  string        // what Park reports
}

func (c *fakeContainers) Park(ctx context.Context, container, name string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.parks = append(c.parks, container+"/"+name)
	return c.parkOut, nil
}

func (c *fakeContainers) Teardown(ctx context.Context, container, name string) error {
	c.mu.Lock()
	hold := c.tearHold
	c.mu.Unlock()
	if hold != nil {
		<-hold
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.torn = append(c.torn, container+"/"+name)
	return nil
}

func (c *fakeContainers) Sweep(ctx context.Context, container string, keep []string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweeps = append(c.sweeps, container+":"+strings.Join(keep, ","))
	return c.sweepOut, nil
}

func (c *fakeContainers) tornDown() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.torn...)
}

func (c *fakeContainers) Pause(ctx context.Context, container, name string, paused bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pauseErr != nil {
		return c.pauseErr
	}
	what := ":resume"
	if paused {
		what = ":pause"
	}
	c.paused = append(c.paused, container+"/"+name+what)
	return nil
}

func (c *fakeContainers) Projects(ctx context.Context, container string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p, ok := c.projects[container]; ok {
		return p, nil
	}
	return "", fmt.Errorf("container %s has no usable lea.projects label", container)
}

func (c *fakeContainers) Live(ctx context.Context, container string) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lives++
	if c.err != nil {
		return nil, c.err
	}
	return append([]string(nil), c.live[container]...), nil
}

func (c *fakeContainers) Stop(ctx context.Context, container, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	c.stopped = append(c.stopped, container+"/"+name)
	if c.stopErr != nil {
		return c.stopErr
	}
	var keep []string
	for _, n := range c.live[container] {
		if n != name {
			keep = append(keep, n)
		}
	}
	c.live[container] = keep
	return nil
}

func (c *fakeContainers) Shells(ctx context.Context, container string) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shellErr != nil {
		return nil, c.shellErr
	}
	return append([]string(nil), c.shells[container]...), nil
}

func (c *fakeContainers) StopShell(ctx context.Context, container, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopped = append(c.stopped, container+"/shell:"+name)
	var keep []string
	for _, n := range c.shells[container] {
		if n != name {
			keep = append(keep, n)
		}
	}
	if c.shells == nil {
		c.shells = map[string][]string{}
	}
	c.shells[container] = keep
	return nil
}

func (c *fakeContainers) setShells(container string, names ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shells == nil {
		c.shells = map[string][]string{}
	}
	c.shells[container] = names
}

func (c *fakeContainers) setLive(container string, names ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.live[container] = names
}

func (c *fakeContainers) calls() int { c.mu.Lock(); defer c.mu.Unlock(); return c.lives }

func (c *fakeContainers) fail(live, stop error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.err, c.stopErr = live, stop
}

// fakeClock drives the respawn backoff: a settable now, and the retries it schedules.
type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	waits []time.Duration
	fns   []func()
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}
func (c *fakeClock) schedule(d time.Duration, f func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.waits, c.fns = append(c.waits, d), append(c.fns, f)
}
func (c *fakeClock) scheduled() ([]time.Duration, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.fns) == 0 {
		return nil, nil
	}
	return append([]time.Duration(nil), c.waits...), c.fns[len(c.fns)-1]
}
