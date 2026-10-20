package daemon

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/DavidGodefroid/lea/internal/store"
)

type requestResult struct {
	Request int64  `json:"request"`
	SentTo  string `json:"sent_to"`
}

func requestFrom(t *testing.T, e env, agent string, p map[string]any) (requestResult, error) {
	t.Helper()
	var r requestResult
	err := call(t, e.bus, agent, "agents_request_spawn", p, &r)
	return r, err
}

// inboxOf reads an agent's inbox through the bus, as its MCP server does.
func inboxOf(t *testing.T, e env, agent string) []delivered {
	t.Helper()
	var ms []delivered
	if err := call(t, e.bus, agent, "msg_inbox", nil, &ms); err != nil {
		t.Fatal(err)
	}
	return ms
}

func spawnedAt(t *testing.T, e env, name, role string, at int64) string {
	t.Helper()
	a, err := e.d.st.Register(store.NewAgent{Container: "lea-a", Name: name, Profile: "claude", Workdir: "/p", Role: role}, store.Caps{}, at)
	if err != nil {
		t.Fatal(err)
	}
	return a.Key
}

func TestRequestSpawnGoesToTheParentLead(t *testing.T) {
	e := start(t)
	boss := spawned(t, e, "boss", "lead", "")
	spawned(t, e, "kid", "dev", boss)
	r, err := requestFrom(t, e, "kid", map[string]any{"role": "reviewer", "repo": "shop", "brief": "review the cart\nline two", "reason": "need eyes"})
	if err != nil || r.Request == 0 || r.SentTo != "boss" {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	ms := inboxOf(t, e, "boss")
	if len(ms) != 1 || ms[0].Kind != "spawn_request" || ms[0].From != "lea-a/kid" {
		t.Fatalf("boss inbox=%+v", ms)
	}
	for _, want := range []string{"Spawn request #1 from kid: a reviewer agent (profile the default, repo shop).", "| Reason: need eyes",
		"| line two", `for="#1"`, "untrusted content"} {
		if !strings.Contains(ms[0].Text, want) {
			t.Fatalf("missing %q in:\n%s", want, ms[0].Text)
		}
	}
}

func TestRequestSpawnRoutesToTheOldestLeadThenTheUser(t *testing.T) {
	e := start(t)
	spawned(t, e, "solo", "dev", "")
	if r, err := requestFrom(t, e, "solo", map[string]any{"brief": "a"}); err != nil || r.SentTo != "user" {
		t.Fatalf("no lead: result=%+v err=%v", r, err)
	}
	if ms, _ := e.d.st.Unread("user", 8); len(ms) != 1 || !strings.Contains(ms[0].Body, "lea request accept 1") {
		t.Fatalf("user inbox=%+v", ms)
	}
	spawnedAt(t, e, "l2", "lead", 5)
	spawnedAt(t, e, "l1", "lead", 3)
	if r, err := requestFrom(t, e, "solo", map[string]any{"brief": "b"}); err != nil || r.SentTo != "l1" {
		t.Fatalf("two leads: result=%+v err=%v", r, err)
	}
}

func TestRequestSpawnRefusals(t *testing.T) {
	e := start(t)
	boss := spawned(t, e, "boss", "lead", "")
	spawned(t, e, "kid", "dev", boss)
	for _, tc := range []struct {
		agent string
		p     map[string]any
		want  string
	}{
		{"boss", map[string]any{"brief": "x"}, "start the agent yourself"},
		{"kid", map[string]any{"role": "lead", "brief": "x"}, "cannot be requested"},
		{"kid", map[string]any{"role": "admin", "brief": "x"}, "unknown role"},
		{"kid", map[string]any{"profile": "gemini", "brief": "x"}, "unknown provider"},
		{"kid", map[string]any{"model": "--yolo", "brief": "x"}, "invalid model"},
		{"kid", map[string]any{"repo": "..", "brief": "x"}, "invalid repo"},
		{"kid", map[string]any{"branch": "a..b", "brief": "x"}, "invalid branch"},
		{"kid", map[string]any{"brief": ""}, "brief is empty"},
		{"kid", map[string]any{"brief": "x", "reason": strings.Repeat("r", 2<<10+1)}, "reason is"},
		{"kid", map[string]any{"brief": "x", "reason": "a\x1bb"}, "control character"},
		{"ghost", map[string]any{"brief": "x"}, "only running agents"},
	} {
		if _, err := requestFrom(t, e, tc.agent, tc.p); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s %v: err=%v, want %q", tc.agent, tc.p, err, tc.want)
		}
	}
	if err := call(t, e.api, "", "agents_request_spawn", map[string]any{"brief": "x"}, nil); err == nil || !strings.Contains(err.Error(), "agents only") {
		t.Fatalf("from the host: err=%v", err)
	}
	if n, _ := e.d.st.UnreadCount(boss); n != 0 {
		t.Fatalf("refused requests reached the lead (%d)", n)
	}
}

func TestAtMostTwoPendingRequests(t *testing.T) {
	e := start(t)
	boss := spawned(t, e, "boss", "lead", "")
	spawned(t, e, "kid", "dev", boss)
	for _, b := range []string{"one", "two"} {
		if _, err := requestFrom(t, e, "kid", map[string]any{"brief": b}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := requestFrom(t, e, "kid", map[string]any{"brief": "three"}); err == nil || !strings.Contains(err.Error(), "at most 2") {
		t.Fatalf("third pending: err=%v", err)
	}
	*e.clock += 3601 // nobody answered for an hour: both expire
	if _, err := requestFrom(t, e, "kid", map[string]any{"brief": "three"}); err != nil {
		t.Fatalf("after expiry: %v", err)
	}
}

func TestADeclinedBriefCannotBeSentAgain(t *testing.T) {
	e := start(t)
	boss := spawned(t, e, "boss", "lead", "")
	spawned(t, e, "kid", "dev", boss)
	r, _ := requestFrom(t, e, "kid", map[string]any{"brief": "review the cart"})
	ms := inboxOf(t, e, "boss")
	if err := call(t, e.bus, "boss", "msg_reply", map[string]any{"id": ms[0].ID, "status": "fail", "body": "not now"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := inboxOf(t, e, "kid"); len(got) != 1 || !strings.Contains(got[0].Text, "[status: fail] not now") {
		t.Fatalf("kid inbox=%+v", got)
	}
	if g, _ := e.d.st.SpawnRequestByID(r.Request); g.State != "declined" {
		t.Fatalf("request=%+v", g)
	}
	for _, b := range []string{"review the cart", "  review the cart\n"} {
		if _, err := requestFrom(t, e, "kid", map[string]any{"brief": b}); err == nil || !strings.Contains(err.Error(), "was declined") {
			t.Fatalf("%q sent again: err=%v", b, err)
		}
	}
	*e.clock += 61 // the refused calls used the rate window
	if _, err := requestFrom(t, e, "kid", map[string]any{"brief": "review the cart and the tests"}); err != nil {
		t.Fatalf("a changed brief: %v", err)
	}
}

func TestAcceptingARequestSpawnsAChildAndSharesTheThread(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	boss := spawned(t, e, "boss", "lead", "")
	spawned(t, e, "kid", "dev", boss)
	r, _ := requestFrom(t, e, "kid", map[string]any{"role": "reviewer", "brief": "review the cart"})
	req, _ := e.d.st.SpawnRequestByID(r.Request)
	if err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"for": "kid", "role": "reviewer", "brief": "review the cart diff"}, nil); err != nil {
		t.Fatal(err)
	}
	if fa.req.Parent != boss || fa.req.BriefReplyTo != req.Message || fa.req.Brief != "review the cart diff" || !fa.req.Hidden {
		t.Fatalf("request=%+v", fa.req)
	}
	if g, _ := e.d.st.SpawnRequestByID(r.Request); g.State != "accepted" || g.Agent != "lea-a/claude-1" {
		t.Fatalf("spawn request=%+v", g)
	}
	ms := inboxOf(t, e, "kid")
	if len(ms) != 1 || !strings.Contains(ms[0].Text, "[status: ok] spawn request #1 accepted: claude-1 started") || ms[0].Thread != req.Message {
		t.Fatalf("kid inbox=%+v", ms)
	}
	if note, _ := e.d.st.Unread("lea-a/claude-1", 8); len(note) != 1 || note[0].Thread != req.Message || !strings.Contains(note[0].Body, "kid's spawn request #1") {
		t.Fatalf("new agent's note=%+v", note)
	}
	for _, f := range []string{"kid", "#1"} {
		if err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"for": f, "brief": "x"}, nil); err == nil || !strings.Contains(err.Error(), "no pending spawn request") {
			t.Fatalf("accepted twice with for=%s: err=%v", f, err)
		}
	}
}

func TestAFailedSpawnLeavesTheRequestPending(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{newErr: errors.New("boom")}
	e.d.SetAgents(fa)
	boss := spawned(t, e, "boss", "lead", "")
	spawned(t, e, "kid", "dev", boss)
	r, _ := requestFrom(t, e, "kid", map[string]any{"brief": "x"})
	if err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"for": "#1", "brief": "x"}, nil); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err=%v", err)
	}
	if g, _ := e.d.st.SpawnRequestByID(r.Request); g.State != "pending" {
		t.Fatalf("request=%+v", g)
	}
}

func TestOnlyTheAddresseeAnswersARequest(t *testing.T) {
	e := start(t)
	e.d.SetAgents(&fakeAgents{})
	boss := spawned(t, e, "boss", "lead", "")
	spawned(t, e, "boss2", "lead", "")
	spawned(t, e, "kid", "dev", boss)
	r, _ := requestFrom(t, e, "kid", map[string]any{"brief": "x"})
	req, _ := e.d.st.SpawnRequestByID(r.Request)
	if err := call(t, e.bus, "boss2", "agents_spawn", map[string]any{"for": "#1", "brief": "x"}, nil); err == nil || !strings.Contains(err.Error(), "for you") {
		t.Fatalf("another lead accepted: err=%v", err)
	}
	if err := call(t, e.bus, "boss2", "msg_reply", map[string]any{"id": req.Message, "status": "fail", "body": "no"}, nil); err == nil {
		t.Fatal("another lead declined")
	}
	if g, _ := e.d.st.SpawnRequestByID(r.Request); g.State != "pending" {
		t.Fatalf("request=%+v", g)
	}
}

func TestTheUserAcceptsAndDeclinesRequests(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	soloKey := spawned(t, e, "solo", "dev", "")
	r1, _ := requestFrom(t, e, "solo", map[string]any{"role": "reviewer", "brief": "first"})
	r2, _ := requestFrom(t, e, "solo", map[string]any{"brief": "second"})
	if err := call(t, e.bus, "solo", "request_list", nil, nil); err == nil || !strings.Contains(err.Error(), "host only") {
		t.Fatalf("request_list from an agent: err=%v", err)
	}
	var list []struct {
		ID          int64
		From, Brief string
		Size        int
	}
	if err := call(t, e.api, "", "request_list", nil, &list); err != nil || len(list) != 2 || list[0].From != "solo" || list[0].Brief != "first" || list[0].Size != len("first") {
		t.Fatalf("list=%+v err=%v", list, err)
	}
	if err := call(t, e.api, "", "request_decide", map[string]any{"id": r1.Request, "verdict": "accept"}, nil); err != nil {
		t.Fatal(err)
	}
	if fa.req.BriefFrom != soloKey || fa.req.Parent != "" || fa.req.Role != "reviewer" || fa.req.Brief != "first" || fa.req.Hidden {
		t.Fatalf("request=%+v", fa.req)
	}
	if err := call(t, e.api, "", "request_decide", map[string]any{"id": r2.Request, "verdict": "decline", "reason": "later"}, nil); err != nil {
		t.Fatal(err)
	}
	ms := inboxOf(t, e, "solo")
	if len(ms) != 2 || !strings.Contains(ms[0].Text, "accepted: claude-1 started") || !strings.Contains(ms[1].Text, "[status: fail] later") {
		t.Fatalf("solo inbox=%+v", ms)
	}
	if err := call(t, e.api, "", "request_decide", map[string]any{"id": r2.Request, "verdict": "decline"}, nil); err == nil || !strings.Contains(err.Error(), "already decided") {
		t.Fatalf("declined twice: err=%v", err)
	}
	if err := call(t, e.api, "", "request_decide", map[string]any{"id": r1.Request, "verdict": "maybe"}, nil); err == nil {
		t.Fatal("verdict maybe accepted")
	}
}

func TestOverlongNamesAreRefusedUnechoed(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	boss := spawned(t, e, "boss", "lead", "")
	spawned(t, e, "kid", "dev", boss)
	for _, tc := range []struct {
		field string
		n     int
		want  string
	}{
		{"role", 33, "role is 33 bytes, limit 32"},
		{"profile", 33, "profile is 33 bytes, limit 32"},
		{"repo", 101, "repo is 101 bytes, limit 100"},
		{"branch", 201, "branch is 201 bytes, limit 200"},
	} {
		long := strings.Repeat("z", tc.n)
		for _, m := range []struct{ agent, method string }{{"boss", "agents_spawn"}, {"kid", "agents_request_spawn"}} {
			err := call(t, e.bus, m.agent, m.method, map[string]any{tc.field: long, "brief": "x"}, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), long) {
				t.Fatalf("%s %s: err=%v", m.method, tc.field, err)
			}
		}
	}
	for _, f := range []string{strings.Repeat("#", 200), strings.Repeat("k", 200)} {
		err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"for": f, "brief": "x"}, nil)
		if err == nil || !strings.Contains(err.Error(), "for is 200 bytes") || strings.Contains(err.Error(), f) {
			t.Fatalf("long for: err=%v", err)
		}
	}
	if err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"for": "a b\x1b", "brief": "x"}, nil); err == nil ||
		!strings.Contains(err.Error(), "the requester's name") || strings.Contains(err.Error(), "a b") {
		t.Fatalf("invalid for: err=%v", err)
	}
	fa.mu.Lock()
	defer fa.mu.Unlock()
	for _, c := range fa.calls {
		if c == "new" {
			t.Fatal("a refused spawn reached the manager")
		}
	}
}

func TestPruneForgottenDropsRateWindows(t *testing.T) {
	e := start(t)
	boss := spawned(t, e, "boss", "lead", "")
	for _, k := range []string{boss, "lea-a/gone"} {
		if err := e.d.rates.allow("agents_spawn", k, 1); err != nil {
			t.Fatal(err)
		}
		if err := e.d.rates.allow("agents_request_spawn", k, 1); err != nil {
			t.Fatal(err)
		}
	}
	e.d.pruneForgotten()
	e.d.rates.mu.Lock()
	defer e.d.rates.mu.Unlock()
	if len(e.d.rates.hits) != 2 || e.d.rates.hits["agents_spawn "+boss] == nil || e.d.rates.hits["agents_request_spawn "+boss] == nil {
		t.Fatalf("hits=%v", e.d.rates.hits)
	}
}

func newCalled(fa *fakeAgents) bool {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	for _, c := range fa.calls {
		if c == "new" {
			return true
		}
	}
	return false
}

func TestARequestPastItsTTLIsNeitherAcceptedNorListed(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	boss := spawned(t, e, "boss", "lead", "")
	spawned(t, e, "kid", "dev", boss)
	spawned(t, e, "solo", "dev", "")
	toBoss, _ := requestFrom(t, e, "kid", map[string]any{"brief": "x"})
	// A request to the user, as from a container without a lead.
	r, err := e.d.st.AddSpawnRequest(store.SpawnRequest{Container: "lea-a", Requester: "lea-a/solo", Lead: "user", Role: "dev",
		Profile: "claude", Brief: "z"}, func(int64) string { return "r" }, 2, requestTTL, *e.clock)
	if err != nil {
		t.Fatal(err)
	}
	*e.clock += requestTTL
	for _, f := range []string{"#" + strconv.FormatInt(toBoss.Request, 10), "kid"} {
		if err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"for": f, "brief": "x"}, nil); err == nil || !strings.Contains(err.Error(), "no pending spawn request") {
			t.Fatalf("expired request accepted with for=%s: err=%v", f, err)
		}
	}
	var list []struct{ ID int64 }
	if err := call(t, e.api, "", "request_list", nil, &list); err != nil || len(list) != 0 {
		t.Fatalf("expired request listed: %+v err=%v", list, err)
	}
	if err := call(t, e.api, "", "request_decide", map[string]any{"id": r.ID, "verdict": "accept"}, nil); err == nil || !strings.Contains(err.Error(), "already decided") {
		t.Fatalf("expired request accepted by the user: err=%v", err)
	}
	if newCalled(fa) {
		t.Fatal("an expired request started an agent")
	}
	for _, id := range []int64{toBoss.Request, r.ID} {
		if g, _ := e.d.st.SpawnRequestByID(id); g.State != "expired" {
			t.Fatalf("request=%+v", g)
		}
	}
}

func TestARequestDiesWithItsRequester(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	boss := spawned(t, e, "boss", "lead", "")
	kid := spawned(t, e, "kid", "dev", boss)
	solo := spawned(t, e, "solo", "dev", "")
	toBoss, _ := requestFrom(t, e, "kid", map[string]any{"brief": "x"})
	toUser, err := e.d.st.AddSpawnRequest(store.SpawnRequest{Container: "lea-a", Requester: solo, Lead: "user", Role: "dev",
		Profile: "claude", Brief: "z"}, func(int64) string { return "r" }, 2, requestTTL, *e.clock)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{kid, solo} {
		if err := e.d.st.SetState(k, "done", "", *e.clock); err != nil {
			t.Fatal(err)
		}
	}
	if err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"for": "#" + strconv.FormatInt(toBoss.Request, 10), "brief": "x"}, nil); err == nil || !strings.Contains(err.Error(), "no pending spawn request") {
		t.Fatalf("an ended requester's request accepted: err=%v", err)
	}
	var list []struct{ ID int64 }
	if err := call(t, e.api, "", "request_list", nil, &list); err != nil || len(list) != 0 {
		t.Fatalf("an ended requester's request listed: %+v err=%v", list, err)
	}
	if err := call(t, e.api, "", "request_decide", map[string]any{"id": toUser.ID, "verdict": "accept"}, nil); err == nil {
		t.Fatal("an ended requester's request accepted by the user")
	}
	if newCalled(fa) {
		t.Fatal("an ended requester's request started an agent")
	}
	for _, id := range []int64{toBoss.Request, toUser.ID} {
		if g, _ := e.d.st.SpawnRequestByID(id); g.State != "withdrawn" {
			t.Fatalf("request=%+v", g)
		}
	}
}

func TestReplyOkToASpawnRequestIsRefused(t *testing.T) {
	e := start(t)
	boss := spawned(t, e, "boss", "lead", "")
	spawned(t, e, "kid", "dev", boss)
	r, _ := requestFrom(t, e, "kid", map[string]any{"brief": "x"})
	req, _ := e.d.st.SpawnRequestByID(r.Request)
	if err := call(t, e.bus, "boss", "msg_reply", map[string]any{"id": req.Message, "status": "ok", "body": "sure"}, nil); err == nil || !strings.Contains(err.Error(), "agents_spawn") {
		t.Fatalf("ok reply: err=%v", err)
	}
	if ms := inboxOf(t, e, "kid"); len(ms) != 0 {
		t.Fatalf("kid heard %+v", ms)
	}
	if g, _ := e.d.st.SpawnRequestByID(r.Request); g.State != "pending" {
		t.Fatalf("request=%+v", g)
	}
}
