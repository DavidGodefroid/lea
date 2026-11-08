package statusline

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runUser runs the status line chained with command and returns what it printed and the reports.
func runUser(t *testing.T, in, command string, timeout time.Duration) (string, []Report) {
	t.Helper()
	t.Setenv("NO_COLOR", "1")
	var out bytes.Buffer
	var got []Report
	post := func(r Report) Answer { got = append(got, r); return Answer{} }
	if err := RunWith(strings.NewReader(in), &out, "", post, User{Command: command, Timeout: timeout}); err != nil {
		t.Fatal(err)
	}
	return strings.TrimRight(out.String(), "\n"), got
}

const userIn = `{"model":{"display_name":"Opus"},"context_window":{"used_percentage":9},"cost":{"total_cost_usd":1.5}}`

// The user's command prints the line, gets the exact JSON on stdin, and the report still goes.
func TestUserLinePrintsAndGetsTheJSON(t *testing.T) {
	seen := filepath.Join(t.TempDir(), "stdin")
	out, got := runUser(t, userIn, "cat > '"+seen+"'; printf '\\033[1mmine\\033[0m\\nsecond\\n\\n'", 0)
	if out != "\x1b[1mmine\x1b[0m\nsecond" {
		t.Fatalf("out=%q", out)
	}
	if b, _ := os.ReadFile(seen); string(b) != userIn {
		t.Fatalf("stdin=%q", b)
	}
	if len(got) != 1 || got[0].CtxPct != 9 || got[0].CostUSD != 1.5 {
		t.Fatalf("reports=%+v", got)
	}
}

// A failing, silent or slow command leaves lea's line, and the report is sent all the same.
func TestUserLineFallsBack(t *testing.T) {
	for _, c := range []struct{ name, cmd string }{
		{"non-zero", "echo partial; exit 3"},
		{"empty", "true"},
		{"blank", "printf '  \\n\\n'"},
		{"not found", "/no/such/statusline.sh"},
	} {
		out, got := runUser(t, userIn, c.cmd, 0)
		if out != "Opus · ctx ▓░░░░░░░ 9%" || len(got) != 1 || got[0].CtxPct != 9 {
			t.Errorf("%s: out=%q reports=%+v", c.name, out, got)
		}
	}
}

// A command past the timeout is killed, with the processes it started, and lea's line printed.
func TestUserLineTimeoutKills(t *testing.T) {
	pid := filepath.Join(t.TempDir(), "pid")
	start := time.Now()
	out, got := runUser(t, userIn, "sleep 30 & echo $! > '"+pid+"'; wait", 200*time.Millisecond)
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("took %v", el)
	}
	if out != "Opus · ctx ▓░░░░░░░ 9%" || len(got) != 1 {
		t.Fatalf("out=%q reports=%+v", out, got)
	}
	b, _ := os.ReadFile(pid)
	p := strings.TrimSpace(string(b))
	if p == "" {
		t.Fatal("no pid")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat("/proc/" + p); err != nil {
			break
		}
		if st, _ := os.ReadFile("/proc/" + p + "/stat"); strings.Contains(string(st), ") Z ") {
			break // killed, waiting to be reaped
		}
		if time.Now().After(deadline) {
			t.Fatalf("child %s still runs", p)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The user's command is told it runs chained, so a lea statusline inside it does not chain again.
func TestUserLineMarksTheChain(t *testing.T) {
	if out, _ := runUser(t, userIn, `printf '%s' "$`+ChainedEnv+`"`, 0); out != "1" {
		t.Fatalf("out=%q", out)
	}
}

// The lox run and restart notices still show behind the user's line.
func TestUserLineKeepsTheLockNotice(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var out bytes.Buffer
	post := func(Report) Answer { return Answer{LocksAdded: []string{"orders"}, LoxRun: "off"} }
	if err := RunWith(strings.NewReader(userIn), &out, "", post, User{Command: "echo mine"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "mine · no lox run: start it in the projects directory · +lock orders: restart lea" {
		t.Fatalf("out=%q", got)
	}
}

func TestUserCommand(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct{ name, file, want string }{
		{"command", `{"statusLine":{"type":"command","command":" ~/.claude/statusline.sh "}}`, "~/.claude/statusline.sh"},
		{"no type", `{"statusLine":{"command":"echo hi"}}`, "echo hi"},
		{"other type", `{"statusLine":{"type":"static","command":"echo hi"}}`, ""},
		{"none", `{"model":"opus"}`, ""},
		{"null", `{"statusLine":null}`, ""},
		{"empty", `{"statusLine":{"type":"command","command":"  "}}`, ""},
		{"lea's own", `{"statusLine":{"type":"command","command":"[ -x /usr/local/bin/lea ] && exec /usr/local/bin/lea statusline; exit 0"}}`, ""},
		{"bare lea", `{"statusLine":{"type":"command","command":"lea statusline --dump /tmp/x"}}`, ""},
		{"not lea", `{"statusLine":{"type":"command","command":"plea statusline"}}`, "plea statusline"},
		{"bad json", `{`, ""},
	} {
		p := filepath.Join(dir, "s.json")
		os.WriteFile(p, []byte(c.file), 0o600)
		if got := UserCommand(p); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	if got := UserCommand(filepath.Join(dir, "missing.json")); got != "" {
		t.Errorf("missing file: %q", got)
	}
}
