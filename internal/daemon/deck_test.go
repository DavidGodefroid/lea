package daemon

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/agents"
	"github.com/DavidGodefroid/lea/internal/store"
)

type fakeAgents struct {
	mu      sync.Mutex
	calls   []string
	req     agents.NewRequest
	changed int
	// unattached: no user terminal shows the deck (Attached reports false).
	unattached bool
	newErr     error         // New fails with it
	parkOut    string        // what ParkStack reports
	parkErr    error         // ParkStack fails with it
	parkHold   chan struct{} // when set, ParkStack waits for it to close
	newHold    chan struct{} // when set, New waits for it to close
	rows       []agents.Row  // the rows Watch reports
	// st, when set, is where SoftStop and ResumeSuspended record the state they leave.
	st        *store.Store
	resumeErr error // ResumeSuspended fails with it
	// resumeHold, when set, holds ResumeSuspended until it is closed.
	resumeHold chan struct{}
}

func (f *fakeAgents) rec(s string) { f.mu.Lock(); f.calls = append(f.calls, s); f.mu.Unlock() }

func (f *fakeAgents) Ensure(ctx context.Context, c, w string) error {
	f.rec("ensure " + c + " " + w)
	return nil
}
func (f *fakeAgents) New(ctx context.Context, r agents.NewRequest) (agents.Row, error) {
	f.mu.Lock()
	f.req = r
	err, hold := f.newErr, f.newHold
	f.mu.Unlock()
	f.rec("new")
	if hold != nil {
		<-hold
	}
	if err != nil {
		return agents.Row{}, err
	}
	return agents.Row{Key: r.Container + "/claude-1", Name: "claude-1", Profile: "claude", State: "unknown"}, nil
}
func (f *fakeAgents) Focus(ctx context.Context, k string, focus bool) error {
	f.rec(fmt.Sprintf("focus %s %v", k, focus))
	return nil
}
func (f *fakeAgents) Stop(ctx context.Context, k string) error { f.rec("stop " + k); return nil }
func (f *fakeAgents) RemoveEnded(ctx context.Context, k string) error {
	f.rec("remove-ended " + k)
	return nil
}
func (f *fakeAgents) Detach(ctx context.Context, c string) error { f.rec("detach " + c); return nil }
func (f *fakeAgents) Watch(ctx context.Context, c string, since int64, to time.Duration) (agents.Snapshot, error) {
	f.rec(fmt.Sprintf("watch %s %d %s", c, since, to))
	return agents.Snapshot{Version: 9, Shown: c + "/claude-1", Agents: append([]agents.Row(nil), f.rows...)}, nil
}
func (f *fakeAgents) MarkStopped(ctx context.Context, c string, forget bool) error {
	f.rec(fmt.Sprintf("mark-stopped %s %v", c, forget))
	return nil
}
func (f *fakeAgents) ResumeStopped(ctx context.Context, c string) error {
	f.rec("resume-stopped " + c)
	return nil
}
func (f *fakeAgents) SweepStacks(ctx context.Context, c string) (string, error) {
	f.rec("sweep " + c)
	return "removed 1 container and 0 pods of ended agents (dev-2)", nil
}
func (f *fakeAgents) ParkStack(ctx context.Context, key string) (string, error) {
	f.rec("park " + key)
	f.mu.Lock()
	hold, err := f.parkHold, f.parkErr
	f.mu.Unlock()
	if hold != nil {
		<-hold
	}
	if err != nil {
		return "", err
	}
	return f.parkOut, nil
}
func (f *fakeAgents) UnparkStack(ctx context.Context, key string) (string, error) {
	f.rec("unpark " + key)
	return "", nil
}
func (f *fakeAgents) SoftStop(ctx context.Context, key string) error {
	f.rec("softstop " + key)
	if f.st == nil {
		return nil
	}
	if a, err := f.st.AgentByKey(key); err != nil || a.State != store.StateSuspendedWarm {
		return agents.ErrNotSuspended
	}
	return f.st.SetState(key, store.StateSuspendedCold, "", 0)
}
func (f *fakeAgents) EndSuspended(ctx context.Context, key string) (bool, error) {
	f.rec("end " + key)
	if f.st == nil {
		return true, nil
	}
	return f.st.EndSuspended(key, 0)
}
func (f *fakeAgents) ResumeSuspended(ctx context.Context, key, prompt string) error {
	f.rec("resume " + key + ": " + prompt)
	f.mu.Lock()
	hold, rerr := f.resumeHold, f.resumeErr
	f.mu.Unlock()
	if hold != nil {
		<-hold
	}
	if rerr != nil {
		return rerr
	}
	if f.st == nil {
		return nil
	}
	return f.st.SetState(key, "unknown", "", 0)
}
func (f *fakeAgents) Attached(ctx context.Context, c string) (bool, error) {
	f.rec("attached " + c)
	return !f.unattached, nil
}
func (f *fakeAgents) Popup(ctx context.Context, k string, compose bool) error {
	f.rec(fmt.Sprintf("popup %s %v", k, compose))
	return nil
}
func (f *fakeAgents) SetLabel(ctx context.Context, k, label string) error {
	f.rec(fmt.Sprintf("label %s %q", k, label))
	return nil
}
func (f *fakeAgents) AutoLabel(ctx context.Context, k, label string) (string, error) {
	f.rec(fmt.Sprintf("autolabel %s %q", k, label))
	if f.st == nil {
		return label, nil
	}
	return f.st.SetLabelUnique(k, label, agents.MaxLabel, true)
}
func (f *fakeAgents) SetPaused(ctx context.Context, k string, paused bool) error {
	f.rec(fmt.Sprintf("paused %s %v", k, paused))
	return nil
}
func (f *fakeAgents) OpenShell(ctx context.Context, c string) (agents.Row, error) {
	f.rec("shell " + c)
	return agents.Row{Key: agents.ShellKey(c, "sh-1"), Name: "sh-1", Kind: agents.KindShell}, nil
}
func (f *fakeAgents) SplitShell(ctx context.Context, k string, below bool) (agents.Row, error) {
	f.rec(fmt.Sprintf("split %s below=%v", k, below))
	return agents.Row{Key: agents.ShellKey("lea-a", "sh-1"), Name: "sh-1", Kind: agents.KindShell}, nil
}
func (f *fakeAgents) Changed() { f.mu.Lock(); f.changed++; f.mu.Unlock() }

func TestDeckMethodsAreHostOnly(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	for m := range deckMethods {
		err := call(t, e.bus, "dev", m, map[string]any{"container": "lea-a"}, nil)
		if err == nil || !strings.Contains(err.Error(), "host only") {
			t.Fatalf("%s from a container: err=%v", m, err)
		}
	}
	if len(fa.calls) != 0 {
		t.Fatalf("a container reached the manager: %q", fa.calls)
	}
}

func TestDeckMethodsReachTheManager(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	must := func(method string, params, result any) {
		t.Helper()
		if err := call(t, e.api, "", method, params, result); err != nil {
			t.Fatalf("%s: %v", method, err)
		}
	}
	must("agents_mark_stopped", map[string]any{"container": "lea-a", "forget": true}, nil)
	must("agents_resume_stopped", map[string]any{"container": "lea-a"}, nil)
	var sw struct{ Notice string }
	must("agents_sweep_stacks", map[string]any{"container": "lea-a"}, &sw)
	if sw.Notice != "removed 1 container and 0 pods of ended agents (dev-2)" {
		t.Fatalf("sweep=%+v", sw)
	}
	must("deck_ensure", map[string]any{"container": "lea-a", "workdir": "/p"}, nil)
	var row agents.Row
	must("agent_new", map[string]any{"container": "lea-a", "workdir": "/p", "profile": "claude", "name": "x",
		"args": []string{"--resume"}, "focus": true}, &row)
	if row.Name != "claude-1" {
		t.Fatalf("row=%+v", row)
	}
	want := agents.NewRequest{Container: "lea-a", Workdir: "/p", Profile: "claude", Name: "x", Args: []string{"--resume"}, Focus: true, Role: "dev"}
	if !reflect.DeepEqual(fa.req, want) {
		t.Fatalf("request=%+v", fa.req)
	}
	must("agent_focus", map[string]any{"key": "lea-a/x", "focus": true}, nil)
	must("agent_stop", map[string]any{"key": "lea-a/x"}, nil)
	must("agent_stop", map[string]any{"key": "lea-a/y", "ended": true}, nil)
	must("deck_detach", map[string]any{"container": "lea-a"}, nil)
	must("deck_popup", map[string]any{"key": "lea-a/x", "compose": true}, nil)
	var snap agents.Snapshot
	must("agents_watch", map[string]any{"container": "lea-a", "since": 3, "timeout_s": 5}, &snap)
	if snap.Version != 9 || snap.Shown != "lea-a/claude-1" {
		t.Fatalf("snapshot=%+v", snap)
	}
	var att struct{ Attached bool }
	must("deck_attached", map[string]any{"container": "lea-a"}, &att)
	if !att.Attached {
		t.Fatal("deck_attached: not attached")
	}
	must("agent_label", map[string]any{"key": "lea-a/x", "label": "auth work"}, nil)
	must("agent_pause", map[string]any{"key": "lea-a/x", "paused": true}, nil)
	var sc agents.Screen
	must("agent_capture", map[string]any{"key": "lea-a/x"}, &sc)
	if len(sc.Lines) != 1 || sc.Lines[0] != "$ make test" || sc.Width != 80 {
		t.Fatalf("screen=%+v", sc)
	}
	var sh agents.Row
	must("shell_open", map[string]any{"container": "lea-a"}, &sh)
	if sh.Kind != agents.KindShell || sh.Key != "shell:lea-a/sh-1" {
		t.Fatalf("shell=%+v", sh)
	}
	wantCalls := []string{"mark-stopped lea-a true", "resume-stopped lea-a", "sweep lea-a", "ensure lea-a /p", "new", "focus lea-a/x true", "stop lea-a/x", "remove-ended lea-a/y", "detach lea-a",
		"popup lea-a/x true", "watch lea-a 3 5s", "attached lea-a", `label lea-a/x "auth work"`, "paused lea-a/x true", "capture lea-a/x", "shell lea-a"}
	if !reflect.DeepEqual(fa.calls, wantCalls) {
		t.Fatalf("calls=%q", fa.calls)
	}
	for _, to := range []int{0, 61} {
		if err := call(t, e.api, "", "agents_watch", map[string]any{"container": "lea-a", "timeout_s": to}, nil); err == nil {
			t.Fatalf("timeout_s %d accepted", to)
		}
	}
}

// An agent never renames, pauses or resumes an agent, nor opens a shell: not through its bus socket (its MCP tools
// and lea inside the container use it), whatever its role.
func TestRenameAndShellsAreRefusedToAgents(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	for _, m := range []string{"agent_label", "shell_open", "agent_pause"} {
		if !deckMethods[m] {
			t.Fatalf("%s is not a deck (host-only) method", m)
		}
		err := call(t, e.bus, "dev", m, map[string]any{"key": "lea-a/dev", "label": "pwned", "container": "lea-a"}, nil)
		if err == nil || !strings.Contains(err.Error(), "host only") {
			t.Fatalf("%s from an agent: err=%v", m, err)
		}
	}
	if len(fa.calls) != 0 {
		t.Fatalf("an agent reached the manager: %q", fa.calls)
	}
}

// An agent never stops, forgets or restarts the agents of its container.
func TestStopAndResumeAreRefusedToAgents(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	for _, m := range []string{"agents_mark_stopped", "agents_resume_stopped", "agents_sweep_stacks"} {
		err := call(t, e.bus, "dev", m, map[string]any{"container": "lea-a", "forget": true}, nil)
		if err == nil || !strings.Contains(err.Error(), "host only") {
			t.Fatalf("%s from an agent: err=%v", m, err)
		}
	}
	if len(fa.calls) != 0 {
		t.Fatalf("an agent reached the manager: %q", fa.calls)
	}
}

func TestDeckMethodsWithoutAManager(t *testing.T) {
	e := start(t)
	err := call(t, e.api, "", "deck_ensure", map[string]any{"container": "lea-a"}, nil)
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("err=%v", err)
	}
}

func TestHookStateChangesNotifyTheDeck(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "UserPromptSubmit", "session_id": "s"}, nil)
	call(t, e.bus, "dev", "hook_event", map[string]any{"event": "Notification", "session_id": "s"}, nil)
	fa.mu.Lock()
	n := fa.changed
	fa.mu.Unlock()
	if n != 1 {
		t.Fatalf("changed=%d, want 1 (a Notification changes no state)", n)
	}
}

// The lock console and hub ticket methods are gone (#300): the host API answers none of them, and
// a deck snapshot says whether lox run serves (loxrun.go).
func TestLockAndHubMethodsAreGone(t *testing.T) {
	e := start(t)
	e.d.SetAgents(&fakeAgents{})
	for _, m := range []string{"lock_open", "lock_close", "lock_list", "lock_request_decide", "hub_decide", "hub_reveal", "hub_renew", "hub_show"} {
		if err := call(t, e.api, "", m, map[string]any{"container": "lea-a", "tool": "locksql"}, nil); err == nil || !strings.Contains(err.Error(), "unknown method") {
			t.Fatalf("%s on the host API: %v", m, err)
		}
	}
	if err := call(t, e.bus, "lead", "lock_request", map[string]any{"tool": "locksql", "profile": "prod"}, nil); err == nil || !strings.Contains(err.Error(), "unknown method") {
		t.Fatalf("lock_request from an agent: %v", err)
	}
}

// Without the deck (LEA_DECK=0, #135) nobody is attached to a deck, yet a permission request is
// not handed straight back to the agent's own dialog: it waits for the host CLI (lea approve),
// and the user is told on the desktop how to answer it.
func TestWithoutTheDeckAnApprovalWaitsForLeaApprove(t *testing.T) {
	e, notes, _ := noDeckEnv(t)
	got := ask(t, e, 3, false)
	p := waitPending(t, e)
	want := "lea needs you: dev asks to use Bash: lea approve p" + strconv.FormatInt(p.ID, 10) + " (once) or lea deny p" + strconv.FormatInt(p.ID, 10) + "; lea requests says what it asks"
	if n := waitNotes(notes, 1); len(n) != 1 || n[0] != want {
		t.Fatalf("notes %q, want %q", n, want)
	}
	if err := call(t, e.api, "", "approval_decide", map[string]any{"id": p.ID, "verdict": "allow"}, nil); err != nil {
		t.Fatal(err)
	}
	if r := receive(t, got); r.Verdict != "allow" || r.ID != p.ID {
		t.Fatalf("verdict %+v", r)
	}
}

// A permission whose dialog offers "always" names that durable answer too, and what it leaves out
// (#412).
func TestWithoutTheDeckAnAlwaysApprovalNamesBothAnswers(t *testing.T) {
	e, notes, _ := noDeckEnv(t)
	got := ask(t, e, 3, true)
	p := waitPending(t, e)
	id := strconv.FormatInt(p.ID, 10)
	want := "lea needs you: dev asks to use Bash: lea approve p" + id + " (once) or lea deny p" + id + "; lea requests says what it asks" +
		"; lea approve p" + id + " always also allows it for this agent until lea restarts; it covers no other agent"
	if n := waitNotes(notes, 1); len(n) != 1 || n[0] != want {
		t.Fatalf("notes %q, want %q", n, want)
	}
	if err := call(t, e.api, "", "approval_decide", map[string]any{"id": p.ID, "verdict": "always"}, nil); err != nil {
		t.Fatal(err)
	}
	receive(t, got)
}

// Without the deck, an answer given the instant the notification shows (a fast lea approve) is
// delivered at once: the verdict channel is registered before the user is told, so the agent
// never waits for its approval timeout. The approval's timer never fires on its own here.
func TestWithoutTheDeckAnImmediateAnswerIsDeliveredAtOnce(t *testing.T) {
	e, _, _ := noDeckEnv(t)
	newManualTimers(t).use(e.d)
	answered := make(chan error, 1)
	e.d.SetNoDeck(func(title, body string) {
		var pend []pendingView
		if err := call(t, e.api, "", "approval_list", nil, &pend); err != nil || len(pend) != 1 {
			answered <- fmt.Errorf("pending %+v: %v", pend, err)
			return
		}
		answered <- call(t, e.api, "", "approval_decide", map[string]any{"id": pend[0].ID, "verdict": "allow"}, nil)
	})
	got := ask(t, e, 600, false)
	select {
	case err := <-answered:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no notification")
	}
	if r := receive(t, got); r.Verdict != "allow" {
		t.Fatalf("verdict %+v", r)
	}
}
func (f *fakeAgents) Capture(ctx context.Context, k string) (agents.Screen, error) {
	f.rec("capture " + k)
	return agents.Screen{Lines: []string{"$ make test"}, Width: 80, Height: 24}, nil
}
