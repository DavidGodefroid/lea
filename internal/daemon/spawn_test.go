package daemon

import (
	"reflect"
	"strings"
	"testing"

	"github.com/DavidGodefroid/lea/internal/agents"
)

func TestAgentsSpawnIsForRunningLeadsOnly(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	spawned(t, e, "dev1", "dev", "")
	old := spawned(t, e, "old", "lead", "")
	e.d.st.SetState(old, "done", "", 2)
	brief := map[string]any{"brief": "fix the cart"}
	for _, tc := range []struct{ sock, agent, want string }{
		{e.bus, "dev1", "agents_request_spawn"},
		{e.bus, "ghost", "only running agents lea started"},
		{e.bus, "old", "only running agents lea started"},
		{e.api, "", "agents only"},
	} {
		if err := call(t, tc.sock, tc.agent, "agents_spawn", brief, nil); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%q: err=%v, want %q", tc.agent, err, tc.want)
		}
	}
	if len(fa.calls) != 0 {
		t.Fatalf("the manager was called: %q", fa.calls)
	}
}

func TestAgentsSpawnPassesThePolicyToTheManager(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	boss := spawned(t, e, "boss", "lead", "")
	var r struct{ Name, Key string }
	if err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"role": "reviewer", "repo": "shop", "branch": "feat/x",
		"brief": "review the cart"}, &r); err != nil {
		t.Fatal(err)
	}
	want := agents.NewRequest{Container: "lea-a", Role: "reviewer", Parent: boss, Repo: "shop", Branch: "feat/x",
		Brief: "review the cart", BriefFrom: boss, NamePrefix: "reviewer", Hidden: true}
	if !reflect.DeepEqual(fa.req, want) || r.Name != "claude-1" {
		t.Fatalf("request=%+v result=%+v", fa.req, r)
	}
	if err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"brief": "x"}, nil); err != nil {
		t.Fatal(err)
	}
	if fa.req.Role != "dev" || fa.req.NamePrefix != "dev" || !fa.req.Hidden {
		t.Fatalf("request=%+v", fa.req)
	}
}

func TestAgentsSpawnRefusesWhatALeadMayNotGive(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	spawned(t, e, "boss", "lead", "")
	for i, p := range []map[string]any{
		{"role": "lead", "brief": "x"},
		{"role": "admin", "brief": "x"},
		{"brief": ""},
		{"brief": "a\x00b"},
		{"brief": strings.Repeat("a", agents.MaxBrief+1)},
	} {
		if err := call(t, e.bus, "boss", "agents_spawn", p, nil); err == nil {
			t.Fatalf("params %d accepted", i)
		}
	}
	if len(fa.calls) != 0 {
		t.Fatalf("the manager was called: %q", fa.calls)
	}
}

func TestAgentsSpawnIsRateLimited(t *testing.T) {
	e := start(t)
	e.d.SetAgents(&fakeAgents{})
	spawned(t, e, "boss", "lead", "")
	for i := 0; i < 3; i++ {
		if err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"brief": "x"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"brief": "x"}, nil); err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("fourth spawn in a minute: err=%v", err)
	}
	*e.clock += 61
	if err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"brief": "x"}, nil); err != nil {
		t.Fatalf("a minute later: %v", err)
	}
}

func TestAgentsStopStopsOnlyYourOwnChildren(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	boss := spawned(t, e, "boss", "lead", "")
	spawned(t, e, "kid", "dev", boss)
	spawned(t, e, "other", "dev", "")
	spawned(t, e, "boss2", "lead", "")
	for _, tc := range []struct{ agent, name, want string }{
		{"boss2", "kid", "not one of your children"},
		{"boss", "other", "not one of your children"},
		{"kid", "other", "only a lead stops"},
		{"boss", "nobody", "no agent named"},
		{"boss", "a\x1b[2Jb", "invalid agent name"},
		{"boss", strings.Repeat("k", 65), "invalid agent name"},
		{"boss", "a/b/c", "invalid agent name"},
	} {
		if err := call(t, e.bus, tc.agent, "agents_stop", map[string]any{"name": tc.name}, nil); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s stops %s: err=%v, want %q", tc.agent, tc.name, err, tc.want)
		}
	}
	if err := call(t, e.bus, "boss", "agents_stop", map[string]any{"name": "kid"}, nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fa.calls, []string{"stop lea-a/kid"}) {
		t.Fatalf("calls=%q", fa.calls)
	}
}

func TestAgentsListShowsRolesTheTreeAndYou(t *testing.T) {
	e := start(t)
	boss := spawned(t, e, "boss", "lead", "")
	spawned(t, e, "kid", "dev", boss)
	var as []struct {
		Key, Name, Role, Parent string
		You                     bool
	}
	if err := call(t, e.bus, "kid", "agents_list", nil, &as); err != nil {
		t.Fatal(err)
	}
	by := map[string]int{}
	for i, a := range as {
		by[a.Name] = i
	}
	k, b := as[by["kid"]], as[by["boss"]]
	if !k.You || b.You || k.Parent != "boss" || k.Role != "dev" || b.Role != "lead" || b.Parent != "" {
		t.Fatalf("agents=%+v", as)
	}
}
