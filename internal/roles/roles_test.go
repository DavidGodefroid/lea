package roles

import (
	"reflect"
	"strings"
	"testing"
)

func TestBuiltinRoles(t *testing.T) {
	if got := Names(); !reflect.DeepEqual(got, []string{"tower", "lead", "dev", "reviewer"}) {
		t.Fatalf("names=%q", got)
	}
	tw, err := Get("tower")
	if err != nil || !tw.CanSpawn || !tw.Root || tw.MaxChildren != 20 || !reflect.DeepEqual(tw.SpawnRoles, []string{"lead"}) {
		t.Fatalf("tower=%+v err=%v", tw, err)
	}
	if !reflect.DeepEqual(RootRoles(), []string{"tower"}) {
		t.Fatalf("root roles=%q", RootRoles())
	}
	l, err := Get("lead")
	if err != nil || !l.CanSpawn || l.Root || l.MaxChildren != 20 || !reflect.DeepEqual(l.SpawnRoles, []string{"dev", "reviewer"}) {
		t.Fatalf("lead=%+v err=%v", l, err)
	}
	for _, n := range []string{"dev", "reviewer"} {
		if r, err := Get(n); err != nil || r.CanSpawn || len(r.SpawnRoles) != 0 {
			t.Fatalf("%s=%+v err=%v", n, r, err)
		}
	}
	if _, err := Get("admin"); err == nil || !strings.Contains(err.Error(), "tower, lead, dev, reviewer") {
		t.Fatalf("unknown role: err=%v", err)
	}
	if Default != "dev" {
		t.Fatalf("default=%s", Default)
	}
}

func TestCheckRefusesACascadeAndOddRoleSets(t *testing.T) {
	for i, rs := range [][]Role{
		{{Name: "lead", CanSpawn: true, SpawnRoles: []string{"lead"}}},
		{{Name: "a", CanSpawn: true, SpawnRoles: []string{"b"}}, {Name: "b", CanSpawn: true, SpawnRoles: []string{"dev"}}, {Name: "dev"}},
		{{Name: "a", CanSpawn: true, SpawnRoles: []string{"ghost"}}},
		{{Name: "dev", SpawnRoles: []string{"dev"}}},
		{{Name: "a", CanSpawn: true, MaxChildren: 21}},
		{{Name: "a", MaxChildren: -1}},
		{{Name: "dev"}, {Name: "dev"}},
		{{Name: ""}},
		// Depth > 2: a root starting a role that starts a role that spawns.
		{{Name: "t", CanSpawn: true, Root: true, SpawnRoles: []string{"l"}}, {Name: "l", CanSpawn: true, SpawnRoles: []string{"m"}},
			{Name: "m", CanSpawn: true, SpawnRoles: []string{"dev"}}, {Name: "dev"}},
		// A root naming itself, or any role naming a root: a root never has a parent.
		{{Name: "t", CanSpawn: true, Root: true, SpawnRoles: []string{"t"}}},
		{{Name: "t", CanSpawn: true, Root: true}, {Name: "l", CanSpawn: true, SpawnRoles: []string{"t"}}},
		{{Name: "t", CanSpawn: true, Root: true}, {Name: "dev", CanSpawn: true, SpawnRoles: []string{"t"}}},
		// Two roots chained.
		{{Name: "a", CanSpawn: true, Root: true, SpawnRoles: []string{"b"}}, {Name: "b", CanSpawn: true, Root: true}},
		// A non-root role starting a lead.
		{{Name: "t", CanSpawn: true, Root: true, SpawnRoles: []string{"l"}}, {Name: "l", CanSpawn: true, SpawnRoles: []string{"dev"}},
			{Name: "x", CanSpawn: true, SpawnRoles: []string{"l"}}, {Name: "dev"}},
		// A lead naming itself under a root.
		{{Name: "t", CanSpawn: true, Root: true, SpawnRoles: []string{"l"}}, {Name: "l", CanSpawn: true, SpawnRoles: []string{"l"}}},
	} {
		if err := Check(rs); err == nil {
			t.Fatalf("set %d accepted: %+v", i, rs)
		}
	}
	if err := Check(builtin); err != nil {
		t.Fatalf("built-in roles: %v", err)
	}
	// Custom sets that stay within two levels.
	for i, rs := range [][]Role{
		{{Name: "t", CanSpawn: true, Root: true, SpawnRoles: []string{"l", "dev"}}, {Name: "l", CanSpawn: true, SpawnRoles: []string{"dev"}}, {Name: "dev"}},
		{{Name: "t", CanSpawn: true, Root: true, SpawnRoles: []string{"a", "b"}}, {Name: "a", CanSpawn: true, SpawnRoles: []string{"dev"}},
			{Name: "b", CanSpawn: true, SpawnRoles: []string{"dev", "rev"}}, {Name: "dev"}, {Name: "rev"}},
		{{Name: "t", Root: true}},
	} {
		if err := Check(rs); err != nil {
			t.Fatalf("set %d refused: %v", i, err)
		}
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
	if err := CanGive(dev, "dev"); err == nil || !strings.Contains(err.Error(), "a dev cannot start agents") || !strings.Contains(err.Error(), "agents_request_spawn") {
		t.Fatalf("dev → dev: err=%v", err)
	}
	tower, _ := Get(Tower)
	if err := CanGive(tower, "lead"); err != nil {
		t.Fatalf("tower → lead: %v", err)
	}
	for _, c := range []string{"dev", "reviewer", "tower"} {
		if err := CanGive(tower, c); err == nil {
			t.Fatalf("a tower gave the %s role", c)
		}
	}
	if err := CanGive(lead, "tower"); err == nil {
		t.Fatal("a lead gave the tower role")
	}
}
