package main

import (
	"bytes"
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

// towerRow is what a tower reads of a child in agents_list.
type towerRow struct {
	Name      string `json:"name"`
	CtxPct    *int   `json:"ctx_pct"`
	CtxTokens *int   `json:"ctx_tokens"`
	Relay     bool   `json:"relay"`
}

// towerCheck is one board check of a tower, through the methods its MCP tools call: the live
// relay_tokens (limits_get), then its children (agents_list).
func towerCheck(t *testing.T, bus, tower, child string) (relayTokens string, row towerRow) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var lim struct {
		Limits []struct{ Key, Value string }
	}
	if err := client.Call(ctx, bus, wire.Request{Method: "limits_get", Agent: tower}, &lim); err != nil {
		t.Fatal(err)
	}
	for _, l := range lim.Limits {
		if l.Key == "relay_tokens" {
			relayTokens = l.Value
		}
	}
	var rows []towerRow
	if err := client.Call(ctx, bus, wire.Request{Method: "agents_list", Agent: tower}, &rows); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Name == child {
			return relayTokens, r
		}
	}
	t.Fatalf("no %s in %+v", child, rows)
	return
}

// E2E of #422 on a real daemon, its sockets, the user configuration on disk and the CLI: a child
// whose status line (lea statusline, fed what Claude Code sends) reports its context shows its
// tokens in agents_list; with relay_tokens 50000 its tower's check finds it due for a relay at 60k
// tokens; lea limits set relay_tokens 200000 on the same running daemon and the next check does
// not. A status line without tokens (an older Claude Code) leaves the percent alone.
func TestRelayTokensE2E(t *testing.T) {
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
	t.Setenv("LEA_RELAY_TOKENS", "")
	t.Setenv("LEA_MAX_RUNNING", "")
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))
	t.Setenv("LEA_STATUSLINE_CHAINED", "")

	api, bus, st, _, _, stop := liveLimitsDaemon(t, dir, root)
	defer stop()
	env := map[string]string{"LEA_DAEMON_SOCK": api}
	if rc, out, e := runEnv(t, env, "limits", "get", "relay_tokens"); rc != 0 || !strings.Contains(out, "relay_tokens 120000") || !strings.Contains(out, "from lea's default") {
		t.Fatalf("lea limits get: rc=%d out=%q err=%q", rc, out, e)
	}
	if rc, out, e := runEnv(t, env, "limits", "set", "relay_tokens", "50000"); rc != 0 || !strings.Contains(out, "relay_tokens 50000") || !strings.Contains(out, "applies now: the tower's next board check") {
		t.Fatalf("set 50000: rc=%d out=%q err=%q", rc, out, e)
	}
	if _, err := st.Register(store.NewAgent{Container: "lea-a", Name: "tower", Profile: "claude", Workdir: "/p", Role: "lead"}, store.Caps{}, 1); err != nil {
		t.Fatal(err)
	}
	r, err := spawnAs(t, bus, "tower", map[string]any{"brief": "work", "queue": true})
	if err != nil || r["name"] == nil {
		t.Fatalf("spawn: %v %v", r, err)
	}
	child := r["name"].(string)
	if _, row := towerCheck(t, bus, "tower", child); row.CtxTokens != nil || row.CtxPct != nil || row.Relay {
		t.Fatalf("before any report: %+v", row)
	}

	// The child's first status report: Claude Code's JSON through lea statusline.
	statusline := func(doc string) {
		t.Helper()
		t.Setenv("LEA_BUS_SOCK", bus)
		t.Setenv("LEA_AGENT", child)
		var out, errb bytes.Buffer
		if rc := run([]string{"statusline"}, strings.NewReader(doc), &out, &errb); rc != 0 || !strings.Contains(out.String(), "ctx") {
			t.Fatalf("statusline: rc=%d out=%q err=%q", rc, out.String(), errb.String())
		}
	}
	statusline(`{"model":{"display_name":"Opus"},"context_window":{"used_percentage":6,"context_window_size":1000000,
	  "current_usage":{"input_tokens":1000,"cache_creation_input_tokens":4000,"cache_read_input_tokens":55000}}}`)
	at, row := towerCheck(t, bus, "tower", child)
	if at != "50000" || row.CtxTokens == nil || *row.CtxTokens != 60000 || row.CtxPct == nil || *row.CtxPct != 6 {
		t.Fatalf("after the first report: relay_tokens=%s row=%+v", at, row)
	}
	if !row.Relay {
		t.Fatalf("60k tokens at relay_tokens 50000: no relay: %+v", row)
	}

	// Raised on the running daemon: the tower's next check reads the new value and lets it work.
	if rc, out, e := runEnv(t, env, "limits", "set", "relay_tokens", "200000"); rc != 0 || !strings.Contains(out, "relay_tokens 200000") {
		t.Fatalf("set 200000: rc=%d out=%q err=%q", rc, out, e)
	}
	if b, _ := os.ReadFile(user); !strings.Contains(string(b), "LEA_RELAY_TOKENS='200000'") {
		t.Fatalf("user file=%q", b)
	}
	if at, row := towerCheck(t, bus, "tower", child); at != "200000" || row.Relay {
		t.Fatalf("after set 200000: relay_tokens=%s row=%+v", at, row)
	}

	// A status line without tokens (an older Claude Code): the percent only, no tokens shown.
	statusline(`{"context_window":{"used_percentage":7}}`)
	if _, row := towerCheck(t, bus, "tower", child); row.CtxTokens != nil || row.CtxPct == nil || *row.CtxPct != 7 || row.Relay {
		t.Fatalf("without tokens: %+v", row)
	}
	raw, _ := json.Marshal(row)
	if strings.Contains(string(raw), "ctx_tokens\":0") {
		t.Fatalf("tokens shown as 0: %s", raw)
	}
}
