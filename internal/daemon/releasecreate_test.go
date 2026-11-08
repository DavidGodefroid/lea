package daemon

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/DavidGodefroid/lea/internal/grants"
	"github.com/DavidGodefroid/lea/internal/store"
)

// TestCheckGrantReleaseCreate (#353): a release-create grant names its repository and GitHub
// repository, and an optional tag glob; no other action takes a tag; delivery never records it.
func TestCheckGrantReleaseCreate(t *testing.T) {
	ok := func(p grantParams) {
		t.Helper()
		if _, err := checkGrant(&p); err != nil {
			t.Fatalf("%+v refused: %v", p, err)
		}
	}
	bad := func(p grantParams, want string) {
		t.Helper()
		if _, err := checkGrant(&p); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%+v: %v, want %q", p, err, want)
		}
	}
	base := grantParams{Container: "lea-a", Action: grants.ReleaseCreate, Repo: "lockkit", GitHub: "o/lockkit"}
	ok(base)
	p := base
	p.Tag = "v0.15.*"
	ok(p)
	p = base
	p.GitHub = ""
	bad(p, "github")
	p = base
	p.Repo = ""
	bad(p, "repo")
	for _, tag := range []string{"0.15.*", "*", "v0.15.?", "v../x"} {
		p = base
		p.Tag = tag
		bad(p, "tag")
	}
	p = base
	p.Action, p.Tag = grants.ReleaseTag, "v1.*"
	bad(p, "tag")
	if slices.Contains(grants.DeliveryActions, grants.ReleaseCreate) {
		t.Fatal("lea delivery on records release-create")
	}
}

// TestGrantRequestReleaseCreate: a lead's release-create request keeps its tag glob, and its
// approval records a grant with that glob for the lead's subtree.
func TestGrantRequestReleaseCreate(t *testing.T) {
	e := start(t)
	lead, err := e.d.st.Register(store.NewAgent{Container: "lea-a", Name: "lead", Profile: "claude", Workdir: "/p", Role: "lead",
		Repo: "fixture", Branch: "feat/lead"}, store.Caps{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"action": "release-create", "repo": "fixture", "github": "o/fixture", "tag": "v0.15.*"})
	v, err := e.d.grantsRequest(e.ctx, Origin{Container: "lea-a"}, lead.Key, raw)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := strconv.ParseInt(strings.TrimPrefix(v.(map[string]any)["request"].(string), "g"), 10, 64)
	if r, _ := e.d.st.GrantRequestByID(id); r.Tag != "v0.15.*" || !strings.Contains(describeRequest(r), "tag v0.15.*") {
		t.Fatalf("request %+v", r)
	}
	if rs, _ := e.d.grantRequestList(); len(rs) != 1 || rs[0].Tag != "v0.15.*" {
		t.Fatalf("list %+v", rs)
	}
	raw, _ = json.Marshal(map[string]any{"id": id, "verdict": "approve"})
	g, err := e.d.grantRequestDecide(raw)
	if err != nil {
		t.Fatal(err)
	}
	if g := g.(grants.Grant); g.Action != grants.ReleaseCreate || g.Tag != "v0.15.*" || g.Agent != lead.Key || g.GitHub != "github.com/o/fixture" {
		t.Fatalf("grant %+v", g)
	}
	gs, _ := e.d.st.ActiveGrants("lea-a", e.clock.Load())
	if len(gs) != 1 || gs[0].Tag != "v0.15.*" {
		t.Fatalf("stored %+v", gs)
	}
}
