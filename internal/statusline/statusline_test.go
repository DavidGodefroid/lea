package statusline

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/daemon"
	"github.com/DavidGodefroid/lea/internal/store"
)

func run(t *testing.T, in string) (string, []Report) {
	t.Helper()
	t.Setenv("NO_COLOR", "1")
	var out bytes.Buffer
	var got []Report
	if err := Run(strings.NewReader(in), &out, "", func(r Report) []string { got = append(got, r); return nil }); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out.String()), got
}

func TestRunPrintsAndDumps(t *testing.T) {
	in := `{"model":{"display_name":"Opus"},"workspace":{"current_dir":"/p/shop"},"cost":{"total_cost_usd":1.2}}`
	dump := filepath.Join(t.TempDir(), "sl.json")
	t.Setenv("NO_COLOR", "1")
	var out bytes.Buffer
	if err := Run(strings.NewReader(in), &out, dump, nil); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "Opus" {
		t.Fatalf("out=%q", got)
	}
	b, err := os.ReadFile(dump)
	if err != nil || string(b) != in {
		t.Fatalf("dump=%q err=%v", b, err)
	}
	if fi, _ := os.Stat(dump); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v", fi.Mode())
	}
}

func TestRunReportsContextCostAndPathNames(t *testing.T) {
	in := `{"model":{"display_name":"Opus"},"workspace":{"current_dir":"/p/shop"},"session_id":"secret-session",
	  "cost":{"total_cost_usd":1.234},"context_window":{"used_percentage":41.6,"context_window_size":200000},
	  "weird key":{"x":1},"list":[1,2]}`
	out, got := run(t, in)
	if out != "Opus · ctx ▓▓▓░░░░░ 42%" {
		t.Fatalf("out=%q", out)
	}
	want := Report{CtxPct: 42, CostUSD: 1.23, Paths: []string{"context_window.context_window_size",
		"context_window.used_percentage", "cost.total_cost_usd", "list[]", "model.display_name", "session_id",
		"workspace.current_dir"}}
	if !reflect.DeepEqual(got, []Report{want}) {
		t.Fatalf("reports=%+v", got)
	}
	if strings.Contains(fmt.Sprint(got), "secret-session") || strings.Contains(fmt.Sprint(got), "Opus") {
		t.Fatal("a value left the container")
	}
}

func TestRunComputesContextFromTokens(t *testing.T) {
	_, got := run(t, `{"context_window":{"context_window_size":200000,"current_usage":{"input_tokens":50000,
	  "cache_read_input_tokens":30000,"cache_creation_input_tokens":20000}}}`)
	if len(got) != 1 || got[0].CtxPct != 50 || got[0].CostUSD != -1 {
		t.Fatalf("reports=%+v", got)
	}
}

func TestRunUnknownValuesAreMinusOne(t *testing.T) {
	for _, in := range []string{
		`{"cost":{"total_cost_usd":"1.2"},"context_window":{"used_percentage":250}}`,
		`{"cost":{"total_cost_usd":-3},"context_window":{"used_percentage":"40"}}`,
		`{"cost":7,"context_window":{"context_window_size":0,"current_usage":{"input_tokens":5}}}`,
		`{}`,
	} {
		out, got := run(t, in)
		if out != "lea" || len(got) != 1 || got[0].CtxPct != -1 || got[0].CostUSD != -1 {
			t.Fatalf("%s: out=%q reports=%+v", in, out, got)
		}
	}
}

func TestRunDoesNotReportWhatIsNotAnObject(t *testing.T) {
	for _, in := range []string{"not json", "null", "[1]", "42", ""} {
		out, got := run(t, in)
		if out != "lea" || len(got) != 0 {
			t.Fatalf("%q: out=%q reports=%+v", in, out, got)
		}
	}
}

func TestRunMissingModelFallsBack(t *testing.T) {
	if out, _ := run(t, `{"workspace":{"current_dir":"/p/shop"},"context_window":{"used_percentage":9}}`); out != "lea · ctx ▓░░░░░░░ 9%" {
		t.Fatalf("out=%q", out)
	}
}

func TestRunStripsControlCharacters(t *testing.T) {
	// ESC and BEL go; the printable rest of a sequence stays visible.
	if out, _ := run(t, `{"model":{"display_name":"Op\u001b[2Jus"},"workspace":{"current_dir":"/p/sh\u0007op"}}`); out != "Op[2Jus" {
		t.Fatalf("out=%q", out)
	}
}

func TestPathsAreCapped(t *testing.T) {
	var b strings.Builder
	b.WriteString("{")
	for i := 0; i < 100; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"k%03d":1`, i)
	}
	b.WriteString("}")
	_, got := run(t, b.String())
	if len(got) != 1 || len(got[0].Paths) != maxPaths || got[0].Paths[0] != "k000" {
		t.Fatalf("paths=%d", len(got[0].Paths))
	}
}

func TestRunDumpFailureStillPrints(t *testing.T) {
	in := `{"model":{"display_name":"Opus"},"workspace":{"current_dir":"/p/shop"}}`
	t.Setenv("NO_COLOR", "1")
	var out bytes.Buffer
	err := Run(strings.NewReader(in), &out, filepath.Join(t.TempDir(), "no", "such", "dir", "sl.json"), nil)
	if err == nil {
		t.Fatal("want dump error")
	}
	if strings.TrimSpace(out.String()) != "Opus" {
		t.Fatalf("out=%q", out.String())
	}
}

func TestPostReachesTheDaemon(t *testing.T) {
	dir, _ := os.MkdirTemp("", "lea")
	t.Cleanup(func() { os.RemoveAll(dir) })
	st, err := store.Open(filepath.Join(dir, "lea.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	d := daemon.New(st, filepath.Join(dir, "run"), func() int64 { return 1 })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	bus, err := d.AttachContainer(ctx, "lea-a")
	if err != nil {
		t.Fatal(err)
	}
	pctx, pcancel := context.WithTimeout(ctx, 2*time.Second)
	defer pcancel()
	if added, err := Post(pctx, bus, "dev", Report{CtxPct: 7, CostUSD: 0.5, Paths: []string{"cost.total_cost_usd"}}); err != nil || len(added) != 0 {
		t.Fatal(err)
	}
	if u, _ := st.Usages("lea-a"); u["lea-a/dev"] != (store.Usage{CtxPct: 7, CostUSD: 0.5}) {
		t.Fatalf("usages=%+v", u)
	}
	if _, err := Post(pctx, bus, "", Report{}); err != nil {
		t.Fatalf("no identity must be a silent no-op: %v", err)
	}
}

const full = `{"model":{"display_name":"Opus 5.5"},"workspace":{"current_dir":"/p/shop"},
  "context_window":{"used_percentage":9},
  "rate_limits":{"five_hour":{"used_percentage":31,"resets_at":1790000000},
                 "seven_day":{"used_percentage":12.4,"resets_at":"2026-10-25T08:00:00Z"}}}`

func TestRunRichLine(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"full", full, "Opus 5.5 · ctx ▓░░░░░░░ 9% · 5h ▓▓░░░░ 31% · 7d ▓░░░░░ 12%"},
		{"no rate_limits", `{"model":{"display_name":"Opus 5.5"},"context_window":{"used_percentage":9}}`,
			"Opus 5.5 · ctx ▓░░░░░░░ 9%"},
		{"rate_limits null", `{"model":{"display_name":"Opus 5.5"},"rate_limits":null}`, "Opus 5.5"},
		{"only 7d", `{"model":{"display_name":"Opus 5.5"},"rate_limits":{"seven_day":{"used_percentage":50}}}`,
			"Opus 5.5 · 7d ▓▓▓░░░ 50%"},
		{"only 5h, no ctx", `{"model":{"display_name":"Opus 5.5"},"rate_limits":{"five_hour":{"used_percentage":100}}}`,
			"Opus 5.5 · 5h ▓▓▓▓▓▓ 100%"},
		{"zero is empty, full is full", `{"model":{"display_name":"M"},"context_window":{"used_percentage":100},
		  "rate_limits":{"five_hour":{"used_percentage":0},"seven_day":{"used_percentage":0.4}}}`,
			"M · ctx ▓▓▓▓▓▓▓▓ 100% · 5h ░░░░░░ 0% · 7d ░░░░░░ 0%"},
		{"bad values dropped", `{"model":{"display_name":"M"},"rate_limits":{"five_hour":{"used_percentage":"31"},
		  "seven_day":{"used_percentage":101}}}`, "M"},
		{"negative and non-object", `{"model":{"display_name":"M"},"rate_limits":{"five_hour":{"used_percentage":-1},
		  "seven_day":[1]}}`, "M"},
		{"rate_limits not an object", `{"model":{"display_name":"M"},"rate_limits":"x"}`, "M"},
		{"no model", `{"rate_limits":{"five_hour":{"used_percentage":31}}}`, "lea · 5h ▓▓░░░░ 31%"},
	} {
		if out, _ := run(t, c.in); out != c.want {
			t.Errorf("%s: out=%q want %q", c.name, out, c.want)
		}
	}
}

// The windows reach the daemon as numbers only (Report.Limits, for auto-resume): no other value
// of the JSON does.
func TestRateLimitsAreReportedAsNumbersOnly(t *testing.T) {
	_, got := run(t, full)
	if len(got) != 1 || len(got[0].Limits) != 2 || got[0].Limits[0].ResetsAt != 1790000000 {
		t.Fatalf("reports=%+v", got)
	}
	if strings.Contains(fmt.Sprint(got[0]), "Opus") {
		t.Fatalf("a value left the container: %+v", got)
	}
}

func TestLineNaNAndInfAreDropped(t *testing.T) {
	// JSON has no NaN; a hand-built document checks num's guard all the same.
	doc := map[string]any{"model": map[string]any{"display_name": "M"}, "rate_limits": map[string]any{
		"five_hour": map[string]any{"used_percentage": math.NaN()},
		"seven_day": map[string]any{"used_percentage": math.Inf(1)}}}
	if got := line(doc, parse(doc), windows(doc), false, time.Now()); got != "M" {
		t.Fatalf("line=%q", got)
	}
}

func TestLineColour(t *testing.T) {
	doc := map[string]any{"model": map[string]any{"display_name": "M"}, "rate_limits": map[string]any{
		"five_hour": map[string]any{"used_percentage": 95.0},
		"seven_day": map[string]any{"used_percentage": 75.0}}}
	r := Report{CtxPct: 10}
	want := "M · ctx \x1b[32m▓\x1b[2m░░░░░░░\x1b[0m 10% · 5h \x1b[31m▓▓▓▓▓▓\x1b[2m\x1b[0m 95% · 7d \x1b[33m▓▓▓▓▓\x1b[2m░\x1b[0m 75%"
	if got := line(doc, r, windows(doc), true, time.Now()); got != want {
		t.Fatalf("line=%q", got)
	}
	if got := line(doc, r, windows(doc), false, time.Now()); strings.Contains(got, "\x1b") {
		t.Fatalf("plain line has ANSI: %q", got)
	}
}

func TestRunColourFollowsNoColor(t *testing.T) {
	var out bytes.Buffer
	t.Setenv("NO_COLOR", "")
	if err := Run(strings.NewReader(full), &out, "", nil); err != nil || !strings.Contains(out.String(), "\x1b[32m") {
		t.Fatalf("out=%q err=%v", out.String(), err)
	}
	out.Reset()
	t.Setenv("NO_COLOR", "1")
	if err := Run(strings.NewReader(full), &out, "", nil); err != nil || strings.Contains(out.String(), "\x1b") {
		t.Fatalf("out=%q err=%v", out.String(), err)
	}
}

func TestLineResetTimeNearTheLimit(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	mk := func(pct float64, resets any) map[string]any {
		return map[string]any{"model": map[string]any{"display_name": "M"}, "rate_limits": map[string]any{
			"five_hour": map[string]any{"used_percentage": pct, "resets_at": resets}}}
	}
	for _, c := range []struct {
		doc  map[string]any
		want string
	}{
		{mk(94, float64(now.Add(65*time.Minute).Unix())), "M · 5h ▓▓▓▓▓▓ 94% ↻1h05"},
		{mk(90, now.Add(45*time.Minute).Format(time.RFC3339)), "M · 5h ▓▓▓▓▓░ 90% ↻45m"},
		{mk(99, float64(now.Add(50*time.Hour).Unix())), "M · 5h ▓▓▓▓▓▓ 99% ↻2d02h"},
		{mk(89, float64(now.Add(time.Hour).Unix())), "M · 5h ▓▓▓▓▓░ 89%"},  // under the alert threshold
		{mk(95, float64(now.Add(-time.Hour).Unix())), "M · 5h ▓▓▓▓▓▓ 95%"}, // already past
		{mk(95, "soon"), "M · 5h ▓▓▓▓▓▓ 95%"},                              // unreadable
		{mk(95, float64(1e15)), "M · 5h ▓▓▓▓▓▓ 95%"},                       // absurd epoch
	} {
		if got := line(c.doc, parse(c.doc), windows(c.doc), false, now); got != c.want {
			t.Errorf("line=%q want %q", got, c.want)
		}
	}
}

// The rate-limit windows reach the daemon as numbers: auto-resume waits for their reset (#20).
func TestRunReportsRateLimitWindows(t *testing.T) {
	_, got := run(t, `{"rate_limits":{"five_hour":{"used_percentage":100,"resets_at":1790000000},
	  "seven_day":{"used_percentage":12.6,"resets_at":"2026-10-27T10:00:00Z"},"other":{"used_percentage":5}}}`)
	want := []Limit{{Window: "5h", Pct: 100, ResetsAt: 1790000000},
		{Window: "7d", Pct: 13, ResetsAt: time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC).Unix()}}
	if len(got) != 1 || !reflect.DeepEqual(got[0].Limits, want) {
		t.Fatalf("reports=%+v", got)
	}
	_, got = run(t, `{"rate_limits":{"five_hour":{"used_percentage":101,"resets_at":1},"seven_day":{"used_percentage":3}}}`)
	if len(got) != 1 || !reflect.DeepEqual(got[0].Limits, []Limit{{Window: "7d", Pct: 3}}) {
		t.Fatalf("reports=%+v", got)
	}
}

// The daemon names the locks lox up runs that the agent's container does not mount: the line says
// to restart lea, a name it should never send is left out.
func TestStatusLineSaysRestartLea(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var out bytes.Buffer
	post := func(Report) []string { return []string{"orders", "Bad\x1b[2J"} }
	if err := Run(strings.NewReader(`{"model":{"display_name":"Opus"}}`), &out, "", post); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "Opus · +lock orders: restart lea" {
		t.Fatalf("out=%q", got)
	}
}
