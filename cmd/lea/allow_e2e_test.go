package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/DavidGodefroid/lea/internal/grants"
	"github.com/DavidGodefroid/lea/internal/store"
)

// hookOut runs the PreToolUse hook as agent in cwd: its decision and the context it gives the
// agent (the hint).
func hookOut(t *testing.T, bus, agent string, input any, cwd string) (decision, context string) {
	t.Helper()
	t.Setenv("LEA_BUS_SOCK", bus)
	t.Setenv("LEA_AGENT", agent)
	b, _ := json.Marshal(map[string]any{"session_id": "s", "hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": input, "cwd": cwd})
	var out, errb bytes.Buffer
	if rc := run([]string{"hook", "claude", "PreToolUse"}, bytes.NewReader(b), &out, &errb); rc != 0 {
		t.Fatalf("hook rc=%d %s", rc, errb.String())
	}
	if out.Len() == 0 {
		return "", ""
	}
	var r struct {
		H struct {
			Decision string `json:"permissionDecision"`
			Context  string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("hook output %q", out.String())
	}
	return r.H.Decision, r.H.Context
}

// TestAllowPushEndToEnd is #319's e2e: a real daemon, the real hook and a real git worktree.
// Before anything is allowed, a push takes the normal path and the hint names the lea allow
// command; lea allow push --repo fixture --for 10m lets the agent's commit and push run without
// a prompt, and lea allow log audits them; lea allow off puts the next push back on the normal
// path.
func TestAllowPushEndToEnd(t *testing.T) {
	api, bus, st := startDaemonWithStore(t)
	if _, err := st.Register(store.NewAgent{Container: "lea-a", Name: "dev", Profile: "claude", Workdir: "/p", Role: "dev",
		Repo: "fixture", Branch: "feat/a"}, store.Caps{}, 1); err != nil {
		t.Fatal(err)
	}
	f := allowFixture{t, t.TempDir(), map[string]string{"LEA_DAEMON_SOCK": api}}
	f.origin("fixture", "https://github.com/o/fixture.git")
	wt := worktree(t, f.proj, "fixture", "dev", "feat/a")
	push, commit := cmd("git push -u origin feat/a"), cmd("git commit -m wip")
	if d, hint := hookOut(t, bus, "dev", push, wt); d != "" || !strings.Contains(hint, "lea allow push --repo fixture") {
		t.Fatalf("before: %q %q", d, hint)
	}
	args := []string{"push", "--repo", "fixture", "--for", "10m"}
	if rc, out, e := f.allow(append(args, "--yes", f.digest(args...))...); rc != 0 || !strings.Contains(out, "(10m left; lea allow off 1 ends it sooner)") {
		t.Fatalf("allow push: rc=%d out=%q err=%q", rc, out, e)
	}
	for _, c := range []map[string]string{commit, push} {
		if d, _ := hookOut(t, bus, "dev", c, wt); d != "allow" {
			t.Fatalf("%v: %q", c, d)
		}
	}
	rc, out, _ := f.allow("log")
	if rc != 0 || !strings.Contains(out, "#1 lea-a/dev push: fixture commit") || !strings.Contains(out, "#1 lea-a/dev push: fixture") {
		t.Fatalf("log: rc=%d %q", rc, out)
	}
	if rc, _, _ := f.allow("off"); rc != 0 {
		t.Fatal("off")
	}
	if d, hint := hookOut(t, bus, "dev", push, wt); d != "" || !strings.Contains(hint, "lea allow push --repo fixture") {
		t.Fatalf("after off: %q %q", d, hint)
	}
}

// TestAllowMigratesAnOldDatabase: a lea.db of the previous release (delivery on over two
// repositories, one hand grant, one revoked) opens in the new daemon as two allowances, and the
// hook gives the same verdicts as before.
func TestAllowMigratesAnOldDatabase(t *testing.T) {
	dir, _ := os.MkdirTemp("", "lea")
	t.Cleanup(func() { os.RemoveAll(dir) })
	db, err := sql.Open("sqlite", filepath.Join(dir, "lea.db"))
	if err != nil {
		t.Fatal(err)
	}
	// The grants table as lea v0.14 created it.
	if _, err := db.Exec(`CREATE TABLE grants(
  id INTEGER PRIMARY KEY AUTOINCREMENT, container TEXT NOT NULL, action TEXT NOT NULL,
  repo TEXT NOT NULL DEFAULT '', branch TEXT NOT NULL DEFAULT '', agent TEXT NOT NULL DEFAULT '',
  github TEXT NOT NULL DEFAULT '', created INTEGER NOT NULL, expires INTEGER NOT NULL, revoked INTEGER NOT NULL DEFAULT 0)`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	add := func(action, repo string, expires, revoked int64) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO grants(container, action, repo, github, created, expires, revoked) VALUES('lea-a',?,?,?,?,?,?)`,
			action, repo, "github.com/o/"+repo, now-60, expires, revoked); err != nil {
			t.Fatal(err)
		}
	}
	for _, repo := range []string{"fixture", "web"} {
		for _, a := range grants.DeliveryActions {
			add(a, repo, grants.Standing, 0)
		}
	}
	add(grants.CloseIssue, "fixture", now+3600, 0)
	add(grants.Merge, "other", now+3600, now-10)
	db.Close()

	api, bus, st := startDaemonIn(t, dir)
	if _, err := st.Register(store.NewAgent{Container: "lea-a", Name: "dev", Profile: "claude", Workdir: "/p", Role: "dev",
		Repo: "fixture", Branch: "feat/a"}, store.Caps{}, 1); err != nil {
		t.Fatal(err)
	}
	f := allowFixture{t, t.TempDir(), map[string]string{"LEA_DAEMON_SOCK": api}}
	rc, out, _ := f.allow()
	if rc != 0 || strings.Count(out, "\n#") != 2 || !regexp.MustCompile(`#1 +deliver +fixture \(github.com/o/fixture\), web \(github.com/o/web\) +revoked`).MatchString(out) ||
		!regexp.MustCompile(`#2 +issues +fixture \(github.com/o/fixture\) +\d{4}-`).MatchString(out) {
		t.Fatalf("allow after the migration: rc=%d %q", rc, out)
	}
	wt := worktree(t, f.proj, "fixture", "dev", "feat/a")
	for c, want := range map[string]string{"git push -u origin feat/a": "allow", "gh pr merge 3 --squash": "allow", "gh issue close 4": "allow",
		"git push origin main": "deny", "gh pr merge 3 --admin": "deny", "gh pr create --head feat/b --title t --body b": ""} {
		if d, _ := hookOut(t, bus, "dev", cmd(c), wt); d != want {
			t.Errorf("%s: %q, want %q", c, d, want)
		}
	}
}
