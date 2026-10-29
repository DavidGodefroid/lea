package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/DavidGodefroid/lea/internal/store"
)

func TestBoardMarkdownEscapesCellsAndKeepsTheTree(t *testing.T) {
	var out bytes.Buffer
	renderBoard(&out, board{Items: []boardItem{
		{Task: 1, Container: "lea-a", Agent: "boss", Role: "lead", State: "working", Title: "plan", Status: "a|b <script> \x1b[2J `x`"},
		{Task: 2, Container: "lea-a", Agent: "other", Role: "dev", State: "idle", Title: "solo", WaitsOn: "lea-a/kid", Question: 7},
		{Task: 3, Container: "lea-a", Agent: "kid", Role: "dev", Parent: "boss", State: "idle", Branch: "feat/x", Next: "line\nbreak"},
		{Task: 4, Container: "lea-a", Agent: "gone", State: "gone", Title: "old", Status: "done", Ended: 1},
		{Task: 5, Container: "lea-b", Agent: "b1", Role: "dev", State: "idle"},
	}})
	s := out.String()
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, "| ") {
			if n := strings.Count(strings.ReplaceAll(l, `\|`, ""), "|"); n != 10 {
				t.Fatalf("row with %d separators: %q", n, l)
			}
		}
	}
	for _, want := range []string{"# lea board", "## lea\\-a", "## lea\\-b", "a\\|b \\<script\\> \\\\x1b\\[2J \\`x\\`", "| ↳ kid |",
		`line\\x0abreak`, "### Ended", "| answer from lea\\-a\\/kid to question \\#7 |", "- gone: old, done (ended ", "| - |"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in:\n%s", want, s)
		}
	}
	if bi, ki, oi := strings.Index(s, "| boss |"), strings.Index(s, "| ↳ kid |"), strings.Index(s, "| other |"); !(bi < ki && ki < oi) {
		t.Fatalf("tree order boss=%d kid=%d other=%d:\n%s", bi, ki, oi, s)
	}
	out.Reset()
	renderBoard(&out, board{})
	if !strings.Contains(out.String(), "No tasks yet.") {
		t.Fatalf("empty board %q", out.String())
	}
}

func TestBoardCommand(t *testing.T) {
	api, st := startDaemonStore(t, nil)
	a, _ := st.Register(store.NewAgent{Container: "lea-a", Name: "boss", Profile: "claude", Workdir: "/p", Role: "lead"}, store.Caps{}, 1)
	st.CreateTask("lea-a", a.Key, "plan the release", 1)
	st.UpdateTask(a.Key, "writing notes", "tag", "", 2)
	env := map[string]string{"LEA_DAEMON_SOCK": api}
	rc, out, e := runEnv(t, env, "board", "--md")
	if rc != 0 || !strings.Contains(out, "| boss | lead | unknown | - | plan the release | writing notes | tag | - |") {
		t.Fatalf("rc=%d out=%q err=%q", rc, out, e)
	}
	if rc, out, _ := runEnv(t, env, "board", "--container", "lea-b"); rc != 0 || !strings.Contains(out, "No tasks yet.") {
		t.Fatalf("other container: rc=%d out=%q", rc, out)
	}
	if rc, _, _ := runEnv(t, env, "board", "--bogus"); rc != 2 {
		t.Fatalf("bad option rc=%d", rc)
	}
}

func TestMdCellNeutralisesMarkdown(t *testing.T) {
	for in, want := range map[string]string{
		"![x](https://h/p.png)": `\!\[x\]\(https\:\/\/h\/p\.png\)`,
		"[a](http://h)":         `\[a\]\(http\:\/\/h\)`,
		"*b* _c_ ~~d~~":         `\*b\* \_c\_ \~\~d\~\~`,
		"&amp; &#35;":           `\&amp\; \&\#35\;`,
		"<https://h> www.h.be":  `\<https\:\/\/h\> www\.h\.be`,
		"# head":                `\# head`,
		"- item":                `\- item`,
		"1. one":                `1\. one`,
		"a\u202eb":              `a\\u202eb`,
	} {
		if got := mdCell(in); got != want {
			t.Errorf("mdCell(%q)=%q want %q", in, got, want)
		}
	}
}

func TestBoardShowsLeases(t *testing.T) {
	var out bytes.Buffer
	renderBoard(&out, board{Leases: []boardLease{{ID: 1, Container: "lea-a", Agent: "kid", Glob: "src/*|x", Expires: 100}}})
	s := out.String()
	for _, want := range []string{"## lea\\-a", "### Leases", "- src\\/\\*\\|x held by kid until "} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, "No tasks yet.") {
		t.Fatalf("a board with leases is not empty:\n%s", s)
	}
}

// What lea stopped of an idle agent's stack is listed with the container (#132).
func TestBoardShowsStoppedStacks(t *testing.T) {
	var out bytes.Buffer
	renderBoard(&out, board{Items: []boardItem{
		{Task: 1, Container: "lea-a", Agent: "kid", State: "idle", Stack: "kid: stopped 2 containers and 1 pod (freed 812 MiB) at 14:03"},
		{Task: 2, Container: "lea-a", Agent: "busy", State: "working"},
	}})
	s := out.String()
	if !strings.Contains(s, "### Stacks stopped while idle\n\n- "+mdCell("kid: stopped 2 containers and 1 pod (freed 812 MiB) at 14:03")+"\n") {
		t.Fatalf("no stopped stack in:\n%s", s)
	}
	if strings.Count(s, "\n- ") != 1 {
		t.Fatalf("another item listed:\n%s", s)
	}
	out.Reset()
	renderBoard(&out, board{Items: []boardItem{{Task: 2, Container: "lea-a", Agent: "busy", State: "working"}}})
	if strings.Contains(out.String(), "Stacks stopped") {
		t.Fatalf("an empty section:\n%s", out.String())
	}
}

// The heavy jobs that run and wait show with their container (#132).
func TestBoardShowsHeavyJobs(t *testing.T) {
	var out bytes.Buffer
	renderBoard(&out, board{Heavy: []boardHeavy{{Container: "lea-a", Slots: 2, Running: []string{"dev-1 phpstan analyse"}, Queued: 3}}})
	s := out.String()
	if !strings.Contains(s, "### Heavy jobs (1 of 2 slots, 3 waiting)\n\n- "+mdCell("dev-1 phpstan analyse")+"\n") || strings.Contains(s, "No tasks yet.") {
		t.Fatalf("no heavy jobs in:\n%s", s)
	}
}

// The host's gauge heads the board, each container's heads its section (#132, P2).
func TestBoardShowsPressure(t *testing.T) {
	var out bytes.Buffer
	renderBoard(&out, board{
		Items: []boardItem{{Task: 1, Container: "lea-a", Agent: "a", State: "working"}},
		Pressure: []boardGauge{
			{Scope: "host", Level: "ok", CPUUsed: 3, CPUs: 22, CPUPSI: 1, MemUsed: -1, Load1: 4},
			{Scope: "lea-a", Level: "over", Over: "cpu PSI 63% > 40%", CPUUsed: 15.8, CPUs: 16, CPUPSI: 63, MemUsed: 18 << 30, MemMax: 24 << 30, Heavy: 2, Slots: 2},
		}})
	s := out.String()
	for _, w := range []string{"\nHost: " + mdCell("CPU 3.0/22 · PSI 1% · load 4.0") + "\n",
		"## " + mdCell("lea-a") + "\n\nPressure: " + mdCell("CPU 15.8/16 · PSI 63% · mem 18.0/24.0 GiB · heavy 2/2") + " (under pressure: " + mdCell("cpu PSI 63% > 40%") + "; new agents wait, heavy slots halved)\n"} {
		if !strings.Contains(s, w) {
			t.Fatalf("%q not in:\n%s", w, s)
		}
	}
}

func TestBoardStateCellCountsSubagents(t *testing.T) {
	var out bytes.Buffer
	renderBoard(&out, board{Items: []boardItem{
		{Task: 1, Container: "lea-a", Agent: "a", State: "idle", Subagents: 2},
		{Task: 2, Container: "lea-a", Agent: "b", State: "idle", Subagents: 1},
		{Task: 3, Container: "lea-a", Agent: "c", State: "idle"},
	}})
	s := out.String()
	for _, want := range []string{`| a | - | idle\, 2 subagents |`, `| b | - | idle\, 1 subagent |`, "| c | - | idle |"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in:\n%s", want, s)
		}
	}
}

// The board says what the deck's mark says (#72): blocked, waiting, or the raw state.
func TestBoardStateCellFollowsTheDeck(t *testing.T) {
	for _, c := range []struct {
		it   boardItem
		want string
	}{
		{boardItem{State: "idle", BlockedOn: "a token", Next: "merge"}, "blocked"},
		{boardItem{State: "working", BlockedOn: "a token"}, "blocked"},
		{boardItem{State: "idle", BlockedOn: "x", Subagents: 2}, "blocked, 2 subagents"},
		{boardItem{State: "idle", Next: "CI"}, "waiting"},
		{boardItem{State: "idle", Next: "  "}, "idle"},
		{boardItem{State: "idle", Next: "CI", Subagents: 1}, "idle, 1 subagent"},
		{boardItem{State: "working", Next: "CI"}, "working"},
		{boardItem{State: "done", Next: "CI", BlockedOn: "x"}, "done"},
		{boardItem{State: "gone", BlockedOn: "x"}, "gone"},
		{boardItem{State: "blocked"}, "blocked"},
		{boardItem{State: "idle"}, "idle"},
	} {
		if got := stateCell(c.it); got != c.want {
			t.Errorf("%+v: %q, want %q", c.it, got, c.want)
		}
	}
}
