package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/DavidGodefroid/lea/internal/daemon"
)

// boardItem is one task of the board (the daemon's board_list).
type boardItem struct {
	Task                                  int64
	Container, Agent, Role, Parent, State string
	Repo, Branch, Title, Status, Next     string
	BlockedOn                             string `json:"blocked_on"`
	Updated, Ended                        int64
	// WaitsOn and Question: the agent waits on an answer to its ask (the daemon's, not its text).
	WaitsOn  string `json:"waits_on"`
	Question int64
	// Subagents counts the subagents the agent still runs: its state cell says so.
	Subagents int
	// Stack is what lea stopped of the agent's nested containers while it was idle (#132).
	Stack string
}

// stateCell is an item's state as the deck shows it (issue #72), with the subagents it still
// runs ("idle, 2 subagents"): a live agent whose task names a blocker is blocked, an idle one
// with a next step and no subagent is waiting.
func stateCell(it boardItem) string {
	state, live := it.State, it.State == "working" || it.State == "idle"
	switch {
	case live && strings.TrimSpace(it.BlockedOn) != "":
		state = "blocked"
	case it.State == "idle" && it.Subagents == 0 && strings.TrimSpace(it.Next) != "":
		state = "waiting"
	}
	switch {
	case it.Subagents == 1:
		return state + ", 1 subagent"
	case it.Subagents > 1:
		return fmt.Sprintf("%s, %d subagents", state, it.Subagents)
	}
	return state
}

type board struct {
	Items  []boardItem  `json:"items"`
	Leases []boardLease `json:"leases"`
	// Heavy is each container's heavy jobs that run or wait (#132).
	Heavy []boardHeavy `json:"heavy"`
	// Pressure is the host's gauge, then each container's (#132, P2).
	Pressure []boardGauge `json:"pressure"`
}

// boardGauge is one scope's pressure (daemon.Gauge): the daemon's text and numbers only.
type boardGauge = daemon.Gauge

type boardHeavy struct {
	Container string
	Slots     int
	Running   []string
	Queued    int
}

// mdCell makes agent text safe in one markdown table cell printed on a terminal: control, bidi
// and zero-width characters become visible escapes and newlines cannot end the row (clean);
// every ASCII punctuation character is backslash-escaped (CommonMark: always literal), so no cell
// can hold a link, image (remote fetch), emphasis, entity, HTML, code, heading, list marker or
// autolink (':' '/' '.' '@' are escaped, so "https://h" and "www.h" stay text); cut at 200
// characters; "-" when empty.
func mdCell(s string) string {
	s = oneLine(s, 200)
	var b strings.Builder
	for _, r := range s {
		if r < 0x80 && unicode.IsPunct(r) || r == '$' || r == '+' || r == '<' || r == '=' || r == '>' || r == '^' || r == '`' || r == '|' || r == '~' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	if b.Len() == 0 {
		return "-"
	}
	return b.String()
}

func stamp(t int64) string {
	if t == 0 {
		return "-"
	}
	return time.Unix(t, 0).Format("2006-01-02 15:04")
}

func hasAgent(items []boardItem, name string) bool {
	for _, it := range items {
		if it.Agent == name {
			return true
		}
	}
	return false
}

// treeOrder puts each lead's children right after it; the others keep their order.
func treeOrder(items []boardItem) []boardItem {
	var out []boardItem
	placed := map[int64]bool{}
	for _, it := range items {
		if it.Parent != "" && hasAgent(items, it.Parent) {
			continue
		}
		out, placed[it.Task] = append(out, it), true
		for _, k := range items {
			if k.Parent == it.Agent && !placed[k.Task] {
				out, placed[k.Task] = append(out, k), true
			}
		}
	}
	return out
}

// boardLease is one live lease (the daemon's board_list).
type boardLease struct {
	ID                     int64
	Container, Agent, Glob string
	Expires                int64
}

// renderBoard prints the board as markdown (spec A13: a read-only export; lea.db is the truth):
// one section per container, the open tasks as a table (children under their lead), the live
// leases, then the last ended tasks.
func renderBoard(w io.Writer, b board) {
	fmt.Fprintln(w, "# lea board")
	gauges := map[string]boardGauge{}
	for _, g := range b.Pressure {
		if g.Scope == "host" {
			fmt.Fprintf(w, "\nHost: %s%s\n", mdCell(g.Line()), gaugeLevel(g))
		} else {
			gauges[g.Scope] = g
		}
	}
	if len(b.Items) == 0 && len(b.Leases) == 0 && len(b.Heavy) == 0 {
		fmt.Fprintln(w, "\nNo tasks yet.")
		return
	}
	items, leases, heavy := map[string][]boardItem{}, map[string][]boardLease{}, map[string]boardHeavy{}
	seen := map[string]bool{}
	var cs []string
	add := func(c string) {
		if !seen[c] {
			seen[c] = true
			cs = append(cs, c)
		}
	}
	for _, it := range b.Items {
		add(it.Container)
		items[it.Container] = append(items[it.Container], it)
	}
	for _, l := range b.Leases {
		add(l.Container)
		leases[l.Container] = append(leases[l.Container], l)
	}
	for _, h := range b.Heavy {
		add(h.Container)
		heavy[h.Container] = h
	}
	sort.Strings(cs)
	for _, c := range cs {
		fmt.Fprintf(w, "\n## %s\n", mdCell(c))
		if g, ok := gauges[c]; ok {
			fmt.Fprintf(w, "\nPressure: %s%s\n", mdCell(g.Line()), gaugeLevel(g))
		}
		var open, ended []boardItem
		for _, it := range items[c] {
			if it.Ended == 0 {
				open = append(open, it)
			} else {
				ended = append(ended, it)
			}
		}
		if len(open) > 0 {
			fmt.Fprintln(w, "\n| agent | role | state | branch | task | status | next | blocked on | updated |")
			fmt.Fprintln(w, "|---|---|---|---|---|---|---|---|---|")
			for _, it := range treeOrder(open) {
				name := mdCell(it.Agent)
				if it.Parent != "" && hasAgent(open, it.Parent) {
					name = "↳ " + name
				}
				blocked := it.BlockedOn
				if it.WaitsOn != "" {
					blocked = strings.TrimSuffix(fmt.Sprintf("answer from %s to question #%d; %s", it.WaitsOn, it.Question, blocked), "; ")
				}
				fmt.Fprintf(w, "| %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", name, mdCell(it.Role), mdCell(stateCell(it)),
					mdCell(it.Branch), mdCell(it.Title), mdCell(it.Status), mdCell(it.Next), mdCell(blocked), stamp(it.Updated))
			}
		}
		var stacks []boardItem
		for _, it := range open {
			if it.Stack != "" {
				stacks = append(stacks, it)
			}
		}
		if len(stacks) > 0 {
			fmt.Fprintln(w, "\n### Stacks stopped while idle")
			fmt.Fprintln(w)
			for _, it := range stacks {
				fmt.Fprintf(w, "- %s\n", mdCell(it.Stack))
			}
		}
		if h, ok := heavy[c]; ok {
			fmt.Fprintf(w, "\n### Heavy jobs (%d of %d slots, %d waiting)\n\n", len(h.Running), h.Slots, h.Queued)
			for _, r := range h.Running {
				fmt.Fprintf(w, "- %s\n", mdCell(r))
			}
		}
		if ls := leases[c]; len(ls) > 0 {
			fmt.Fprintln(w, "\n### Leases")
			fmt.Fprintln(w)
			for _, l := range ls {
				fmt.Fprintf(w, "- %s held by %s until %s\n", mdCell(l.Glob), mdCell(l.Agent), stamp(l.Expires))
			}
		}
		if len(ended) > 0 {
			fmt.Fprintln(w, "\n### Ended")
			fmt.Fprintln(w)
			for _, it := range ended {
				fmt.Fprintf(w, "- %s: %s, %s (ended %s)\n", mdCell(it.Agent), mdCell(it.Title), mdCell(it.Status), stamp(it.Ended))
			}
		}
	}
}

// gaugeLevel is what follows a gauge's line: nothing when all is well, else its level and, over a
// threshold, which ones.
func gaugeLevel(g boardGauge) string {
	switch g.Level {
	case "warn":
		return " (near a threshold)"
	case "over":
		return " (under pressure: " + mdCell(g.Over) + "; new agents wait, heavy slots halved)"
	}
	return ""
}
