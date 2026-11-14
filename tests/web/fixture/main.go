// Command fixture is the test daemon of lea web's browser end-to-end test (tests/web/e2e.mjs,
// #414): lea's real daemon on a fresh store, listening on LEA_DAEMON_SOCK, with one container,
// lea-e2e, whose agents are rows in the store (no tmux, no docker). Its deck is never attached
// in a terminal: only a lea web client attached as a deck makes a permission prompt wait.
//
// It reads one JSON command per line on stdin and answers JSON lines on stdout:
//
//	{"op":"spawn","name":"dev-1","role":"dev"}          the user starts an agent (agent_new)
//	{"op":"task","agent":"dev-1","status":"…","next":"…"}  the agent reports its task
//	{"op":"approve","agent":"dev-1","tool":"Bash","command":"make test"}
//	                                                    the agent asks a permission; once
//	                                                    answered: {"event":"verdict",…}
//	{"op":"inbox","agent":"dev-1"}                      the agent reads its messages
//
// It prints {"event":"ready"} once it serves.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/DavidGodefroid/lea/internal/agents"
	"github.com/DavidGodefroid/lea/internal/client"
	"github.com/DavidGodefroid/lea/internal/daemon"
	"github.com/DavidGodefroid/lea/internal/store"
	"github.com/DavidGodefroid/lea/internal/wire"
)

const container = "lea-e2e"

var out = struct {
	sync.Mutex
	enc *json.Encoder
}{enc: json.NewEncoder(os.Stdout)}

func say(v any) {
	out.Lock()
	defer out.Unlock()
	out.enc.Encode(v)
}

func main() {
	sock := os.Getenv("LEA_DAEMON_SOCK")
	if sock == "" {
		fmt.Fprintln(os.Stderr, "fixture: set LEA_DAEMON_SOCK")
		os.Exit(2)
	}
	bus, err := start(context.Background(), sock)
	if err != nil {
		fail(err)
	}
	say(map[string]string{"event": "ready"})
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		var c struct {
			Op, Name, Role, Agent, Status, Next, Tool, Command string
		}
		if err := json.Unmarshal(in.Bytes(), &c); err != nil {
			say(map[string]string{"event": "done", "error": err.Error()})
			continue
		}
		var res any
		switch c.Op {
		case "spawn":
			err = call(sock, "", "agent_new", map[string]any{"container": container, "name": c.Name, "role": c.Role}, &res)
		case "task":
			err = call(bus, c.Agent, "task_update", map[string]any{"status_text": c.Status, "next": c.Next}, &res)
		case "inbox":
			err = call(bus, c.Agent, "msg_inbox", nil, &res)
		case "approve":
			// Answered in the browser, or at once ({"id":0}) when no deck is attached.
			go func(agent, tool, command string) {
				var v map[string]any
				err := call(bus, agent, "approval_request", map[string]any{"tool": tool,
					"input": map[string]any{"command": command}, "timeout_s": 120, "always": true}, &v)
				say(map[string]any{"event": "verdict", "result": v, "error": errText(err)})
			}(c.Agent, c.Tool, c.Command)
		default:
			err = fmt.Errorf("unknown op %q", c.Op)
		}
		say(map[string]any{"event": "done", "op": c.Op, "result": res, "error": errText(err)})
	}
}

// start serves the daemon on sock, its store next to it, until ctx ends, and returns the bus
// socket of the container.
func start(ctx context.Context, sock string) (string, error) {
	dir := filepath.Dir(sock)
	st, err := store.Open(filepath.Join(dir, "lea.db"))
	if err != nil {
		return "", err
	}
	now := func() int64 { return time.Now().Unix() }
	d := daemon.New(st, filepath.Join(dir, "run"), now)
	d.SetAgents(&fakeAgents{st: st, now: now, ch: make(chan struct{})})
	l, err := daemon.Listen(sock)
	if err != nil {
		return "", err
	}
	go d.Serve(ctx, l, daemon.Origin{})
	return d.AttachContainer(ctx, container)
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "fixture:", err)
	os.Exit(1)
}

func call(sock, agent, method string, params, result any) error {
	raw, _ := json.Marshal(params)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	return client.Call(ctx, sock, wire.Request{Method: method, Agent: agent, Params: raw}, result)
}

// fakeAgents is the daemon's agent manager without tmux or docker: an agent is its store row.
type fakeAgents struct {
	st  *store.Store
	now func() int64
	mu  sync.Mutex
	ver int64
	ch  chan struct{} // closed at each change
}

func (f *fakeAgents) Changed() {
	f.mu.Lock()
	f.ver++
	close(f.ch)
	f.ch = make(chan struct{})
	f.mu.Unlock()
}

func (f *fakeAgents) Watch(ctx context.Context, c string, since int64, timeout time.Duration) (agents.Snapshot, error) {
	t := time.NewTimer(timeout)
	defer t.Stop()
	for {
		f.mu.Lock()
		v, ch := f.ver, f.ch
		f.mu.Unlock()
		if v != since {
			return f.snapshot(c, v)
		}
		select {
		case <-ch:
		case <-t.C:
			return f.snapshot(c, v)
		case <-ctx.Done():
			return agents.Snapshot{}, ctx.Err()
		}
	}
}

func (f *fakeAgents) snapshot(c string, v int64) (agents.Snapshot, error) {
	as, err := f.st.SpawnedAgents(c)
	if err != nil {
		return agents.Snapshot{}, err
	}
	unread, err := f.st.UnreadCounts()
	if err != nil {
		return agents.Snapshot{}, err
	}
	rows := []agents.Row{}
	for _, a := range as {
		rows = append(rows, agents.Row{Key: a.Key, Name: a.Name, Label: a.Label, Profile: a.Profile, State: a.State,
			Created: a.Created, Updated: a.Updated, Role: a.Role, Parent: a.Parent, Repo: a.Repo, Branch: a.Branch,
			Paused: a.Paused, Held: a.HoldSince > 0, CtxPct: 42, CostUSD: 1.5, Unread: unread[a.Key]})
	}
	return agents.Snapshot{Version: v, Agents: rows}, nil
}

func (f *fakeAgents) New(ctx context.Context, r agents.NewRequest) (agents.Row, error) {
	name := r.Name
	if name == "" {
		as, _ := f.st.SpawnedAgents(r.Container)
		name = fmt.Sprintf("claude-%d", len(as)+1)
	}
	profile := r.Profile
	if profile == "" {
		profile = "claude"
	}
	a, err := f.st.Register(store.NewAgent{Container: r.Container, Name: name, Profile: profile, Role: r.Role,
		Repo: r.Repo, Branch: r.Branch}, store.Caps{}, f.now())
	if err != nil {
		return agents.Row{}, err
	}
	label := r.Label
	if r.Tower {
		label = agents.TowerLabel
	}
	if label != "" {
		if err := f.st.SetLabel(a.Key, label); err != nil {
			return agents.Row{}, err
		}
	}
	f.st.SetState(a.Key, "idle", "", f.now())
	if r.Brief != "" {
		f.st.Send(store.Message{From: "user", To: a.Key, Kind: "note", Body: r.Brief, At: f.now()})
	}
	f.Changed()
	return agents.Row{Key: a.Key, Name: a.Name, Label: label, Profile: profile, State: "idle", Role: r.Role}, nil
}

func (f *fakeAgents) Stop(ctx context.Context, key string) error {
	defer f.Changed()
	return f.st.SetState(key, "done", "", f.now())
}

func (f *fakeAgents) SetLabel(ctx context.Context, key, label string) error {
	defer f.Changed()
	return f.st.SetLabel(key, strings.TrimSpace(label))
}

func (f *fakeAgents) SetPaused(ctx context.Context, key string, paused bool) error {
	defer f.Changed()
	return f.st.SetPaused(key, paused)
}

func (f *fakeAgents) AutoLabel(ctx context.Context, key, label string) (string, error) {
	return f.st.SetLabelUnique(key, label, 24, true)
}

func (f *fakeAgents) Attached(ctx context.Context, c string) (bool, error) { return false, nil }

func (f *fakeAgents) Ensure(ctx context.Context, c, w string) error                { return nil }
func (f *fakeAgents) Focus(ctx context.Context, k string, focus bool) error        { return nil }
func (f *fakeAgents) RemoveEnded(ctx context.Context, k string) error              { return nil }
func (f *fakeAgents) Detach(ctx context.Context, c string) error                   { return nil }
func (f *fakeAgents) MarkStopped(ctx context.Context, c string, forget bool) error { return nil }
func (f *fakeAgents) ResumeStopped(ctx context.Context, c string) error            { return nil }
func (f *fakeAgents) SweepStacks(ctx context.Context, c string) (string, error)    { return "", nil }
func (f *fakeAgents) ParkStack(ctx context.Context, k string) (string, error)      { return "", nil }
func (f *fakeAgents) UnparkStack(ctx context.Context, k string) (string, error)    { return "", nil }
func (f *fakeAgents) SoftStop(ctx context.Context, k string) error                 { return nil }
func (f *fakeAgents) ResumeSuspended(ctx context.Context, k, p string) error       { return nil }
func (f *fakeAgents) EndSuspended(ctx context.Context, k string) (bool, error)     { return true, nil }
func (f *fakeAgents) Popup(ctx context.Context, k string, compose bool) error      { return nil }
func (f *fakeAgents) OpenShell(ctx context.Context, c string) (agents.Row, error) {
	return agents.Row{}, fmt.Errorf("no shell in the fixture")
}
func (f *fakeAgents) SplitShell(ctx context.Context, k string, below bool) (agents.Row, error) {
	return agents.Row{}, fmt.Errorf("no shell in the fixture")
}
