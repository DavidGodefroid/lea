package daemon

import (
	"context"
	"encoding/json"
	"time"

	"github.com/DavidGodefroid/lea/internal/client"
	"github.com/DavidGodefroid/lea/internal/wire"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/DavidGodefroid/lea/internal/agents"
	"github.com/DavidGodefroid/lea/internal/store"
)

// spawned registers an agent lea started in lea-a, as the manager would.
func spawned(t *testing.T, e env, name, role, parent string) string {
	t.Helper()
	a, err := e.d.st.Register(store.NewAgent{Container: "lea-a", Name: name, Profile: "claude", Workdir: "/p", Role: role, Parent: parent}, store.Caps{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	return a.Key
}

func TestAgentNewPassesRoleRepoBranchAndBrief(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	if err := call(t, e.api, "", "agent_new", map[string]any{"container": "lea-a", "role": "reviewer", "repo": "shop",
		"branch": "feat/x", "brief": "review\nthe cart"}, nil); err != nil {
		t.Fatal(err)
	}
	want := agents.NewRequest{Container: "lea-a", Role: "reviewer", Repo: "shop", Branch: "feat/x", Brief: "review\nthe cart", BriefFrom: "user"}
	if !reflect.DeepEqual(fa.req, want) {
		t.Fatalf("request=%+v", fa.req)
	}
	if err := call(t, e.api, "", "agent_new", map[string]any{"container": "lea-a"}, nil); err != nil {
		t.Fatal(err)
	}
	if fa.req.Role != "dev" || fa.req.BriefFrom != "" {
		t.Fatalf("request=%+v", fa.req)
	}
}

func TestAgentNewRefusesUnknownRolesAndBadBriefs(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	for _, p := range []map[string]any{
		{"container": "lea-a", "role": "admin"},
		{"container": "lea-a", "brief": "clear\x1b[2J"},
		{"container": "lea-a", "brief": strings.Repeat("a", agents.MaxBrief+1)},
	} {
		if err := call(t, e.api, "", "agent_new", p, nil); err == nil {
			t.Fatalf("accepted %v", p)
		}
	}
	if len(fa.calls) != 0 {
		t.Fatalf("the manager was called: %q", fa.calls)
	}
}

func TestRoleChangeIsTheUsersCall(t *testing.T) {
	e := start(t)
	boss := spawned(t, e, "boss", "dev", "")
	if err := call(t, e.bus, "boss", "agent_role", map[string]any{"agent": "boss", "role": "lead"}, nil); err == nil ||
		!strings.Contains(err.Error(), "host only") {
		t.Fatalf("an agent changed a role: err=%v", err)
	}
	var r struct{ Key, Role string }
	if err := call(t, e.api, "", "agent_role", map[string]any{"agent": "boss", "role": "lead"}, &r); err != nil || r.Key != boss || r.Role != "lead" {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	if a, _ := e.d.st.AgentByKey(boss); a.Role != "lead" {
		t.Fatalf("role=%q", a.Role)
	}
	ms, _ := e.d.st.Unread(boss, 8)
	if len(ms) != 1 || ms[0].From != "user" || !strings.Contains(ms[0].Body, "your role is now lead") || !strings.Contains(ms[0].Body, "agents_spawn") {
		t.Fatalf("notice=%+v", ms)
	}
	if err := call(t, e.api, "", "agent_role", map[string]any{"agent": "lea-a/boss", "role": "lead"}, nil); err != nil {
		t.Fatal(err)
	}
	if n, _ := e.d.st.UnreadCount(boss); n != 1 {
		t.Fatalf("an unchanged role sent a second notice (%d unread)", n)
	}
	spawned(t, e, "kid", "dev", boss)
	for _, tc := range []struct{ agent, role, want string }{
		{"kid", "lead", "never spawns"},
		{"boss", "dev", "stop them first"},
		{"nobody", "dev", "no agent named"},
		{"boss", "admin", "unknown role"},
		{"user", "dev", "not an agent"},
	} {
		if err := call(t, e.api, "", "agent_role", map[string]any{"agent": tc.agent, "role": tc.role}, nil); err == nil ||
			!strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s → %s: err=%v, want %q", tc.agent, tc.role, err, tc.want)
		}
	}
}

// canSpawn is derived from the role table: a request cannot carry its own.
func TestRoleChangeIgnoresACallerSuppliedCanSpawn(t *testing.T) {
	e := start(t)
	boss := spawned(t, e, "boss", "lead", "")
	kid := spawned(t, e, "kid", "dev", boss)
	if err := call(t, e.api, "", "agent_role", map[string]any{"agent": "kid", "role": "lead", "can_spawn": false, "canSpawn": false}, nil); err == nil {
		t.Fatal("a child was promoted")
	}
	if a, _ := e.d.st.AgentByKey(kid); a.Role != "dev" {
		t.Fatalf("role=%q", a.Role)
	}
	if err := call(t, e.api, "", "agent_role", map[string]any{"agent": "boss", "role": "dev", "can_spawn": true}, nil); err == nil ||
		!strings.Contains(err.Error(), "stop them first") {
		t.Fatalf("a lead with children was demoted: %v", err)
	}
}

// A demotion racing a spawn either wins (the spawn is refused) or loses (the demotion is
// refused): the lead never ends up demoted with a child.
func TestDemotionAndSpawnNeverBothSucceed(t *testing.T) {
	for i := 0; i < 20; i++ {
		e := start(t)
		boss := spawned(t, e, "boss", "lead", "")
		var wg sync.WaitGroup
		spawnC, roleC := make(chan error, 1), make(chan error, 1)
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := e.d.st.Register(store.NewAgent{Container: "lea-a", Name: "kid", Profile: "claude", Workdir: "/p", Role: "dev", Parent: boss},
				store.Caps{MaxChildren: 6}, 2)
			spawnC <- err
		}()
		go func() {
			defer wg.Done()
			p, _ := json.Marshal(map[string]any{"agent": "boss", "role": "dev"})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			roleC <- client.Call(ctx, e.api, wire.Request{Method: "agent_role", Params: p}, nil)
		}()
		wg.Wait()
		spawnErr, roleErr := <-spawnC, <-roleC
		if spawnErr == nil && roleErr == nil {
			t.Fatal("the lead was demoted while it spawned a child")
		}
		a, _ := e.d.st.AgentByKey(boss)
		kids, _ := e.d.st.Children(boss)
		if a.Role == "dev" && len(kids) > 0 {
			t.Fatalf("a dev has %d children", len(kids))
		}
	}
}
