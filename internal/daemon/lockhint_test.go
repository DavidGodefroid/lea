package daemon

import (
	"slices"
	"strings"
	"testing"
)

// The hint for a lock without a socket comes from the lock's state (#343): never lox up, never a
// command that restarts or recreates a lock while its console waits for an answer.
func TestLockHintSelection(t *testing.T) {
	cases := []struct {
		name, loxRun   string
		known, mounted bool
		running        []string
		want           string
	}{
		{"waits for an answer", "serving", true, true, []string{"shop-prod"},
			"lock shop-prod waits for you: answer in lox run (or lox attach shop-prod)"},
		{"no lox run", "off", true, true, []string{"shop-prod"},
			"lock shop-prod has no console: lox run is not running: start lox run in the projects directory, in a terminal of its own outside lea"},
		{"no lox run, lock unknown", "off", false, false, nil,
			"lock shop-prod has no console: lox run is not running: start lox run in the projects directory, in a terminal of its own outside lea"},
		{"lea cannot tell", "", true, true, []string{"shop-prod"},
			"lock shop-prod waits for you: answer in lox run (or lox attach shop-prod); if lox run is not running, start it in the projects directory"},
		{"added while lea runs", "serving", true, false, []string{"shop-prod"},
			"lock shop-prod added: restart lea to use it (lea stop, then lea)"},
		{"not lox run's", "serving", false, false, []string{"orders", "stock"},
			"lox run runs no lock shop-prod for this projects directory (it runs: orders, stock)"},
		{"no lock at all", "serving", false, false, nil,
			"lox run runs no lock shop-prod for this projects directory (lox add sets one up, then lox run)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := lockHint("shop-prod", c.loxRun, c.known, c.mounted, c.running)
			if got != c.want {
				t.Fatalf("hint %q, want %q", got, c.want)
			}
			for _, bad := range []string{"lox up", "lox approve", "restart lox", "recreate", "lox down"} {
				if strings.Contains(got, bad) {
					t.Fatalf("hint %q says %q", got, bad)
				}
			}
		})
	}
}

// lock_hint on the bus: the named lock, or every lock up.json names; agents only.
func TestLockHintCall(t *testing.T) {
	e := start(t)
	f := newLockFamily(t)
	f.write(t, 0o600, "shop", "orders")
	runWatch(t, e.d, f, map[string][]string{"lea-a": {f.project + "_shop-run"}})
	e.d.SetLoxRunWatch(func() string { return "serving" })
	e.d.readLoxRun()

	hints := func(params map[string]any) []string {
		t.Helper()
		var ans struct {
			Hints []string `json:"hints"`
		}
		if err := call(t, e.bus, "dev", "lock_hint", params, &ans); err != nil {
			t.Fatal(err)
		}
		return ans.Hints
	}
	if got := hints(map[string]any{"lock": "shop"}); !slices.Equal(got, []string{lockWaitsHint("shop")}) {
		t.Fatalf("shop: %q", got)
	}
	if got := hints(map[string]any{}); !slices.Equal(got, []string{lockAddedHint("orders"), lockWaitsHint("shop")}) {
		t.Fatalf("every lock: %q", got)
	}

	e.d.SetLoxRunWatch(func() string { return "off" })
	e.d.readLoxRun()
	if got := hints(map[string]any{"lock": "shop"}); len(got) != 1 || !strings.Contains(got[0], "start lox run in the projects directory") {
		t.Fatalf("no lox run: %q", got)
	}
	// The status line hears it too.
	var ans struct {
		LoxRun string `json:"lox_run"`
	}
	if err := call(t, e.bus, "dev", "status_report", map[string]any{"ctx_pct": 1, "cost_usd": 0, "paths": []string{}}, &ans); err != nil {
		t.Fatal(err)
	}
	if ans.LoxRun != "off" {
		t.Fatalf("status_report lox_run %q", ans.LoxRun)
	}

	if err := call(t, e.bus, "dev", "lock_hint", map[string]any{"lock": "../x"}, nil); err == nil {
		t.Fatal("a name that is no instance was accepted")
	}
	if err := call(t, e.api, "", "lock_hint", map[string]any{}, nil); err == nil {
		t.Fatal("lock_hint answered off the bus")
	}
}
