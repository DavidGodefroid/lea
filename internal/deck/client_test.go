package deck

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/agents"
	"github.com/DavidGodefroid/lea/internal/daemon"
	"github.com/DavidGodefroid/lea/internal/store"
)

type recAgents struct {
	calls []string
	req   agents.NewRequest
}

func (r *recAgents) Ensure(ctx context.Context, c, w string) error { return nil }
func (r *recAgents) New(ctx context.Context, q agents.NewRequest) (agents.Row, error) {
	r.req = q
	return agents.Row{Key: "lea-a/claude-1", Name: "claude-1", Profile: "claude", State: "unknown"}, nil
}
func (r *recAgents) Focus(ctx context.Context, k string, f bool) error {
	r.calls = append(r.calls, "focus "+k)
	return nil
}
func (r *recAgents) Stop(ctx context.Context, k string) error {
	r.calls = append(r.calls, "stop "+k)
	return nil
}
func (r *recAgents) RemoveEnded(ctx context.Context, k string) error {
	r.calls = append(r.calls, "remove-ended "+k)
	return nil
}
func (r *recAgents) Detach(ctx context.Context, c string) error {
	r.calls = append(r.calls, "detach "+c)
	return nil
}
func (r *recAgents) Watch(ctx context.Context, c string, since int64, to time.Duration) (agents.Snapshot, error) {
	return agents.Snapshot{Version: since + 1, Shown: "lea-a/claude-1",
		Agents: []agents.Row{{Key: "lea-a/claude-1", Name: "claude-1", Profile: "claude", State: "idle", Workdir: "/p", Updated: 5}}}, nil
}
func (r *recAgents) Attached(ctx context.Context, c string) (bool, error)    { return false, nil }
func (r *recAgents) MarkStopped(ctx context.Context, c string, f bool) error { return nil }
func (r *recAgents) ResumeStopped(ctx context.Context, c string) error       { return nil }
func (r *recAgents) Changed()                                                {}
func (r *recAgents) SetLabel(ctx context.Context, k, label string) error {
	r.calls = append(r.calls, "label "+k+" "+label)
	return nil
}
func (r *recAgents) SetPaused(ctx context.Context, k string, paused bool) error {
	r.calls = append(r.calls, fmt.Sprintf("paused %s %v", k, paused))
	return nil
}
func (r *recAgents) OpenShell(ctx context.Context, c string) (agents.Row, error) {
	r.calls = append(r.calls, "shell "+c)
	return agents.Row{Key: agents.ShellKey(c, "sh-1"), Name: "sh-1", Kind: agents.KindShell, State: "running"}, nil
}
func (r *recAgents) SplitShell(ctx context.Context, k string, below bool) (agents.Row, error) {
	r.calls = append(r.calls, fmt.Sprintf("split %s %v", k, below))
	return agents.Row{Key: agents.ShellKey("lea-a", "sh-2"), Name: "sh-2", Kind: agents.KindShell, State: "running"}, nil
}
func (r *recAgents) Popup(ctx context.Context, k string, compose bool) error {
	r.calls = append(r.calls, "popup "+k)
	return nil
}

func TestClientSpeaksTheDaemonAPI(t *testing.T) {
	dir, _ := os.MkdirTemp("", "lea")
	t.Cleanup(func() { os.RemoveAll(dir) })
	st, err := store.Open(filepath.Join(dir, "lea.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	d := daemon.New(st, filepath.Join(dir, "run"), func() int64 { return 1 })
	ra := &recAgents{}
	d.SetAgents(ra)
	sock := filepath.Join(dir, "d.sock")
	l, err := daemon.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go d.Serve(ctx, l, daemon.Origin{})

	c := Client{Sock: sock}
	snap, err := c.Watch(ctx, "lea-a", 4)
	want := Snapshot{Version: 5, Shown: "lea-a/claude-1", Agents: []Agent{{Key: "lea-a/claude-1", Name: "claude-1", Profile: "claude", State: "idle", Workdir: "/p", Updated: 5}}}
	if err != nil || !reflect.DeepEqual(snap, want) {
		t.Fatalf("snapshot=%+v err=%v", snap, err)
	}
	a, err := c.New(ctx, "lea-a")
	if err != nil || a.Name != "claude-1" {
		t.Fatalf("new=%+v err=%v", a, err)
	}
	if !reflect.DeepEqual(ra.req, agents.NewRequest{Container: "lea-a", Profile: "claude", Focus: true, Role: "dev"}) {
		t.Fatalf("request=%+v", ra.req)
	}
	c.Show(ctx, "lea-a/claude-1", true)
	c.Stop(ctx, "lea-a/claude-1")
	c.RemoveEnded(ctx, "lea-a/claude-2")
	c.Detach(ctx, "lea-a")
	c.Popup(ctx, "lea-a/claude-1", true)
	if err := c.SetLabel(ctx, "lea-a/claude-1", "auth work"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetPaused(ctx, "lea-a/claude-1", true); err != nil {
		t.Fatal(err)
	}
	sh, err := c.OpenShell(ctx, "lea-a")
	if err != nil || sh.Key != "shell:lea-a/sh-1" || sh.Kind != KindShell {
		t.Fatalf("shell=%+v err=%v", sh, err)
	}
	if !reflect.DeepEqual(ra.calls, []string{"focus lea-a/claude-1", "stop lea-a/claude-1", "remove-ended lea-a/claude-2", "detach lea-a", "popup lea-a/claude-1",
		"label lea-a/claude-1 auth work", "paused lea-a/claude-1 true", "shell lea-a"}) {
		t.Fatalf("calls=%q", ra.calls)
	}
}

func TestClientDecideReachesTheDaemon(t *testing.T) {
	dir, _ := os.MkdirTemp("", "lea")
	t.Cleanup(func() { os.RemoveAll(dir) })
	st, err := store.Open(filepath.Join(dir, "lea.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	d := daemon.New(st, filepath.Join(dir, "run"), func() int64 { return 1 })
	sock := filepath.Join(dir, "d.sock")
	l, err := daemon.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go d.Serve(ctx, l, daemon.Origin{})
	err = Client{Sock: sock}.Decide(ctx, 99, "allow")
	if err == nil || !strings.Contains(err.Error(), "no approval #99") {
		t.Fatalf("err=%v", err)
	}
}

func TestClientThreadAndSend(t *testing.T) {
	dir, _ := os.MkdirTemp("", "lea")
	t.Cleanup(func() { os.RemoveAll(dir) })
	st, err := store.Open(filepath.Join(dir, "lea.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.RegisterAgent("lea-a", "claude-1", "claude", "/p", 1); err != nil {
		t.Fatal(err)
	}
	d := daemon.New(st, filepath.Join(dir, "run"), func() int64 { return 1 })
	sock := filepath.Join(dir, "d.sock")
	l, err := daemon.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go d.Serve(ctx, l, daemon.Origin{})
	c := Client{Sock: sock}
	if err := c.Send(ctx, "lea-a/claude-1", "hello\nthere"); err != nil {
		t.Fatal(err)
	}
	ms, err := c.Thread(ctx, "lea-a/claude-1")
	if err != nil || len(ms) != 1 || ms[0].From != "user" || ms[0].To != "lea-a/claude-1" || ms[0].Body != "hello\nthere" || ms[0].Kind != "note" {
		t.Fatalf("thread=%+v err=%v", ms, err)
	}
	if l, err := c.Label(ctx, "lea-a/claude-1"); err != nil || l != "" {
		t.Fatalf("label=%q err=%v", l, err)
	}
	st.SetLabel("lea-a/claude-1", "auth work")
	if l, err := c.Label(ctx, "lea-a/claude-1"); err != nil || l != "auth work" {
		t.Fatalf("label=%q err=%v", l, err)
	}
}
