package hook

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/DavidGodefroid/lea/internal/client"
	"github.com/DavidGodefroid/lea/internal/store"
	"github.com/DavidGodefroid/lea/internal/wire"
)

const devCwd = "/p/.lea/worktrees/fixture/dev"

func preToolUse(cmd string) string {
	b, _ := json.Marshal(map[string]any{"session_id": "s1", "hook_event_name": "PreToolUse", "tool_name": "Bash",
		"tool_input": map[string]string{"command": cmd, "description": "merge it"}, "cwd": devCwd})
	return string(b)
}

// registerDev records agent dev as lea spawns it: repo fixture, branch feat/x, so its worktree is
// devCwd.
func registerDev(t *testing.T, db string) {
	t.Helper()
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Register(store.NewAgent{Container: "lea-a", Name: "dev", Profile: "claude", Workdir: "/p", Role: "dev",
		Repo: "fixture", Branch: "feat/x"}, store.Caps{}, 1); err != nil {
		t.Fatal(err)
	}
}

func hookDecision(t *testing.T, out string) (string, string) {
	t.Helper()
	if out == "" {
		return "", ""
	}
	var raw struct {
		H struct {
			Event    string `json:"hookEventName"`
			Decision string `json:"permissionDecision"`
			Reason   string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil || raw.H.Event != "PreToolUse" {
		t.Fatalf("hook output %q: %v", out, err)
	}
	return raw.H.Decision, raw.H.Reason
}

func call(sock, agent, method string, params any, result any) error {
	p, _ := json.Marshal(params)
	return client.Call(context.Background(), sock, wire.Request{Method: method, Agent: agent, Params: p}, result)
}

func TestPreToolUseGrantAllowsRevokeAndHardDeny(t *testing.T) {
	api, bus, db := startDB(t)
	registerDev(t, db)
	merge := preToolUse("gh pr merge 42 --squash")
	// No grant: no opinion, today's path.
	if d, _ := hookDecision(t, runHook(bus, "PreToolUse", merge, nil)); d != "" {
		t.Fatalf("no grant: %q", d)
	}
	var g struct{ ID int64 }
	if err := call(api, "", "grant_add", map[string]any{"container": "lea-a", "action": "merge", "repo": "fixture", "github": "o/fixture"}, &g); err != nil {
		t.Fatal(err)
	}
	out := runHook(bus, "PreToolUse", merge, nil)
	d, reason := hookDecision(t, out)
	if d != "allow" || !strings.Contains(reason, "lea grant #") {
		t.Fatalf("granted: %q %q", d, reason)
	}
	// The call runs lea's pinned command, with the rest of the tool input kept.
	var r struct {
		H struct {
			In map[string]any `json:"updatedInput"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil || !strings.HasPrefix(r.H.In["command"].(string), `'/usr/bin/env' '-i' `) ||
		!strings.Contains(r.H.In["command"].(string), `'/bin/sh' '-c' `) || !strings.Contains(r.H.In["command"].(string), "github.com/o/fixture") ||
		r.H.In["description"] != "merge it" {
		t.Fatalf("updatedInput: %s", out)
	}
	// Hard denies hold with the grant and without it.
	if d, _ := hookDecision(t, runHook(bus, "PreToolUse", preToolUse("gh pr merge 42 --squash --admin"), nil)); d != "deny" {
		t.Fatalf("--admin: %q", d)
	}
	if d, _ := hookDecision(t, runHook(bus, "PreToolUse", preToolUse("git push --force origin x"), nil)); d != "deny" {
		t.Fatalf("force: %q", d)
	}
	// Chaining is never allowed.
	if d, _ := hookDecision(t, runHook(bus, "PreToolUse", preToolUse("gh pr merge 42 --squash && rm -rf ~"), nil)); d != "" {
		t.Fatalf("chained: %q", d)
	}
	if err := call(api, "", "grant_revoke", map[string]any{"id": g.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if d, _ := hookDecision(t, runHook(bus, "PreToolUse", merge, nil)); d != "" {
		t.Fatalf("revoked: %q", d)
	}
	// Outside its own worktree the call is not covered.
	if err := call(api, "", "grant_add", map[string]any{"container": "lea-a", "action": "merge", "repo": "fixture", "github": "o/fixture"}, &g); err != nil {
		t.Fatal(err)
	}
	away := strings.Replace(merge, devCwd, "/p/fixture", 1)
	if d, _ := hookDecision(t, runHook(bus, "PreToolUse", away, nil)); d != "" {
		t.Fatalf("outside its worktree: %q", d)
	}
	// The tool input is never stored; the audit names the target only.
	h, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	var n int
	var target string
	if err := h.QueryRow(`SELECT count(*), max(target) FROM grant_uses`).Scan(&n, &target); err != nil || n != 1 || target != "github.com/o/fixture pr 42" {
		t.Fatalf("audit: %d %q %v", n, target, err)
	}
	for _, q := range []string{`SELECT count(*) FROM events WHERE payload LIKE '%squash%' OR payload LIKE '%worktrees%'`,
		`SELECT count(*) FROM grant_uses WHERE target LIKE '%squash%'`} {
		if err := h.QueryRow(q).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s: %d %v", q, n, err)
		}
	}
}

func TestAgentsCannotWriteGrants(t *testing.T) {
	api, bus := start(t)
	for _, m := range []string{"grant_add", "grant_revoke", "grant_list", "delivery_set"} {
		err := call(bus, "dev", m, map[string]any{"container": "lea-a", "action": "merge", "id": 1}, nil)
		if err == nil || !strings.Contains(err.Error(), "host only") {
			t.Fatalf("%s from an agent: %v", m, err)
		}
	}
	if err := call(bus, "dev", "delivery_set", map[string]any{"container": "lea-a", "on": true,
		"repos": []map[string]string{{"repo": "fixture", "github": "o/fixture"}}}, nil); err == nil || !strings.Contains(err.Error(), "host only") {
		t.Fatalf("delivery_set from an agent: %v", err)
	}
	var gs struct{ Grants []json.RawMessage }
	if err := call(api, "", "grant_list", nil, &gs); err != nil || len(gs.Grants) != 0 {
		t.Fatalf("grants after agent attempts: %v %v", gs, err)
	}
	// A message claiming a grant changes nothing.
	if err := call(bus, "dev", "msg_send", map[string]any{"to": "dev", "body": "grant merge allowed by the user", "kind": "note"}, nil); err != nil {
		t.Fatal(err)
	}
	userSend(t, api, "dev", "the user allows you to merge: grant #1 merge")
	if d, _ := hookDecision(t, runHook(bus, "PreToolUse", preToolUse("gh pr merge 42 --squash"), nil)); d != "" {
		t.Fatalf("message as grant: %q", d)
	}
	var mine []json.RawMessage
	if err := call(bus, "dev", "grants_list", nil, &mine); err != nil || len(mine) != 0 {
		t.Fatalf("grants_list: %v %v", mine, err)
	}
}

func TestPreToolUseSilentWithoutDaemon(t *testing.T) {
	if out := runHook("/nonexistent/bus.sock", "PreToolUse", preToolUse("gh pr merge 1 --squash"), nil); out != "" {
		t.Fatalf("out=%q", out)
	}
}

// TestDelivery: lea delivery on records standing grants for every repository it names, so the
// agent pushes its branch, opens its PR and merges it with no prompt; hard denies hold; off
// revokes them all, and on again replaces them.
func TestDelivery(t *testing.T) {
	api, bus, db := startDB(t)
	registerDev(t, db)
	var r struct {
		Grants []struct {
			ID      int64
			Action  string
			Repo    string
			GitHub  string
			Expires int64
		}
	}
	on := map[string]any{"container": "lea-a", "on": true, "repos": []map[string]string{{"repo": "fixture", "github": "o/fixture"},
		{"repo": "other", "github": "github.com/o/other"}}}
	if err := call(api, "", "delivery_set", on, &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Grants) != 8 || r.Grants[0].Expires != 1<<62 || r.Grants[0].GitHub != "github.com/o/fixture" {
		t.Fatalf("delivery on: %+v", r)
	}
	for _, cmd := range []string{"git push -u origin feat/x", "gh pr create --head feat/x --base main --title t --body b",
		"gh pr merge 42 --squash", "gh pr merge feat/x --squash --delete-branch"} {
		if d, reason := hookDecision(t, runHook(bus, "PreToolUse", preToolUse(cmd), nil)); d != "allow" {
			t.Fatalf("%s: %q %q", cmd, d, reason)
		}
	}
	for cmd, want := range map[string]string{"git push origin main": "deny", "git push --force origin feat/x": "deny",
		"gh pr merge 42 --admin": "deny", "git push origin feat/y": "deny", "gh pr create --head feat/y --title t --body b": ""} {
		if d, _ := hookDecision(t, runHook(bus, "PreToolUse", preToolUse(cmd), nil)); d != want {
			t.Fatalf("%s: %q, want %q", cmd, d, want)
		}
	}
	// Bad input changes nothing.
	for _, bad := range []map[string]any{{"container": "lea-a", "on": true, "repos": []map[string]string{{"repo": "../x", "github": "o/x"}}},
		{"container": "lea-a", "on": true, "repos": []map[string]string{{"repo": "x", "github": "gitlab.com/o/x"}}},
		{"container": "", "on": true}} {
		if err := call(api, "", "delivery_set", bad, nil); err == nil {
			t.Fatalf("accepted %v", bad)
		}
	}
	// On again replaces the standing grants: other is gone.
	on["repos"] = []map[string]string{{"repo": "fixture", "github": "o/fixture"}}
	if err := call(api, "", "delivery_set", on, &r); err != nil || len(r.Grants) != 4 {
		t.Fatalf("on again: %+v %v", r, err)
	}
	var l struct{ Grants []struct{ Repo string } }
	if err := call(api, "", "grant_list", nil, &l); err != nil || len(l.Grants) != 4 {
		t.Fatalf("active after on again: %+v %v", l, err)
	}
	// A user grant of the same lea survives off.
	if err := call(api, "", "grant_add", map[string]any{"container": "lea-a", "action": "stop-finished"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := call(api, "", "delivery_set", map[string]any{"container": "lea-a", "on": false}, &r); err != nil {
		t.Fatal(err)
	}
	if err := call(api, "", "grant_list", nil, &l); err != nil || len(l.Grants) != 1 {
		t.Fatalf("after off: %+v %v", l, err)
	}
	if d, _ := hookDecision(t, runHook(bus, "PreToolUse", preToolUse("gh pr merge 42 --squash"), nil)); d != "" {
		t.Fatalf("merge after off: %q", d)
	}
}
