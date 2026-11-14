package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/agents"
	"github.com/DavidGodefroid/lea/internal/client"
	"github.com/DavidGodefroid/lea/internal/daemon"
	"github.com/DavidGodefroid/lea/internal/deck"
	"github.com/DavidGodefroid/lea/internal/launcher"
	"github.com/DavidGodefroid/lea/internal/settings"
	"github.com/DavidGodefroid/lea/internal/store"
	"github.com/DavidGodefroid/lea/internal/wire"
)

// regAgents is an agent manager that starts nothing: New records the agent in the store, as
// running, the way the real manager does. The daemon calls no other method here.
type regAgents struct {
	daemon.Agents
	st *store.Store
	mu sync.Mutex
	n  int
}

func (r *regAgents) New(ctx context.Context, nr agents.NewRequest) (agents.Row, error) {
	r.mu.Lock()
	r.n++
	name := nr.NamePrefix + "-" + string(rune('0'+r.n))
	r.mu.Unlock()
	a, err := r.st.Register(store.NewAgent{Container: nr.Container, Name: name, Profile: "claude", Workdir: "/p", Role: nr.Role, Parent: nr.Parent}, store.Caps{}, time.Now().Unix())
	if err != nil {
		return agents.Row{}, err
	}
	return agents.Row{Key: a.Key, Name: a.Name, Role: a.Role, Profile: a.Profile, State: "working", Label: nr.Label}, nil
}

func (r *regAgents) Changed() {}

func (r *regAgents) started() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

// liveLimitsDaemon runs a daemon on the store at dir with the live limits of the lea installed
// at root (its user configuration in the environment), until stop.
func liveLimitsDaemon(t *testing.T, dir, root string) (api, bus string, st *store.Store, ra *regAgents, d *daemon.Daemon, stop func()) {
	t.Helper()
	st, err := store.Open(filepath.Join(dir, "lea.db"))
	if err != nil {
		t.Fatal(err)
	}
	d = daemon.New(st, filepath.Join(dir, "run"), func() int64 { return time.Now().Unix() })
	ra = &regAgents{st: st}
	d.SetAgents(ra)
	daemonLimits(d, filepath.Join(root, "build", "lea"), os.Stderr)
	ctx, cancel := context.WithCancel(context.Background())
	api = filepath.Join(dir, "d.sock")
	l, err := daemon.Listen(api)
	if err != nil {
		t.Fatal(err)
	}
	go d.Serve(ctx, l, daemon.Origin{})
	if bus, err = d.AttachContainer(ctx, "lea-a"); err != nil {
		t.Fatal(err)
	}
	stop = func() {
		cancel()
		for i := 0; i < 100; i++ {
			if _, err := os.Stat(api); os.IsNotExist(err) {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		st.Close()
	}
	return api, bus, st, ra, d, stop
}

func spawnAs(t *testing.T, bus, agent string, params map[string]any) (map[string]any, error) {
	t.Helper()
	var out map[string]any
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, _ := json.Marshal(params)
	err := client.Call(ctx, bus, wire.Request{Method: "agents_spawn", Agent: agent, Params: p}, &out)
	return out, err
}

func waitStarted(t *testing.T, ra *regAgents, n int) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if ra.started() == n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%d agents started, want %d", ra.started(), n)
}

// E2E of #420 on a real daemon, its sockets, the user configuration on disk and the CLI: with
// max_running 1 a lead's second child waits, and the board says why; lea limits set raises the
// cap on the same running daemon (no restart, no stamp changed) and the queued child starts;
// the deck's settings screen (its daemon client) does the same; the value outlives a restart.
func TestLiveLimitsE2E(t *testing.T) {
	dir, _ := os.MkdirTemp("", "lea")
	t.Cleanup(func() { os.RemoveAll(dir) })
	root := filepath.Join(dir, "root")
	if err := os.MkdirAll(filepath.Join(root, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "lea.conf"), []byte("LEA_APPROVAL_TIMEOUT=60\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	user := filepath.Join(dir, "lea.conf")
	if err := os.WriteFile(user, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LEA_CONFIG", user)
	t.Setenv("LEA_MAX_RUNNING", "")
	cfg := func() *launcher.Config {
		c, err := launcher.LoadConfig(os.Getenv, root, dir)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	stamps := func() string {
		c := cfg()
		return strings.Join([]string{launcher.DaemonResumeStamp(c.Get), launcher.DaemonLockHTTPStamp(c.Get), launcher.DaemonHubStamp(c.Get),
			launcher.DaemonStacksStamp(c.Get), launcher.DaemonAdmitStamp(c.Get), launcher.DaemonDeckStamp(c.Get)}, "|")
	}
	before := stamps()

	api, bus, st, ra, d, stop := liveLimitsDaemon(t, dir, root)
	env := map[string]string{"LEA_DAEMON_SOCK": api}
	if rc, out, e := runEnv(t, env, "limits"); rc != 0 || !strings.Contains(out, "max_running  3") || !strings.Contains(out, "from lea's default; 0 in use") {
		t.Fatalf("lea limits: rc=%d out=%q err=%q", rc, out, e)
	}
	tower, err := st.Register(store.NewAgent{Container: "lea-a", Name: "tower", Profile: "claude", Workdir: "/p", Role: "lead"}, store.Caps{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if rc, out, e := runEnv(t, env, "limits", "set", "max_running", "1"); rc != 0 || !strings.Contains(out, "max_running  1") || !strings.Contains(out, "from your file") || !strings.Contains(out, "applies now: new spawns") {
		t.Fatalf("set 1: rc=%d out=%q err=%q", rc, out, e)
	}
	if b, _ := os.ReadFile(user); string(b) != "LEA_MAX_RUNNING='1'\n" {
		t.Fatalf("user file=%q", b)
	}

	// A lead spawns two children with queue: true: one runs, one waits.
	if r, err := spawnAs(t, bus, "tower", map[string]any{"brief": "first", "queue": true}); err != nil || r["name"] == nil {
		t.Fatalf("first: %v %v", r, err)
	}
	r, err := spawnAs(t, bus, "tower", map[string]any{"brief": "second", "label": "lea#2 second", "queue": true})
	if err != nil || r["queued"] == nil || !strings.Contains(r["reason"].(string), "1 children already running (limit max_running 1)") {
		t.Fatalf("second: %v %v", r, err)
	}
	if ra.started() != 1 {
		t.Fatalf("%d started", ra.started())
	}
	if rc, out, e := runEnv(t, env, "board"); rc != 0 || !strings.Contains(out, "### Queued spawns") || !strings.Contains(out, `lea\#2 second (dev) for tower: queued\: max\_running 1\/1`) {
		t.Fatalf("board: rc=%d out=%s err=%q", rc, out, e)
	}

	// Raised on the running daemon: the queued child starts, the daemon was not restarted (the
	// same one serves) and no stamp the launcher compares changed.
	if rc, out, e := runEnv(t, env, "limits", "set", "max_running", "2"); rc != 0 || !strings.Contains(out, "max_running  2") {
		t.Fatalf("set 2: rc=%d out=%q err=%q", rc, out, e)
	}
	waitStarted(t, ra, 2)
	if got := stamps(); got != before {
		t.Fatalf("a daemon stamp changed: %q, was %q", got, before)
	}
	// The start counts until it ends, then the agent it started does.
	for i := 0; ; i++ {
		rc, out, _ := runEnv(t, env, "limits", "get", "max_running")
		if rc == 0 && strings.Contains(out, "2 in use") {
			break
		}
		if i == 100 {
			t.Fatalf("get: %q", out)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The deck's settings screen sets it through the same daemon method.
	if r, err := spawnAs(t, bus, "tower", map[string]any{"brief": "third", "queue": true}); err != nil || r["queued"] == nil {
		t.Fatalf("third: %v %v", r, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := (deck.Client{Sock: api}).SetLimit(ctx, "max_running", "3"); err != nil {
		t.Fatal(err)
	}
	waitStarted(t, ra, 3)
	if s := findSetting(t, settings.Store{Root: root, Cwd: dir, Getenv: os.Getenv}, "LEA_MAX_RUNNING"); s.Value != "3" || s.Source != settings.SourceFile {
		t.Fatalf("deck's screen reads %+v", s)
	}
	// A cap lowered below the running children stops none.
	if rc, _, e := runEnv(t, env, "limits", "set", "max_running", "1"); rc != 0 {
		t.Fatal(e)
	}
	kids, _ := st.Agents()
	for _, a := range kids {
		if a.Parent == tower.Key && (a.State == "done" || a.State == "stopped") {
			t.Fatalf("%s was stopped", a.Name)
		}
	}
	_ = d

	// After lea stop --all and a new lea: the new daemon reads the value from the file.
	stop()
	api, _, _, _, _, stop = liveLimitsDaemon(t, dir, root)
	defer stop()
	env["LEA_DAEMON_SOCK"] = api
	if rc, out, e := runEnv(t, env, "limits"); rc != 0 || !strings.Contains(out, "max_running  1") || !strings.Contains(out, "from your file; 3 in use") {
		t.Fatalf("after a restart: rc=%d out=%q err=%q", rc, out, e)
	}
	if rc, out, e := runEnv(t, env, "limits", "reset", "max_running"); rc != 0 || !strings.Contains(out, "max_running  3") || !strings.Contains(out, "from lea's default") {
		t.Fatalf("reset: rc=%d out=%q err=%q", rc, out, e)
	}
	for _, bad := range [][]string{{"limits", "set", "max_running"}, {"limits", "nope"}, {"limits", "reset"}} {
		if rc, _, _ := runEnv(t, env, bad...); rc != 2 {
			t.Fatalf("%v: rc=%d", bad, rc)
		}
	}
	if rc, _, e := runEnv(t, env, "limits", "set", "max_running", "lots"); rc != 1 || !strings.Contains(e, "whole number") {
		t.Fatalf("bad value: rc=%d err=%q", rc, e)
	}
}

func findSetting(t *testing.T, s settings.Store, key string) settings.Setting {
	t.Helper()
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range list {
		if x.Key == key {
			return x
		}
	}
	t.Fatalf("no setting %s", key)
	return settings.Setting{}
}
