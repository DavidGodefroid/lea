package deck

import (
	"encoding/json"
	"strings"
	"testing"
)

// An agent lea paused on a spend budget (#424) shows why on its row, not plain "paused"; one the
// user paused shows "paused".
func TestABudgetPausedAgentShowsWhy(t *testing.T) {
	a := ag("a", "working", clock.Unix())
	a.Paused, a.Budget = true, "budget reached: $12.40 ≥ budget_usd_agent $10"
	a.Status = "writing tests"
	s := strings.Join(viewLines(sized(loaded(t, &fakeAPI{}, "", a), 80, 8)), "\n")
	if !strings.Contains(s, "budget reached: $12.40") || strings.Contains(s, "writing tests") {
		t.Fatalf("row:\n%s", s)
	}
	b := ag("b", "working", clock.Unix())
	b.Paused = true
	if s := strings.Join(viewLines(sized(loaded(t, &fakeAPI{}, "", b), 80, 8)), "\n"); !strings.Contains(s, "paused") || strings.Contains(s, "budget") {
		t.Fatalf("row:\n%s", s)
	}
}

// The header shows the total spend against budget_usd_total while it is on; nothing when off.
func TestTheHeaderShowsTheSpend(t *testing.T) {
	m := sized(loaded(t, &fakeAPI{}, "", ag("a", "idle", clock.Unix())), 60, 8)
	if top := viewLines(m)[0]; strings.Contains(top, "$") {
		t.Fatalf("off: %q", top)
	}
	var s Snapshot
	if err := json.Unmarshal([]byte(`{"version":2,"agents":[],"spend":{"total_usd":12.4,"limit_usd":50}}`), &s); err != nil || s.Spend == nil {
		t.Fatalf("snapshot: %+v %v", s, err)
	}
	m.spend = s.Spend
	if top := viewLines(m)[0]; !strings.Contains(top, "$12.40/$50") || !strings.Contains(top, "1 agent") {
		t.Fatalf("on: %q", top)
	}
	// On a narrow deck the spend takes the count's place.
	m = sized(m, 30, 8)
	if top := viewLines(m)[0]; !strings.Contains(top, "$12.40/$50") || strings.Contains(top, "agent") {
		t.Fatalf("narrow: %q", top)
	}
	if got := spendText(1234); got != "$1.2k" {
		t.Fatalf("spendText=%q", got)
	}
}
