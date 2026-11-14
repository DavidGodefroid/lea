package limits

import "testing"

// The spend budgets take an amount in USD with at most two decimals, from 0 to 1e6; the step a
// whole percent from 1 to 100.
func TestBudgetKeysCheck(t *testing.T) {
	d, err := Lookup("budget_usd_agent")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"0", "10", "12.5", "12.40", "1000000"} {
		if err := d.Check(v); err != nil {
			t.Errorf("%q: %v", v, err)
		}
	}
	for _, v := range []string{"", "-1", "1e3", "10.", ".5", "1.234", "010", "1000000.01", "ten", "1,5", " 1"} {
		if err := d.Check(v); err == nil {
			t.Errorf("%q passed", v)
		}
	}
	if got := d.USD("12.40"); got != 12.4 {
		t.Fatalf("USD=%v", got)
	}
	if got := d.USD("lots"); got != 0 {
		t.Fatalf("USD of a refused value=%v", got)
	}
	s, _ := Lookup("LEA_BUDGET_USD_STEP")
	if s.Default != "20" || s.Check("0") == nil || s.Check("101") == nil || s.Check("50") != nil {
		t.Fatalf("step %+v", s)
	}
	if tot, _ := Lookup("budget_usd_total"); tot.Default != "0" || tot.Env != "LEA_BUDGET_USD_TOTAL" {
		t.Fatalf("total %+v", tot)
	}
}
