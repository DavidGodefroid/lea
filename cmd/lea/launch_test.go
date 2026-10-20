package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

func TestLauncherCommandSplitsLauncherFromCore(t *testing.T) {
	launcherArgs := [][]string{nil, {"init"}, {"shell"}, {"stop", "--all"}, {"update"}, {"doctor"}, {"kube", "refresh"},
		{"--help"}, {"new"}, {"new", "-r", "dev", "x"}, {"new", "--", "--container"}, {"--resume"}, {"something"},
		// A user's `lea new --container X` goes through the launcher's steps, as the shell's lea_new
		// does with any argument: only the launchers' own call form reaches the core.
		{"new", "--container", "c"}, {"new", "-r", "dev", "--container", "c", "--workdir", "/w"},
		{"new", "--container", "c", "x", "--workdir"}, {"new", "--container", "c", "--workdir"}}
	for _, a := range launcherArgs {
		if !launcherCommand(a) {
			t.Errorf("%q should go to the launcher", a)
		}
	}
	coreArgs := [][]string{{"version"}, {"daemon"}, {"agents"}, {"msg", "x", "y"}, {"hook", "claude", "Stop"}, {"mcp"},
		{"statusline"}, {"deck", "--placeholder"}, {"deck-open", "--container", "c"}, {"new", "--container", "c", "--workdir", "/w"},
		{"new", "--container", "c", "--workdir", "/w", "-r", "dev", "x", "--", "--model", "m"},
		{"attach-container", "c"}, {"board"}, {"role", "a", "dev"}, {"launcher-check"}}
	for _, a := range coreArgs {
		if launcherCommand(a) {
			t.Errorf("%q should stay a core command", a)
		}
	}
}

func TestLauncherRefusesInsideLea(t *testing.T) {
	for _, env := range []map[string]string{{"LEA_BUS_SOCK": "/run/lea/bus/bus.sock"}, {}} {
		getenv := func(k string) string { return env[k] }
		ready := func(string) bool { return len(env) == 0 } // /run/lea/ready exists in the second case
		if !insideLea(getenv, ready) {
			t.Errorf("env %v: inside lea", env)
		}
	}
	if insideLea(func(string) string { return "" }, func(string) bool { return false }) {
		t.Error("on the host: not inside lea")
	}
	var out, errb bytes.Buffer
	t.Setenv("LEA_BUS_SOCK", "/run/lea/bus/bus.sock")
	if got := run(nil, strings.NewReader(""), &out, &errb); got != 2 || !strings.Contains(errb.String(), "the launcher runs on the host") {
		t.Fatalf("%d %q", got, errb.String())
	}
	if got := run([]string{"stop", "--all"}, strings.NewReader(""), &out, &errb); got != 2 {
		t.Fatalf("stop --all inside lea: %d", got)
	}
}

func TestLauncherCheck(t *testing.T) {
	var out, errb bytes.Buffer
	if got := run([]string{"launcher-check"}, strings.NewReader(""), &out, &errb); got != 0 || out.String() != "go launcher\n" {
		t.Fatalf("%d %q", got, out.String())
	}
}

// TestCoreCommandsMatchTheSwitch ties coreCommands to the switch of run: a subcommand added to
// the switch but not to the list would be taken by the launcher, and answered inside lea by "the
// launcher runs on the host". `new` is the one case routed by its arguments (launcherCommand).
func TestCoreCommandsMatchTheSwitch(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "run" {
			return true
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if c, ok := n.(*ast.CaseClause); ok {
				for _, e := range c.List {
					if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						v, _ := strconv.Unquote(lit.Value)
						cases[v] = true
					}
				}
			}
			return true
		})
		return false
	})
	if len(cases) < 10 {
		t.Fatalf("found only %d case labels in run: %v", len(cases), cases)
	}
	for c := range cases {
		if c == "new" || c == "lock" {
			continue
		}
		if !coreCommands[c] {
			t.Errorf("%q is a case of run but not in coreCommands: the launcher would take it", c)
		}
		if launcherCommand([]string{c}) {
			t.Errorf("%q goes to the launcher", c)
		}
	}
	for c := range coreCommands {
		if !cases[c] {
			t.Errorf("%q is in coreCommands but run has no case for it", c)
		}
	}
	if !cases["new"] || coreCommands["new"] {
		t.Error("new: a case of run, routed by launcherCommand, never in coreCommands")
	}
	if !cases["lock"] || coreCommands["lock"] {
		t.Error("lock: a case of run, routed by launcherCommand, never in coreCommands")
	}
	if !launcherCommand([]string{"lock", "open", "locksql"}) || launcherCommand([]string{"lock", "--container", "lea-a", "list"}) {
		t.Error("lock: the user's form goes to the launcher, the core form (--container) to the core")
	}
}

// Inside lea every core subcommand still runs: the refusal is for launcher commands only.
func TestCoreCommandsRunInsideLea(t *testing.T) {
	t.Setenv("LEA_BUS_SOCK", "/run/lea/bus/bus.sock")
	var out, errb bytes.Buffer
	if got := run([]string{"version"}, strings.NewReader(""), &out, &errb); got != 0 {
		t.Fatalf("version inside lea: %d %q", got, errb.String())
	}
}

// A user's `lea new --container X` reaches the launcher (here: the test binary is in no lea
// checkout, so the launcher stops there), never the core's runNew.
func TestUserNewWithContainerGoesToTheLauncher(t *testing.T) {
	t.Setenv("LEA_BUS_SOCK", "")
	if fileExists("/run/lea/ready") {
		t.Skip("running inside lea")
	}
	var out, errb bytes.Buffer
	if got := run([]string{"new", "--container", "x"}, strings.NewReader(""), &out, &errb); got != 1 ||
		!strings.Contains(errb.String(), "is not in a lea checkout") {
		t.Fatalf("%d %q", got, errb.String())
	}
}
