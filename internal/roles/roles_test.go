package roles

import (
	"reflect"
	"strings"
	"testing"
)

func TestBuiltinRoles(t *testing.T) {
	if got := Names(); !reflect.DeepEqual(got, []string{"lead", "dev", "reviewer"}) {
		t.Fatalf("names=%q", got)
	}
	l, err := Get("lead")
	if err != nil || !l.CanSpawn || l.MaxChildren != 6 || !reflect.DeepEqual(l.SpawnRoles, []string{"dev", "reviewer"}) {
		t.Fatalf("lead=%+v err=%v", l, err)
	}
	for _, n := range []string{"dev", "reviewer"} {
		if r, err := Get(n); err != nil || r.CanSpawn || len(r.SpawnRoles) != 0 {
			t.Fatalf("%s=%+v err=%v", n, r, err)
		}
	}
	if _, err := Get("admin"); err == nil || !strings.Contains(err.Error(), "lead, dev, reviewer") {
		t.Fatalf("unknown role: err=%v", err)
	}
	if Default != "dev" || MaxAgents != 12 {
		t.Fatalf("default=%s max=%d", Default, MaxAgents)
	}
}

func TestCheckRefusesACascadeAndOddRoleSets(t *testing.T) {
	for i, rs := range [][]Role{
		{{Name: "lead", CanSpawn: true, SpawnRoles: []string{"lead"}}},
		{{Name: "a", CanSpawn: true, SpawnRoles: []string{"b"}}, {Name: "b", CanSpawn: true, SpawnRoles: []string{"dev"}}, {Name: "dev"}},
		{{Name: "a", CanSpawn: true, SpawnRoles: []string{"ghost"}}},
		{{Name: "dev", SpawnRoles: []string{"dev"}}},
		{{Name: "a", CanSpawn: true, MaxChildren: 7}},
		{{Name: "a", MaxChildren: -1}},
		{{Name: "dev"}, {Name: "dev"}},
		{{Name: ""}},
	} {
		if err := Check(rs); err == nil {
			t.Fatalf("set %d accepted: %+v", i, rs)
		}
	}
	if err := Check(builtin); err != nil {
		t.Fatalf("built-in roles: %v", err)
	}
}

func TestCanGive(t *testing.T) {
	lead, _ := Get(Lead)
	dev, _ := Get(Dev)
	for _, c := range []string{"dev", "reviewer"} {
		if err := CanGive(lead, c); err != nil {
			t.Fatalf("lead → %s: %v", c, err)
		}
	}
	if err := CanGive(lead, "lead"); err == nil {
		t.Fatal("a lead gave the lead role")
	}
	if err := CanGive(lead, "ghost"); err == nil {
		t.Fatal("a lead gave an unknown role")
	}
	if err := CanGive(dev, "dev"); err == nil || !strings.Contains(err.Error(), "agents_request_spawn") {
		t.Fatalf("dev → dev: err=%v", err)
	}
}
