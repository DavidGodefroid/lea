package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/store"
)

// TestGrantRequestsShowTheGroupedAllow is #412's e2e: a lead files pr-create, release-create and
// close-issue requests on one repository; the user's message and lea requests show the one-off
// route and one grouped lea allow command covering them all, with what it leaves out; running
// that command as printed lets the lead's child call through, with no lea approve.
func TestGrantRequestsShowTheGroupedAllow(t *testing.T) {
	api, bus, st := startDaemonWithStore(t)
	lead, err := st.Register(store.NewAgent{Container: "lea-a", Name: "lead", Profile: "claude", Workdir: "/p", Role: "lead", Repo: "fixture"}, store.Caps{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Register(store.NewAgent{Container: "lea-a", Name: "dev", Profile: "claude", Workdir: "/p", Role: "dev",
		Parent: lead.Key, Repo: "fixture", Branch: "feat/a"}, store.Caps{}, 1); err != nil {
		t.Fatal(err)
	}
	f := allowFixture{t, t.TempDir(), map[string]string{"LEA_DAEMON_SOCK": api}}
	f.origin("fixture", "https://github.com/o/fixture.git")
	calls := []string{"gh pr create --head feat/a --title t --body b", "gh release create v0.15.0 --verify-tag --generate-notes", "gh issue close 4"}
	for _, c := range calls {
		if d := preToolUseAs(t, bus, "dev", "Bash", cmd(c)); d == "allow" {
			t.Fatalf("%q before any grant", c)
		}
	}
	var res json.RawMessage
	for _, ask := range []map[string]any{
		{"action": "pr-create", "repo": "fixture", "github": "o/fixture"},
		{"action": "release-create", "repo": "fixture", "github": "o/fixture", "tag": "v0.15.*"},
		{"action": "close-issue", "repo": "fixture", "github": "o/fixture"},
	} {
		if res, err = busCall(t, bus, "lead", "grants_request", ask); err != nil {
			t.Fatalf("%v: %v", ask, err)
		}
	}
	allow := "lea allow pr issues release --repo fixture --tag 'v0.15.*'"
	routes := []string{"one-off: lea approve g1 g2 g3 (or lea deny gN [reason]); lea requests says what each asks",
		"durable, for every agent of this lea until lea allow off N (add --for 8h to limit it): " + allow,
		"it covers only pr, issues, release on fixture, not push, merge, delete-branch, release-tag, nor other repositories"}
	// The lead is told to relay both routes, as is; the user's message has them.
	var note struct{ Note string }
	if err := json.Unmarshal(res, &note); err != nil || !strings.HasSuffix(note.Note, strings.Join(routes, " | ")) {
		t.Fatalf("tool note %s", res)
	}
	if rc, out, _ := runEnv(t, f.host, "inbox"); rc != 0 || !strings.Contains(out, allow) || !strings.Contains(out, "lea approve g1 g2 g3") {
		t.Fatalf("user inbox: %q", out)
	}
	rc, out, e := runEnv(t, f.host, "requests")
	if rc != 0 || !strings.HasSuffix(out, strings.Join(routes, "\n")+"\n") {
		t.Fatalf("requests rc=%d out=%q err=%q", rc, out, e)
	}
	// The command as printed, without its quotes.
	args := strings.Fields(strings.ReplaceAll(strings.TrimPrefix(allow, "lea allow "), "'", ""))
	if rc, out, e := f.allow(append(args, "--yes", f.digest(args...))...); rc != 0 {
		t.Fatalf("allow rc=%d out=%q err=%q", rc, out, e)
	}
	for _, c := range calls {
		if d := preToolUseAs(t, bus, "dev", "Bash", cmd(c)); d != "allow" {
			t.Fatalf("%q after lea allow: %q", c, d)
		}
	}
	if gs, err := st.ActiveGrants("lea-a", time.Now().Unix()); err != nil || len(gs) != 3 {
		t.Fatalf("grants %d %v, want the three lea allow rows", len(gs), err)
	}
}
