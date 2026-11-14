package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/client"
	"github.com/DavidGodefroid/lea/internal/store"
	"github.com/DavidGodefroid/lea/internal/wire"
)

// SetPaused freezes or thaws an agent as the manager does: the store says so once it is done.
func (r *regAgents) SetPaused(ctx context.Context, key string, paused bool) error {
	return r.st.SetPaused(key, paused)
}

func budgetCall(t *testing.T, bus, agent, method string, params map[string]any) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, _ := json.Marshal(params)
	return client.Call(ctx, bus, wire.Request{Method: method, Agent: agent, Params: p}, nil)
}

func waitPaused(t *testing.T, st *store.Store, key string, paused bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if a, err := st.AgentByKey(key); err == nil && a.Paused == paused {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s: paused is not %v", key, paused)
}

// E2E of #424 on a real daemon, its sockets, the user configuration on disk and the CLI: with
// budget_usd_agent just above a child's cost, one status report over it pauses the child (its
// session kept) and the board says why; lea limits set raises it on the same daemon and the
// child resumes. With budget_usd_total reached, a lead's agents_spawn is refused with the
// reason, or queued; lea limits reset budget_usd_total starts a new period, resumes the child
// and starts the queued spawn. lea limits and lea doctor's line name the agents of unknown cost.
func TestSpendBudgetsE2E(t *testing.T) {
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
	for _, k := range []string{"LEA_MAX_RUNNING", "LEA_BUDGET_USD_AGENT", "LEA_BUDGET_USD_TOTAL", "LEA_BUDGET_USD_STEP"} {
		t.Setenv(k, "")
	}
	api, bus, st, ra, _, stop := liveLimitsDaemon(t, dir, root)
	defer stop()
	env := map[string]string{"LEA_DAEMON_SOCK": api}
	if _, err := st.Register(store.NewAgent{Container: "lea-a", Name: "tower", Profile: "claude", Workdir: "/p", Role: "lead"}, store.Caps{}, 1); err != nil {
		t.Fatal(err)
	}
	r, err := spawnAs(t, bus, "tower", map[string]any{"brief": "work"})
	if err != nil {
		t.Fatal(err)
	}
	kidName, _ := r["name"].(string)
	kid := "lea-a/" + kidName
	before, err := st.AgentByKey(kid)
	if err != nil {
		t.Fatal(err)
	}
	if err := budgetCall(t, bus, kidName, "task_update", map[string]any{"status_text": "building"}); err != nil {
		t.Fatal(err)
	}
	report := func(cost float64) {
		t.Helper()
		if err := budgetCall(t, bus, kidName, "status_report", map[string]any{"ctx_pct": 20, "cost_usd": cost}); err != nil {
			t.Fatal(err)
		}
	}
	report(4.9)

	// Just above its cost: one report over it pauses it.
	if rc, out, e := runEnv(t, env, "limits", "set", "budget_usd_agent", "5"); rc != 0 || !strings.Contains(out, "budget_usd_agent 5") || !strings.Contains(out, "$4.90 spent") {
		t.Fatalf("set: rc=%d out=%q err=%q", rc, out, e)
	}
	if b, _ := os.ReadFile(user); !strings.Contains(string(b), "LEA_BUDGET_USD_AGENT='5'") {
		t.Fatalf("user file=%q", b)
	}
	report(5.25)
	waitPaused(t, st, kid, true)
	reason := `budget reached\: \$5\.25 ≥ budget\_usd\_agent \$5`
	if rc, out, e := runEnv(t, env, "board"); rc != 0 || !strings.Contains(out, "### Paused on a spend budget") || !strings.Contains(out, reason) {
		t.Fatalf("board: rc=%d out=%s err=%q", rc, out, e)
	}
	if rc, out, _ := runEnv(t, env, "limits"); rc != 0 || !strings.Contains(out, "paused: "+kidName+": budget reached: $5.25") {
		t.Fatalf("limits: %q", out)
	}
	// Its session is kept: paused, not stopped.
	if a, _ := st.AgentByKey(kid); a.State == "done" || a.State == "stopped" || a.SessionID != before.SessionID || a.Incarnation != before.Incarnation {
		t.Fatalf("after the pause: %+v", a)
	}

	// Raised on the running daemon: it resumes, without a restart.
	if rc, _, e := runEnv(t, env, "limits", "set", "budget_usd_agent", "20"); rc != 0 {
		t.Fatal(e)
	}
	waitPaused(t, st, kid, false)

	// The total reached: the child is paused, and a lead's spawn is refused, or queued.
	if rc, _, e := runEnv(t, env, "limits", "set", "budget_usd_total", "5"); rc != 0 {
		t.Fatal(e)
	}
	waitPaused(t, st, kid, true)
	if _, err := spawnAs(t, bus, "tower", map[string]any{"brief": "more"}); err == nil || !strings.Contains(err.Error(), "budget reached: total spend $5.25 ≥ budget_usd_total $5") {
		t.Fatalf("spawn: %v", err)
	}
	if r, err := spawnAs(t, bus, "tower", map[string]any{"brief": "more", "queue": true}); err != nil || r["queued"] == nil {
		t.Fatalf("queued: %v %v", r, err)
	}
	if ra.started() != 1 {
		t.Fatalf("%d started", ra.started())
	}
	if rc, out, _ := runEnv(t, env, "board"); rc != 0 || !strings.Contains(out, `queued\: budget\_usd\_total \$5\.25\/\$5`) {
		t.Fatalf("board: %s", out)
	}
	// A new period: the child resumes and the queued spawn starts.
	if rc, out, e := runEnv(t, env, "limits", "reset", "budget_usd_total"); rc != 0 || !strings.Contains(out, "budget_usd_total 5") || !strings.Contains(out, "$0.00 spent") {
		t.Fatalf("reset: rc=%d out=%q err=%q", rc, out, e)
	}
	waitPaused(t, st, kid, false)
	waitStarted(t, ra, 2)

	// An agent whose status line reports no cost is never paused, and is named.
	if err := budgetCall(t, bus, "tower", "status_report", map[string]any{"ctx_pct": 5, "cost_usd": -1}); err != nil {
		t.Fatal(err)
	}
	if rc, out, _ := runEnv(t, env, "limits"); rc != 0 || !strings.Contains(out, "not covered by the spend budgets (unknown cost): ") || !strings.Contains(out, "tower") {
		t.Fatalf("limits: %q", out)
	}
	if rc, out, _ := runEnv(t, env, "limits", "doctor"); rc != 0 || !strings.Contains(out, "warn  spend budgets:") || !strings.Contains(out, "tower") {
		t.Fatalf("doctor: %q", out)
	}
	if a, _ := st.AgentByKey("lea-a/tower"); a.Paused {
		t.Fatal("the tower, of unknown cost, was paused")
	}
}
