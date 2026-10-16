// Package statusline is `lea statusline`: Claude Code's status line inside lea. It prints a
// short line and, as a side effect, reports the agent's context use and cost to the daemon.
package statusline

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/DavidGodefroid/lea/internal/client"
	"github.com/DavidGodefroid/lea/internal/textsafe"
	"github.com/DavidGodefroid/lea/internal/wire"
)

// Report is what the status line tells the daemon: context use and cost (-1 when unknown), and
// the key paths of the JSON Claude sent, never their values.
type Report struct {
	CtxPct  int      `json:"ctx_pct"`
	CostUSD float64  `json:"cost_usd"`
	Paths   []string `json:"paths"`
}

const (
	maxPaths = 64
	maxDepth = 3
)

// keyName is a key lea may report: a plain identifier. Other keys are left out of Paths.
var keyName = regexp.MustCompile(`^[A-Za-z0-9_]{1,40}$`)

// Run prints the status line for the JSON on stdin and hands post (when not nil) what lea needs
// from it. When dump is set the raw JSON is also written there (mode 0600). Nothing here may
// fail Claude: bad input prints "lea" and reports nothing.
func Run(stdin io.Reader, stdout io.Writer, dump string, post func(Report)) error {
	in, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
	if err != nil {
		fmt.Fprintln(stdout, "lea")
		return err
	}
	// A failed dump must not hide the status line: report it after printing.
	var dumpErr error
	if dump != "" {
		dumpErr = os.WriteFile(dump, in, 0o600)
	}
	var doc map[string]any
	if json.Unmarshal(in, &doc) != nil || doc == nil {
		fmt.Fprintln(stdout, "lea")
		return dumpErr
	}
	r := parse(doc)
	fmt.Fprintln(stdout, line(doc, r))
	if post != nil {
		post(r)
	}
	return dumpErr
}

// Post sends r to the daemon on the container's bus. Without an agent identity it does nothing.
// The caller bounds it with ctx: the status line must never hold Claude up.
func Post(ctx context.Context, sock, agent string, r Report) error {
	if agent == "" {
		return nil
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return client.Call(ctx, sock, wire.Request{Method: "status_report", Agent: agent, Params: raw}, nil)
}

// parse reads the context % and the cost. The status-line fields are not a documented contract
// (phase 0 V2): each candidate is tried in turn, and a missing, non-numeric or out-of-range value
// is unknown (-1).
func parse(doc map[string]any) Report {
	r := Report{CtxPct: -1, CostUSD: -1, Paths: paths(doc)}
	if p, ok := num(doc, "context_window", "used_percentage"); ok && p >= 0 && p <= 100 {
		r.CtxPct = int(math.Round(p))
	} else if size, ok := num(doc, "context_window", "context_window_size"); ok && size > 0 {
		used, seen := 0.0, false
		for _, k := range []string{"input_tokens", "cache_creation_input_tokens", "cache_read_input_tokens"} {
			if n, ok := num(doc, "context_window", "current_usage", k); ok && n >= 0 {
				used, seen = used+n, true
			}
		}
		if seen {
			r.CtxPct = int(math.Min(100, math.Round(used/size*100)))
		}
	}
	if c, ok := num(doc, "cost", "total_cost_usd"); ok && c >= 0 && c < 1e6 {
		r.CostUSD = math.Round(c*100) / 100
	}
	return r
}

func get(doc map[string]any, path ...string) (any, bool) {
	var v any = doc
	for _, k := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		if v, ok = m[k]; !ok {
			return nil, false
		}
	}
	return v, true
}

func num(doc map[string]any, path ...string) (float64, bool) {
	v, _ := get(doc, path...)
	f, ok := v.(float64)
	return f, ok && !math.IsNaN(f) && !math.IsInf(f, 0)
}

// text is a string field with control characters removed (the status line is drawn by Claude).
func text(doc map[string]any, path ...string) string {
	v, _ := get(doc, path...)
	s, _ := v.(string)
	return strings.Map(func(r rune) rune {
		if textsafe.Unsafe(r) {
			return -1
		}
		return r
	}, s)
}

// paths lists the key paths of doc ("cost.total_cost_usd"; "list[]" for an array), sorted, to
// depth 3 and at most 64.
func paths(doc map[string]any) []string {
	var out []string
	var walk func(m map[string]any, prefix string, depth int)
	walk = func(m map[string]any, prefix string, depth int) {
		for k, v := range m {
			if !keyName.MatchString(k) {
				continue
			}
			p := prefix + k
			switch x := v.(type) {
			case map[string]any:
				if depth < maxDepth && len(x) > 0 {
					walk(x, p+".", depth+1)
					continue
				}
				out = append(out, p)
			case []any:
				out = append(out, p+"[]")
			default:
				out = append(out, p)
			}
		}
	}
	walk(doc, "", 1)
	sort.Strings(out)
	if len(out) > maxPaths {
		out = out[:maxPaths]
	}
	return out
}

// line is "<model> · <dir> · 42% · $1.20": the last two only when known, "lea" without a model
// and a directory.
func line(doc map[string]any, r Report) string {
	parts := []string{"lea"}
	model, dir := text(doc, "model", "display_name"), text(doc, "workspace", "current_dir")
	if model != "" && dir != "" {
		parts = []string{model, filepath.Base(dir)}
	}
	if r.CtxPct >= 0 {
		parts = append(parts, fmt.Sprintf("%d%%", r.CtxPct))
	}
	if r.CostUSD >= 0 {
		parts = append(parts, fmt.Sprintf("$%.2f", r.CostUSD))
	}
	return strings.Join(parts, " · ")
}
