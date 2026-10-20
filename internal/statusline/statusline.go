// Package statusline is `lea statusline`: Claude Code's status line inside lea. It prints a
// short line (model, context bar, 5-hour and 7-day usage bars) and, as a side effect, reports the
// agent's context use and cost to the daemon. Everything comes from the JSON Claude writes on
// stdin: no token, credential or API call is involved.
package statusline

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

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
	fmt.Fprintln(stdout, line(doc, r, windows(doc), os.Getenv("NO_COLOR") == "", time.Now()))
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

// window is one rate-limit window of the account: its used share (0 to 100) and, when known, when
// it resets (zero time otherwise).
type window struct {
	label  string
	pct    float64
	resets time.Time
}

// windows reads the 5-hour and 7-day rate-limit windows Claude Code passes to the status line
// (subscription accounts only, after the first API response). A window that is missing or whose
// share is not a number from 0 to 100 is left out.
func windows(doc map[string]any) []window {
	var out []window
	for _, w := range []struct{ key, label string }{{"five_hour", "5h"}, {"seven_day", "7d"}} {
		p, ok := num(doc, "rate_limits", w.key, "used_percentage")
		if !ok || p < 0 || p > 100 {
			continue
		}
		out = append(out, window{label: w.label, pct: p, resets: resetsAt(doc, "rate_limits", w.key, "resets_at")})
	}
	return out
}

// resetsAt is a reset time given in Unix seconds or as an RFC 3339 string; zero when absent or
// unreadable.
func resetsAt(doc map[string]any, path ...string) time.Time {
	if n, ok := num(doc, path...); ok && n > 0 && n < 1e11 {
		return time.Unix(int64(n), 0)
	}
	v, _ := get(doc, path...)
	if s, ok := v.(string); ok {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

const (
	ctxWidth    = 8
	windowWidth = 6
	// warnPct and alertPct colour a bar yellow, then red. A window at alertPct or more also shows
	// the time left before it resets.
	warnPct  = 70
	alertPct = 90
)

// bar is a gauge of width cells for pct (0 to 100): round(pct*width/100) cells full, so 0 is all
// empty and 100 all full. With colour on, the full cells are green, yellow from warnPct, red from
// alertPct.
func bar(pct float64, width int, color bool) string {
	n := int(math.Round(math.Max(0, math.Min(100, pct)) * float64(width) / 100))
	full, empty := strings.Repeat("▓", n), strings.Repeat("░", width-n)
	if !color {
		return full + empty
	}
	code := "32"
	switch {
	case pct >= alertPct:
		code = "31"
	case pct >= warnPct:
		code = "33"
	}
	return "\x1b[" + code + "m" + full + "\x1b[2m" + empty + "\x1b[0m"
}

// left is the time until t as "2h15" or "45m"; empty when t is unknown or past.
func left(t, now time.Time) string {
	if t.IsZero() || !t.After(now) {
		return ""
	}
	d := t.Sub(now).Round(time.Minute)
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d >= 24*time.Hour {
		return fmt.Sprintf("%dd%02dh", int(d.Hours())/24, int(d.Hours())%24)
	}
	return fmt.Sprintf("%dh%02d", int(d.Hours()), int(d.Minutes())%60)
}

// line is "<model> · ctx ▓░░░░░░░ 9% · 5h ▓▓░░░░ 31% · 7d ▓░░░░░ 12%": each segment only when its
// figure is known, "lea" without a model. A window at alertPct or more adds the time before it
// resets ("↻1h05"), relative so the container's time zone does not matter. The cost is reported
// to the daemon but not shown: it is a client-side estimate, not what the user is billed.
func line(doc map[string]any, r Report, ws []window, color bool, now time.Time) string {
	parts := []string{"lea"}
	if model := text(doc, "model", "display_name"); model != "" {
		parts = []string{model}
	}
	if r.CtxPct >= 0 {
		parts = append(parts, fmt.Sprintf("ctx %s %d%%", bar(float64(r.CtxPct), ctxWidth, color), r.CtxPct))
	}
	for _, w := range ws {
		seg := fmt.Sprintf("%s %s %d%%", w.label, bar(w.pct, windowWidth, color), int(math.Round(w.pct)))
		if l := left(w.resets, now); l != "" && w.pct >= alertPct {
			seg += " ↻" + l
		}
		parts = append(parts, seg)
	}
	return strings.Join(parts, " · ")
}
