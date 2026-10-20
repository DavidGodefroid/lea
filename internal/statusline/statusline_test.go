package statusline

import (
	"bytes"
	"context"
	"fmt"
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
	var out bytes.Buffer
	var got []Report
	if err := Run(strings.NewReader(in), &out, "", func(r Report) { got = append(got, r) }); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out.String()), got
}

func TestRunPrintsAndDumps(t *testing.T) {
	in := `{"model":{"display_name":"Opus"},"workspace":{"current_dir":"/p/shop"},"cost":{"total_cost_usd":1.2}}`
	dump := filepath.Join(t.TempDir(), "sl.json")
	var out bytes.Buffer
	if err := Run(strings.NewReader(in), &out, dump, nil); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "Opus · shop" {
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
	if out != "Opus · shop · 42%" {
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

func TestRunMissingWorkspaceFallsBack(t *testing.T) {
	if out, _ := run(t, `{"model":{"display_name":"Opus"}}`); out != "lea" {
		t.Fatalf("out=%q", out)
	}
}

func TestRunStripsControlCharacters(t *testing.T) {
	// ESC and BEL go; the printable rest of a sequence stays visible.
	if out, _ := run(t, `{"model":{"display_name":"Op\u001b[2Jus"},"workspace":{"current_dir":"/p/sh\u0007op"}}`); out != "Op[2Jus · shop" {
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
	var out bytes.Buffer
	err := Run(strings.NewReader(in), &out, filepath.Join(t.TempDir(), "no", "such", "dir", "sl.json"), nil)
	if err == nil {
		t.Fatal("want dump error")
	}
	if strings.TrimSpace(out.String()) != "Opus · shop" {
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
	if err := Post(pctx, bus, "dev", Report{CtxPct: 7, CostUSD: 0.5, Paths: []string{"cost.total_cost_usd"}}); err != nil {
		t.Fatal(err)
	}
	if u, _ := st.Usages("lea-a"); u["lea-a/dev"] != (store.Usage{CtxPct: 7, CostUSD: 0.5}) {
		t.Fatalf("usages=%+v", u)
	}
	if err := Post(pctx, bus, "", Report{}); err != nil {
		t.Fatalf("no identity must be a silent no-op: %v", err)
	}
}
