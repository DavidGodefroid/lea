package launcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// LEA_TOOL is an argv: words of a safe alphabet, the first one a command; empty is claude. The
// shell (lea__check_tool, tests/unit/config.bats) refuses the same values with the same text.
func TestToolArgv(t *testing.T) {
	for in, want := range map[string]string{
		"": "claude", "  ": "claude", "claude": "claude", "codex --model gpt-5": "codex --model gpt-5",
		" /usr/local/bin/my-tool\t--x=1  a,b ": "/usr/local/bin/my-tool --x=1 a,b", "aider --model=openai/gpt-4o@x%2+y": "aider --model=openai/gpt-4o@x%2+y",
	} {
		got, err := ToolArgv(in)
		if err != nil || strings.Join(got, " ") != want {
			t.Errorf("ToolArgv(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"claude; rm -rf /", "sh -c 'x'", "a|b", "$HOME/x", "`id`", "a&&b", "x >y", "~/bin/x", "-p claude",
		"a *", "x\"y", "a\\b", "x(y)", "x{y}", "x#y", "x!y", strings.Repeat("a ", 33)} {
		_, err := ToolArgv(in)
		want := "LEA_TOOL must be a command and its arguments separated by spaces (at most 32 words of letters, digits and _ . / : = @ % + , -, the first one not an option), not '" + in + "'"
		if err == nil || err.Error() != want {
			t.Errorf("ToolArgv(%q): %v", in, err)
		}
	}
}

func TestLoadConfigChecksTheDeckSwitch(t *testing.T) {
	root := repoRoot(t)
	for _, c := range []struct{ conf, deck, tool, err string }{
		{"", "1", "claude", ""},
		{"LEA_DECK=0\n", "0", "claude", ""},
		{"LEA_DECK=0\nLEA_TOOL='codex  --x'\n", "0", "codex --x", ""},
		{"LEA_DECK=yes\n", "", "", "LEA_DECK must be 1 (the lea deck) or 0 (your own tool, LEA_TOOL, in the lea container), not 'yes'"},
		{"LEA_TOOL='a;b'\n", "", "", "LEA_TOOL must be a command"},
	} {
		w := newWorld(t, root)
		writeUserConf(t, w, c.conf)
		cfg, err := LoadConfig(w.getenv, w.root, w.proj)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%q: err=%v, want %q", c.conf, err, c.err)
			}
			continue
		}
		if err != nil || cfg.Get("LEA_DECK") != c.deck || cfg.Get("LEA_TOOL") != c.tool || cfg.NoDeck() != (c.deck == "0") {
			t.Errorf("%q: LEA_DECK=%q LEA_TOOL=%q err=%v", c.conf, cfg.Get("LEA_DECK"), cfg.Get("LEA_TOOL"), err)
		}
		// Both reach every child: the daemon (its approvals) and lea-sh (lea doctor).
		env := strings.Join(cfg.Environ(nil), "\n") + "\n"
		if !strings.Contains(env, "LEA_DECK="+c.deck+"\n") || !strings.Contains(env, "LEA_TOOL="+c.tool+"\n") {
			t.Errorf("%q: children get\n%s", c.conf, env)
		}
	}
}

// The isolation is the deck's: LEA_DECK=0 starts the container with the very same docker run
// arguments (proxy, networks, seccomp, AppArmor, mounts, Lock sockets), then runs the tool in it.
func TestNoDeckStartsTheSameContainerAsTheDeck(t *testing.T) {
	root := copyRoot(t)
	stopped := map[string]string{"FAKE_DOCKER_OUT_INSPECT__F_AT_LEA_$ID": "false"}
	run := func(t *testing.T, sc commandScenario) string {
		g, _ := runGoSide(t, root, sc)
		if g.status != 0 {
			t.Fatalf("status %d\n%s\n%s", g.status, g.calls, g.lea)
		}
		for _, c := range strings.Split(g.calls, "\n") {
			if strings.HasPrefix(c, "docker [run] [-d] [--init] [--name] [lea-$ID]") {
				return c
			}
		}
		t.Fatalf("no docker run of lea-$ID:\n%s", g.calls)
		return ""
	}
	deck := run(t, commandScenario{name: "deck", env: stopped})
	tool := run(t, commandScenario{name: "no deck", env: stopped, setup: func(w *world) { noDeck(t, w, "") }})
	if deck != tool {
		t.Fatalf("the docker run arguments differ\ndeck:    %s\nno deck: %s", deck, tool)
	}
}

// lea attach --print prints the docker exec command that runs the tool, or joins an agent, in
// the running container: one line a shell runs as is.
func TestAttachPrint(t *testing.T) {
	root := repoRoot(t)
	for _, c := range []struct {
		tool string
		args []string
		cwd  string
		want string
	}{
		{"", []string{"attach", "--print"}, "", "lea-session --claude"},
		{"codex --model gpt-5", []string{"attach", "--print"}, "sub", "lea-session --shell codex --model gpt-5"},
		{"", []string{"attach", "dev", "--print"}, "", "abduco -a ag-dev"},
	} {
		w, _, _ := commandSetup(t, root, commandScenario{setup: func(w *world) { noDeck(t, w, c.tool) }})
		l := w.launcher()
		wd := w.proj
		if c.cwd != "" {
			wd = filepath.Join(w.proj, c.cwd)
			os.MkdirAll(wd, 0o755)
			l.Cwd = wd
		}
		if err := l.dispatch(c.args); err != nil {
			t.Fatalf("%q: %v\n%s", c.args, err, w.errb.String())
		}
		want := "docker exec -it -u " + uidgid() + " -e TERM -w " + wd + " " + w.name() + " " + c.want + "\n"
		if w.out.String() != want {
			t.Errorf("%q: printed %q, want %q", c.args, w.out.String(), want)
		}
		if w.hasCall("docker [exec]") {
			t.Errorf("%q: --print ran docker exec", c.args)
		}
	}
	// lea attach AGENT (no --print) joins it on this terminal.
	w, _, _ := commandSetup(t, root, commandScenario{})
	if err := w.launcher().dispatch([]string{"attach", "dev"}); err != nil {
		t.Fatal(err)
	}
	if !w.hasCall("docker [exec] [-it] [-u] [" + uidgid() + "] [-e] [TERM] [-w] [" + w.proj + "] [" + w.name() + "] [abduco] [-a] [ag-dev]") {
		t.Fatalf("calls:\n%s", strings.Join(w.calls(), "\n"))
	}
}

func TestShellLine(t *testing.T) {
	got := shellLine([]string{"docker", "exec", "-w", "/home/me/my projects", "it's", ""})
	if want := `docker exec -w '/home/me/my projects' 'it'\''s' ''`; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// lea config deck on|off and lea config tool write the user configuration and say when the
// change applies; a bad value writes nothing.
func TestConfigDeckAndTool(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	conf := func() string {
		b, _ := os.ReadFile(filepath.Join(w.env["XDG_CONFIG_HOME"], "lea", "lea.conf"))
		return string(b)
	}
	l := w.launcher()
	if err := l.dispatch([]string{"config", "deck", "off"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conf(), "LEA_DECK='0'\n") ||
		!strings.Contains(w.errb.String(), "lea: LEA_DECK=0: the next lea runs claude in the lea container, without the deck; running agents are not touched") {
		t.Fatalf("conf=%q err=%q", conf(), w.errb.String())
	}
	w.errb.Reset()
	if err := w.launcher().dispatch([]string{"config", "tool", "codex", "--model", "gpt-5"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conf(), "LEA_TOOL='codex --model gpt-5'\n") ||
		!strings.Contains(w.errb.String(), "lea: LEA_TOOL=codex --model gpt-5: lea runs it in the lea container from the next lea") {
		t.Fatalf("conf=%q err=%q", conf(), w.errb.String())
	}
	w.errb.Reset()
	if err := w.launcher().dispatch([]string{"config", "deck", "on"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conf(), "LEA_DECK='1'\n") || strings.Contains(conf(), "LEA_DECK='0'") ||
		!strings.Contains(w.errb.String(), "lea: LEA_DECK=1: the next lea opens the deck (tmux 3.2 or later); running agents are not touched") {
		t.Fatalf("conf=%q err=%q", conf(), w.errb.String())
	}
	w.errb.Reset()
	if err := w.launcher().dispatch([]string{"config", "tool", "claude"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.errb.String(), "with LEA_DECK=0 (lea config deck off)") {
		t.Fatalf("err=%q", w.errb.String())
	}
	before := conf()
	for _, args := range [][]string{{"config", "deck"}, {"config", "deck", "maybe"}, {"config", "tool"}} {
		if err := w.launcher().dispatch(args); err == nil || err.Error() != "exit status 2" {
			t.Errorf("%q: %v", args, err)
		}
	}
	err := w.launcher().dispatch([]string{"config", "tool", "sh", "-c", "'rm -rf ~'"})
	if err == nil || !strings.Contains(err.Error(), "LEA_TOOL must be a command") || conf() != before {
		t.Fatalf("err=%v conf=%q", err, conf())
	}
}
