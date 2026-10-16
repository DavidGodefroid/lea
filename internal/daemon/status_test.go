package daemon

import (
	"fmt"
	"strings"
	"testing"

	"github.com/DavidGodefroid/lea/internal/store"
)

func report(t *testing.T, e env, ctx int, cost float64, paths ...string) error {
	t.Helper()
	if paths == nil {
		paths = []string{}
	}
	return call(t, e.bus, "dev", "status_report", map[string]any{"ctx_pct": ctx, "cost_usd": cost, "paths": paths}, nil)
}

func TestStatusReportStoresUsageAndNotifiesOnChange(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	changes := func() int { fa.mu.Lock(); defer fa.mu.Unlock(); return fa.changed }
	if err := report(t, e, 42, 1.204); err != nil {
		t.Fatal(err)
	}
	if u, _ := e.d.st.Usages("lea-a"); u["lea-a/dev"] != (store.Usage{CtxPct: 42, CostUSD: 1.2}) {
		t.Fatalf("usages=%+v", u)
	}
	report(t, e, 42, 1.2)
	if n := changes(); n != 1 {
		t.Fatalf("changed=%d after the same values", n)
	}
	report(t, e, 43, 1.2)
	if n := changes(); n != 2 {
		t.Fatalf("changed=%d after new values", n)
	}
	report(t, e, -1, -1)
	if u, _ := e.d.st.Usages("lea-a"); u["lea-a/dev"] != (store.Usage{CtxPct: -1, CostUSD: -1}) {
		t.Fatalf("unknown again: %+v", u)
	}
}

func TestStatusReportRecordsPathsWhenTheyChange(t *testing.T) {
	e := start(t)
	report(t, e, 1, 0, "cost.total_cost_usd", "model.display_name")
	report(t, e, 2, 0, "cost.total_cost_usd", "model.display_name")
	report(t, e, 3, 0, "context_window.used_percentage")
	report(t, e, 4, 0) // no paths: nothing to record
	ps, err := e.d.st.EventPayloads("lea-a/dev", "paths")
	if err != nil || len(ps) != 2 || !strings.Contains(ps[0], "model.display_name") || !strings.Contains(ps[1], "used_percentage") {
		t.Fatalf("payloads=%q err=%v", ps, err)
	}
}

func TestStatusReportIsForAgentsOnly(t *testing.T) {
	e := start(t)
	err := call(t, e.api, "", "status_report", map[string]any{"ctx_pct": 1, "cost_usd": 1, "paths": []string{}}, nil)
	if err == nil || !strings.Contains(err.Error(), "agents only") {
		t.Fatalf("err=%v", err)
	}
}

func TestStatusReportRefusesOddValues(t *testing.T) {
	e := start(t)
	many := make([]string, 65)
	for i := range many {
		many[i] = fmt.Sprintf("k%d", i)
	}
	for name, err := range map[string]error{
		"ctx 101":   report(t, e, 101, 0),
		"ctx -2":    report(t, e, -2, 0),
		"cost -2":   report(t, e, 1, -2),
		"cost 1e7":  report(t, e, 1, 1e7),
		"bad path":  report(t, e, 1, 0, "a b"),
		"long path": report(t, e, 1, 0, strings.Repeat("a", 129)),
		"65 paths":  report(t, e, 1, 0, many...),
	} {
		if err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestStatusReportCapsRecordedPathEvents(t *testing.T) {
	e := start(t)
	for i := 0; i < maxPathEvents+10; i++ {
		report(t, e, 1, 0, fmt.Sprintf("k%d", i))
	}
	ps, _ := e.d.st.EventPayloads("lea-a/dev", "paths")
	if len(ps) != maxPathEvents {
		t.Fatalf("events=%d", len(ps))
	}
}
