// Package daemon is the lea host daemon: the only writer of lea.db, the message broker and the
// endpoint of every container's bus socket. It never runs inside a container.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/DavidGodefroid/lea/internal/bus"
	"github.com/DavidGodefroid/lea/internal/providers"
	"github.com/DavidGodefroid/lea/internal/store"
	"github.com/DavidGodefroid/lea/internal/wire"
)

// Origin says which socket a connection came in on: the host API (Container == "") or one
// container's bus socket. It is the caller's identity; nothing the caller claims overrides it.
type Origin struct{ Container string }

var (
	agentName     = regexp.MustCompile(`^[A-Za-z0-9:._-]{1,64}$`)
	containerName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
)

const (
	inboxBatch = 8
	// batchBytes caps the encoded size of one delivered batch, well under wire.MaxLine, so a
	// response always fits on the wire: messages taken (marked read) are never lost to a write
	// failure. A single message (64 KiB body, at most ~6x when escaped) always fits.
	batchBytes = 768 << 10
	// maxHookPayload caps a stored hook payload; the hook forwards only a few small fields.
	maxHookPayload = 8 << 10
)

type Daemon struct {
	st       *store.Store
	lim      bus.Limits
	now      func() int64
	runDir   string
	mu       sync.Mutex
	attached map[string]string
	notify   map[string]chan struct{}
	verdicts map[int64]chan string
	// asks: the asks in flight, by asker (one each); in memory, a restart ends them.
	asks map[string]pendingAsk

	// afterWait is a test seam: it runs after an approval waiter woke, before it deregisters.
	afterWait func()

	blocks map[string]int
	// stopped: agents whose turn ended (Stop) and no new one began (prompt or stop block).
	stopped map[string]bool
	// rewakes: the generation of each agent's rewake watch; a newer one supersedes the older.
	rewakes map[string]int
	keyMu   map[string]*sync.Mutex
	agents  Agents
	// statusPaths: the last status-line key paths recorded per agent.
	statusPaths map[string]string
	// life is the daemon's lifetime: the context of the host API's Serve. Bus sockets bound
	// through the API (container_attach) live as long as it, not as long as the request.
	life context.Context
	// rates caps the agent-tree, board and lease methods per caller.
	rates rates
	// namer names agents after their first prompt (nil: off); named holds the agents it was
	// asked for. afterName is a test seam: it runs when a naming ends.
	// providers is the registry of the providers agents may name (nil: the built-in ones).
	providers   func() (*providers.Registry, error)
	defProvider string
	namer       Namer
	named       map[string]bool
	afterName   func()
	// sounder plays the attention and done sounds (nil: off); rang is when each kind last rang;
	// soundOff is the user's switch (lea sound, the deck's S), which keeps the sounder aside.
	sounder  Sounder
	soundOff bool
	rang     map[string]time.Time
	// doneDelay holds back the done sound (a test seam): another Stop hook can still block the
	// stop. doneTimers: the pending done sound of each agent, cancelled by its next hook.
	doneDelay  time.Duration
	doneTimers map[string]*time.Timer
	// subs: the subagents each agent runs (trackSubagents); subTTL forgets them (a test seam).
	subs   map[string]*subagents
	subTTL time.Duration
	// Auto-resume after a usage limit (resume.go): the user's switch and delay, each agent's last
	// rate-limit windows and its newest LimitWait job.
	resumeOff   bool
	resumeDelay int64
	windows     map[string][]limitWindow
	limitJobs   map[string]string
}

func New(st *store.Store, runDir string, now func() int64) *Daemon {
	// The running subagents live in memory: counts a previous daemon left are stale.
	if err := st.ClearSubagents(); err != nil {
		log.Printf("lea daemon: clear subagents: %v", err)
	}
	return &Daemon{subs: map[string]*subagents{}, subTTL: subagentTTL, st: st, lim: bus.Default, now: now, runDir: runDir,
		attached: map[string]string{}, notify: map[string]chan struct{}{},
		verdicts: map[int64]chan string{}, asks: map[string]pendingAsk{}, blocks: map[string]int{}, stopped: map[string]bool{}, rewakes: map[string]int{}, keyMu: map[string]*sync.Mutex{}, statusPaths: map[string]string{}, named: map[string]bool{}, rang: map[string]time.Time{},
		doneDelay: doneDelay, doneTimers: map[string]*time.Timer{},
		resumeDelay: DefaultResumeDelay, windows: map[string][]limitWindow{}, limitJobs: map[string]string{}}
}

func (d *Daemon) Serve(ctx context.Context, l net.Listener, o Origin) {
	if o.Container == "" {
		d.mu.Lock()
		if d.life == nil {
			d.life = ctx
		}
		d.mu.Unlock()
	}
	go func() { <-ctx.Done(); l.Close() }()
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go d.handle(ctx, c, o)
	}
}

func (d *Daemon) AttachContainer(ctx context.Context, name string) (string, error) {
	if !containerName.MatchString(name) {
		return "", fmt.Errorf("invalid container name %q", name)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if p, ok := d.attached[name]; ok {
		return p, nil
	}
	dir := filepath.Join(d.runDir, name)
	if err := os.MkdirAll(d.runDir, 0o700); err != nil {
		return "", err
	}
	if fi, err := os.Lstat(d.runDir); err != nil {
		return "", err
	} else if fi.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%s is a symlink, refused", d.runDir)
	}
	if fi, err := os.Lstat(dir); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%s is a symlink, refused", dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	p := filepath.Join(dir, "bus.sock")
	l, err := Listen(p)
	if err != nil {
		return "", err
	}
	d.attached[name] = p
	go func() {
		d.Serve(ctx, l, Origin{Container: name})
		// The listener is closed (and its socket file removed): a later attach binds it again.
		d.mu.Lock()
		if d.attached[name] == p {
			delete(d.attached, name)
		}
		d.mu.Unlock()
	}()
	return p, nil
}

// lifetime is the context bus sockets bound through the API live under: the host API's Serve
// context, or ctx when no host API serves (tests that attach directly).
func (d *Daemon) lifetime(ctx context.Context) context.Context {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.life != nil {
		return d.life
	}
	return context.WithoutCancel(ctx)
}

// handle serves exactly one request per connection. After any wire.Read error (including
// ErrTooLong, which leaves the stream mid-line) it answers once and closes: it never parses on.
func (d *Daemon) handle(ctx context.Context, c net.Conn, o Origin) {
	defer c.Close()
	var req wire.Request
	if err := wire.Read(wire.NewReader(c), &req); err != nil {
		wire.Write(c, wire.Response{Error: "bad request: " + err.Error()})
		return
	}
	// One request per connection: any read result (EOF, error, stray bytes) means the client is
	// gone or misbehaving, so the request context is cancelled and a blocked msg_wait gives up
	// before it can take a message nobody will receive.
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		var b [1]byte
		c.Read(b[:])
		cancel()
	}()
	res, err := d.dispatch(rctx, o, req)
	if rctx.Err() != nil {
		if err == nil {
			d.restore(res) // the client is gone: nobody will read what was taken
		}
		return
	}
	resp := wire.Response{}
	if err != nil {
		resp.Error = err.Error()
	} else if res != nil {
		if resp.Result, err = json.Marshal(res); err != nil {
			resp = wire.Response{Error: "internal error: " + err.Error()}
			log.Printf("lea daemon: marshal %s: %v", req.Method, err)
			d.restore(res)
		}
	}
	if err := wire.Write(c, resp); err != nil {
		log.Printf("lea daemon: write %s response: %v", req.Method, err)
		if resp.Error == "" {
			d.restore(res)
		}
	}
}

// lockKey serialises work per agent key (rate-limit check + insert, inbox take).
func (d *Daemon) lockKey(key string) func() {
	d.mu.Lock()
	m, ok := d.keyMu[key]
	if !ok {
		m = &sync.Mutex{}
		d.keyMu[key] = m
	}
	d.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// caller returns the sender/recipient key of a request: "user" on the host API, otherwise the
// container's agent, registered on first contact.
func (d *Daemon) caller(o Origin, agent string) (string, error) {
	if o.Container == "" {
		return "user", nil
	}
	if !agentName.MatchString(agent) {
		return "", fmt.Errorf("invalid agent name %q (set LEA_AGENT)", agent)
	}
	return d.st.UpsertAgent(o.Container, agent, d.now())
}

func (d *Daemon) dispatch(ctx context.Context, o Origin, req wire.Request) (any, error) {
	if deckMethods[req.Method] {
		return d.dispatchDeck(ctx, o, req)
	}
	if req.Method == "container_attach" || req.Method == "approval_decide" || req.Method == "approval_list" ||
		req.Method == "thread_list" || req.Method == "agent_role" || req.Method == "request_list" || req.Method == "request_decide" || req.Method == "board_list" ||
		req.Method == "sound" {
		if o.Container != "" {
			return nil, fmt.Errorf("%s: host only", req.Method)
		}
	}
	me, err := d.caller(o, req.Agent)
	if err != nil {
		return nil, err
	}
	switch req.Method {
	case "sound":
		return d.switchSound(req.Params)
	case "agent_role":
		return d.setRole(req.Params)
	case "agents_list":
		return d.agentsList(o, me)
	case "providers_list":
		return d.providersList()
	case "msg_send":
		var p struct {
			To, Body, Kind string
			ReplyTo        int64 `json:"reply_to"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		if p.Kind == "question" {
			return nil, errors.New("kind question is the ask tool's: use ask to wait for an answer")
		}
		return d.send(me, p.To, p.Body, p.Kind, p.ReplyTo)
	case "msg_reply":
		var p struct {
			ID           int64
			Status, Body string
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		return d.reply(me, p.ID, p.Status, p.Body)
	case "msg_inbox":
		return d.inbox(me)
	case "ask":
		return d.ask(ctx, o, me, req.Params)
	case "msg_wait":
		var p struct {
			TimeoutS int `json:"timeout_s"`
		}
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &p); err != nil {
				return nil, err
			}
		}
		return d.wait(ctx, me, p.TimeoutS)
	case "agents_spawn":
		return d.agentsSpawn(ctx, o, me, req.Params)
	case "agents_stop":
		return d.agentsStop(ctx, o, me, req.Params)
	case "agents_request_spawn":
		return d.requestSpawn(o, me, req.Params)
	case "request_list":
		return d.requestList()
	case "request_decide":
		return d.requestDecide(ctx, req.Params)
	case "lease_acquire":
		return d.leaseAcquire(o, me, req.Params)
	case "lease_release":
		return d.leaseRelease(o, me, req.Params)
	case "task_update":
		return d.taskUpdate(o, me, req.Params)
	case "board_list":
		return d.boardList(req.Params)
	case "board_read":
		return d.boardRead(o)
	case "msg_watch":
		var p struct {
			TimeoutS int  `json:"timeout_s"`
			Rewake   bool `json:"rewake"`
		}
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &p); err != nil {
				return nil, err
			}
		}
		return d.watch(ctx, me, p.TimeoutS, p.Rewake)
	case "limit_wait":
		return d.limitWait(ctx, o, me, req.Params)
	case "thread_list":
		return d.threadList(req.Params)
	case "container_attach":
		var p struct{ Name string }
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		s, err := d.AttachContainer(d.lifetime(ctx), p.Name)
		return map[string]string{"socket": s}, err
	default:
		return d.dispatchHooks(ctx, o, me, req)
	}
}

type agentView struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	// Label is the user's display name for the agent: the host API's list only.
	Label   string `json:"label,omitempty"`
	State   string `json:"state"`
	Updated int64  `json:"updated"`
	Role    string `json:"role,omitempty"`
	Profile string `json:"profile,omitempty"`
	Model   string `json:"model,omitempty"`
	Parent  string `json:"parent,omitempty"`
	Repo    string `json:"repo,omitempty"`
	Branch  string `json:"branch,omitempty"`
	Task    string `json:"task,omitempty"`
	// CtxPct is the percent of its context window the agent's status line last reported; absent
	// while unknown.
	CtxPct *int `json:"ctx_pct,omitempty"`
	// Subagents counts the subagents the agent still runs (its Subagent hooks), for display.
	Subagents int  `json:"subagents,omitempty"`
	You       bool `json:"you,omitempty"`
}

// agentsList lists the agents (spec §6.1: name, role, profile, state, branch; never screen
// content). Parent is the name of the lead that started one; You marks the caller. A lead reads
// its children's context use here, from the caller's own container (the host API sees all).
func (d *Daemon) agentsList(o Origin, me string) ([]agentView, error) {
	as, err := d.st.Agents()
	if err != nil {
		return nil, err
	}
	usages := map[string]map[string]store.Usage{}
	subs := map[string]map[string]int{}
	out := []agentView{}
	for _, a := range as {
		v := agentView{Key: a.Key, Name: a.Name, State: a.State, Updated: a.Updated, You: a.Key == me}
		if o.Container == "" || a.Container == o.Container {
			u, ok := usages[a.Container]
			if !ok {
				if u, err = d.st.Usages(a.Container); err != nil {
					return nil, err
				}
				usages[a.Container] = u
			}
			if p := u[a.Key].CtxPct; p >= 0 {
				v.CtxPct = &p
			}
			n, ok := subs[a.Container]
			if !ok {
				if n, err = d.st.Subagents(a.Container); err != nil {
					return nil, err
				}
				subs[a.Container] = n
			}
			v.Subagents = n[a.Key]
		}
		if a.Profile != "" {
			v.Role, v.Profile, v.Model, v.Repo, v.Branch = a.Role, a.Profile, a.Model, a.Repo, a.Branch
			if o.Container == "" {
				v.Label = a.Label
			}
			if a.Parent != "" {
				v.Parent = shortName(a.Parent)
			}
			// Task text is another agent's words: only the caller's own container sees it
			// (the host API sees all), marked untrusted.
			if o.Container == "" || a.Container == o.Container {
				if t, err := d.st.TaskOf(a.Key); err == nil && t.StatusText != "" {
					v.Task = "untrusted: " + t.StatusText
				}
			}
		}
		out = append(out, v)
	}
	return out, nil
}

func (d *Daemon) keys() ([]string, error) {
	as, err := d.st.Agents()
	if err != nil {
		return nil, err
	}
	ks := make([]string, 0, len(as))
	for _, a := range as {
		ks = append(ks, a.Key)
	}
	return ks, nil
}

type sentView struct {
	ID     int64 `json:"id"`
	Thread int64 `json:"thread"`
}

func (d *Daemon) send(from, to, body, kind string, replyTo int64) (sentView, error) {
	ks, err := d.keys()
	if err != nil {
		return sentView{}, err
	}
	dest, err := bus.Resolve(to, ks)
	if err != nil {
		return sentView{}, err
	}
	hops := 0
	if replyTo != 0 {
		p, err := d.st.Get(replyTo)
		if err != nil {
			return sentView{}, fmt.Errorf("reply_to %d: %w", replyTo, err)
		}
		hops = p.Hops + 1
	}
	defer d.lockKey("send:" + from)()
	n, err := d.st.SentSince(from, d.now()-60)
	if err != nil {
		return sentView{}, err
	}
	if err := d.lim.Check(body, kind, n, hops); err != nil {
		return sentView{}, err
	}
	m, err := d.st.Send(store.Message{From: from, To: dest, Kind: kind, Body: body, ReplyTo: replyTo, Hops: hops, At: d.now()})
	if err != nil {
		return sentView{}, err
	}
	d.wake(dest)
	d.changed()
	return sentView{m.ID, m.Thread}, nil
}

func (d *Daemon) reply(me string, id int64, status, body string) (sentView, error) {
	switch status {
	case "ok", "fail", "needs_user":
	default:
		return sentView{}, fmt.Errorf("status must be ok, fail or needs_user")
	}
	p, err := d.st.Get(id)
	if err != nil {
		return sentView{}, fmt.Errorf("message %d: %w", id, err)
	}
	if p.To != me {
		return sentView{}, fmt.Errorf("message %d is not addressed to you", id)
	}
	if status == "ok" && p.Kind == "spawn_request" {
		return sentView{}, fmt.Errorf("status ok does not start anything: call agents_spawn with for=\"#N\" to accept, or reply with status fail to decline")
	}
	v, err := d.send(me, p.From, "[status: "+status+"] "+body, "result", id)
	if err == nil && status == "fail" && p.Kind == "spawn_request" {
		d.declineRequest(id, me)
	}
	return v, err
}

type deliveredView struct {
	ID     int64  `json:"id"`
	From   string `json:"from"`
	Kind   string `json:"kind"`
	Thread int64  `json:"thread"`
	Text   string `json:"text"`
}

// batch is a set of messages taken from an inbox (marked read) for one delivery. It encodes as
// the JSON array of its items.
type batch struct {
	to    string
	items []deliveredView
}

func (b batch) MarshalJSON() ([]byte, error) { return json.Marshal(b.items) }

func (b batch) taken() (string, []int64) {
	ids := make([]int64, len(b.items))
	for i, v := range b.items {
		ids[i] = v.ID
	}
	return b.to, ids
}

// stopVerdict answers a Stop hook. A blocking verdict carries the messages it delivers, so they
// go back to the inbox when the answer cannot be written.
type stopVerdict struct {
	Block  bool   `json:"block"`
	Reason string `json:"reason"`
	from   batch
}

func (v stopVerdict) taken() (string, []int64) { return v.from.taken() }

// delivery is a result that took messages out of an inbox.
type delivery interface {
	taken() (to string, ids []int64)
}

// restore puts back the messages of a delivery that never reached its reader (the client went
// away, or the answer could not be written), and wakes whoever waits for them.
func (d *Daemon) restore(res any) {
	t, ok := res.(delivery)
	if !ok {
		return
	}
	to, ids := t.taken()
	if len(ids) == 0 {
		return
	}
	if err := d.st.MarkUnread(ids); err != nil {
		log.Printf("lea daemon: put back %d message(s) for %s: %v", len(ids), to, err)
		return
	}
	d.wake(to)
	d.changed()
}

// take returns up to inboxBatch unread messages for me, at most batchBytes once encoded (but
// always at least one), and marks exactly those read; the rest stay unread for the next call.
func (d *Daemon) take(me string) (batch, error) {
	defer d.lockKey("take:" + me)()
	ms, err := d.st.Unread(me, inboxBatch)
	if err != nil {
		return batch{}, err
	}
	out := batch{to: me, items: []deliveredView{}}
	ids := make([]int64, 0, len(ms))
	size := 0
	for _, m := range ms {
		v := deliveredView{m.ID, m.From, m.Kind, m.Thread, bus.Wrap(m)}
		b, err := json.Marshal(v)
		if err != nil {
			return batch{}, err
		}
		if len(out.items) > 0 && size+len(b)+1 > batchBytes {
			break
		}
		size += len(b) + 1
		out.items = append(out.items, v)
		ids = append(ids, m.ID)
	}
	if err := d.st.MarkRead(ids, d.now()); err != nil {
		return batch{}, err
	}
	if len(ids) > 0 {
		d.changed()
	}
	return out, nil
}

func (d *Daemon) stoppedNow(me string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.stopped[me]
}

func (d *Daemon) inbox(me string) (batch, error) { return d.take(me) }

func (d *Daemon) waitChan(me string) chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	ch, ok := d.notify[me]
	if !ok {
		ch = make(chan struct{})
		d.notify[me] = ch
	}
	return ch
}

func (d *Daemon) wake(key string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if ch, ok := d.notify[key]; ok {
		close(ch)
		delete(d.notify, key)
	}
}

func (d *Daemon) wait(ctx context.Context, me string, timeoutS int) (batch, error) {
	if timeoutS < 1 || timeoutS > 600 {
		return batch{}, errors.New("timeout_s must be between 1 and 600")
	}
	deadline := time.After(time.Duration(timeoutS) * time.Second)
	for {
		if err := ctx.Err(); err != nil {
			return batch{}, err
		}
		ch := d.waitChan(me)
		out, err := d.take(me)
		if err != nil || len(out.items) > 0 {
			return out, err
		}
		select {
		case <-ch:
		case <-deadline:
			return batch{to: me, items: []deliveredView{}}, nil
		case <-ctx.Done():
			return batch{}, ctx.Err()
		}
	}
}

// unreadNotice counts the caller's unread messages and names their senders. Only the count and
// the senders: no body content enters the context outside the untrusted wrapping that msg_inbox
// applies.
func (d *Daemon) unreadNotice(me string) (map[string]any, error) {
	n, err := d.st.UnreadCount(me)
	if err != nil || n == 0 {
		return map[string]any{"count": 0, "text": ""}, err
	}
	ms, err := d.st.Unread(me, inboxBatch)
	if err != nil {
		return nil, err
	}
	var from []string
	seen := map[string]bool{}
	for _, m := range ms {
		if !seen[m.From] {
			seen[m.From] = true
			from = append(from, m.From)
		}
	}
	text := fmt.Sprintf("lea: %d unread message(s) from %s. Read them with the msg_inbox tool.",
		n, strings.Join(from, ", "))
	return map[string]any{"count": n, "text": text}, nil
}

// maxWatch is the longest msg_watch: an hour, the span of a background job that wakes an idle
// agent.
const maxWatch = 3600

// watch waits until the caller has an unread message or the timeout ends, and answers with
// unreadNotice, leaving the messages unread. An agent whose turn is over hears of no message
// (Stop delivers them only while a turn ends): a background job running lea inbox --wait, which
// calls this, ends when one arrives, and Claude Code then starts a turn to read it.
//
// A rewake watch is the one the Stop hook starts (lea hook claude Rewake, an asyncRewake hook):
// it answers only while the agent's turn is over, so a working agent or one waiting on a
// permission dialog is not woken, and only the newest one per agent stays: it supersedes the
// older ones, which answer superseded and wake nobody.
func (d *Daemon) watch(ctx context.Context, me string, timeoutS int, rewake bool) (map[string]any, error) {
	if timeoutS < 1 || timeoutS > maxWatch {
		return nil, fmt.Errorf("timeout_s must be between 1 and %d", maxWatch)
	}
	gen := 0
	if rewake {
		d.mu.Lock()
		d.rewakes[me]++
		gen = d.rewakes[me]
		d.mu.Unlock()
		d.wake(me) // the older watch sees it is superseded
	}
	deadline := time.After(time.Duration(timeoutS) * time.Second)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ch := d.waitChan(me)
		idle := true
		if rewake {
			d.mu.Lock()
			cur, stopped := d.rewakes[me], d.stopped[me]
			d.mu.Unlock()
			if cur != gen {
				return map[string]any{"count": 0, "text": "", "superseded": true}, nil
			}
			idle = stopped
		}
		n, err := d.unreadNotice(me)
		if err != nil || (n["count"] != 0 && idle) {
			return n, err
		}
		select {
		case <-ch:
		case <-deadline:
			n["count"], n["text"] = 0, ""
			return n, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

var hookState = map[string]string{
	"SessionStart": "idle", "UserPromptSubmit": "working", "PreToolUse": "working",
	"PostToolUse": "working", "PermissionRequest": "blocked", "Stop": "idle", "SessionEnd": "done",
	// StopFailure fires instead of Stop when an API error ended the turn: the turn is over.
	"StopFailure": "idle",
}

const maxStopBlocks = 3

// pruneForgotten drops the hook bookkeeping and the rate windows of agents whose row is gone.
func (d *Daemon) pruneForgotten() {
	d.rates.prune(func(k string) bool {
		_, err := d.st.AgentByKey(k)
		return errors.Is(err, store.ErrNotFound)
	})
	d.mu.Lock()
	keys := make([]string, 0, len(d.stopped)+len(d.blocks))
	for k := range d.stopped {
		keys = append(keys, k)
	}
	for k := range d.blocks {
		keys = append(keys, k)
	}
	for k := range d.named {
		keys = append(keys, k)
	}
	for k := range d.doneTimers {
		keys = append(keys, k)
	}
	for k := range d.subs {
		keys = append(keys, k)
	}
	for k := range d.windows {
		keys = append(keys, k)
	}
	for k := range d.limitJobs {
		keys = append(keys, k)
	}
	d.mu.Unlock()
	for _, k := range keys {
		if _, err := d.st.AgentByKey(k); errors.Is(err, store.ErrNotFound) {
			d.mu.Lock()
			delete(d.stopped, k)
			delete(d.blocks, k)
			delete(d.named, k)
			d.cancelDoneLocked(k)
			d.forgetSubagentsLocked(k)
			delete(d.windows, k)
			delete(d.limitJobs, k)
			d.mu.Unlock()
		}
	}
}

func (d *Daemon) dispatchHooks(ctx context.Context, o Origin, me string, req wire.Request) (any, error) {
	switch req.Method {
	case "hook_event":
		var p struct {
			Event     string          `json:"event"`
			SessionID string          `json:"session_id"`
			Payload   json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		if len(p.Payload) > maxHookPayload {
			return nil, fmt.Errorf("hook payload is %d bytes, limit %d", len(p.Payload), maxHookPayload)
		}
		if err := d.st.AddEvent(me, "hook", p.Event, string(p.Payload), d.now()); err != nil {
			return nil, err
		}
		d.trackSubagents(me, p.Event, p.Payload)
		if p.Event == "SubagentStart" || p.Event == "SubagentStop" {
			// Display only: a subagent neither starts nor ends the agent's turn, nor its done sound.
			return struct{}{}, nil
		}
		s, ok := hookState[p.Event]
		if p.Event == "Notification" {
			// Claude fires no Stop when the user interrupts a turn or dismisses a permission
			// dialog: idle_prompt (the agent has waited a minute at its prompt) ends it. Claude
			// does not send it while a dialog is open, so it ends a blocked agent too.
			var n struct {
				Type string `json:"notification_type"`
			}
			if json.Unmarshal(p.Payload, &n) == nil && n.Type == "idle_prompt" {
				s, ok = "idle", true
			}
		}
		d.mu.Lock()
		stopped := d.stopped[me]
		switch {
		case p.Event == "Notification":
		case stopped && (p.Event == "PreToolUse" || p.Event == "PostToolUse"):
			// After Stop, a tool is an agent-type Stop hook at work, or the agent itself once
			// another hook blocked the stop: the wait starts over. Cancelling would leave a stop
			// the hook lets through without its sound, since no stop_check follows.
			d.restartDoneLocked(me, o.Container)
		default:
			// A new turn began, or a new Stop comes with its own stop_check.
			d.cancelDoneLocked(me)
		}
		switch {
		case p.Event == "UserPromptSubmit" || p.Event == "SessionStart":
			delete(d.blocks, me)
			delete(d.stopped, me)
		case p.Event == "Stop" || p.Event == "StopFailure" || (p.Event == "Notification" && ok):
			d.stopped[me] = true
		}
		d.mu.Unlock()
		if p.Event == "StopFailure" {
			d.turnFailed(me, p.Payload)
		} else {
			d.limitProgress(me, p.Event)
		}
		if !stopped && d.stoppedNow(me) {
			// The turn is over: a rewake watch looks again at the messages that came during it.
			d.wake(me)
		}
		if ok && stopped && (p.Event == "PreToolUse" || p.Event == "PostToolUse") {
			// A Stop hook of the agent or prompt type runs its own tools after Stop, under the
			// agent's identity: they do not start a turn. An approval it asked for is answered.
			ok = false
			if p.Event == "PostToolUse" {
				if a, err := d.st.AgentByKey(me); err == nil && a.State == "blocked" {
					s, ok = "idle", true
				}
			}
		}
		if ok {
			waits := false
			if s == "blocked" {
				a, err := d.st.AgentByKey(me)
				waits = err == nil && a.State != "blocked"
			}
			if err := d.st.SetState(me, s, p.SessionID, d.now()); err != nil {
				return nil, err
			}
			d.changed()
			if waits {
				d.sound(ctx, o.Container, SoundAttention)
			}
		}
		if ok && (s == "idle" || s == "done" || p.Event == "UserPromptSubmit") {
			// The turn is over (or a new one starts): no permission dialog is open any more.
			// Claude does not end the hook when the user answers its own dialog in the
			// terminal, so the card would stay until the timeout.
			d.withdrawApprovals(me)
		}
		return struct{}{}, nil
	case "name_hint":
		return d.dispatchNameHint(o, me, req)
	case "stop_check":
		// One check at a time per agent: the round count is read, then written.
		defer d.lockKey("stop:" + me)()
		d.mu.Lock()
		n := d.blocks[me]
		d.mu.Unlock()
		// The turn ends unless the stop is blocked: the done sound. Claude runs the Stop hooks in
		// parallel and another one can still block the stop, so the sound waits for the agent to
		// stay quiet (armDone).
		if n >= maxStopBlocks {
			d.armDone(me, o.Container)
			return stopVerdict{}, nil
		}
		ms, err := d.take(me)
		if err != nil || len(ms.items) == 0 {
			if err == nil {
				d.armDone(me, o.Container)
			} else {
				d.cancelDone(me)
			}
			return stopVerdict{}, err
		}
		d.mu.Lock()
		d.cancelDoneLocked(me)
		d.blocks[me] = n + 1
		delete(d.stopped, me)
		d.mu.Unlock()
		// Blocking the stop means the agent keeps working on the delivered messages.
		if err := d.st.SetState(me, "working", "", d.now()); err != nil {
			log.Printf("lea daemon: set state of %s: %v", me, err)
		}
		d.changed()
		parts := make([]string, 0, len(ms.items)+1)
		for _, m := range ms.items {
			parts = append(parts, m.Text)
		}
		parts = append(parts, "Answer with the msg_reply tool when a reply is expected.")
		return stopVerdict{Block: true, Reason: strings.Join(parts, "\n\n"), from: ms}, nil
	case "context_unread":
		return d.unreadNotice(me)
	case "approval_request":
		var p struct {
			Tool     string          `json:"tool"`
			Input    json.RawMessage `json:"input"`
			TimeoutS int             `json:"timeout_s"`
			Always   bool            `json:"always"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		if p.TimeoutS < 1 || p.TimeoutS > 600 {
			return nil, errors.New("timeout_s must be between 1 and 600")
		}
		if !d.deckAttached(ctx, o.Container) {
			// Nobody can answer in the deck: the agent's own dialog asks at once (spec A10).
			return map[string]any{"id": 0, "verdict": ""}, nil
		}
		id, err := d.st.AddApproval(me, capTool(p.Tool), capText(string(p.Input), maxApprovalInput), p.Always, d.now())
		if err != nil {
			return nil, err
		}
		ch := make(chan string, 1)
		d.mu.Lock()
		d.verdicts[id] = ch
		d.mu.Unlock()
		d.changed()
		timer := time.NewTimer(time.Duration(p.TimeoutS) * time.Second)
		defer timer.Stop()
		v := ""
		hookGone := false
		select {
		case v = <-ch:
		case <-timer.C:
		case <-ctx.Done():
			hookGone = true
		}
		if d.afterWait != nil {
			d.afterWait()
		}
		// Deregister, then drain: a verdict (or the withdraw marker) delivered just before
		// deregistration is not lost.
		d.mu.Lock()
		delete(d.verdicts, id)
		d.mu.Unlock()
		if v == "" {
			select {
			case v = <-ch:
			default:
			}
		}
		withdrawn := v == verdictWithdrawn
		if withdrawn {
			v = ""
		}
		if v == "" {
			// Nobody answered in time (or the hook is gone): the agent's dialog takes over. A
			// verdict stored meanwhile wins: it is the one the user gave.
			switch err := d.st.ExpireApproval(id); {
			case errors.Is(err, store.ErrDecided):
				if a, err := d.st.ApprovalByID(id); err != nil {
					log.Printf("lea daemon: read approval #%d: %v", id, err)
				} else {
					v = a.Verdict
				}
			case err != nil:
				log.Printf("lea daemon: expire approval #%d: %v", id, err)
			}
		}
		// A delivered verdict resumes the agent's turn (allow, always and deny alike: Claude
		// carries on, and Stop or the idle notification ends it). A vanished hook means the
		// dialog was answered in the terminal or the turn was cut short, and no hook may follow:
		// the agent is no longer blocked either. A plain timeout leaves the dialog open, so the
		// state stays blocked.
		// A withdrawn request leaves the state to the hook event that withdrew it (even when a
		// deck verdict raced it: the turn is over either way).
		if !withdrawn && (v != "" || hookGone) {
			if err := d.st.SetState(me, "working", "", d.now()); err != nil {
				log.Printf("lea daemon: set state of %s: %v", me, err)
			}
		}
		d.changed()
		return map[string]any{"id": id, "verdict": v}, nil
	case "approval_list":
		as, err := d.st.PendingApprovals()
		if err != nil {
			return nil, err
		}
		out := []json.RawMessage{}
		size := 2
		for _, a := range as {
			// The reply is JSON: measure each entry encoded (< > & escape to 6 bytes, quotes double)
			// so the whole answer stays under batchBytes.
			b, err := json.Marshal(map[string]any{"id": a.ID, "agent": a.Agent, "tool": a.Tool, "input": a.Input, "at": a.At, "always": a.Always})
			if err != nil {
				return nil, err
			}
			if size+len(b)+1 > batchBytes {
				break
			}
			size += len(b) + 1
			out = append(out, b)
		}
		return out, nil
	case "approval_decide":
		var p struct {
			ID      int64  `json:"id"`
			Verdict string `json:"verdict"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		switch p.Verdict {
		case "allow", "deny", "always":
		default:
			return nil, errors.New("verdict must be allow, deny or always")
		}
		if p.Verdict == "always" {
			if a, err := d.st.ApprovalByID(p.ID); err == nil && a.Verdict == "" && !a.Always {
				return nil, fmt.Errorf("approval #%d: always is not offered for this request", p.ID)
			}
		}
		if err := d.st.SetVerdict(p.ID, p.Verdict); err != nil {
			switch {
			case errors.Is(err, store.ErrNotFound):
				return nil, fmt.Errorf("no approval #%d", p.ID)
			case errors.Is(err, store.ErrDecided):
				if a, gerr := d.st.ApprovalByID(p.ID); gerr == nil && a.Verdict == "expired" {
					return nil, fmt.Errorf("approval #%d expired: answer it in the agent's terminal", p.ID)
				}
				return nil, fmt.Errorf("approval #%d is already decided", p.ID)
			}
			return nil, err
		}
		// The waiter takes it from the channel, or (when it just timed out) reads it from the
		// store as it expires the row: the stored verdict is the one delivered.
		d.mu.Lock()
		if ch, ok := d.verdicts[p.ID]; ok {
			select { // buffered and decided at most once (SetVerdict), never blocks
			case ch <- p.Verdict:
			default:
			}
		}
		d.mu.Unlock()
		d.changed()
		return struct{}{}, nil
	case "status_report":
		return d.statusReport(o, me, req.Params)
	case "lock_request":
		return d.lockRequest(ctx, o, me, req.Params)
	default:
		return nil, fmt.Errorf("unknown method %q", req.Method)
	}
}

// verdictWithdrawn wakes an approval waiter whose request is moot: the agent's turn ended, so
// its dialog is no longer open. It is never stored nor sent to the hook.
const verdictWithdrawn = "\x00withdrawn"

// withdrawApprovals wakes the waiters of agent me's pending approvals with verdictWithdrawn:
// each expires its row and its hook returns no verdict.
func (d *Daemon) withdrawApprovals(me string) {
	as, err := d.st.PendingApprovals()
	if err != nil {
		log.Printf("lea daemon: list approvals of %s: %v", me, err)
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, a := range as {
		if a.Agent != me {
			continue
		}
		if ch, ok := d.verdicts[a.ID]; ok {
			select { // buffered: a verdict already queued wins
			case ch <- verdictWithdrawn:
			default:
			}
		}
	}
}

// maxToolName caps an approval's tool name, which the agent controls and every deck snapshot
// carries. A longer one is truncated on a rune boundary (the request is still served).
const maxToolName = 128

// maxApprovalInput caps the tool input kept for an approval card: the deck shows a summary, and
// the database must not hold file contents the agent passed to a tool.
const maxApprovalInput = 8 << 10

// capText cuts s to at most n bytes on a rune boundary.
func capText(s string, n int) string {
	s = strings.ToValidUTF8(s, "?")
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func capTool(s string) string {
	s = strings.ToValidUTF8(s, "?")
	if len(s) <= maxToolName {
		return s
	}
	n := maxToolName
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
