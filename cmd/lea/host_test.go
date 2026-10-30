package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/agents"
	"github.com/DavidGodefroid/lea/internal/daemon"
	"github.com/DavidGodefroid/lea/internal/store"
)

func startDaemon(t *testing.T) (api, bus string) {
	t.Helper()
	api, bus, _ = startDaemonWithStore(t)
	return api, bus
}

func startDaemonWithStore(t *testing.T) (api, bus string, st *store.Store) {
	t.Helper()
	dir, _ := os.MkdirTemp("", "lea")
	t.Cleanup(func() { os.RemoveAll(dir) })
	st, err := store.Open(filepath.Join(dir, "lea.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	d := daemon.New(st, filepath.Join(dir, "run"), func() int64 { return time.Now().Unix() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	api = filepath.Join(dir, "d.sock")
	l, err := daemon.Listen(api)
	if err != nil {
		t.Fatal(err)
	}
	go d.Serve(ctx, l, daemon.Origin{})
	bus, err = d.AttachContainer(ctx, "lea-a")
	if err != nil {
		t.Fatal(err)
	}
	return api, bus, st
}

func runEnv(t *testing.T, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
	var out, errb bytes.Buffer
	rc := run(args, strings.NewReader(""), &out, &errb)
	return rc, out.String(), errb.String()
}

func TestHostMsgAndAgents(t *testing.T) {
	api, bus := startDaemon(t)
	// an agent registers through the bus
	if rc, _, e := runEnv(t, map[string]string{"LEA_BUS_SOCK": bus, "LEA_AGENT": "dev"}, "inbox", "--agent"); rc != 0 {
		t.Fatalf("agent inbox rc=%d %s", rc, e)
	}
	rc, out, e := runEnv(t, map[string]string{"LEA_DAEMON_SOCK": api}, "agents")
	if rc != 0 || !strings.Contains(out, "lea-a/dev") {
		t.Fatalf("agents rc=%d out=%q err=%q", rc, out, e)
	}
	if rc, _, e := runEnv(t, map[string]string{"LEA_DAEMON_SOCK": api}, "msg", "dev", "hello", "there"); rc != 0 {
		t.Fatalf("msg rc=%d %s", rc, e)
	}
	rc, out, _ = runEnv(t, map[string]string{"LEA_BUS_SOCK": bus, "LEA_AGENT": "dev"}, "inbox", "--agent")
	if rc != 0 || !strings.Contains(out, "hello there") {
		t.Fatalf("inbox out=%q", out)
	}
}

// lea inbox --agent --wait returns on a message, names its sender, and leaves it to msg_inbox.
func TestAgentInboxWaitEndsOnAMessage(t *testing.T) {
	api, bus := startDaemon(t)
	agent := map[string]string{"LEA_BUS_SOCK": bus, "LEA_AGENT": "dev"}
	if rc, out, _ := runEnv(t, agent, "inbox", "--agent", "--wait", "1"); rc != 1 || !strings.Contains(out, "no message") {
		t.Fatalf("empty wait rc=%d out=%q", rc, out)
	}
	if rc, _, e := runEnv(t, map[string]string{"LEA_DAEMON_SOCK": api}, "msg", "dev", "hello"); rc != 0 {
		t.Fatalf("msg rc=%d %s", rc, e)
	}
	rc, out, e := runEnv(t, agent, "inbox", "--agent", "--wait", "5")
	if rc != 0 || !strings.Contains(out, "1 unread message(s) from user") || strings.Contains(out, "hello") {
		t.Fatalf("wait rc=%d out=%q err=%q", rc, out, e)
	}
	if rc, out, _ := runEnv(t, agent, "inbox", "--agent"); rc != 0 || !strings.Contains(out, "hello") {
		t.Fatalf("the wait took the message: rc=%d out=%q", rc, out)
	}
	for _, bad := range [][]string{{"0"}, {"3601"}, {"x"}, {"1", "2"}} {
		if rc, _, _ := runEnv(t, agent, append([]string{"inbox", "--agent", "--wait"}, bad...)...); rc != 2 {
			t.Fatalf("--wait %q rc=%d, want 2", bad, rc)
		}
	}
}

func TestHostAgentsShowsTheLabelBesideTheName(t *testing.T) {
	api, _, st := startDaemonWithStore(t)
	for _, n := range []string{"claude-1", "claude-2"} {
		if _, err := st.RegisterAgent("lea-a", n, "claude", "/p", 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetLabel("lea-a/claude-1", "auth work"); err != nil {
		t.Fatal(err)
	}
	rc, out, e := runEnv(t, map[string]string{"LEA_DAEMON_SOCK": api}, "agents")
	if rc != 0 || !strings.Contains(out, "auth work (lea-a/claude-1)") || strings.Contains(out, "(lea-a/claude-2)") ||
		!strings.Contains(out, "lea-a/claude-2") {
		t.Fatalf("agents rc=%d out=%q err=%q", rc, out, e)
	}
}

func TestHostDaemonDown(t *testing.T) {
	rc, _, e := runEnv(t, map[string]string{"LEA_DAEMON_SOCK": "/nonexistent/d.sock"}, "agents")
	if rc != 1 || !strings.Contains(e, "lea daemon is not running") {
		t.Fatalf("rc=%d err=%q", rc, e)
	}
}

func TestApproveUsage(t *testing.T) {
	rc, _, e := runEnv(t, nil, "approve", "7", "maybe")
	if rc != 2 || !strings.Contains(e, "allow|deny") {
		t.Fatalf("rc=%d err=%q", rc, e)
	}
}

func TestEnsureHostDir(t *testing.T) {
	base := t.TempDir()
	d := filepath.Join(base, "host")
	os.Mkdir(d, 0o755)
	os.Chmod(d, 0o755)
	if err := ensureHostDir(d); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(d); fi.Mode().Perm() != 0o700 {
		t.Fatal(fi.Mode())
	}
	link := filepath.Join(base, "link")
	os.Symlink(d, link)
	if err := ensureHostDir(link); err == nil || !strings.Contains(err.Error(), link) {
		t.Fatalf("symlink not refused: %v", err)
	}
}

func TestCleanAndOneLine(t *testing.T) {
	got := oneLine("a\n#99 agent tool x\x1b[2J\u009b", 200)
	if strings.ContainsAny(got, "\n\x1b\u009b") || !strings.Contains(got, `\x0a#99`) || !strings.Contains(got, `\x1b[2J`) {
		t.Fatalf("%q", got)
	}
	if got := clean("a\nb\x07", true); got != "a\nb\\x07" {
		t.Fatalf("%q", got)
	}
	if got := oneLine(strings.Repeat("é", 300), 200); len([]rune(got)) != 201 {
		t.Fatal(len([]rune(got)))
	}
	if got := clean("a\u202eb\u200bc", false); got != `a\u202eb\u200bc` {
		t.Fatalf("bidi and zero-width: %q", got)
	}
	if got := clean("a\u00adb\u061cc\U000e0041d\ufff9", false); got != `a\u00adb\u061cc\U000e0041d\ufff9` {
		t.Fatalf("soft hyphen, ALM, tags, annotations: %q", got)
	}
}

type cliAgents struct {
	attached  bool
	mu        sync.Mutex
	calls     []string
	reqs      []agents.NewRequest
	snap      agents.Snapshot
	ensureErr error
	sweep     string // what SweepStacks reports
	sweepErr  error
}

func (c *cliAgents) rec(s string) { c.mu.Lock(); c.calls = append(c.calls, s); c.mu.Unlock() }
func (c *cliAgents) SetLabel(ctx context.Context, k, label string) error {
	c.rec("label " + k)
	return nil
}
func (c *cliAgents) SetPaused(ctx context.Context, k string, paused bool) error {
	c.rec("paused " + k)
	return nil
}
func (c *cliAgents) OpenShell(ctx context.Context, ctr string) (agents.Row, error) {
	c.rec("shell " + ctr)
	return agents.Row{}, nil
}
func (c *cliAgents) SplitShell(ctx context.Context, k string, below bool) (agents.Row, error) {
	c.rec("split " + k)
	return agents.Row{}, nil
}
func (c *cliAgents) LockOpen(ctx context.Context, ctr, tool, profile string) (string, error) {
	c.rec("lock-open " + ctr + " " + tool + " " + profile)
	return "opened", nil
}
func (c *cliAgents) LockClose(ctx context.Context, ctr, tool, profile string) (string, error) {
	c.rec("lock-close " + ctr + " " + tool + " " + profile)
	return "closed", nil
}
func (c *cliAgents) LockCheckRequest(ctx context.Context, ctr, tool, profile string) error {
	c.rec("lock-check " + ctr + " " + tool + " " + profile)
	return nil
}
func (c *cliAgents) LockList(ctx context.Context, ctr string) ([]agents.LockRow, error) {
	c.rec("lock-list " + ctr)
	return []agents.LockRow{{ID: "locksql/prod", Shown: true}, {ID: "locksql"}}, nil
}
func (c *cliAgents) Ensure(ctx context.Context, ctr, w string) error {
	c.rec("ensure " + ctr + " " + w)
	return c.ensureErr
}
func (c *cliAgents) New(ctx context.Context, r agents.NewRequest) (agents.Row, error) {
	c.mu.Lock()
	c.reqs = append(c.reqs, r)
	c.mu.Unlock()
	c.rec("new")
	name := r.Name
	if name == "" {
		name = "claude-1"
	}
	return agents.Row{Key: r.Container + "/" + name, Name: name, Profile: "claude", State: "unknown"}, nil
}
func (c *cliAgents) Focus(ctx context.Context, k string, f bool) error { return nil }
func (c *cliAgents) Stop(ctx context.Context, k string) error          { return nil }
func (c *cliAgents) RemoveEnded(ctx context.Context, k string) error   { return nil }
func (c *cliAgents) Detach(ctx context.Context, ctr string) error      { return nil }
func (c *cliAgents) Watch(ctx context.Context, ctr string, since int64, to time.Duration) (agents.Snapshot, error) {
	c.rec(fmt.Sprintf("watch %s", ctr))
	return c.snap, nil
}
func (c *cliAgents) MarkStopped(ctx context.Context, ctr string, forget bool) error {
	c.rec(fmt.Sprintf("mark-stopped %s %v", ctr, forget))
	return nil
}
func (c *cliAgents) ResumeStopped(ctx context.Context, ctr string) error {
	c.rec("resume-stopped " + ctr)
	return nil
}
func (c *cliAgents) ParkStack(ctx context.Context, key string) (string, error) {
	c.rec("park " + key)
	return "", nil
}
func (c *cliAgents) UnparkStack(ctx context.Context, key string) (string, error) {
	c.rec("unpark " + key)
	return "", nil
}
func (c *cliAgents) SoftStop(ctx context.Context, key string) error {
	c.rec("softstop " + key)
	return nil
}
func (c *cliAgents) ResumeSuspended(ctx context.Context, key, prompt string) error {
	c.rec("resume " + key)
	return nil
}
func (c *cliAgents) SweepStacks(ctx context.Context, ctr string) (string, error) {
	c.rec("sweep " + ctr)
	return c.sweep, c.sweepErr
}
func (c *cliAgents) Attached(ctx context.Context, ctr string) (bool, error) {
	c.rec("attached " + ctr)
	return c.attached, nil
}
func (c *cliAgents) Changed()                                                {}
func (c *cliAgents) Popup(ctx context.Context, k string, compose bool) error { return nil }

func startDaemonWith(t *testing.T, a daemon.Agents) string {
	t.Helper()
	dir, _ := os.MkdirTemp("", "lea")
	t.Cleanup(func() { os.RemoveAll(dir) })
	st, err := store.Open(filepath.Join(dir, "lea.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	d := daemon.New(st, filepath.Join(dir, "run"), func() int64 { return time.Now().Unix() })
	d.SetAgents(a)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	api := filepath.Join(dir, "d.sock")
	l, err := daemon.Listen(api)
	if err != nil {
		t.Fatal(err)
	}
	go d.Serve(ctx, l, daemon.Origin{})
	return api
}

func TestParseNew(t *testing.T) {
	o, err := parseNew([]string{"--container", "lea-a", "-p", "claude", "-r", "reviewer", "--repo", "shop",
		"--branch", "feat/x", "--brief", "b.md", "rev", "--", "--resume", "-c"})
	want := newOpts{Container: "lea-a", Profile: "claude", Name: "rev", Role: "reviewer", Repo: "shop",
		Branch: "feat/x", BriefFile: "b.md", Args: []string{"--resume", "-c"}}
	if err != nil || !reflect.DeepEqual(o, want) {
		t.Fatalf("opts=%+v err=%v", o, err)
	}
	if o, err := parseNew([]string{"--container", "lea-a", "--", "-p"}); err != nil || o.Name != "" || !reflect.DeepEqual(o.Args, []string{"-p"}) {
		t.Fatalf("opts=%+v err=%v", o, err)
	}
	for _, bad := range [][]string{
		{"-r"}, {"--role"}, {"--repo"}, {"--branch"}, {"--brief"}, {"--strict"}, {"--bogus"}, {"one", "two"}, {"-p"}, {"--container"},
	} {
		if _, err := parseNew(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
	// The launchers always pass the session's --workdir first: with --repo it is dropped (the agent
	// starts in the projects directory). A second --workdir is the user's own, refused with --repo.
	if o, err := parseNew([]string{"--container", "lea-a", "--workdir", "/p", "--repo", "shop"}); err != nil || o.Workdir != "" || o.Repo != "shop" {
		t.Fatalf("launcher form with --repo: opts=%+v err=%v", o, err)
	}
	for _, both := range [][]string{{"--workdir", "/q", "--repo", "shop"}, {"--repo", "shop", "--workdir", "/q"}} {
		if _, err := parseNew(append([]string{"--container", "lea-a", "--workdir", "/p"}, both...)); err == nil || !strings.Contains(err.Error(), "--repo and --workdir") {
			t.Fatalf("%q: err=%v", both, err)
		}
	}
	if _, err := parseNew([]string{"--strict"}); err == nil || !strings.Contains(err.Error(), "not available yet") {
		t.Fatalf("strict: err=%v", err)
	}
}

func TestNewSendsTheRequest(t *testing.T) {
	ca := &cliAgents{}
	api := startDaemonWith(t, ca)
	rc, out, e := runEnv(t, map[string]string{"LEA_DAEMON_SOCK": api},
		"new", "--container", "lea-a", "--workdir", "/p", "-p", "claude", "rev", "--", "--resume")
	if rc != 0 || !strings.Contains(out, "started rev; open the deck with: lea") {
		t.Fatalf("rc=%d out=%q err=%q", rc, out, e)
	}
	want := agents.NewRequest{Container: "lea-a", Workdir: "/p", Profile: "claude", Name: "rev", Args: []string{"--resume"}, Role: "dev"}
	if len(ca.reqs) != 1 || !reflect.DeepEqual(ca.reqs[0], want) {
		t.Fatalf("requests=%+v", ca.reqs)
	}
}

func TestNewNamesTheDeckOnlyWhenItIsNotOpen(t *testing.T) {
	ca := &cliAgents{attached: true}
	api := startDaemonWith(t, ca)
	rc, out, e := runEnv(t, map[string]string{"LEA_DAEMON_SOCK": api}, "new", "--container", "lea-a", "--workdir", "/p", "rev")
	if rc != 0 || strings.Contains(out, "open the deck") || !strings.Contains(out, "started rev; shown in the deck") {
		t.Fatalf("rc=%d out=%q err=%q", rc, out, e)
	}
}

func TestNewSendsRoleRepoBranchAndBrief(t *testing.T) {
	ca := &cliAgents{}
	api := startDaemonWith(t, ca)
	brief := filepath.Join(t.TempDir(), "brief.md")
	if err := os.WriteFile(brief, []byte("review the cart\nand the tests\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The launchers' call form (the one run routes to the core): with --repo, the session's
	// --workdir is dropped and the daemon gets an empty workdir (the agents manager starts the
	// agent in the projects directory: TestNewInARepoWithoutBranchOrBriefIsToldItsRepo).
	rc, _, e := runEnv(t, map[string]string{"LEA_DAEMON_SOCK": api}, "new", "--container", "lea-a", "--workdir", "/p",
		"-r", "reviewer", "--repo", "shop", "--branch", "feat/x", "--brief", brief, "rev")
	want := agents.NewRequest{Container: "lea-a", Name: "rev", Role: "reviewer", Repo: "shop", Branch: "feat/x",
		Brief: "review the cart\nand the tests\n", BriefFrom: "user"}
	if rc != 0 || len(ca.reqs) != 1 || !reflect.DeepEqual(ca.reqs[0], want) {
		t.Fatalf("rc=%d err=%q requests=%+v", rc, e, ca.reqs)
	}
}

func TestNewRefusesAnOversizedOrMissingBrief(t *testing.T) {
	ca := &cliAgents{}
	api := startDaemonWith(t, ca)
	big := filepath.Join(t.TempDir(), "big.md")
	os.WriteFile(big, bytes.Repeat([]byte("a"), agents.MaxBrief+1), 0o600)
	empty := filepath.Join(t.TempDir(), "empty.md")
	os.WriteFile(empty, []byte(" \n\t\n"), 0o600)
	for _, f := range []string{big, empty, filepath.Join(t.TempDir(), "missing.md")} {
		rc, _, e := runEnv(t, map[string]string{"LEA_DAEMON_SOCK": api}, "new", "--container", "lea-a", "--workdir", "/p", "--brief", f)
		if rc != 1 || !strings.HasPrefix(e, "lea new:") || (f == empty && !strings.Contains(e, "is empty")) {
			t.Fatalf("%s: rc=%d err=%q", f, rc, e)
		}
	}
	if len(ca.reqs) != 0 {
		t.Fatalf("requests=%+v", ca.reqs)
	}
}

func TestDeckOpenStartsTheDefaultAgentAsALead(t *testing.T) {
	ca := &cliAgents{}
	api := startDaemonWith(t, ca)
	if rc, _, e := runEnv(t, map[string]string{"LEA_DAEMON_SOCK": api}, "deck-open", "--container", "lea-a", "--workdir", "/p"); rc != 0 {
		t.Fatalf("rc=%d err=%q", rc, e)
	}
	if len(ca.reqs) != 1 || ca.reqs[0].Role != "lead" {
		t.Fatalf("requests=%+v", ca.reqs)
	}
}

// startDaemonStore is startDaemonWith (a may be nil) that also returns the store.
func startDaemonStore(t *testing.T, a daemon.Agents) (string, *store.Store) {
	t.Helper()
	dir, _ := os.MkdirTemp("", "lea")
	t.Cleanup(func() { os.RemoveAll(dir) })
	st, err := store.Open(filepath.Join(dir, "lea.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	d := daemon.New(st, filepath.Join(dir, "run"), func() int64 { return time.Now().Unix() })
	if a != nil {
		d.SetAgents(a)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	api := filepath.Join(dir, "d.sock")
	l, err := daemon.Listen(api)
	if err != nil {
		t.Fatal(err)
	}
	go d.Serve(ctx, l, daemon.Origin{})
	return api, st
}

func TestRoleCommand(t *testing.T) {
	api, st := startDaemonStore(t, nil)
	if _, err := st.Register(store.NewAgent{Container: "lea-a", Name: "claude-1", Profile: "claude", Workdir: "/p", Role: "dev"}, store.Caps{}, 1); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"LEA_DAEMON_SOCK": api}
	if rc, out, e := runEnv(t, env, "role", "claude-1", "lead"); rc != 0 || !strings.Contains(out, "lea-a/claude-1 is now lead") {
		t.Fatalf("rc=%d out=%q err=%q", rc, out, e)
	}
	if rc, _, e := runEnv(t, env, "role", "claude-1", "admin"); rc != 1 || !strings.Contains(e, "unknown role") {
		t.Fatalf("rc=%d err=%q", rc, e)
	}
	if rc, _, e := runEnv(t, env, "role", "claude-1"); rc != 2 || !strings.Contains(e, "lead, dev, reviewer") {
		t.Fatalf("rc=%d err=%q", rc, e)
	}
}

func TestDeckOpenSpawnsTheDefaultAgentOnlyWhenNoneRuns(t *testing.T) {
	for _, tc := range []struct {
		name   string
		states []string
		args   []string
		spawn  bool
	}{
		{"no agent", nil, nil, true},
		{"only ended agents", []string{"done", "done"}, nil, true},
		{"one idle agent", []string{"done", "idle"}, nil, false},
		{"no default asked", nil, []string{"--no-default"}, false},
	} {
		ca := &cliAgents{}
		for i, s := range tc.states {
			ca.snap.Agents = append(ca.snap.Agents, agents.Row{Key: fmt.Sprintf("lea-a/a%d", i), State: s})
		}
		api := startDaemonWith(t, ca)
		args := append([]string{"deck-open", "--container", "lea-a", "--workdir", "/p"}, tc.args...)
		rc, _, e := runEnv(t, map[string]string{"LEA_DAEMON_SOCK": api}, args...)
		if rc != 0 {
			t.Fatalf("%s: rc=%d err=%q", tc.name, rc, e)
		}
		// The sweep comes at every lea (#132), then the deck.
		if len(ca.calls) < 2 || ca.calls[0] != "sweep lea-a" || ca.calls[1] != "ensure lea-a /p" {
			t.Fatalf("%s: calls=%q", tc.name, ca.calls)
		}
		spawned := len(ca.reqs) == 1
		if spawned != tc.spawn {
			t.Fatalf("%s: spawned=%v calls=%q", tc.name, spawned, ca.calls)
		}
		if spawned && !reflect.DeepEqual(ca.reqs[0], agents.NewRequest{Container: "lea-a", Workdir: "/p", Profile: "claude", Focus: true, Role: "lead"}) {
			t.Fatalf("%s: request=%+v", tc.name, ca.reqs[0])
		}
	}
}

func TestDeckOpenOfAFreshContainerResumesTheStoppedAgentsFirst(t *testing.T) {
	ca := &cliAgents{}
	api := startDaemonWith(t, ca)
	rc, _, e := runEnv(t, map[string]string{"LEA_DAEMON_SOCK": api}, "deck-open", "--container", "lea-a", "--workdir", "/p", "--fresh", "--no-default")
	if rc != 0 {
		t.Fatalf("rc=%d err=%q", rc, e)
	}
	if !reflect.DeepEqual(ca.calls, []string{"resume-stopped lea-a", "sweep lea-a", "ensure lea-a /p"}) {
		t.Fatalf("calls=%q", ca.calls)
	}
	if e != "" {
		t.Fatalf("a sweep with nothing to say printed %q", e)
	}
}

// A container that already runs is swept too (#132): the stacks of agents gone or idle for long
// do not wait for a restart.
func TestDeckOpenOfARunningContainerSweepsIt(t *testing.T) {
	ca := &cliAgents{sweep: "kid: stopped 2 containers and 1 pod (freed 812 MiB)"}
	api := startDaemonWith(t, ca)
	rc, _, e := runEnv(t, map[string]string{"LEA_DAEMON_SOCK": api}, "deck-open", "--container", "lea-a", "--workdir", "/p", "--no-default")
	if rc != 0 || e != "lea: kid: stopped 2 containers and 1 pod (freed 812 MiB)\n" {
		t.Fatalf("rc=%d err=%q", rc, e)
	}
	if !reflect.DeepEqual(ca.calls, []string{"sweep lea-a", "ensure lea-a /p"}) {
		t.Fatalf("calls=%q", ca.calls)
	}
}

// A fresh container's sweep of the nested Podman says what it removed and what no agent started
// (#97); a failed sweep is a warning, and the deck opens anyway.
func TestDeckOpenOfAFreshContainerReportsItsSweep(t *testing.T) {
	ca := &cliAgents{sweep: "3 containers that no agent started (a shell, or an older lea) stay in the nested Podman"}
	api := startDaemonWith(t, ca)
	rc, _, e := runEnv(t, map[string]string{"LEA_DAEMON_SOCK": api}, "deck-open", "--container", "lea-a", "--workdir", "/p", "--fresh", "--no-default")
	if rc != 0 || e != "lea: 3 containers that no agent started (a shell, or an older lea) stay in the nested Podman\n" {
		t.Fatalf("rc=%d err=%q", rc, e)
	}
	ca = &cliAgents{sweepErr: errors.New("lea-agent sweep failed")}
	api = startDaemonWith(t, ca)
	rc, _, e = runEnv(t, map[string]string{"LEA_DAEMON_SOCK": api}, "deck-open", "--container", "lea-a", "--workdir", "/p", "--fresh", "--no-default")
	if rc != 0 || !strings.Contains(e, "warning: the nested Podman was not swept") || !strings.Contains(ca.calls[len(ca.calls)-1], "ensure") {
		t.Fatalf("rc=%d err=%q calls=%q", rc, e, ca.calls)
	}
}

func TestDeckStopRecordsTheAgentsAsStopped(t *testing.T) {
	ca := &cliAgents{}
	api := startDaemonWith(t, ca)
	for _, args := range [][]string{{"deck-stop", "--container", "lea-a"}, {"deck-stop", "--container", "lea-b", "--forget"}} {
		if rc, _, e := runEnv(t, map[string]string{"LEA_DAEMON_SOCK": api}, args...); rc != 0 {
			t.Fatalf("%q: rc=%d err=%q", args, rc, e)
		}
	}
	if !reflect.DeepEqual(ca.calls, []string{"mark-stopped lea-a false", "mark-stopped lea-b true"}) {
		t.Fatalf("calls=%q", ca.calls)
	}
	if rc, _, e := runEnv(t, nil, "deck-stop", "--forget"); rc != 2 || !strings.Contains(e, "usage: lea deck-stop") {
		t.Fatalf("rc=%d err=%q", rc, e)
	}
}

// Without a daemon nothing records the agents: the stop goes on, with a warning.
func TestDeckStopWithoutADaemonWarns(t *testing.T) {
	rc, _, e := runEnv(t, map[string]string{"LEA_DAEMON_SOCK": filepath.Join(t.TempDir(), "none.sock")}, "deck-stop", "--container", "lea-a")
	if rc != 1 || !strings.Contains(e, "lea: warning: the agents of lea-a were not recorded as stopped") {
		t.Fatalf("rc=%d err=%q", rc, e)
	}
}

func TestDeckOpenUsage(t *testing.T) {
	if rc, _, e := runEnv(t, nil, "deck-open", "--workdir", "/p"); rc != 2 || !strings.Contains(e, "usage: lea deck-open") {
		t.Fatalf("rc=%d err=%q", rc, e)
	}
}

func TestDeckOpenPrintsDaemonErrorsWithoutControlCharacters(t *testing.T) {
	ca := &cliAgents{ensureErr: errors.New("boom \x1b[31mred\x07")}
	api := startDaemonWith(t, ca)
	rc, _, e := runEnv(t, map[string]string{"LEA_DAEMON_SOCK": api}, "deck-open", "--container", "lea-a", "--workdir", "/p", "--no-default")
	if rc != 1 || strings.ContainsAny(e, "\x1b\x07") || !strings.Contains(e, "boom") {
		t.Fatalf("rc=%d err=%q", rc, e)
	}
}

func TestApproveAcceptsAlways(t *testing.T) {
	rc, _, e := runEnv(t, map[string]string{"LEA_DAEMON_SOCK": "/nonexistent/d.sock"}, "approve", "7", "always")
	if rc != 1 || !strings.Contains(e, "lea daemon is not running") {
		t.Fatalf("always was refused before reaching the daemon: rc=%d err=%q", rc, e)
	}
	if rc, _, e := runEnv(t, nil, "approve", "7", "sometimes"); rc != 2 || !strings.Contains(e, "allow|deny|always") {
		t.Fatalf("rc=%d err=%q", rc, e)
	}
}

func TestApprovalSettingsFromTheEnvironment(t *testing.T) {
	for in, want := range map[string]string{"": "deck", "deck": "deck", "pane": "pane", "PANE": "deck", "bogus": "deck"} {
		if got := approvalsMode(in); got != want {
			t.Errorf("approvalsMode(%q)=%q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]int{"": 0, "60": 60, "1": 1, "600": 600, "0": 0, "601": 0, "1m": 0, "-5": 0} {
		if got := approvalTimeout(in); got != want {
			t.Errorf("approvalTimeout(%q)=%d, want %d", in, got, want)
		}
	}
}

func TestRequestCommands(t *testing.T) {
	ca := &cliAgents{}
	api, st := startDaemonStore(t, ca)
	kid, _ := st.Register(store.NewAgent{Container: "lea-a", Name: "kid", Profile: "claude", Workdir: "/p", Role: "dev"}, store.Caps{}, 1)
	r, err := st.AddSpawnRequest(store.SpawnRequest{Container: "lea-a", Requester: kid.Key, Lead: "user", Role: "reviewer", Profile: "claude",
		Repo: "shop", Brief: "review\x1b[2J the cart\nsecond line", Reason: "eyes"}, func(int64) string { return "req" }, 2, 3600, time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"LEA_DAEMON_SOCK": api}
	rc, out, e := runEnv(t, env, "requests")
	if rc != 0 || !strings.Contains(out, fmt.Sprintf("#%d kid asks for a reviewer repo shop: review\\x1b[2J the cart", r.ID)) ||
		!strings.Contains(out, fmt.Sprintf("(%d bytes)", len("review\x1b[2J the cart\nsecond line"))) ||
		!strings.Contains(out, "reason: eyes") || strings.Contains(out, "second line") {
		t.Fatalf("rc=%d out=%q err=%q", rc, out, e)
	}
	for _, bad := range [][]string{{"request"}, {"request", "maybe", "1"}, {"request", "accept", "x"}, {"request", "accept", "1", "extra"}} {
		if rc, _, _ := runEnv(t, env, bad...); rc != 2 {
			t.Fatalf("%q: rc=%d", bad, rc)
		}
	}
	rc, out, e = runEnv(t, env, "request", "accept", fmt.Sprint(r.ID))
	if rc != 0 || !strings.Contains(out, "started claude-1 for request") || len(ca.reqs) != 1 || ca.reqs[0].Role != "reviewer" {
		t.Fatalf("rc=%d out=%q err=%q reqs=%+v", rc, out, e, ca.reqs)
	}
	if rc, out, _ := runEnv(t, env, "requests"); rc != 0 || !strings.Contains(out, "no spawn request waits for you") {
		t.Fatalf("rc=%d out=%q", rc, out)
	}
}

func TestSoundFilesFromTheEnvironment(t *testing.T) {
	const att, done = "/usr/share/sounds/freedesktop/stereo/message-new-instant.oga", "/usr/share/sounds/freedesktop/stereo/complete.oga"
	for _, c := range []struct {
		env  map[string]string
		want map[string]string
		on   bool
	}{
		{nil, map[string]string{daemon.SoundAttention: att, daemon.SoundDone: done}, true},
		{map[string]string{"LEA_SOUND_DONE": "0"}, map[string]string{daemon.SoundAttention: att}, true},
		{map[string]string{"LEA_SOUND_ATTENTION": "0"}, map[string]string{daemon.SoundDone: done}, true},
		{map[string]string{"LEA_SOUND_ATTENTION": "/a.oga", "LEA_SOUND_DONE": "/d.oga"}, map[string]string{daemon.SoundAttention: "/a.oga", daemon.SoundDone: "/d.oga"}, true},
		{map[string]string{"LEA_SOUND": "0"}, map[string]string{daemon.SoundAttention: att, daemon.SoundDone: done}, false},
		{map[string]string{"LEA_SOUND": "1"}, map[string]string{daemon.SoundAttention: att, daemon.SoundDone: done}, true},
		// A value the player could read as an option, or a relative path, plays nothing.
		{map[string]string{"LEA_SOUND_ATTENTION": "--help", "LEA_SOUND_DONE": "x.oga"}, map[string]string{}, true},
	} {
		files, on := soundFiles(func(k string) string { return c.env[k] })
		if !reflect.DeepEqual(files, c.want) || on != c.on {
			t.Errorf("%v: files %v on %v, want %v %v", c.env, files, on, c.want, c.on)
		}
	}
}

func TestSoundCommand(t *testing.T) {
	dir, _ := os.MkdirTemp("", "lea")
	t.Cleanup(func() { os.RemoveAll(dir) })
	st, err := store.Open(filepath.Join(dir, "lea.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	d := daemon.New(st, filepath.Join(dir, "run"), func() int64 { return time.Now().Unix() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	api := filepath.Join(dir, "d.sock")
	l, err := daemon.Listen(api)
	if err != nil {
		t.Fatal(err)
	}
	go d.Serve(ctx, l, daemon.Origin{})
	env := map[string]string{"LEA_DAEMON_SOCK": api}
	if rc, out, _ := runEnv(t, env, "sound"); rc != 0 || !strings.Contains(out, "no sound player") {
		t.Fatalf("rc=%d out=%q", rc, out)
	}
	d.SetSounder(func(string) {})
	for _, c := range [][2]string{{"", "sounds on"}, {"off", "sounds off"}, {"", "sounds off"}, {"on", "sounds on"}} {
		args := []string{"sound"}
		if c[0] != "" {
			args = append(args, c[0])
		}
		if rc, out, e := runEnv(t, env, args...); rc != 0 || strings.TrimSpace(out) != c[1] {
			t.Fatalf("%q: rc=%d out=%q err=%q", args, rc, out, e)
		}
	}
	for _, bad := range [][]string{{"sound", "loud"}, {"sound", "on", "off"}} {
		if rc, _, _ := runEnv(t, env, bad...); rc != 2 {
			t.Fatalf("%q: rc=%d", bad, rc)
		}
	}
}

// lea lock (its core form, the launcher naming the container) reaches the host API with what the
// user typed, and prints the outcome.
func TestLockSendsTheRequest(t *testing.T) {
	ca := &cliAgents{}
	api := startDaemonWith(t, ca)
	env := map[string]string{"LEA_DAEMON_SOCK": api}
	if rc, out, e := runEnv(t, env, "lock", "--container", "lea-a", "open", "locksql", "prod"); rc != 0 || out != "locksql prod: opened\n" {
		t.Fatalf("open: rc=%d out=%q err=%q", rc, out, e)
	}
	if rc, out, e := runEnv(t, env, "lock", "--container", "lea-a", "close", "locksql"); rc != 0 || out != "locksql: closed\n" {
		t.Fatalf("close: rc=%d out=%q err=%q", rc, out, e)
	}
	if rc, out, e := runEnv(t, env, "lock", "--container", "lea-a", "list"); rc != 0 || out != "locksql/prod\tshown\nlocksql\tparked\n" {
		t.Fatalf("list: rc=%d out=%q err=%q", rc, out, e)
	}
	for _, bad := range [][]string{{"lock", "--container", "lea-a"}, {"lock", "--container", "lea-a", "open"},
		{"lock", "--container", "lea-a", "open", "locksql", "prod", "--auto"}, {"lock", "--container", "lea-a", "list", "x"}} {
		if rc, _, _ := runEnv(t, env, bad...); rc != 2 {
			t.Errorf("%q: rc=%d, want usage", bad, rc)
		}
	}
	want := []string{"lock-open lea-a locksql prod", "lock-close lea-a locksql ", "lock-list lea-a"}
	if !reflect.DeepEqual(ca.calls, want) {
		t.Fatalf("calls=%q", ca.calls)
	}
}

// The deck's facade keys are where lock up writes them: $XDG_STATE_HOME/lockfamily/facades, or
// ~/.local/state's; LEA_HUB_FACADE_KEYS overrides.
func TestHubFacadeKeys(t *testing.T) {
	for _, tc := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"XDG_STATE_HOME": "/s", "HOME": "/h"}, "/s/lockfamily/facades"},
		{map[string]string{"HOME": "/h"}, "/h/.local/state/lockfamily/facades"},
		{map[string]string{"LEA_HUB_FACADE_KEYS": "/k", "HOME": "/h"}, "/k"},
		{map[string]string{}, ""},
	} {
		if got := hubFacadeKeys(func(k string) string { return tc.env[k] }); got != tc.want {
			t.Errorf("%v: %q, want %q", tc.env, got, tc.want)
		}
	}
}
