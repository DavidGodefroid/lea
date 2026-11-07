package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/client"
	"github.com/DavidGodefroid/lea/internal/store"
	"github.com/DavidGodefroid/lea/internal/wire"
)

// busCall calls the daemon as agent from inside the container, as lea's MCP server does.
func busCall(t *testing.T, bus, agent, method string, params any) (json.RawMessage, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	raw, _ := json.Marshal(params)
	var res json.RawMessage
	err := client.Call(ctx, bus, wire.Request{Method: method, Agent: agent, Params: raw}, &res)
	return res, err
}

// TestGrantRequestReachesTheLeadsChildren is issue #342's scenario: the user tells a lead its team
// may merge; the lead files grants_request; nothing is granted until the user approves it on the
// host; then the lead's child merges its own PR through the pinned command, without a prompt, and
// an agent outside the lead's subtree is still not covered. The same flow without the approval
// stays uncovered; a release-tag request covers the one pinned tag push.
func TestGrantRequestReachesTheLeadsChildren(t *testing.T) {
	api, bus, st := startDaemonWithStore(t)
	reg := func(name, role, parent, branch string) store.Agent {
		a, err := st.Register(store.NewAgent{Container: "lea-a", Name: name, Profile: "claude", Workdir: "/p", Role: role,
			Parent: parent, Repo: "fixture", Branch: branch}, store.Caps{}, 1)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	lead := reg("lead", "lead", "", "")
	reg("dev", "dev", lead.Key, "feat/a")
	reg("other", "dev", "", "feat/b")
	host := map[string]string{"LEA_DAEMON_SOCK": api}
	merge := cmd("gh pr merge 7 --squash --delete-branch")
	ask := map[string]any{"action": "merge", "repo": "fixture", "github": "o/fixture", "ttl_s": 7 * 24 * 3600,
		"reason": "the user: you may merge on fixture"}
	active := func() int {
		gs, err := st.ActiveGrants("lea-a", time.Now().Unix())
		if err != nil {
			t.Fatal(err)
		}
		return len(gs)
	}

	if d := preToolUseAs(t, bus, "dev", "Bash", merge); d != "" {
		t.Fatalf("before any grant: %q", d)
	}
	// Only a lead files a request, and a request is checked as grant add checks a grant.
	if _, err := busCall(t, bus, "dev", "grants_request", ask); err == nil || !strings.Contains(err.Error(), "leads only") {
		t.Fatalf("a dev's request: %v", err)
	}
	for _, bad := range []map[string]any{
		{"action": "rm-rf", "repo": "fixture", "github": "o/fixture"},
		{"action": "merge", "repo": "fixture"},
		{"action": "merge", "github": "o/fixture"},
		{"action": "merge", "repo": "fixture", "github": "o/fixture", "branch": "main"},
		{"action": "merge", "repo": "fixture", "github": "o/fixture", "ttl_s": 90 * 24 * 3600},
		{"action": "merge", "repo": "../x", "github": "o/fixture"},
	} {
		if _, err := busCall(t, bus, "lead", "grants_request", bad); err == nil {
			t.Fatalf("request %v accepted", bad)
		}
	}
	res, err := busCall(t, bus, "lead", "grants_request", ask)
	if err != nil || !strings.Contains(string(res), `"request":"g1"`) {
		t.Fatalf("lead's request: %v %s", err, res)
	}
	// The same request again is the same one; still no grant.
	if res, err := busCall(t, bus, "lead", "grants_request", ask); err != nil || !strings.Contains(string(res), `"request":"g1"`) {
		t.Fatalf("same request: %v %s", err, res)
	}
	if n := active(); n != 0 {
		t.Fatalf("a request recorded %d grants", n)
	}
	if d := preToolUseAs(t, bus, "dev", "Bash", merge); d != "" {
		t.Fatalf("before the approval: %q", d)
	}
	// No agent decides a request: the method is the host's.
	for _, m := range []string{"grant_request_decide", "grant_request_list"} {
		if _, err := busCall(t, bus, "lead", m, map[string]any{"id": 1, "verdict": "approve"}); err == nil || !strings.Contains(err.Error(), "host only") {
			t.Fatalf("%s from a container: %v", m, err)
		}
	}
	if n := active(); n != 0 {
		t.Fatalf("a container decided: %d grants", n)
	}
	rc, out, e := runEnv(t, host, "requests")
	if rc != 0 || !strings.Contains(out, "g1 lead (lead in lea-a) asks for a merge grant on repo fixture (gh github.com/o/fixture)") ||
		!strings.Contains(out, "reason: the user: you may merge on fixture") || !strings.Contains(out, "lea approve gN") {
		t.Fatalf("requests rc=%d out=%q err=%q", rc, out, e)
	}
	// The user was told, in their inbox.
	if rc, out, _ := runEnv(t, host, "inbox"); rc != 0 || !strings.Contains(out, "lea approve g1") {
		t.Fatalf("user inbox: %q", out)
	}
	rc, out, e = runEnv(t, host, "approve", "g1")
	if rc != 0 || !strings.Contains(out, "g1 approved: grant #1: merge in lea-a repo fixture gh github.com/o/fixture for lea-a/lead and its children") {
		t.Fatalf("approve rc=%d out=%q err=%q", rc, out, e)
	}
	if d := preToolUseAs(t, bus, "dev", "Bash", merge); d != "allow" {
		t.Fatalf("the lead's child after the approval: %q", d)
	}
	if d := preToolUseAs(t, bus, "other", "Bash", cmd("gh pr merge 8 --squash --delete-branch")); d != "" {
		t.Fatalf("outside the lead's subtree: %q", d)
	}
	if rc, _, _ := runEnv(t, host, "approve", "g1"); rc != 1 {
		t.Fatalf("approved twice: rc=%d", rc)
	}
	rc, out, _ = runEnv(t, map[string]string{"LEA_BUS_SOCK": bus, "LEA_AGENT": "lead"}, "inbox", "--agent")
	if rc != 0 || !strings.Contains(out, "grant request g1 approved by the user: grant #1") {
		t.Fatalf("lead inbox: %q", out)
	}

	// release-tag: denied, then requested and approved; the tag push is then covered.
	tag := cmd("git push origin refs/tags/v1.2.3")
	if d := preToolUseAs(t, bus, "dev", "Bash", tag); d != "deny" {
		t.Fatalf("tag push without a grant: %q", d)
	}
	res, err = busCall(t, bus, "lead", "grants_request", map[string]any{"action": "release-tag", "repo": "fixture", "github": "o/fixture"})
	if err != nil || !strings.Contains(string(res), `"request":"g2"`) {
		t.Fatalf("release-tag request: %v %s", err, res)
	}
	if rc, out, e := runEnv(t, host, "deny", "g2", "not", "now"); rc != 0 || !strings.Contains(out, "g2 denied") {
		t.Fatalf("deny rc=%d out=%q err=%q", rc, out, e)
	}
	if d := preToolUseAs(t, bus, "dev", "Bash", tag); d != "deny" {
		t.Fatalf("tag push after a denial: %q", d)
	}
	rc, out, _ = runEnv(t, map[string]string{"LEA_BUS_SOCK": bus, "LEA_AGENT": "lead"}, "inbox", "--agent")
	if rc != 0 || !strings.Contains(out, "grant request g2 denied") || !strings.Contains(out, "not now") {
		t.Fatalf("lead inbox after the denial: %q", out)
	}
	if _, err := busCall(t, bus, "lead", "grants_request", map[string]any{"action": "release-tag", "repo": "fixture", "github": "o/fixture"}); err != nil {
		t.Fatal(err)
	}
	if rc, out, e := runEnv(t, host, "grant", "requests"); rc != 0 || !strings.Contains(out, "g3 ") || !strings.Contains(out, "release-tag") {
		t.Fatalf("grant requests rc=%d out=%q err=%q", rc, out, e)
	}
	if rc, out, e := runEnv(t, host, "grant", "approve", "3", "--expires", "30d"); rc != 0 || !strings.Contains(out, "g3 approved") {
		t.Fatalf("grant approve rc=%d out=%q err=%q", rc, out, e)
	}
	if d := preToolUseAs(t, bus, "dev", "Bash", tag); d != "allow" {
		t.Fatalf("tag push with the grant: %q", d)
	}
	if d := preToolUseAs(t, bus, "dev", "Bash", cmd("git push --tags origin")); d != "deny" {
		t.Fatalf("--tags with the grant: %q", d)
	}

	// A request nobody answers expires, and its lead is told.
	if _, err := st.AddGrantRequest(store.GrantRequest{Container: "lea-a", Requester: lead.Key, Action: "close-issue", Repo: "fixture",
		GitHub: "github.com/o/fixture", TTLS: 3600}, 20, 1); err != nil {
		t.Fatal(err)
	}
	if rc, out, _ := runEnv(t, host, "grant", "requests"); rc != 0 || !strings.Contains(out, "no grant request") {
		t.Fatalf("expired request listed: %q", out)
	}
	rc, out, _ = runEnv(t, map[string]string{"LEA_BUS_SOCK": bus, "LEA_AGENT": "lead"}, "inbox", "--agent")
	if rc != 0 || !strings.Contains(out, "grant request g4 expired") {
		t.Fatalf("lead inbox after the expiry: %q", out)
	}
}

func TestGrantRequestUsage(t *testing.T) {
	api, _ := startDaemon(t)
	host := map[string]string{"LEA_DAEMON_SOCK": api}
	for _, args := range [][]string{{"approve", "g"}, {"approve", "g1", "--expires"}, {"approve", "g1", "--expires", "soon"},
		{"approve", "g1", "always"}, {"deny", "g1", "g2", "why"}, {"grant", "approve"}, {"grant", "requests", "x"}} {
		if rc, _, _ := runEnv(t, host, args...); rc != 2 {
			t.Errorf("%v: rc=%d", args, rc)
		}
	}
	if rc, _, e := runEnv(t, host, "approve", "g99"); rc != 1 || !strings.Contains(e, "no grant request g99") {
		t.Errorf("approve g99: rc=%d %s", rc, e)
	}
	if rc, out, _ := runEnv(t, host, "requests"); rc != 0 || !strings.Contains(out, "nothing waits for you") {
		t.Errorf("requests: %q", out)
	}
}
