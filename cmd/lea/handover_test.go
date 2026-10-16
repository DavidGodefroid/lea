package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/DavidGodefroid/lea/internal/launcher"
)

// checkout is a minimal lea checkout for runLauncher: the real config/lea.conf, an empty
// build/lea (the launcher's own path), and a fake libexec/lea-sh.
type checkout struct {
	root, conf string
	execs      [][]string // argv of each replacing exec, env appended after a "--env" marker
}

var errStubExec = errors.New("stub exec")

func newCheckout(t *testing.T) *checkout {
	t.Helper()
	if fileExists("/run/lea/ready") {
		t.Skip("running inside lea")
	}
	c := &checkout{root: t.TempDir()}
	c.conf = filepath.Join(c.root, "user.conf")
	src, err := os.ReadFile("../../config/lea.conf")
	if err != nil {
		t.Fatal(err)
	}
	for p, body := range map[string]string{"config/lea.conf": string(src), "build/lea": "",
		"libexec/lea-sh": "#!/bin/sh\n", "user.conf": ""} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(c.root, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(c.root, p), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	home := filepath.Join(c.root, "home")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/.config")
	t.Setenv("LEA_CONFIG", c.conf)
	t.Setenv("LEA_BUS_SOCK", "")
	t.Setenv("LEA_LAUNCHER", "")
	t.Setenv("LEA_UI", "")
	t.Setenv("LEA_PROJECTS_DIR", "")
	exe, ex := executable, execProcess
	t.Cleanup(func() { executable, execProcess = exe, ex })
	executable = func() (string, error) { return filepath.Join(c.root, "build", "lea"), nil }
	execProcess = func(argv0 string, argv, env []string) error {
		c.execs = append(c.execs, append(append([]string{argv0}, argv...), append([]string{"--env"}, env...)...))
		return errStubExec
	}
	return c
}

func (c *checkout) writeConf(t *testing.T, s string) {
	t.Helper()
	if err := os.WriteFile(c.conf, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func hasEnv(argv []string, kv string) bool {
	for i, a := range argv {
		if a == "--env" {
			for _, e := range argv[i+1:] {
				if e == kv || strings.HasSuffix(kv, "*") && strings.HasPrefix(e, strings.TrimSuffix(kv, "*")) {
					return true
				}
			}
		}
	}
	return false
}

// A configuration line the Go launcher refuses names the file, the line and the way to rewrite it,
// and no longer points to the retired shell launcher.
func TestBadConfigNamesTheLine(t *testing.T) {
	c := newCheckout(t)
	c.writeConf(t, "LEA_IMAGE=\"/home/$USER/i\"\n")
	var out, errb bytes.Buffer
	if rc := run([]string{"stop"}, strings.NewReader(""), &out, &errb); rc != 1 {
		t.Fatalf("rc=%d stderr=%q", rc, errb.String())
	}
	msg := errb.String()
	for _, want := range []string{c.conf + " line 1", "KEY='value'"} {
		if !strings.Contains(msg, want) {
			t.Errorf("stderr lacks %q: %q", want, msg)
		}
	}
	if strings.Contains(msg, "never runs") || strings.Contains(msg, "LEA_LAUNCHER") {
		t.Errorf("stderr %q", msg)
	}
	if len(c.execs) != 0 {
		t.Errorf("handed over: %q", c.execs)
	}
}

// lea doctor is not blocked by a line the Go launcher refuses: libexec/lea-sh doctor (which loads
// the configuration in shell, as it always did) runs and is told about the line.
func TestDoctorReportsABadConfigLine(t *testing.T) {
	c := newCheckout(t)
	c.writeConf(t, "LEA_PREFIX=lea\nLEA_IMAGE=${X:-y}\n")
	var out, errb bytes.Buffer
	run([]string{"doctor"}, strings.NewReader(""), &out, &errb)
	if len(c.execs) != 1 {
		t.Fatalf("execs=%q stderr=%q", c.execs, errb.String())
	}
	got := c.execs[0]
	sh := filepath.Join(c.root, "libexec", "lea-sh")
	if got[0] != sh || got[1] != sh || got[2] != "doctor" || got[3] != "--env" {
		t.Errorf("exec %q, want %s doctor", got, sh)
	}
	if !hasEnv(got, "LEA_GO_CONFIG_ERROR="+c.conf+" line 2: *") {
		t.Errorf("lea-sh doctor is not told about line 2: %q", got)
	}
}

// The words that only run inside lea (hook, mcp, statusline) are typed on the host only by mistake:
// with a terminal on stdin they go to the launcher, which handles them as the shell launcher's
// default case does (lea_open with them as claude arguments). Claude Code starts them with pipes,
// never a terminal, and inside lea they stay core words.
func TestInsideWordsTypedOnTheHostGoToTheLauncher(t *testing.T) {
	if fileExists("/run/lea/ready") {
		t.Skip("running inside lea")
	}
	term := stdinIsTerminal
	t.Cleanup(func() { stdinIsTerminal = term })
	exe := executable
	t.Cleanup(func() { executable = exe })
	nowhere := filepath.Join(t.TempDir(), "lea") // a binary outside any checkout
	if err := os.WriteFile(nowhere, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	executable = func() (string, error) { return nowhere, nil }
	t.Setenv("LEA_BUS_SOCK", "")
	stdinIsTerminal = func(io.Reader) bool { return true }
	for _, w := range []string{"hook", "mcp", "statusline"} {
		var out, errb bytes.Buffer
		if rc := run([]string{w, "claude", "Stop"}, strings.NewReader(""), &out, &errb); rc != 1 ||
			!strings.Contains(errb.String(), "is not in a lea checkout") {
			t.Errorf("%s on the host, from a terminal: rc=%d %q", w, rc, errb.String())
		}
	}
	for _, w := range []string{"deck", "deck-open"} { // run on the host by the launcher and lea's tmux
		if launcherCommand([]string{w}) {
			t.Errorf("%s went to the launcher", w)
		}
	}
	var out, errb bytes.Buffer
	t.Setenv("LEA_BUS_SOCK", "/run/lea/bus/bus.sock")
	if rc := run([]string{"hook", "other", "x"}, strings.NewReader(""), &out, &errb); rc != 0 {
		t.Errorf("hook inside lea, from a terminal: rc=%d %q", rc, errb.String())
	}
	t.Setenv("LEA_BUS_SOCK", "")
	stdinIsTerminal = func(io.Reader) bool { return false }
	if rc := run([]string{"hook", "other", "x"}, strings.NewReader(""), &out, &errb); rc != 0 {
		t.Errorf("hook on the host without a terminal: rc=%d %q", rc, errb.String())
	}
	if launcher.IsTerminal(strings.NewReader("")) {
		t.Error("a reader is not a terminal")
	}
}

// A child started by the launcher must not inherit an ignored SIGINT or SIGTERM (SIG_IGN survives
// exec). The launcher runs here as its own process, through run as main calls it, so a
// signal.Ignore anywhere on that path would show in the child's SigIgn.
func TestLauncherChildrenDoNotInheritIgnoredSignals(t *testing.T) {
	if root := os.Getenv("LEA_TEST_LAUNCHER_ROOT"); root != "" {
		executable = func() (string, error) { return filepath.Join(root, "build", "lea"), nil }
		os.Exit(run([]string{"doctor"}, os.Stdin, os.Stdout, os.Stderr))
	}
	c := newCheckout(t)
	status := filepath.Join(c.root, "child.status")
	sh := "#!/bin/sh\ngrep SigIgn /proc/self/status >\"" + status + "\"\n"
	if err := os.WriteFile(filepath.Join(c.root, "libexec", "lea-sh"), []byte(sh), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestLauncherChildrenDoNotInheritIgnoredSignals$")
	cmd.Env = append(os.Environ(), "LEA_TEST_LAUNCHER_ROOT="+c.root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("launcher: %v\n%s", err, out)
	}
	got, err := os.ReadFile(status)
	if err != nil {
		t.Fatalf("the launcher started no child: %v", err)
	}
	f := strings.Fields(string(got))
	if len(f) != 2 {
		t.Fatalf("SigIgn line %q", got)
	}
	var mask uint64
	for _, r := range f[1] {
		mask = mask<<4 | uint64(strings.IndexRune("0123456789abcdef", r))
	}
	if mask&(1<<(uint(syscall.SIGINT)-1)) != 0 || mask&(1<<(uint(syscall.SIGTERM)-1)) != 0 {
		t.Fatalf("the launcher's child ignores SIGINT or SIGTERM: %s", got)
	}
}
