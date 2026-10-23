package main

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/DavidGodefroid/lea/internal/store"
)

// preToolUse runs `lea hook claude PreToolUse` as agent in the container, as Claude Code does,
// and returns the permission decision it prints ("" when none).
func preToolUseAs(t *testing.T, bus, agent, tool string, input any) string {
	t.Helper()
	cwd := "/p/.lea/worktrees/fixture/" + agent
	t.Setenv("LEA_BUS_SOCK", bus)
	t.Setenv("LEA_AGENT", agent)
	b, _ := json.Marshal(map[string]any{"session_id": "s", "hook_event_name": "PreToolUse", "tool_name": tool, "tool_input": input, "cwd": cwd})
	var out, errb bytes.Buffer
	if rc := run([]string{"hook", "claude", "PreToolUse"}, bytes.NewReader(b), &out, &errb); rc != 0 {
		t.Fatalf("hook rc=%d %s", rc, errb.String())
	}
	if out.Len() == 0 {
		return ""
	}
	var r struct {
		H struct {
			Decision string `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("hook output %q", out.String())
	}
	return r.H.Decision
}

func cmd(c string) map[string]string { return map[string]string{"command": c} }

// TestGrantsReachTheWholeSubtree is issue #73's scenario: the user grants merge on a repo from
// the CLI to a lead's subtree; its child merges without asking; revoke ends it; --admin is
// refused throughout; an agent outside the subtree is not covered. (lea's tree is a lead and its
// children today: deeper lineages are covered by the grants package's tests.)
func TestGrantsReachTheWholeSubtree(t *testing.T) {
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
	dev := reg("dev", "dev", lead.Key, "feat/a")
	reg("sub", "reviewer", lead.Key, "feat/a-1")
	reg("other", "dev", "", "feat/b")
	host := map[string]string{"LEA_DAEMON_SOCK": api}
	merge := cmd("gh pr merge feat/a-1 --squash --delete-branch")
	admin := cmd("gh pr merge 7 --squash --admin")

	if d := preToolUseAs(t, bus, "sub", "Bash", merge); d != "" {
		t.Fatalf("before any grant: %q", d)
	}
	rc, out, e := runEnv(t, host, "grant", "add", "merge", "--container", "lea-a", "--repo", "fixture", "--github", "o/fixture", "--agent", "lead", "--expires", "2h")
	if rc != 0 || !strings.Contains(out, "merge in lea-a repo fixture gh github.com/o/fixture for lea-a/lead and its children") {
		t.Fatalf("grant add rc=%d out=%q err=%q", rc, out, e)
	}
	id := regexp.MustCompile(`#(\d+)`).FindStringSubmatch(out)[1]
	if d := preToolUseAs(t, bus, "sub", "Bash", merge); d != "allow" {
		t.Fatalf("two levels below the lead: %q", d)
	}
	if d := preToolUseAs(t, bus, "other", "Bash", merge); d != "" {
		t.Fatalf("outside the subtree: %q", d)
	}
	if d := preToolUseAs(t, bus, "sub", "Bash", admin); d != "deny" {
		t.Fatalf("--admin with a grant: %q", d)
	}
	rc, out, _ = runEnv(t, host, "grant", "ls")
	if rc != 0 || !strings.Contains(out, "#"+id+"  active") || !strings.Contains(out, "lea-a/sub merge: github.com/o/fixture pr of feat/a-1") {
		t.Fatalf("grant ls rc=%d out=%q", rc, out)
	}
	if rc, out, _ := runEnv(t, host, "grant", "revoke", id); rc != 0 || !strings.Contains(out, "revoked") {
		t.Fatalf("revoke rc=%d out=%q", rc, out)
	}
	if d := preToolUseAs(t, bus, "sub", "Bash", merge); d != "" {
		t.Fatalf("after revoke: %q", d)
	}
	if d := preToolUseAs(t, bus, "sub", "Bash", admin); d != "deny" {
		t.Fatalf("--admin after revoke: %q", d)
	}
	if rc, out, _ := runEnv(t, host, "grant", "ls", "--all"); rc != 0 || !strings.Contains(out, "#"+id+"  revoked") {
		t.Fatalf("grant ls --all rc=%d out=%q", rc, out)
	}

	// delete-own-branch: the caller's own branch only while no other live agent shares it.
	if rc, _, e := runEnv(t, host, "grant", "add", "delete-own-branch", "--container", "lea-a", "--github", "o/fixture"); rc != 0 {
		t.Fatalf("grant delete-own-branch: %s", e)
	}
	del := cmd("git push origin --delete feat/a-1")
	if d := preToolUseAs(t, bus, "sub", "Bash", del); d != "allow" {
		t.Fatalf("delete own branch: %q", d)
	}
	reg("twin", "dev", lead.Key, "feat/a-1")
	if d := preToolUseAs(t, bus, "sub", "Bash", del); d != "deny" {
		t.Fatalf("delete a branch another agent works on: %q", d)
	}

	// stop-finished: the lead stops its idle child, not a working one, not another's child.
	if rc, _, e := runEnv(t, host, "grant", "add", "stop-finished", "--container", "lea-a"); rc != 0 {
		t.Fatalf("grant stop-finished: %s", e)
	}
	stop := map[string]string{"name": "dev"}
	if d := preToolUseAs(t, bus, "lead", "mcp__lea__agents_stop", stop); d != "" {
		t.Fatalf("stop a working child: %q", d)
	}
	if err := st.SetState(dev.Key, "idle", "", 2); err != nil {
		t.Fatal(err)
	}
	if d := preToolUseAs(t, bus, "lead", "mcp__lea__agents_stop", stop); d != "allow" {
		t.Fatalf("stop an idle child: %q", d)
	}
	if d := preToolUseAs(t, bus, "other", "mcp__lea__agents_stop", stop); d != "" {
		t.Fatalf("stop another's child: %q", d)
	}
}

func TestGrantCommandUsage(t *testing.T) {
	api, _ := startDaemon(t)
	host := map[string]string{"LEA_DAEMON_SOCK": api}
	for _, args := range [][]string{{"grant"}, {"grant", "add", "rm-rf", "--container", "lea-a"}, {"grant", "add", "merge"},
		{"grant", "add", "merge", "--container"}, {"grant", "add", "merge", "--container", "lea-a", "--expires", "soon"},
		{"grant", "revoke", "x"}, {"grant", "ls", "--bogus"}} {
		if rc, _, _ := runEnv(t, host, args...); rc != 2 {
			t.Errorf("%v: rc=%d", args, rc)
		}
	}
	for _, args := range [][]string{{"grant", "add", "merge", "--container", "lea-a", "--github", "o/r", "--branch", "main"},
		{"grant", "add", "merge", "--container", "lea-a"}, {"grant", "add", "close-issue", "--container", "lea-a", "--github", "o/../x"},
		{"grant", "add", "merge", "--container", "lea-a", "--expires", "1000h"}, {"grant", "revoke", "99"}} {
		if rc, _, _ := runEnv(t, host, args...); rc != 1 {
			t.Errorf("%v: rc=%d", args, rc)
		}
	}
}
