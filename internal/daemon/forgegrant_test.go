package daemon

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/grants"
	"github.com/DavidGodefroid/lea/internal/keyproxy"
	"github.com/DavidGodefroid/lea/internal/store"
)

var grantTokRe = regexp.MustCompile(`lea-grant\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)

// The pinned push and merge the grant hook builds carry the daemon's grant token for exactly
// that ref or PR, from the caller's container: what the forge forwarder requires of a tower's
// tree (#110, re-review B2). Without a key, none is minted.
func TestGrantCheckMintsTheForgeGrantTokens(t *testing.T) {
	e := start(t)
	key := bytes.Repeat([]byte{3}, 32)
	tw := spawned(t, e, "tw", "tower", "")
	lead := spawned(t, e, "lead", "lead", tw)
	if _, err := e.d.st.Register(store.NewAgent{Container: "lea-a", Name: "dev1", Profile: "claude", Workdir: "/p", Role: "dev",
		Parent: lead, Repo: "shop", Branch: "feat/73-x"}, store.Caps{}, 1); err != nil {
		t.Fatal(err)
	}
	for _, a := range []string{grants.CommitIdentity, grants.Merge} {
		if _, err := e.d.st.AddGrant(grants.Grant{Container: "lea-a", Action: a, Repo: "shop", GitHub: "github.com/someone/shop",
			Created: 1, Expires: grants.Standing}); err != nil {
			t.Fatal(err)
		}
	}
	e.d.st.SetDeployWindow(lead, "mon-sun 00:00-23:59 UTC")
	check := func(cmd string) map[string]any {
		t.Helper()
		var r map[string]any
		in := map[string]any{"tool": "Bash", "input": map[string]string{"command": cmd}, "cwd": "/p/.lea/worktrees/shop/dev1",
			"env": map[string]string{"HOME": "/home/u"}}
		if err := call(t, e.bus, "dev1", "grant_check", in, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := check("git push origin feat/73-x")
	if r["verdict"] != "allow" || grantTokRe.MatchString(fmt.Sprint(r["command"])) {
		t.Fatalf("without a key: %v", r)
	}
	e.d.SetGrantKey(key)
	r = check("git push origin feat/73-x")
	cmd := fmt.Sprint(r["command"])
	tok := grantTokRe.FindString(cmd)
	if r["verdict"] != "allow" || !strings.Contains(cmd, keyproxy.GrantHeader+": "+tok) {
		t.Fatalf("no grant token: %v", r)
	}
	g, err := keyproxy.VerifyGrant(key, tok, time.Now())
	if err != nil || g.Kind != keyproxy.GrantPush || g.Container != "lea-a" || g.Repo != "someone/shop" || g.Ref != "refs/heads/feat/73-x" {
		t.Fatalf("%+v %v", g, err)
	}
	if tok2 := grantTokRe.FindString(fmt.Sprint(check("git push origin feat/73-x")["command"])); tok2 == tok {
		t.Fatal("two pushes got the same token")
	}
	r = check("gh pr merge 7 --squash")
	tok = grantTokRe.FindString(fmt.Sprint(r["command"]))
	g, err = keyproxy.VerifyGrant(key, tok, time.Now())
	if r["verdict"] != "allow" || err != nil || g.Kind != keyproxy.GrantMerge || g.PR != 7 || g.Repo != "someone/shop" || g.Container != "lea-a" {
		t.Fatalf("merge: %v, %+v %v", r, g, err)
	}
}

// The forge forwarder's tree is the containers of the running towers.
func TestTreeContainers(t *testing.T) {
	got := TreeContainers([]store.Agent{
		{Container: "lea-a", Role: "tower", State: "idle"},
		{Container: "lea-a", Role: "lead", Parent: "lea-a/tw"},
		{Container: "lea-b", Role: "dev"},
		{Container: "lea-c", Role: "tower", State: "done"},
		{Container: "lea-d", Role: "tower", Parent: "lea-x/y"},
	})
	if len(got) != 1 || !got["lea-a"] {
		t.Fatalf("%v", got)
	}
}
