package hook

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

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
	var g struct{ Set struct{ ID int64 } }
	if err := call(api, "", "allow_set", allowMerge, &g); err != nil {
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
	if err := call(api, "", "allow_off", map[string]any{"container": "lea-a", "set": g.Set.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if d, _ := hookDecision(t, runHook(bus, "PreToolUse", merge, nil)); d != "" {
		t.Fatalf("revoked: %q", d)
	}
	// Outside its own worktree the call is not covered.
	if err := call(api, "", "allow_set", allowMerge, &g); err != nil {
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
	for _, m := range []string{"allow_set", "allow_off", "allow_list", "grant_revoke", "grant_add", "grant_list", "delivery_set"} {
		err := call(bus, "dev", m, map[string]any{"container": "lea-a", "action": "merge", "id": 1}, nil)
		if err == nil || (!strings.Contains(err.Error(), "host only") && !strings.Contains(err.Error(), "unknown method")) {
			t.Fatalf("%s from an agent: %v", m, err)
		}
	}
	if err := call(bus, "dev", "allow_set", allowMerge, nil); err == nil || !strings.Contains(err.Error(), "host only") {
		t.Fatalf("allow_set from an agent: %v", err)
	}
	var gs struct{ Sets []json.RawMessage }
	if err := call(api, "", "allow_list", map[string]any{"container": "lea-a"}, &gs); err != nil || len(gs.Sets) != 0 {
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

// TestPreToolUseHint: a form the caller's grant does not cover takes the normal path, with the
// covered forms in the agent's context (#271).
func TestPreToolUseHint(t *testing.T) {
	api, bus, db := startDB(t)
	registerDev(t, db)
	if err := call(api, "", "allow_set", allowMerge, nil); err != nil {
		t.Fatal(err)
	}
	out := runHook(bus, "PreToolUse", preToolUse("gh pr merge 42 --squash -R o/fixture"), nil)
	var r struct {
		H struct {
			Event    string `json:"hookEventName"`
			Decision string `json:"permissionDecision"`
			Context  string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil || r.H.Event != "PreToolUse" || r.H.Decision != "" ||
		!strings.Contains(r.H.Context, "gh pr merge <number|feat/x>") {
		t.Fatalf("near miss: %q %v", out, err)
	}
	// Covered: allowed, no hint.
	out = runHook(bus, "PreToolUse", preToolUse("gh pr merge 42 --squash --delete-branch"), nil)
	if d, _ := hookDecision(t, out); d != "allow" || strings.Contains(out, "additionalContext") {
		t.Fatalf("covered: %q", out)
	}
}

// lea's guard fails closed (#111): a PreToolUse it cannot check (no bus, no identity, an identity
// the daemon refuses, no tool) is a deny, never the normal path. The other events stay silent.
func TestPreToolUseFailsClosed(t *testing.T) {
	began := time.Now()
	d, why := hookDecision(t, runHook("/nonexistent/bus.sock", "PreToolUse", preToolUse("gh pr merge 1 --squash"), nil))
	if d != "deny" || !strings.Contains(why, "fails closed") || !strings.Contains(why, "bus did not answer") {
		t.Fatalf("no bus: %q %q", d, why)
	}
	if time.Since(began) > 3*time.Second {
		t.Fatalf("no bus: answered after %s", time.Since(began))
	}
	if out := runHook("/nonexistent/bus.sock", "PostToolUse", `{"session_id":"s","tool_name":"Bash"}`, nil); out != "" {
		t.Fatalf("PostToolUse without a bus: %q", out)
	}
	_, bus := start(t)
	for name, env := range map[string]map[string]string{
		"no LEA_AGENT":    {"LEA_BUS_SOCK": bus},
		"empty LEA_AGENT": {"LEA_BUS_SOCK": bus, "LEA_AGENT": ""},
	} {
		var out bytes.Buffer
		Claude(context.Background(), "PreToolUse", strings.NewReader(preToolUse("ls")), &out, envOf(env))
		if d, why := hookDecision(t, out.String()); d != "deny" || !strings.Contains(why, "no lea agent identity") {
			t.Fatalf("%s: %q %q", name, d, why)
		}
	}
	// An identity the daemon refuses: hook_event fails, the call is denied.
	if d, why := hookDecision(t, runHook(bus, "PreToolUse", preToolUse("ls"), map[string]string{"LEA_AGENT": "bad name"})); d != "deny" ||
		!strings.Contains(why, "invalid agent name") {
		t.Fatalf("refused identity: %q %q", d, why)
	}
	if d, why := hookDecision(t, runHook(bus, "PreToolUse", `{"session_id":"s1"}`, nil)); d != "deny" || !strings.Contains(why, "names no tool") {
		t.Fatalf("no tool: %q %q", d, why)
	}
	// A call the guard checked and has no verdict for still takes the normal path.
	if out := runHook(bus, "PreToolUse", preToolUse("ls"), nil); out != "" {
		t.Fatalf("no verdict: %q", out)
	}
}

// allowMerge is lea allow merge --repo fixture, as the CLI sends it.
var allowMerge = map[string]any{"container": "lea-a", "capabilities": []string{"merge"}, "repos": []map[string]string{{"repo": "fixture", "pin": "o/fixture"}}}

type allowed struct {
	Set struct {
		ID      int64
		Expires int64
		Grants  []struct {
			ID      int64
			Action  string
			Repo    string
			Pin     string
			Expires int64
			Revoked int64
		}
	}
	Unchanged bool
}

// TestDelivery: lea allow deliver records a set of grants that hold until revoked, for every
// repository it names, so the agent pushes its branch, opens its PR and merges it with no
// prompt; hard denies hold; recorded again, the set keeps its unchanged rows (#319); lea allow
// off deliver revokes it and leaves the lea's other sets alone.
func TestDelivery(t *testing.T) {
	api, bus, db := startDB(t)
	registerDev(t, db)
	var r allowed
	on := map[string]any{"container": "lea-a", "capabilities": []string{"deliver"}, "all": true, "repos": []map[string]string{{"repo": "fixture", "pin": "o/fixture"},
		{"repo": "other", "pin": "github.com/o/other"}, {"repo": "gl", "pin": "gitlab:gitlab.example.com/g/sub/gl"}}}
	if err := call(api, "", "allow_set", on, &r); err != nil {
		t.Fatal(err)
	}
	pins := map[string]string{}
	for _, g := range r.Set.Grants {
		pins[g.Repo] = g.Pin
	}
	if len(r.Set.Grants) != 12 || pins["gl"] != "gitlab:gitlab.example.com/g/sub/gl" || r.Set.Expires != 1<<62 || r.Set.Grants[0].Expires != 1<<62 ||
		pins["fixture"] != "github.com/o/fixture" || r.Unchanged {
		t.Fatalf("allow deliver: %+v", r)
	}
	first := r.Set
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
	for _, bad := range []map[string]any{{"container": "lea-a", "capabilities": []string{"deliver"}, "repos": []map[string]string{{"repo": "../x", "pin": "o/x"}}},
		{"container": "lea-a", "capabilities": []string{"deliver"}, "repos": []map[string]string{{"repo": "x", "pin": "gitlab.com/o/x"}}},
		{"container": "lea-a", "capabilities": []string{"deliver"}, "repos": []map[string]string{{"repo": "x", "pin": "gitlab:gitlab.com/x"}}},
		{"container": "lea-a", "capabilities": []string{"deliver"}, "repos": []map[string]string{{"repo": "x", "pin": "gitlab:evil host/o/x"}}},
		{"container": "lea-a", "capabilities": []string{"deliver"}},
		{"container": "lea-a", "capabilities": []string{"rm-rf"}, "repos": []map[string]string{{"repo": "x", "pin": "o/x"}}},
		{"container": "lea-a", "capabilities": []string{"stop-finished"}, "repos": []map[string]string{{"repo": "x", "pin": "o/x"}}},
		{"container": "lea-a", "capabilities": []string{"merge"}, "branch": "main", "repos": []map[string]string{{"repo": "x", "pin": "o/x"}}},
		{"container": "lea-a", "capabilities": []string{"merge"}, "ttl_s": 30, "repos": []map[string]string{{"repo": "x", "pin": "o/x"}}},
		{"container": "lea-a", "capabilities": []string{"merge"}, "ttl_s": 31 * 24 * 3600, "repos": []map[string]string{{"repo": "x", "pin": "o/x"}}},
		{"container": "lea-a", "capabilities": []string{"merge"}, "agent": "nobody", "repos": []map[string]string{{"repo": "x", "pin": "o/x"}}},
		{"container": "", "capabilities": []string{"deliver"}}} {
		if err := call(api, "", "allow_set", bad, nil); err == nil {
			t.Fatalf("accepted %v", bad)
		}
	}
	list := func() []struct {
		ID     int64
		Grants []struct{ ID, Revoked int64 }
	} {
		t.Helper()
		var l struct {
			Sets []struct {
				ID     int64
				Grants []struct{ ID, Revoked int64 }
			}
		}
		if err := call(api, "", "allow_list", map[string]any{"container": "lea-a"}, &l); err != nil {
			t.Fatal(err)
		}
		return l.Sets
	}
	// The same call again changes nothing: ids are stable.
	if err := call(api, "", "allow_set", on, &r); err != nil || !r.Unchanged || r.Set.ID != first.ID {
		t.Fatalf("again: %+v %v", r, err)
	}
	// Recorded again over every repository, fixture alone: the same set, its fixture rows kept,
	// other's and gl's revoked.
	on["repos"] = []map[string]string{{"repo": "fixture", "pin": "o/fixture"}}
	if err := call(api, "", "allow_set", on, &r); err != nil || r.Unchanged || r.Set.ID != first.ID {
		t.Fatalf("on again: %+v %v", r, err)
	}
	kept := 0
	for _, g := range r.Set.Grants {
		if g.Revoked == 0 {
			kept++
			if g.Repo != "fixture" || g.ID != first.Grants[indexOf(first, g.ID)].ID {
				t.Fatalf("kept row: %+v", g)
			}
		}
	}
	if kept != 4 {
		t.Fatalf("on again: %d rows active: %+v", kept, r)
	}
	// A changed pin replaces only that repository's rows.
	on["repos"] = []map[string]string{{"repo": "fixture", "pin": "o/fork"}}
	if err := call(api, "", "allow_set", on, &r); err != nil || r.Set.ID != first.ID {
		t.Fatalf("new pin: %+v %v", r, err)
	}
	if d, _ := hookDecision(t, runHook(bus, "PreToolUse", merge42, nil)); d != "allow" {
		t.Fatalf("merge after the new pin: %q", d)
	}
	// Another set of the same lea survives off deliver.
	if err := call(api, "", "allow_set", map[string]any{"container": "lea-a", "capabilities": []string{"stop-finished"}}, nil); err != nil {
		t.Fatal(err)
	}
	if len(list()) != 2 {
		t.Fatalf("two sets: %+v", list())
	}
	if err := call(api, "", "allow_off", map[string]any{"container": "lea-a", "capability": "deliver"}, nil); err != nil {
		t.Fatal(err)
	}
	if l := list(); len(l) != 1 || l[0].ID == first.ID {
		t.Fatalf("after off deliver: %+v", l)
	}
	if d, _ := hookDecision(t, runHook(bus, "PreToolUse", merge42, nil)); d != "" {
		t.Fatalf("merge after off: %q", d)
	}
	// Another lea's set is not this lea's to revoke.
	if err := call(api, "", "allow_off", map[string]any{"container": "lea-b", "set": list()[0].ID}, nil); err == nil {
		t.Fatal("revoked another lea's set")
	}
	if err := call(api, "", "allow_off", map[string]any{"container": "lea-a"}, nil); err != nil || len(list()) != 0 {
		t.Fatalf("off everything: %v %+v", err, list())
	}
}

var merge42 = preToolUse("gh pr merge 42 --squash")

func indexOf(s struct {
	ID      int64
	Expires int64
	Grants  []struct {
		ID      int64
		Action  string
		Repo    string
		Pin     string
		Expires int64
		Revoked int64
	}
}, id int64) int {
	for i, g := range s.Grants {
		if g.ID == id {
			return i
		}
	}
	return 0
}
