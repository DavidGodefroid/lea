package daemon

import (
	"strings"
	"testing"
)

func TestLeasesKeepAgentsOffEachOthersFiles(t *testing.T) {
	e := start(t)
	spawned(t, e, "a", "dev", "")
	spawned(t, e, "b", "dev", "")
	var l struct {
		ID      int64
		Glob    string
		Expires int64
	}
	if err := call(t, e.bus, "a", "lease_acquire", map[string]any{"glob": "src/*.go", "ttl": 600}, &l); err != nil || l.ID == 0 || l.Expires != 1600 {
		t.Fatalf("lease=%+v err=%v", l, err)
	}
	if err := call(t, e.bus, "b", "lease_acquire", map[string]any{"glob": "src/main.go", "ttl": 60}, nil); err == nil || !strings.Contains(err.Error(), "a holds") {
		t.Fatalf("overlap: err=%v", err)
	}
	if err := call(t, e.bus, "b", "lease_release", map[string]any{"id": l.ID}, nil); err == nil || !strings.Contains(err.Error(), "no lease") {
		t.Fatalf("release of another agent's lease: err=%v", err)
	}
	if err := call(t, e.bus, "a", "lease_release", map[string]any{"id": l.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if err := call(t, e.bus, "b", "lease_acquire", map[string]any{"glob": "src/main.go", "ttl": 60}, nil); err != nil {
		t.Fatal(err)
	}
	var b struct {
		Leases []struct{ Agent, Glob string }
	}
	if err := call(t, e.api, "", "board_list", nil, &b); err != nil || len(b.Leases) != 1 || b.Leases[0].Agent != "b" || b.Leases[0].Glob != "src/main.go" {
		t.Fatalf("board leases=%+v err=%v", b.Leases, err)
	}
}

func TestLeaseRefusals(t *testing.T) {
	e := start(t)
	for i, p := range []map[string]any{
		{"glob": "", "ttl": 60}, {"glob": "/etc/*", "ttl": 60}, {"glob": "a/../b", "ttl": 60}, {"glob": "[", "ttl": 60},
		{"glob": "a\nb", "ttl": 60}, {"glob": strings.Repeat("a", 257), "ttl": 60}, {"glob": "a/**", "ttl": 60}, {"glob": "a\\*", "ttl": 60}, {"glob": strings.Repeat("*a", 7), "ttl": 60}, {"glob": "a", "ttl": 0}, {"glob": "a", "ttl": 3601},
	} {
		if err := call(t, e.bus, "a", "lease_acquire", p, nil); err == nil {
			t.Fatalf("params %d accepted", i)
		}
	}
	if err := call(t, e.api, "", "lease_acquire", map[string]any{"glob": "a", "ttl": 60}, nil); err == nil || !strings.Contains(err.Error(), "agents only") {
		t.Fatalf("from the host: err=%v", err)
	}
}

func TestLeasesAndTaskUpdatesRefuseAMadeUpAgentName(t *testing.T) {
	e := start(t)
	for method, params := range map[string]map[string]any{
		"lease_acquire": {"glob": "a.go", "ttl": 60},
		"lease_release": {"id": 1},
		"task_update":   {"status_text": "hi"},
	} {
		if err := call(t, e.bus, "phantom", method, params, nil); err == nil || !strings.Contains(err.Error(), "only running agents lea started") {
			t.Fatalf("%s as a made-up name: err=%v", method, err)
		}
	}
}
