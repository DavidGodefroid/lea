package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode"
)

// boardItem is one task of the board (the daemon's board_list).
type boardItem struct {
	Task                                  int64
	Container, Agent, Role, Parent, State string
	Repo, Branch, Title, Status, Next     string
	BlockedOn                             string `json:"blocked_on"`
	Updated, Ended                        int64
	// Subagents counts the subagents the agent still runs: its state cell says so.
	Subagents int
}

// stateCell is an item's state, with the subagents it still runs ("idle, 2 subagents").
func stateCell(it boardItem) string {
	switch {
	case it.Subagents == 1:
		return it.State + ", 1 subagent"
	case it.Subagents > 1:
		return fmt.Sprintf("%s, %d subagents", it.State, it.Subagents)
	}
	return it.State
}

type board struct {
	Items  []boardItem  `json:"items"`
	Leases []boardLease `json:"leases"`
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
	if len(b.Items) == 0 && len(b.Leases) == 0 {
		fmt.Fprintln(w, "\nNo tasks yet.")
		return
	}
	items, leases := map[string][]boardItem{}, map[string][]boardLease{}
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
	sort.Strings(cs)
	for _, c := range cs {
		fmt.Fprintf(w, "\n## %s\n", mdCell(c))
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
				fmt.Fprintf(w, "| %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", name, mdCell(it.Role), mdCell(stateCell(it)),
					mdCell(it.Branch), mdCell(it.Title), mdCell(it.Status), mdCell(it.Next), mdCell(it.BlockedOn), stamp(it.Updated))
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
