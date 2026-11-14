package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DavidGodefroid/lea/internal/agents"
	"github.com/DavidGodefroid/lea/internal/client"
	"github.com/DavidGodefroid/lea/internal/deck"
	"github.com/DavidGodefroid/lea/internal/store"
	"github.com/DavidGodefroid/lea/internal/wire"
)

// watchAgents is regAgents with an empty deck snapshot: the daemon adds what it knows (the usage).
type watchAgents struct{ *regAgents }

func (w watchAgents) Watch(ctx context.Context, container string, since int64, timeout time.Duration) (agents.Snapshot, error) {
	return agents.Snapshot{Version: since + 1}, nil
}

func budgetCall(t *testing.T, bus, agent, method string, params, out any) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, _ := json.Marshal(params)
	return client.Call(ctx, bus, wire.Request{Method: method, Agent: agent, Params: p}, out)
}

// E2E of #421 on a real daemon, its sockets, the user configuration on disk, the CLI and the
// deck's model: a status report with 5-hour usage 70 % shows in the deck's top border, in lea
// limits and in limits_get; with budget_5h_pct 60 a lead's agents_spawn with queue: true waits
// and the lead hears why; lea limits set budget_5h_pct 80 on the same running daemon starts it.
func TestUsageBudgetE2E(t *testing.T) {
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
	t.Setenv("LEA_BUDGET_5H_PCT", "")

	api, bus, st, ra, d, stop := liveLimitsDaemon(t, dir, root)
	defer stop()
	d.SetAgents(watchAgents{ra})
	env := map[string]string{"LEA_DAEMON_SOCK": api}
	if rc, out, e := runEnv(t, env, "limits"); rc != 0 || !strings.Contains(out, "budget_5h_pct 60") || !strings.Contains(out, "usage: unknown") {
		t.Fatalf("lea limits before a report: rc=%d out=%q err=%q", rc, out, e)
	}
	tower, err := st.Register(store.NewAgent{Container: "lea-a", Name: "tower", Profile: "claude", Workdir: "/p", Role: "lead"}, store.Caps{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Register(store.NewAgent{Container: "lea-a", Name: "kid", Profile: "claude", Workdir: "/p", Role: "dev", Parent: tower.Key}, store.Caps{}, 1); err != nil {
		t.Fatal(err)
	}
	// A child's status line reports the account's windows, as statusline does.
	reset := time.Now().Add(2 * time.Hour).Unix()
	if err := budgetCall(t, bus, "kid", "status_report", map[string]any{"ctx_pct": 10, "cost_usd": -1, "limits": []map[string]any{
		{"window": "5h", "pct": 70, "resets_at": reset}, {"window": "7d", "pct": 18, "resets_at": reset + 86400}}}, nil); err != nil {
		t.Fatal(err)
	}

	// The deck's top border, through the daemon's agents_watch.
	m := deck.New(deck.Client{Sock: api}, "lea-a", false, time.Now)
	tm, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 10})
	tm, _ = tm.Update(m.Init()())
	top := strings.SplitN(tm.View(), "\n", 2)[0]
	if want := "5h 70% (" + time.Unix(reset, 0).Format("15:04") + ") · 7d 18%"; !strings.Contains(top, want) {
		t.Fatalf("deck top %q, want %q", top, want)
	}
	// lea limits and limits_get (an agent's, read-only).
	if rc, out, e := runEnv(t, env, "limits"); rc != 0 || !strings.Contains(out, "budget_5h_pct 60") || !strings.Contains(out, "70 in use") ||
		!strings.Contains(out, "usage: 5h 70 %") || !strings.Contains(out, "7d 18 %") || !strings.Contains(out, "new spawns held (budget_5h_pct 60)") {
		t.Fatalf("lea limits: rc=%d out=%q err=%q", rc, out, e)
	}
	var g struct {
		Usage struct {
			FiveHour struct{ Pct int } `json:"five_hour"`
			Over     bool
		}
	}
	if err := budgetCall(t, bus, "tower", "limits_get", nil, &g); err != nil || g.Usage.FiveHour.Pct != 70 || !g.Usage.Over {
		t.Fatalf("limits_get: %+v %v", g, err)
	}

	// The lead's spawn waits, and the lead heard once why.
	r, err := spawnAs(t, bus, "tower", map[string]any{"brief": "x", "queue": true})
	if err != nil || r["queued"] == nil || !strings.Contains(r["reason"].(string), "5h usage 70 % ≥ budget_5h_pct 60") {
		t.Fatalf("spawn: %v %v", r, err)
	}
	if ra.started() != 0 {
		t.Fatalf("%d started above the budget", ra.started())
	}
	var inbox []struct{ Kind, Text string }
	if err := budgetCall(t, bus, "tower", "msg_inbox", nil, &inbox); err != nil {
		t.Fatal(err)
	}
	notes := 0
	for _, x := range inbox {
		if x.Kind == "note" && strings.Contains(x.Text, "5h usage 70 % ≥ budget_5h_pct 60") {
			notes++
		}
	}
	if notes != 1 {
		t.Fatalf("tower inbox %+v: %d budget notes, want 1", inbox, notes)
	}

	// The user's plan changed: raised on the same running daemon, the queued spawn starts.
	if rc, out, e := runEnv(t, env, "limits", "set", "budget_5h_pct", "80"); rc != 0 || !strings.Contains(out, "budget_5h_pct 80") || !strings.Contains(out, "from your file") {
		t.Fatalf("set 80: rc=%d out=%q err=%q", rc, out, e)
	}
	waitStarted(t, ra, 1)
	if b, _ := os.ReadFile(user); string(b) != "LEA_BUDGET_5H_PCT='80'\n" {
		t.Fatalf("user file=%q", b)
	}
	if kid, _ := st.AgentByKey("lea-a/kid"); kid.State == "done" || kid.State == "stopped" {
		t.Fatalf("kid is %s", kid.State)
	}
}
