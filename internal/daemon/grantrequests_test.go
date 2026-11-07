package daemon

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/DavidGodefroid/lea/internal/grants"
	"github.com/DavidGodefroid/lea/internal/store"
)

// TestGrantRequestLifecycle (#342): a lead's request records no grant; the host's approval records
// one scoped to the lead's subtree, with the life the user gives; a decided request is decided
// once; a request whose lead is gone is not approved; one nobody answers expires and its lead is
// told.
func TestGrantRequestLifecycle(t *testing.T) {
	e := start(t)
	reg := func(name, role, parent string) store.Agent {
		a, err := e.d.st.Register(store.NewAgent{Container: "lea-a", Name: name, Profile: "claude", Workdir: "/p", Role: role,
			Parent: parent, Repo: "fixture", Branch: "feat/" + name}, store.Caps{}, 1)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	lead := reg("lead", "lead", "")
	o := Origin{Container: "lea-a"}
	request := func(p map[string]any) (int64, error) {
		raw, _ := json.Marshal(p)
		v, err := e.d.grantsRequest(e.ctx, o, lead.Key, raw)
		if err != nil {
			return 0, err
		}
		return strconv.ParseInt(strings.TrimPrefix(v.(map[string]any)["request"].(string), "g"), 10, 64)
	}
	decide := func(p map[string]any) (any, error) {
		raw, _ := json.Marshal(p)
		return e.d.grantRequestDecide(raw)
	}
	// The request names no lea and no agent of its own: they are the caller's.
	id, err := request(map[string]any{"action": "merge", "repo": "fixture", "github": "o/fixture", "container": "lea-b", "agent": "x"})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := e.d.st.GrantRequestByID(id)
	if r.Container != "lea-a" || r.Requester != lead.Key || r.GitHub != "github.com/o/fixture" || r.TTLS != int64(DefaultGrantTTL.Seconds()) {
		t.Fatalf("request %+v", r)
	}
	if gs, _ := e.d.st.ActiveGrants("", e.clock.Load()); len(gs) != 0 {
		t.Fatalf("a request recorded %+v", gs)
	}
	if _, err := decide(map[string]any{"id": id, "verdict": "always"}); err == nil {
		t.Fatal("verdict always accepted")
	}
	if _, err := decide(map[string]any{"id": id, "verdict": "approve", "ttl_s": 40 * 24 * 3600}); err == nil {
		t.Fatal("a life beyond the cap accepted")
	}
	v, err := decide(map[string]any{"id": id, "verdict": "approve", "ttl_s": 30 * 24 * 3600})
	if err != nil {
		t.Fatal(err)
	}
	g := v.(grants.Grant)
	if g.Agent != lead.Key || g.Container != "lea-a" || g.Action != grants.Merge || g.Repo != "fixture" ||
		g.Expires != e.clock.Load()+30*24*3600 {
		t.Fatalf("grant %+v", g)
	}
	if _, err := decide(map[string]any{"id": id, "verdict": "deny"}); err == nil || !strings.Contains(err.Error(), "approved") {
		t.Fatalf("decided twice: %v", err)
	}
	if r, _ := e.d.st.GrantRequestByID(id); r.State != "approved" || r.Grant != g.ID {
		t.Fatalf("request after approval %+v", r)
	}

	// A lead that ended takes its request with it.
	id, err = request(map[string]any{"action": "close-issue", "repo": "fixture", "github": "o/fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.d.st.SetState(lead.Key, "done", "", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := decide(map[string]any{"id": id, "verdict": "approve"}); err == nil {
		t.Fatal("approved for a lead that is gone")
	}
	if r, _ := e.d.st.GrantRequestByID(id); r.State != "withdrawn" {
		t.Fatalf("request of a gone lead: %+v", r)
	}
	if err := e.d.st.SetState(lead.Key, "idle", "", 3); err != nil {
		t.Fatal(err)
	}

	// Expiry: a day without an answer.
	id, err = request(map[string]any{"action": "release-tag", "repo": "fixture", "github": "o/fixture"})
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Add(grantRequestTTL)
	if rs, err := e.d.grantRequestList(); err != nil || len(rs) != 0 {
		t.Fatalf("list after expiry: %v %+v", err, rs)
	}
	if r, _ := e.d.st.GrantRequestByID(id); r.State != "expired" {
		t.Fatalf("request after a day: %+v", r)
	}
	ms, err := e.d.st.Unread(lead.Key, 20)
	if err != nil {
		t.Fatal(err)
	}
	var all []string
	for _, m := range ms {
		all = append(all, m.Body)
	}
	if s := strings.Join(all, "\n"); !strings.Contains(s, "approved by the user") || !strings.Contains(s, "expired") {
		t.Fatalf("lead's messages: %s", s)
	}
}

// TestGrantRequestLimits: a lead's open requests are capped, and a non-lead files none.
func TestGrantRequestLimits(t *testing.T) {
	e := start(t)
	lead, err := e.d.st.Register(store.NewAgent{Container: "lea-a", Name: "lead", Profile: "claude", Workdir: "/p", Role: "lead"}, store.Caps{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	dev, err := e.d.st.Register(store.NewAgent{Container: "lea-a", Name: "dev", Profile: "claude", Workdir: "/p", Role: "dev", Parent: lead.Key}, store.Caps{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	o := Origin{Container: "lea-a"}
	raw := func(repo string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"action": "merge", "repo": repo, "github": "o/" + repo})
		return b
	}
	if _, err := e.d.grantsRequest(e.ctx, o, dev.Key, raw("r")); err == nil || !strings.Contains(err.Error(), "leads only") {
		t.Fatalf("dev: %v", err)
	}
	if _, err := e.d.grantsRequest(e.ctx, Origin{}, "user", raw("r")); err == nil {
		t.Fatal("host-origin request accepted")
	}
	for i := 0; i < maxPendingGrantRequests; i++ {
		if _, err := e.d.grantsRequest(e.ctx, o, lead.Key, raw("r"+string(rune('a'+i)))); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if _, err := e.d.grantsRequest(e.ctx, o, lead.Key, raw("over")); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("over the cap: %v", err)
	}
}
