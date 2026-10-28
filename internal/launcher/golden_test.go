package launcher

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The Go launcher's behaviour is locked by golden files under testdata/golden, recorded from the
// last green run of the parity suite against the retired shell launcher (Task 10 of slice 4): the
// commands' calls, lea: lines and output, the docker run arguments and the configuration. Every
// scenario still pins the step it names (exit, want, absent), so a golden file rewritten by
// mistake cannot pass a scenario vacuously. LEA_UPDATE_GOLDEN=1 rewrites the files from the Go
// side; review the diff before committing it.

var updateGolden = os.Getenv("LEA_UPDATE_GOLDEN") == "1"

// goldenPath is the file of one recorded case.
func goldenPath(kind, name string) string {
	return filepath.Join("testdata", "golden", kind+"-"+goldenSlug.ReplaceAllString(strings.ToLower(name), "-")+".txt")
}

var goldenSlug = regexp.MustCompile(`[^a-z0-9]+`)

// hostNeutral replaces what depends on the account the tests run under (uid, gid, login name).
func hostNeutral(s string) string {
	uid, gid := strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid())
	s = strings.ReplaceAll(s, "["+uid+":"+gid+"]", "[$UID:$GID]")
	s = strings.ReplaceAll(s, "LEA_UID="+uid, "LEA_UID=$UID")
	s = strings.ReplaceAll(s, "LEA_GID="+gid, "LEA_GID=$GID")
	s = strings.ReplaceAll(s, "/run/user/"+uid+"/", "/run/user/$UID/")
	s = strings.ReplaceAll(s, "/run/user/"+uid+"]", "/run/user/$UID]")
	s = strings.ReplaceAll(s, "/run/user/"+uid+"\n", "/run/user/$UID\n")
	return leaUser.ReplaceAllString(s, "LEA_USER=$$USER")
}

var leaUser = regexp.MustCompile(`LEA_USER=[a-z_][a-z0-9_-]*`)

// checkGolden compares got with the recorded file, or records it under LEA_UPDATE_GOLDEN=1.
func checkGolden(t *testing.T, kind, name, got string) {
	t.Helper()
	p := goldenPath(kind, name)
	if updateGolden {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (record it with LEA_UPDATE_GOLDEN=1)", err)
	}
	if string(want) != got {
		t.Fatalf("%s differs from the recorded behaviour\nrecorded:\n%s\nnow:\n%s\nfirst difference:\n%s", p, want, got, firstLineDiff(string(want), got))
	}
}

// firstLineDiff is the first line where a and b part, in full.
func firstLineDiff(a, b string) string {
	x, y := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(x) || i < len(y); i++ {
		var p, q string
		if i < len(x) {
			p = x[i]
		}
		if i < len(y) {
			q = y[i]
		}
		if p != q {
			return "recorded: " + p + "\nnow:      " + q
		}
	}
	return "(none)"
}

// golden is one command's recorded behaviour: exit status, calls, lea: lines, stdout.
func (p commandSide) golden() string {
	return hostNeutral("status " + strconv.Itoa(p.status) + "\n-- calls\n" + p.calls + "\n-- lea\n" + p.lea + "-- stdout\n" + p.stdout)
}

// TestConfigMatchesTheGoldenFiles: LoadConfig resolves each case as lea_load_config did when the
// cases were recorded (every exported key, or the error).
func TestConfigMatchesTheGoldenFiles(t *testing.T) {
	cases := []struct {
		name, user string
		env        map[string]string
	}{
		{"defaults", "", nil},
		{"user file", "LEA_IMAGE='registry.example.com/lea:1'\nLEA_PROXY_IMAGE=lea-proxy:dev\n", nil},
		{"quotes written by lea_user_config_set", "LEA_PROJECTS_DIR='/srv/it'\\''s'\n", map[string]string{"LEA_PROJECTS_DIR": ""}},
		{"environment wins for protected keys", "LEA_APPROVALS=pane\nLEA_APPROVAL_TIMEOUT=5\n", map[string]string{"LEA_APPROVALS": "deck", "LEA_APPROVAL_TIMEOUT": ""}},
		{"files win over the environment otherwise", "LEA_PREFIX=x\n", map[string]string{"LEA_PREFIX": "y", "LEA_INTERNAL_HOSTS": "/etc/h"}},
		{"export, comments, blank lines", "# c\n\nexport LEA_OTEL_UPSTREAM=otel.example.com:4317 # trailing\n", nil},
		{"known hosts follow XDG_CONFIG_HOME", "", map[string]string{"LEA_GITLAB_KNOWN_HOSTS": ""}},
		{"home expansion", "LEA_IMAGE=~/i\nLEA_PROXY_IMAGE=\"$HOME/p\"\nLEA_REGISTRY=${HOME}\n", nil},
		{"leading zero", "LEA_APPROVAL_TIMEOUT=060\n", nil},
	}
	root := repoRoot(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, root)
			delete(w.env, "LEA_PROJECTS_DIR")
			for k, v := range tc.env {
				if v == "" {
					delete(w.env, k)
				} else {
					w.env[k] = v
				}
			}
			if tc.user != "" {
				writeUserConf(t, w, tc.user)
			}
			c, err := LoadConfig(w.getenv, root, w.proj)
			if err != nil {
				checkGolden(t, "config", tc.name, "error "+normalize(w, err.Error())+"\n")
				return
			}
			var b strings.Builder
			for _, k := range ExportedKeys {
				b.WriteString(k + "=" + c.Get(k) + "\n")
			}
			checkGolden(t, "config", tc.name, normalize(w, b.String()))
		})
	}
}

// The shell left in libexec/lib names the scan snapshots with lea_id (host/scan/<id>.start), which
// the launcher checks under ID: the two must agree.
func TestIDAndSlugMatchTheShell(t *testing.T) {
	root := repoRoot(t)
	w := newWorld(t, root)
	for _, p := range []string{"/home/alice/projects", "/srv/a b/c.d", "/x/y_z-1"} {
		cmd := "set -- '" + strings.ReplaceAll(p, "'", `'\''`) + "'; " + `printf '%s %s' "$(lea_id "$1")" "$(lea_slug "$1")"`
		out, _, code := shellEval(t, root, w.environ(), w.dir, cmd)
		if code != 0 || out != ID(p)+" "+Slug(p) {
			t.Errorf("%s: shell %q, Go %q", p, out, ID(p)+" "+Slug(p))
		}
	}
}

func TestShippedConfigParses(t *testing.T) {
	root := repoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "config", "lea.conf"))
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(string(b), "\n") {
		if _, _, _, err := parseLine(line, "/home/u"); err != nil {
			t.Errorf("config/lea.conf line %d: %v", i+1, err)
		}
	}
}

// TestRunArgsMatchTheGoldenFile: the docker run arguments, with the clipboard bridge on, under
// AppArmor and with a ~/.claude holding every kind of entry, as recorded from lea_run_args.
func TestRunArgsMatchTheGoldenFile(t *testing.T) {
	w := newWorld(t, copyRoot(t))
	w.env["LEA_CLIPBOARD"] = "1"
	w.env["FAKE_DOCKER_OUT_INFO___FORMAT"] = `[{"name=apparmor"},{"name=seccomp"}]`
	claudeHome(t, w)
	a, err := w.launcher().runArgs(w.proj, "172.18.0.5")
	if err != nil {
		t.Fatal(err)
	}
	g := strings.Join(a, "\n") + "\n"
	for _, m := range []string{"apparmor=lea", ",dst=/lea/clipboard/copy", "/.claude/skills,readonly", "/.claude/CLAUDE.md,readonly"} {
		if !strings.Contains(g, m) {
			t.Errorf("the arguments lack %q", m)
		}
	}
	checkGolden(t, "run-args", "clipboard on and apparmor", hostNeutral(normalize(w, g)))
}

type commandSide struct {
	status int
	calls  string
	lea    string // the "lea:" lines (and the locksql hint) of stderr
	stdout string
}

type commandScenario struct {
	name string
	args []string // "$W" is the world's directory
	cwd  string   // relative to the projects directory
	// env is added to the world's environment: "$W" and "$ID" are expanded in values, and "$ID"
	// in names becomes the ID as the fake docker spells it in a key (fakeKey).
	env   map[string]string
	unset []string       // removed from it
	setup func(w *world) // runs on each world before the command
	// exit, want and absent pin the step a scenario names: the exit status, substrings that must
	// be in (and must not be in) the calls, the lea: lines and stdout. Every scenario has a want,
	// an absent or a callback, so a run that dies early cannot pass a scenario named after a later
	// step, whatever its golden file says.
	exit         int
	want, absent []string
	goSide       func(t *testing.T, w *world, p commandSide) // runs after the command, when set
}

// fakeKey is s as tests/fakes/docker spells it in a FAKE_DOCKER_* name.
func fakeKey(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r - 'a' + 'A'
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			return r
		}
		return '_'
	}, s)
}

func normalize(w *world, s string) string {
	s = strings.ReplaceAll(s, Slug(w.proj), "$SLUG")
	s = strings.ReplaceAll(s, w.dir, "$W")
	s = strings.ReplaceAll(s, w.root, "$R")
	if r := (&Launcher{Root: w.root}).proxyRules(); r != "" {
		s = strings.ReplaceAll(s, r, "$RULES")
	}
	return strings.ReplaceAll(s, ID(w.proj), "$ID")
}

func leaLines(s string) string {
	var b strings.Builder
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, "lea:") {
			b.WriteString(l + "\n")
		}
	}
	return b.String()
}

// commandSetup builds one world for one side: running container and proxy by default.
func commandSetup(t *testing.T, root string, sc commandScenario) (*world, []string, string) {
	w := newWorld(t, root)
	w.env["FAKE_DOCKER_OUT_NETWORK_INSPECT"] = "true"
	w.env["FAKE_DOCKER_OUT_CONTAINER_INSPECT"] = "172.18.0.5"
	w.env["FAKE_DOCKER_OUT_INSPECT__F"] = "true"
	if r := (&Launcher{Root: w.root}).proxyRules(); r != "" {
		w.env["FAKE_DOCKER_OUT_INSPECT___FORMAT"] = "@" + r
	}
	for k, v := range sc.env {
		v = strings.ReplaceAll(strings.ReplaceAll(v, "$W", w.dir), "$ID", w.id())
		w.env[strings.ReplaceAll(k, "$ID", fakeKey(w.id()))] = v
	}
	for _, k := range sc.unset {
		delete(w.env, k)
	}
	if sc.setup != nil {
		sc.setup(w)
	}
	args := make([]string, len(sc.args))
	for i, a := range sc.args {
		args[i] = strings.ReplaceAll(a, "$W", w.dir)
	}
	cwd := w.proj
	if sc.cwd != "" {
		cwd = filepath.Join(w.proj, sc.cwd) // cleaned: the physical path the launcher sees
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return w, args, cwd
}

func runGoSide(t *testing.T, root string, sc commandScenario) (commandSide, *world) {
	w, args, cwd := commandSetup(t, root, sc)
	l := w.launcher()
	l.Cwd = cwd
	status := l.Main(args)
	return commandSide{status, normalize(w, strings.Join(w.calls(), "\n")), normalize(w, leaLines(w.errb.String())), normalize(w, w.out.String())}, w
}

// symlinkDir replaces the directory p with a symlink to a copy of it elsewhere in the world.
func symlinkDir(t *testing.T, w *world, p string) {
	t.Helper()
	moved := w.dir + "/moved-" + filepath.Base(p)
	if err := os.Rename(p, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, p); err != nil {
		t.Fatal(err)
	}
}

// clipboardOn turns the bridge on in w: a host clipboard command (a fake wl-copy) and the FIFO a
// started container would have; every loop left on that FIFO is killed when the test ends.
func clipboardOn(t *testing.T, w *world) {
	t.Helper()
	w.env["LEA_CLIPBOARD"], w.env["WAYLAND_DISPLAY"] = "1", "wayland-0"
	if err := os.WriteFile(w.dir+"/bin/wl-copy", []byte("#!/bin/sh\ncat >/dev/null\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	fifo := w.data + "/lea/state/clipboard/copy"
	if err := os.MkdirAll(filepath.Dir(fifo), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, pid := range clipboardLoops(fifo) {
			syscall.Kill(-pid, syscall.SIGKILL)
			syscall.Kill(pid, syscall.SIGKILL)
		}
	})
}

// clipboardLoops are the live (non-zombie) host loops reading fifo, found by their command line.
func clipboardLoops(fifo string) []int {
	var pids []int
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || !liveProcess(pid) {
			continue
		}
		b, _ := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if strings.Contains(strings.ReplaceAll(string(b), "\x00", " "), " lea-clipboard "+fifo+" ") {
			pids = append(pids, pid)
		}
	}
	return pids
}

func TestCommandsMatchTheGoldenFiles(t *testing.T) {
	root := copyRoot(t)
	// Only this container is stopped (the proxy runs); the default image is lea:dev.
	stopped := func(extra ...string) map[string]string {
		m := map[string]string{"FAKE_DOCKER_OUT_INSPECT__F_AT_LEA_$ID": "false"}
		for i := 0; i+1 < len(extra); i += 2 {
			m[extra[i]] = extra[i+1]
		}
		return m
	}
	loops := func(n int) func(*testing.T, *world, commandSide) {
		return func(t *testing.T, w *world, _ commandSide) {
			if got := len(clipboardLoops(w.data + "/lea/state/clipboard/copy")); got != n {
				t.Errorf("%d clipboard loops, want %d", got, n)
			}
		}
	}
	open := "tmux [-L] [lea] [-f] [$W/data/lea/host/tmux.conf] [attach-session] [-t] [=lea-$ID]"
	started := "docker [run] [-d] [--init] [--name] [lea-$ID]"
	// The fake core accepts any arguments: what the core does with them (a refusal, --workdir
	// dropped with --repo) is tested in cmd/lea, not here. Scenarios check the arguments it gets.
	scenarios := []commandScenario{
		{name: "deck and the container started", env: stopped(), want: []string{started, "[--fresh]", open}},
		{name: "deck and the container running", want: []string{open}, absent: []string{"docker [run]", "[--fresh]"}},
		{name: "deck from a subdirectory", cwd: "sub", want: []string{"[--workdir] [$W/projects/sub]\n" + open}},
		{name: "deck from outside the projects dir starts at its root", cwd: "../home",
			want: []string{"core [deck-open] [--container] [lea-$ID] [--workdir] [$W/projects]\n" + open}},
		{name: "proxy not running is started", env: map[string]string{"FAKE_DOCKER_OUT_INSPECT__F_AT_LEA_PROXY": "false"},
			want: []string{"docker [rm] [-f] [lea-proxy]", "docker [run] [-d] [--name] [lea-proxy]", open}},
		{name: "networks missing are created", env: map[string]string{"FAKE_DOCKER_RC_NETWORK_INSPECT": "1"},
			want: []string{"docker [network] [create] [--label] [lea.prefix=lea] [lea-egress]",
				"[--internal] [-o] [com.docker.network.bridge.gateway_mode_ipv4=isolated] [lea-internal-$ID]", open}},
		{name: "proxy not on the internal network is connected", env: map[string]string{"FAKE_DOCKER_OUTSEQ_CONTAINER_INSPECT": "|172.18.0.5"},
			want: []string{"docker [network] [connect] [--alias] [lea-proxy] [lea-internal-$ID] [lea-proxy]", open}},
		{name: "proxy connected but without an address", env: map[string]string{"FAKE_DOCKER_OUT_CONTAINER_INSPECT": ""}, exit: 1,
			want:   []string{"docker [network] [connect]", "could not read the address of lea-proxy on lea-internal-$ID (got '')"},
			absent: []string{"attach-container"}},
		{name: "proxy connect fails", env: map[string]string{"FAKE_DOCKER_OUT_CONTAINER_INSPECT": "", "FAKE_DOCKER_RC_NETWORK_CONNECT": "1"},
			exit: 1, want: []string{"could not connect lea-proxy to lea-internal-$ID"}},
		{name: "new with role and name and claude args", args: []string{"new", "-r", "reviewer", "rev", "--", "--model", "x"},
			want:   []string{"[--no-default]", "core [new] [--container] [lea-$ID] [--workdir] [$W/projects] [-r] [reviewer] [rev] [--] [--model] [x]"},
			absent: []string{"attach-session"}},
		{name: "new with a repo", args: []string{"new", "--repo", "r"},
			want:   []string{"core [new] [--container] [lea-$ID] [--workdir] [$W/projects] [--repo] [r]"},
			absent: []string{"cannot be combined"}},
		// lea new passes every argument on, --container included, after its own (as lea_new did).
		{name: "new with a user-typed --container", args: []string{"new", "--container", "x"},
			want: []string{"core [new] [--container] [lea-$ID] [--workdir] [$W/projects] [--container] [x]"}},
		{name: "new and the container started", args: []string{"new"}, env: stopped(),
			want: []string{started, "[--no-default] [--fresh]", "core [new]"}},
		{name: "deck-open fails", env: map[string]string{"FAKE_CORE_RC_deck_open": "1"}, exit: 1,
			want: []string{"core [deck-open]"}, absent: []string{"attach-session"}},
		{name: "new and deck-open fails", args: []string{"new"}, env: map[string]string{"FAKE_CORE_RC_deck_open": "1"}, exit: 1,
			want: []string{"core [deck-open]"}, absent: []string{"core [new]"}},
		{name: "new fails", args: []string{"new"}, env: map[string]string{"FAKE_CORE_RC_new": "4"}, exit: 4, want: []string{"core [new]"}},
		{name: "claude args refused with the deck", args: []string{"--resume"}, exit: 1,
			want: []string{"lea: error: with the deck, claude arguments go to a new agent: lea new -- --resume"}, absent: []string{"docker"}},
		{name: "shell and last session and no agents", args: []string{"shell", "--", "true"},
			want: []string{"[lea-session] [--shell] [true]", "[lea-agent] [ls]", "docker [stop] [lea-$ID]", "docker [network] [rm] [lea-internal-$ID]"}},
		{name: "shell and the container started", args: []string{"shell"}, env: stopped(),
			want: []string{started, "[lea-session] [--shell]\n", "docker [stop] [lea-$ID]"}},
		{name: "shell and agents keep the container", args: []string{"shell", "--", "true"},
			env:  map[string]string{"FAKE_DOCKER_OUT_EXEC__U": "claude-1"},
			want: []string{"[lea-agent] [ls]"}, absent: []string{"docker [stop]", "docker [network] [rm]"}},
		{name: "shell and the command's status is returned", args: []string{"shell", "--", "false"},
			env: map[string]string{"FAKE_DOCKER_RC_EXEC__I": "3"}, exit: 3, want: []string{"[lea-session] [--shell] [false]"}},
		{name: "shell from a subdirectory", args: []string{"shell"}, cwd: "sub",
			want: []string{"[-w] [$W/projects/sub] [lea-$ID] [lea-session] [--shell]"}, absent: []string{"docker [run]"}},
		{name: "shell from outside the projects dir starts at its root", args: []string{"shell"}, cwd: "../home",
			want: []string{"[-w] [$W/projects] [lea-$ID] [lea-session] [--shell]"}},
		// The other session is another live process: the launcher runs in the test's own process.
		{name: "shell while another session is open", args: []string{"shell"}, setup: func(w *world) {
			other := exec.Command("sleep", "30")
			if err := other.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { other.Process.Kill(); other.Wait() })
			d := w.hostDir() + "/sessions/" + w.id()
			os.MkdirAll(d, 0o755)
			os.WriteFile(d+"/"+strconv.Itoa(other.Process.Pid), nil, 0o600)
		}, want: []string{"[lea-session] [--shell]"}, absent: []string{"docker [stop]", "[network] [rm]"}},
		{name: "shell without a projects directory", args: []string{"shell"}, unset: []string{"LEA_PROJECTS_DIR"}, exit: 1,
			want: []string{"lea: error: no projects directory: run 'lea init', or set LEA_PROJECTS_DIR"}, absent: []string{"docker"}},
		{name: "stop without a projects directory", args: []string{"stop"}, unset: []string{"LEA_PROJECTS_DIR"}, exit: 1,
			want: []string{"lea: error: no projects directory"}, absent: []string{"docker"}},
		{name: "new with role and repo and branch and brief", args: []string{"new", "-r", "lead", "--repo", "shop", "--branch", "feat/x", "--brief", "b.md", "rev"},
			want: []string{"core [new] [--container] [lea-$ID] [--workdir] [$W/projects] [-r] [lead] [--repo] [shop] [--branch] [feat/x] [--brief] [b.md] [rev]"}},
		{name: "deck with an old tmux", env: map[string]string{"FAKE_TMUX_VERSION": "tmux 3.1c", "LEA_OS_RELEASE": "$W/os-release"},
			setup: func(w *world) { os.WriteFile(w.dir+"/os-release", []byte("ID=fedora\n"), 0o644) }, exit: 1,
			want:   []string{"lea: error: the deck needs tmux 3.2 or later; found 'tmux 3.1c'. Upgrade it with: sudo dnf install tmux, or build a static one with: make -C \"$R\" tmux-build"},
			absent: []string{"docker", "sudo dnf install tmux]"}},
		{name: "deck without a usable tmux", env: map[string]string{"LEA_TMUX_BIN": "$W/none"}, exit: 1,
			want: []string{"LEA_TMUX_BIN=$W/none is not an executable file"}, absent: []string{"docker", "is not installed"}},
		{name: "stop without a usable tmux", args: []string{"stop"}, env: map[string]string{"LEA_TMUX_BIN": "$W/none"},
			want: []string{"docker [stop] [lea-$ID]"}, absent: []string{"kill-session"}},
		{name: "stop --all past a failing docker stop", args: []string{"stop", "--all"},
			env:  map[string]string{"FAKE_DOCKER_OUT_PS___FILTER": "c1\t\nc2\t", "FAKE_DOCKER_RC_STOP_C1": "1"},
			want: []string{"docker [stop] [c1]\ncore [deck-stop] [--container] [c2]\ndocker [stop] [c2]", "[kill-server]"}},
		{name: "shell old form refused", args: []string{"shell", "$W/projects"}, exit: 2,
			want: []string{"lea: error: lea no longer takes a directory: LEA_PROJECTS_DIR=$W/projects lea"}, absent: []string{"docker"}},
		{name: "stop", args: []string{"stop"},
			want: []string{"core [deck-stop] [--container] [lea-$ID]\ndocker [inspect]", "[kill-session] [-t] [=lea-$ID]", "docker [stop] [lea-$ID]", "docker [network] [rm] [lea-internal-$ID]",
				"lea: WARNING: no start snapshot for $W/projects"},
			absent: []string{"still open"}},
		{name: "stop with an open session", args: []string{"stop"}, setup: func(w *world) {
			d := w.hostDir() + "/sessions/" + w.id()
			os.MkdirAll(d, 0o755)
			os.WriteFile(d+"/"+strconv.Itoa(os.Getpid()), nil, 0o600)
		}, want: []string{"lea: warning: 1 lea session(s) still open on $W/projects; stopping anyway", "docker [stop] [lea-$ID]"}},
		// Not running and no start snapshot: no exit scan (and so no warning about a snapshot).
		{name: "stop when not running", args: []string{"stop"}, env: stopped(),
			want: []string{"docker [stop] [lea-$ID]"}, absent: []string{"no start snapshot"}},
		{name: "stop --all", args: []string{"stop", "--all"}, env: map[string]string{
			"FAKE_DOCKER_OUT_PS___FILTER": "c1\t$W/projects\nc2\t", "FAKE_DOCKER_OUT_NETWORK_LS": "lea-internal-abc\nlea-egress"},
			want: []string{"core [deck-stop] [--container] [c1]\ndocker [stop] [c1]\ncore [deck-stop] [--container] [c2]\ndocker [stop] [c2]",
				"docker [network] [rm] [lea-internal-abc]", "[kill-server]"},
			absent: []string{"[network] [rm] [lea-egress]", "--forget"}},
		{name: "stop --all --forget", args: []string{"stop", "--all", "--forget"}, env: map[string]string{"FAKE_DOCKER_OUT_PS___FILTER": "c1\t"},
			want: []string{"core [deck-stop] [--container] [c1] [--forget]\ndocker [stop] [c1]", "[kill-server]"}},
		{name: "stop --forget", args: []string{"stop", "--forget"},
			want: []string{"core [deck-stop] [--container] [lea-$ID] [--forget]", "docker [stop] [lea-$ID]"}},
		// The daemon cannot record the agents (it prints its own warning): the stop goes on.
		{name: "stop past a failing deck-stop", args: []string{"stop"}, env: map[string]string{"FAKE_CORE_RC_deck_stop": "1"},
			want: []string{"core [deck-stop]", "docker [stop] [lea-$ID]"}},
		{name: "stop with an unknown argument", args: []string{"stop", "--al"}, exit: 2,
			want: []string{"lea: error: usage: lea stop [--all] [--forget]"}, absent: []string{"docker"}},
		{name: "stop old form refused", args: []string{"stop", "$W/projects"}, exit: 2,
			want: []string{"lea no longer takes a directory"}, absent: []string{"docker"}},
		{name: "no projects directory", unset: []string{"LEA_PROJECTS_DIR"}, exit: 1,
			want: []string{"lea: error: no projects directory: run 'lea init', or set LEA_PROJECTS_DIR"}},
		{name: "projects directory missing", env: map[string]string{"LEA_PROJECTS_DIR": "$W/nowhere"}, exit: 1,
			want: []string{"lea: error: LEA_PROJECTS_DIR=$W/nowhere is not a directory"}},
		{name: "projects directory is HOME", env: map[string]string{"LEA_PROJECTS_DIR": "$W/home"}, exit: 1,
			want: []string{"lea: error: $W/home cannot be a projects directory: it is / or contains your home directory"}},
		{name: "projects directory inside the data dir", env: map[string]string{"LEA_PROJECTS_DIR": "$W/data/lea/state"}, exit: 1,
			want: []string{"cannot be a projects directory: it overlaps $W/data/lea"}},
		{name: "symlinked projects directory resolves to its target", env: map[string]string{"LEA_PROJECTS_DIR": "$W/plink"},
			setup: func(w *world) { os.Symlink(w.proj, w.dir+"/plink") },
			want:  []string{"[--workdir] [$W/projects]\n" + open}, absent: []string{"plink"}},
		{name: "symlinked state dir refused", setup: func(w *world) { symlinkDir(t, w, w.data+"/lea/state") }, exit: 1,
			want: []string{"$W/data/lea/state is a symbolic link (remove it, then retry)"}, absent: []string{"docker"}},
		{name: "symlinked host dir refused", setup: func(w *world) { symlinkDir(t, w, w.hostDir()) }, exit: 1,
			want: []string{"$W/data/lea/host is a symbolic link (remove it, then retry)"}, absent: []string{"docker"}},
		{name: "symlinked claude transcripts refused", setup: func(w *world) {
			d := w.home + "/.claude/projects"
			os.MkdirAll(d, 0o755)
			os.Symlink(w.dir, d+"/"+Slug(w.proj))
		}, exit: 1, want: []string{"$W/home/.claude/projects/$SLUG is a symbolic link"}, absent: []string{"docker"}},
		{name: "symlinked session dir refused", args: []string{"shell"}, setup: func(w *world) {
			os.MkdirAll(w.hostDir()+"/sessions", 0o755)
			os.Symlink(w.dir, w.hostDir()+"/sessions/"+w.id())
		}, exit: 1, want: []string{"$W/data/lea/host/sessions/$ID is a symbolic link"}, absent: []string{"docker"}},
		{name: "not initialised", setup: func(w *world) { os.Remove(w.data + "/lea/state/claude.json") }, exit: 1,
			want: []string{"lea: error: lea is not initialised on this workstation: run 'lea init' first"}, absent: []string{"docker"}},
		{name: "old form refused", args: []string{"$W/projects"}, exit: 2,
			want: []string{"lea: error: lea no longer takes a directory"}, absent: []string{"docker"}},
		// The usage goes to stderr, which only the lea: lines of are compared.
		{name: "kube without refresh", args: []string{"kube"}, exit: 2, absent: []string{"kubectl", "lea:"}},
		{name: "help", args: []string{"--help"}, want: []string{"Usage:\n  lea init", "  lea doctor\n"}, absent: []string{"LEA_LAUNCHER"}},
		{name: "lock held by another lea", env: map[string]string{"LEA_LOCK_TIMEOUT": "1"}, setup: func(w *world) {
			os.MkdirAll(w.hostDir()+"/locks/"+w.id(), 0o755)
		}, exit: 1, want: []string{"another lea holds the '$ID' lock (remove $W/data/lea/host/locks/$ID if it is stale)"}},
		{name: "global lock held by another lea", env: stopped("LEA_LOCK_TIMEOUT", "1"), setup: func(w *world) {
			os.MkdirAll(w.hostDir()+"/locks/global", 0o755)
		}, exit: 1, want: []string{"another lea holds the 'global' lock"}, absent: []string{"docker [run]"}},
		// The fake core always answers `agents`: both launchers loop 50 times waiting for the old
		// daemon to stop, then die with the same message after the same 51 calls.
		{name: "daemon records differ and the old daemon never stops", setup: func(w *world) {
			os.WriteFile(w.hostDir()+"/daemon.approvals", []byte("pane 5"), 0o600)
		}, exit: 1, want: []string{"lea: error: the running lea daemon did not stop (kill it, then retry)"}, absent: []string{"docker"}},
		{name: "daemon not running and never starts", env: map[string]string{"FAKE_CORE_RC_agents": "1"}, exit: 1,
			want: []string{"core [agents]\ncore [agents]", "the lea daemon did not start in 5 s"}, absent: []string{"docker", "[daemon]"},
			goSide: daemonStarted},
		{name: "internal network exists but is not internal", env: map[string]string{"FAKE_DOCKER_OUT_NETWORK_INSPECT": "false"}, exit: 1,
			want: []string{"network lea-internal-$ID exists but is not an internal network"}, absent: []string{"lea-proxy"}},
		{name: "docker run fails", env: stopped("FAKE_DOCKER_RC_RUN__D_AT_LEA_DEV", "1"), exit: 1,
			want: []string{started}, absent: []string{"[/run/lea/ready]", "deck-open"}},
		{name: "proxy run fails", env: map[string]string{"FAKE_DOCKER_OUT_INSPECT__F_AT_LEA_PROXY": "false", "FAKE_DOCKER_RC_RUN__D": "1"},
			exit: 1, want: []string{"docker [run] [-d] [--name] [lea-proxy]"}, absent: []string{"attach-container", "[--init]"}},
		{name: "bus attach fails", env: stopped("FAKE_CORE_RC_attach_container", "1"),
			want: []string{"lea: warning: the container's bus socket could not be bound; agents run without lea messaging", open}},
		{name: "deck starts the clipboard loop", setup: func(w *world) { clipboardOn(t, w) },
			want: []string{open}, goSide: loops(1)},
		{name: "no host clipboard command", env: map[string]string{"LEA_CLIPBOARD": "1"},
			setup: func(w *world) {
				os.MkdirAll(w.data+"/lea/state/clipboard", 0o700)
				syscall.Mkfifo(w.data+"/lea/state/clipboard/copy", 0o600)
			},
			want: []string{"lea: no wl-copy (Wayland) or xclip (X11) on the host", open}},
		// On a symlinked pid file (after deck-open) the launcher refuses once, before starting the
		// loop, and does not attach. (The retired shell launcher started the loop, then died on the
		// pid file write: the recorded difference of Task 5.)
		{name: "unsafe clipboard pid file", exit: 1, want: []string{"clipboard.pid is a symbolic link (remove it, then retry)"},
			setup: func(w *world) {
				clipboardOn(t, w)
				os.Symlink(w.dir+"/elsewhere", w.hostDir()+"/clipboard.pid")
			},
			goSide: func(t *testing.T, w *world, p commandSide) {
				if p.status != 1 || strings.Count(p.lea, "clipboard.pid is a symbolic link (remove it, then retry)\n") != 1 {
					t.Errorf("go: status %d\n%s", p.status, p.lea)
				}
				if n := len(clipboardLoops(w.data + "/lea/state/clipboard/copy")); n != 0 {
					t.Errorf("go: %d clipboard loops, want none", n)
				}
				if strings.Contains(p.calls, "attach-session") {
					t.Errorf("go: attached after the refusal:\n%s", p.calls)
				}
			}},
		// No deck session left (the fake tmux fails list-sessions): stop ends the loop.
		{name: "stop kills the clipboard loop", args: []string{"stop"}, env: map[string]string{"FAKE_TMUX_RC": "1"},
			setup: func(w *world) { clipboardOn(t, w); standInLoop(t, w) },
			want:  []string{"docker [stop] [lea-$ID]"}, goSide: loops(0)},
		// A deck session still runs (list-sessions succeeds): the loop stays for its agents.
		{name: "stop keeps the clipboard loop while a deck runs", args: []string{"stop"},
			setup: func(w *world) { clipboardOn(t, w); standInLoop(t, w) },
			want:  []string{"[list-sessions]"}, goSide: loops(1)},
		// ~/.claude/*.md mounts come in byte order (as dash, Debian's /bin/sh, expanded the glob).
		{name: "claude md files in byte order", env: stopped(), setup: func(w *world) {
			os.MkdirAll(w.home+"/.claude", 0o755)
			for _, n := range []string{"b.md", "B.md", "a.md", "_x.md", "Z.md"} {
				os.WriteFile(w.home+"/.claude/"+n, []byte("x"), 0o600)
			}
		}, want: []string{"[type=bind,src=$W/home/.claude/B.md,dst=$W/home/.claude/B.md,readonly] [--mount] [type=bind,src=$W/home/.claude/Z.md"}},
		{name: "update", args: []string{"update"}, setup: fakeRelease,
			env: map[string]string{"FAKE_CURL_DIR": "$W/release", "FAKE_CURL_EFFECTIVE": "https://github.com/DavidGodefroid/lea/releases/tag/v9.9.9"},
			want: []string{"curl [-fsSL] [-o] [$W/data/lea/host/update/install.sh] [https://github.com/DavidGodefroid/lea/releases/download/v9.9.9/install.sh]",
				"cosign [verify-blob] [--bundle] [$W/data/lea/host/update/checksums.txt.sigstore.json]",
				"installer [--version] [v9.9.9] [--no-init]", "docker [image] [inspect] [locksql:",
				"lea: updating this release install to lea v9.9.9 with its installer", "lea: updated; running lea containers keep their image until they stop",
				"curl [-fsSLI] [-o] [/dev/null] [-w] [%{url_effective}] [https://github.com/DavidGodefroid/locksql/releases/latest]"},
			absent: []string{"go-build", "raw.githubusercontent.com"}},
		// The world's root is no release's directory: rollback is for a release install.
		{name: "rollback outside a release install", args: []string{"rollback"}, exit: 1,
			want: []string{"lea: error: lea rollback is for a release install"}, absent: []string{"ln", "curl"}},
		{name: "rollback takes no argument", args: []string{"rollback", "now"}, exit: 2, absent: []string{"lea:"}},
		// The host versions are pinned: a fixed uname -r and os-release, and Docker's components.
		{name: "doctor", args: []string{"doctor"}, exit: 1,
			env: map[string]string{"LEA_OS_RELEASE": "$W/os-release", "FAKE_DOCKER_OUT_VERSION___FORMAT": "Engine=29.4.2\ncontainerd=v2.1.4\nrunc=1.3.0"},
			setup: func(w *world) {
				os.WriteFile(w.dir+"/os-release", []byte("ID=ubuntu\nVERSION_ID=\"24.04\"\n"), 0o644)
				os.WriteFile(w.dir+"/bin/uname", []byte("#!/bin/sh\necho 7.0.0-38-generic\n"), 0o755)
			},
			want: []string{"ok    docker reachable", "ok    Docker Engine 29.4.2 (containerd v2.1.4, runc 1.3.0)",
				"warn  kernel 7.0.0-38-generic: no known minimum for ubuntu 24.04", "FAIL  user config",
				"core [delivery] [--container] [lea-$ID] [--projects] [$W/projects] [check] [--state]"}, absent: []string{"launcher"}},
		// Without a projects directory there is no lea to check delivery for.
		{name: "doctor without a projects dir", args: []string{"doctor"}, exit: 1, env: map[string]string{"LEA_PROJECTS_DIR": "$W/none"},
			want: []string{"ok    docker reachable"}, absent: []string{"core [delivery]"}},
		// lea delivery: the core form, with this projects dir's container and the directory.
		{name: "delivery on", args: []string{"delivery", "on"},
			want: []string{"core [delivery] [--container] [lea-$ID] [--projects] [$W/projects] [on]"}},
		{name: "delivery on --yes", args: []string{"delivery", "on", "--yes", "0123456789abcdef"},
			want: []string{"core [delivery] [--container] [lea-$ID] [--projects] [$W/projects] [on] [--yes] [0123456789abcdef]"}},
		{name: "delivery on --gitlab", args: []string{"delivery", "on", "--gitlab", "git.corp.example", "--yes", "0123456789abcdef"},
			want: []string{"core [delivery] [--container] [lea-$ID] [--projects] [$W/projects] [on] [--gitlab] [git.corp.example] [--yes] [0123456789abcdef]"}},
		{name: "delivery on --yes without a digest", args: []string{"delivery", "on", "--yes"}, exit: 2, absent: []string{"core [delivery]"}},
		{name: "delivery off --yes", args: []string{"delivery", "off", "--yes"}, exit: 2, absent: []string{"core [delivery]"}},
		{name: "delivery without a verb", args: []string{"delivery"}, exit: 2,
			want: []string{"lea: error: usage: lea delivery on [--yes DIGEST] [--gitlab HOST]...|off|status"}, absent: []string{"core [delivery]"}},
		{name: "kube refresh", args: []string{"kube", "refresh"}, exit: 1, env: map[string]string{"LEA_K8S_USER": "dev"},
			want: []string{"kubectl [config] [get-contexts]", "lea: error: no cluster issued a token"}},
		{name: "init without a terminal", args: []string{"init"}, want: []string{"lea: ready. Run: lea"}, absent: []string{"ssh-keygen"}},
		{name: "init with git ssh and no pinned keys", args: []string{"init"}, exit: 1, env: map[string]string{"LEA_GIT_SSH": "github.com:22"},
			want: []string{"no github.com key in", "pin the host keys of github.com"}},
	}

	for _, sc := range scenarios {
		if strings.Contains(sc.name, ",") {
			t.Fatalf("%q: a comma in a test name puts one in t.TempDir(), which the launcher refuses to mount", sc.name)
		}
		if len(sc.want)+len(sc.absent) == 0 && sc.goSide == nil {
			t.Fatalf("%q pins nothing: give it a want, an absent or a callback", sc.name)
		}
		t.Run(sc.name, func(t *testing.T) {
			g, gw := runGoSide(t, root, sc)
			if sc.goSide != nil {
				sc.goSide(t, gw, g)
			}
			all := g.calls + "\n" + g.lea + g.stdout
			if g.status != sc.exit {
				t.Errorf("status %d, want %d", g.status, sc.exit)
			}
			for _, x := range sc.want {
				if !strings.Contains(all, x) {
					t.Errorf("missing %q", x)
				}
			}
			for _, x := range sc.absent {
				if strings.Contains(all, x) {
					t.Errorf("unexpected %q", x)
				}
			}
			if t.Failed() {
				t.Logf("launcher:\n%s", all)
			}
			checkGolden(t, "command", sc.name, g.golden())
		})
	}
}

// fakeRelease is the release lea update downloads, served by the fake curl from $W/release: an
// install.sh (listed in its checksums.txt) that logs its arguments and links ~/.local/bin/lea to
// this world's installation, whose lea-sh then refreshes the images, and a stand-in Sigstore
// bundle the fake cosign accepts.
func fakeRelease(w *world) {
	dir := w.dir + "/release"
	inst := "{ printf installer; for a in \"$@\"; do printf ' [%s]' \"$a\"; done; printf '\\n'; } >>\"$FAKE_LOG\"\n" +
		"mkdir -p \"$HOME/.local/bin\" && ln -sfn '" + w.root + "/bin/lea' \"$HOME/.local/bin/lea\"\n"
	sum := sha256.Sum256([]byte(inst))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		w.t.Fatal(err)
	}
	for name, body := range map[string]string{"install.sh": inst, "checksums.txt": hex.EncodeToString(sum[:]) + "  install.sh\n",
		"checksums.txt.sigstore.json": "{}\n"} {
		if err := os.WriteFile(dir+"/"+name, []byte(body), 0o644); err != nil {
			w.t.Fatal(err)
		}
	}
}

// daemonStarted: the launcher started one daemon (the fake core logs it apart).
func daemonStarted(t *testing.T, w *world, _ commandSide) {
	b, _ := os.ReadFile(w.log + ".daemon")
	if string(b) != "core [daemon]\n" {
		t.Errorf("daemon starts: %q", b)
	}
}

// standInLoop starts a stand-in for the host clipboard loop, with the command line both launchers
// recognise (`sh -c … lea-clipboard <fifo> …`), in a session of its own, and records its pid.
func standInLoop(t *testing.T, w *world) {
	t.Helper()
	fifo := w.data + "/lea/state/clipboard/copy"
	c := exec.Command("setsid", "sh", "-c", "while :; do sleep 1; done", "lea-clipboard", fifo, "x")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	go c.Wait()
	if err := os.WriteFile(w.hostDir()+"/clipboard.pid", []byte(strconv.Itoa(c.Process.Pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(clipboardLoops(fifo)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the stand-in loop does not show its command line")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// liveProcess: pid runs and is not a zombie. A loop is orphaned when its starter ends; in a
// container whose init does not reap (go test as pid 1), the dead loop stays a zombie, which
// kill -0 still reaches.
func liveProcess(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	_, st, _ := strings.Cut(string(b), ") ")
	return !strings.HasPrefix(st, "Z")
}
