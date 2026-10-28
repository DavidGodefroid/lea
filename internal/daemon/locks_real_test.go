package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/agents"
	"github.com/DavidGodefroid/lea/internal/tmux"
)

// realContainers stands in for the container runtime: the deck's container serves /home/u/p.
type realContainers struct{}

func (realContainers) Live(context.Context, string) ([]string, error)   { return nil, nil }
func (realContainers) Stop(context.Context, string, string) error       { return nil }
func (realContainers) Shells(context.Context, string) ([]string, error) { return nil, nil }
func (realContainers) Teardown(context.Context, string, string) error   { return nil }
func (realContainers) Sweep(context.Context, string, []string) (string, error) {
	return "", nil
}
func (realContainers) Park(context.Context, string, string) (string, error) { return "", nil }
func (realContainers) StopShell(context.Context, string, string) error      { return nil }
func (realContainers) Pause(context.Context, string, string, bool) error {
	return nil
}
func (realContainers) Projects(context.Context, string) (string, error) { return "/home/u/p", nil }

// noConsoles: no console container exists or runs.
type noConsoles struct{}

func (noConsoles) ConsoleState(context.Context, string) (agents.ConsoleState, error) {
	return agents.ConsoleState{}, nil
}
func (noConsoles) RemoveConsole(context.Context, string) error  { return nil }
func (noConsoles) StopConsole(context.Context, string) error    { return nil }
func (noConsoles) StopConsolesOf(context.Context, string) error { return nil }
func (noConsoles) Unconfined(context.Context, string, string) ([]string, error) {
	return nil, nil
}

// TestRealLockRequestOpensOnlyWhenTheUserSaysSo runs the whole path against a real tmux server,
// with a stand-in for docker run: an agent asks over its bus; nothing opens, no console runs; the
// user opens it through the host API; the console's pane sits under the agent's slot in the deck
// window with the keyboard left on the deck, from the fixed template with the requested profile.
// The agent learns opened, and a second answer is refused.
func TestRealLockRequestOpensOnlyWhenTheUserSaysSo(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed: make go-test-tmux runs this test")
	}
	e := start(t)
	dir := shortDir(t)
	t.Setenv("TMUX_TMPDIR", dir)
	conf := filepath.Join(dir, "tmux.conf")
	if err := tmux.WriteConf(conf); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "lea")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 600\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	docker := filepath.Join(dir, "docker")
	argv := filepath.Join(dir, "argv")
	if err := os.WriteFile(docker, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > "+argv+"\nexec sleep 600\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	const sock, C = "lea-test-lockreq", "lea-a"
	t.Cleanup(func() { exec.Command("tmux", "-L", sock, "kill-server").Run() })
	tm := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-L", sock, "-f", conf}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	spec := agents.LockSpec{Enabled: true, Image: "locksql:0.8.0", AgentProfiles: []string{"prod"},
		ConsoleDir: func(string) error { return nil },
		ContainerName: func(p string) string {
			if p == "/home/u/p" {
				return C
			}
			return "lea-other"
		}}
	m := agents.NewManager(agents.Config{Store: e.d.st, Term: agents.Tmux(tmux.Server{Socket: sock, Conf: conf}),
		Containers: realContainers{}, LeaBin: bin, Docker: docker, UID: 1000, GID: 1000,
		Now: func() int64 { return *e.clock }, Consoles: noConsoles{},
		LockSpec: func(string) (agents.LockSpec, error) { return spec, nil }})
	e.d.SetAgents(m)
	if err := m.Ensure(e.ctx, C, "/"); err != nil {
		t.Fatal(err)
	}
	spawned(t, e, "dev", "dev", "")
	panes := func() string {
		return tm("list-panes", "-s", "-t", "="+C, "-F", "#{@lea_role}|#{@lea_lock}|#{@lea_deck}|#{pane_at_top}|#{pane_active}")
	}
	before := panes()

	// A profile the user did not allow: refused, nothing recorded.
	if _, err := lockAsk(t, e, "dev", "locksql", "staging"); err == nil || !strings.Contains(err.Error(), "not one agents may ask for") {
		t.Fatalf("staging: %v", err)
	}
	r, err := lockAsk(t, e, "dev", "locksql", "prod")
	if err != nil || r.Status != "requested" {
		t.Fatalf("lock_request: %+v %v", r, err)
	}
	// The agent asked: nothing opened, no console ran.
	time.Sleep(300 * time.Millisecond)
	if got := panes(); got != before {
		t.Fatalf("a request changed the panes:\n%s\nwas\n%s", got, before)
	}
	if _, err := os.Stat(argv); !os.IsNotExist(err) {
		t.Fatalf("a console ran on a request: %v", err)
	}
	// The deck's row shows the request.
	snap, err := m.Watch(e.ctx, C, 0, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	shown := false
	for _, a := range snap.Agents {
		shown = shown || a.Key == C+"/dev" && a.LockAsk != nil && a.LockAsk.ID == r.Request
	}
	if !shown {
		t.Fatalf("no card: %+v", snap.Agents)
	}

	// The user opens it.
	if st, err := decideLock(t, e, C, r.Request, true); err != nil || st != "opened" {
		t.Fatalf("decide: %q %v", st, err)
	}
	got := panes()
	for _, want := range []string{"deck||1|1|1", "lock|locksql/prod|1|0|0"} {
		if !strings.Contains(got, want) {
			t.Fatalf("no pane %q in\n%s", want, got)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	var a []byte
	for len(a) == 0 {
		a, _ = os.ReadFile(argv)
		if time.Now().After(deadline) {
			t.Fatal("the console never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if s := string(a); !strings.HasPrefix(s, "run\n-it\n--rm\n--log-driver\nnone\n") || !strings.HasSuffix(s, "\nlocksql:0.8.0\nconsole\n--project\n/home/u/p\n--profile\nprod\n") {
		t.Fatalf("console argv:\n%s", s)
	}
	if _, err := decideLock(t, e, C, r.Request, true); err == nil || !strings.Contains(err.Error(), "already answered") {
		t.Fatalf("replay: %v", err)
	}
	ms := inboxOf(t, e, "dev")
	if len(ms) != 1 || !strings.Contains(ms[0].Text, "lock request #1 (locksql/prod): opened by the user.") {
		t.Fatalf("inbox=%+v", ms)
	}
}
