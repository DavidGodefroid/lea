package launcher

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func claudeHome(t *testing.T, w *world) {
	t.Helper()
	c := w.home + "/.claude"
	os.MkdirAll(c+"/skills/s1", 0o755)
	os.MkdirAll(c+"/hooks", 0o755)
	os.WriteFile(c+"/settings.json", []byte("{}"), 0o644)
	os.WriteFile(c+"/CLAUDE.md", []byte("x"), 0o644)
	os.WriteFile(c+"/.hidden.md", []byte("x"), 0o644)
	os.WriteFile(w.dir+"/elsewhere.md", []byte("x"), 0o644)
	os.Symlink(w.dir+"/elsewhere.md", c+"/linked.md")
	os.Symlink(w.dir, c+"/commands")
}

func TestRunArgsNeverMountControlSockets(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	claudeHome(t, w)
	args, err := w.launcher().runArgs(w.proj, "172.18.0.5")
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(args, "\n")
	for _, bad := range []string{"docker.sock", "daemon.sock", "/tmp/tmux-", "TMUX", "LEA_DAEMON_SOCK", "/var/run/docker", "/run/docker"} {
		if strings.Contains(all, bad) {
			t.Errorf("run args contain %q", bad)
		}
	}
	mustHave := []string{
		"type=bind,src=" + w.core + ",dst=/usr/local/bin/lea,readonly",
		"type=bind,src=" + w.hostDir() + "/run/" + w.name() + ",dst=/run/lea/bus",
		"LEA_BUS_SOCK=/run/lea/bus/bus.sock",
	}
	for _, m := range mustHave {
		if !contains(args, m) {
			t.Errorf("missing %q", m)
		}
	}
	if args[len(args)-1] != "lea:dev" {
		t.Errorf("image last, got %q", args[len(args)-1])
	}
}

func TestClaudeSharedEntriesAreReadOnly(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	claudeHome(t, w)
	args, err := w.launcher().runArgs(w.proj, "172.18.0.5")
	if err != nil {
		t.Fatal(err)
	}
	c := w.home + "/.claude"
	for _, e := range []string{"settings.json", "skills", "hooks", "CLAUDE.md"} {
		if !contains(args, "type=bind,src="+c+"/"+e+",dst="+c+"/"+e+",readonly") {
			t.Errorf("%s not mounted read-only", e)
		}
	}
	for _, a := range args {
		if strings.Contains(a, "linked.md") || strings.Contains(a, ".hidden.md") || strings.Contains(a, c+"/commands") {
			t.Errorf("symlinked or hidden entry mounted: %s", a)
		}
		// The one exception: this projects dir's own transcripts, at the same path.
		transcripts := "type=bind,src=" + c + "/projects/" + Slug(w.proj) + ",dst=" + c + "/projects/" + Slug(w.proj)
		if strings.HasPrefix(a, "type=bind,src="+c+"/") && !strings.HasSuffix(a, ",readonly") && a != transcripts {
			t.Errorf("a ~/.claude entry other than the project's transcripts is writable: %s", a)
		}
	}
	if !strings.Contains(w.errb.String(), "skipped "+c+"/linked.md: it is a symbolic link") {
		t.Errorf("stderr %q", w.errb.String())
	}
}

func TestRunArgsRefuseCommas(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	os.MkdirAll(w.dir+"/a,b", 0o755)
	if _, err := w.launcher().runArgs(w.dir+"/a,b", "172.18.0.5"); err == nil || !strings.Contains(err.Error(), "comma") {
		t.Fatalf("err = %v", err)
	}
}

func TestWaitReadyRemovesAContainerThatNeverGetsReady(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["FAKE_DOCKER_RC_EXEC_LEA_X"] = "1"
	err := w.launcher().waitReady("lea-x")
	if err == nil || !strings.Contains(err.Error(), "did not become ready in 60 s") {
		t.Fatalf("err = %v", err)
	}
	if !w.hasCall("docker [logs] [--tail] [30] [lea-x]") || !w.hasCall("docker [rm] [-f] [lea-x]") {
		t.Fatalf("calls:\n%s", strings.Join(w.calls(), "\n"))
	}
}

func TestShRefusesUnknownCommands(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	err := w.launcher().sh("lea_die", "x")
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 2 {
		t.Fatalf("lea-sh runs only its listed commands: err = %v", err)
	}
	if !strings.Contains(w.errb.String(), "usage: lea-sh") {
		t.Fatalf("stderr %q", w.errb.String())
	}
}

// TestClaudeMdNeverGlobsHome: HOME is a path, not a pattern. With HOME=/x/h[o]me the *.md
// entries come from /x/h[o]me/.claude only, never from /x/home/.claude.
func TestClaudeMdNeverGlobsHome(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	os.MkdirAll(w.home+"/.claude", 0o755)
	os.WriteFile(w.home+"/.claude/notes.md", []byte("x"), 0o644)
	w.home = w.dir + "/h[o]me"
	w.env["HOME"] = w.home
	os.MkdirAll(w.home+"/.claude", 0o755)
	os.WriteFile(w.home+"/.claude/CLAUDE.md", []byte("x"), 0o644)
	os.WriteFile(w.home+"/.claude/B.md", []byte("x"), 0o644)
	args, err := w.launcher().runArgs(w.proj, "172.18.0.5")
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(args, "\n")
	if strings.Contains(all, "notes.md") {
		t.Fatalf("a file outside $HOME/.claude is mounted:\n%s", all)
	}
	c := w.home + "/.claude"
	b, m := strings.Index(all, "src="+c+"/B.md,"), strings.Index(all, "src="+c+"/CLAUDE.md,")
	if b < 0 || m < 0 || b > m {
		t.Fatalf("B.md then CLAUDE.md, read-only:\n%s", all)
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// TestContainerStartSequence: lea_container_start's order — the start snapshot through lea-sh,
// a fresh container, ready, the bus socket bound, the locksql hint.
func TestContainerStartSequence(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	l := w.launcher()
	if err := l.containerStart(w.proj, "172.18.0.5"); err != nil {
		t.Fatalf("%v\n%s", err, w.errb.String())
	}
	var seq []string
	for _, c := range w.calls() {
		switch {
		case strings.HasPrefix(c, "docker [rm] [-f] ["+w.name()+"]"):
			seq = append(seq, "rm")
		case strings.HasPrefix(c, "docker [run] [-d] [--init] [--name] ["+w.name()+"]"):
			seq = append(seq, "run")
		case c == "docker [exec] ["+w.name()+"] [test] [-e] [/run/lea/ready]":
			seq = append(seq, "ready")
		case c == "core [attach-container] ["+w.name()+"]":
			seq = append(seq, "attach")
		}
	}
	if strings.Join(seq, " ") != "rm run ready attach" {
		t.Fatalf("sequence %v\ncalls:\n%s", seq, strings.Join(w.calls(), "\n"))
	}
	if _, err := os.Stat(w.hostDir() + "/scan/" + w.id() + ".start"); err != nil {
		t.Fatalf("no start snapshot: %v", err)
	}
	if !strings.Contains(w.errb.String(), "run locksql (or lea locksql)") {
		t.Fatalf("stderr %q", w.errb.String())
	}
}

func TestContainerStartStopsWhenDockerRunFails(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["FAKE_DOCKER_RC_RUN__D"] = "1"
	if err := w.launcher().containerStart(w.proj, "172.18.0.5"); err == nil {
		t.Fatal("a failed docker run must fail the start")
	}
	if w.hasCall("[test] [-e] [/run/lea/ready]") || w.hasCall("attach-container") {
		t.Fatalf("calls after a failed run:\n%s", strings.Join(w.calls(), "\n"))
	}
}

func TestContainerExecAndAgentsLive(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["TERM"] = ""
	l := w.launcher()
	if err := l.containerExec("lea-x", "/w", "bash", "-l"); err != nil {
		t.Fatal(err)
	}
	if !w.hasCall("docker [exec] [-i] [-u] [" + uidgid() + "] [-e] [TERM=xterm-256color] [-w] [/w] [lea-x] [bash] [-l]") {
		t.Fatalf("calls:\n%s", strings.Join(w.calls(), "\n"))
	}
	if l.agentsLive(w.proj) {
		t.Fatal("no agent listed: not live")
	}
	w.env["FAKE_DOCKER_OUT_EXEC__U"] = "a1"
	if !w.launcher().agentsLive(w.proj) {
		t.Fatal("an agent listed: live")
	}
	if !w.hasCall("docker [exec] [-u] [" + uidgid() + "] [" + w.name() + "] [lea-agent] [ls]") {
		t.Fatalf("calls:\n%s", strings.Join(w.calls(), "\n"))
	}
}

// The bats suites of the retired shell launcher (container.bats, core.bats, claude_mounts.bats)
// asserted the docker run arguments of lea_run_args; they are asserted here on runArgs.

// TestRunArgsContents: the container's network, identity, proxy, podman, state, kube, bus,
// security and git host arguments, and what is never in them.
func TestRunArgsContents(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["LEA_CLIPBOARD"] = "1"
	st := w.data + "/lea/state"
	args, err := w.launcher().runArgs(w.proj, "172.18.0.5")
	if err != nil {
		t.Fatal(err)
	}
	uid := strconv.Itoa(os.Getuid())
	for _, m := range []string{
		"lea-internal-" + w.id(), "LEA_UID=" + uid, "HOME=" + w.home, "HTTPS_PROXY=http://172.18.0.5:3128",
		"NO_PROXY=localhost,127.0.0.1,::1,lea-proxy,172.18.0.5", "LEA_PROXY_IP=172.18.0.5",
		"DOCKER_HOST=unix:///run/user/" + uid + "/podman/podman.sock",
		"type=bind,src=" + w.proj + ",dst=" + w.proj, "type=volume,src=lea-podman-" + w.id() + ",dst=/lea/podman",
		"type=bind,src=" + st + "/ssh,dst=/lea/state/ssh,readonly", "type=bind,src=" + st + "/git,dst=/lea/state/git",
		"type=bind,src=" + st + "/gh,dst=/lea/state/gh",
		"type=bind,src=" + st + "/glab-cli,dst=/lea/state/glab-cli", "type=bind,src=" + st + "/containers,dst=/lea/state/containers",
		"type=bind,src=" + st + "/go,dst=/lea/state/go",
		"type=volume,src=locksql-run,dst=/run/locksql", "10000", "LEA_LOCKSQL=1", "LEA_GIT_SSH=",
		"type=bind,src=" + w.proj + "/.locksql,dst=" + w.proj + "/.locksql,readonly",
		"type=bind,src=" + st + "/kube,dst=" + w.home + "/.kube/lea,readonly", "KUBECONFIG=" + w.home + "/.kube/lea/config",
		"/dev/fuse", "/dev/net/tun", "seccomp=" + repoRoot(t) + "/image/seccomp.json",
		"type=bind,src=" + st + "/clipboard/copy,dst=/lea/clipboard/copy", "WAYLAND_DISPLAY=lea-clipboard",
	} {
		if !contains(args, m) {
			t.Errorf("missing %q", m)
		}
	}
	n := 0
	for _, a := range args {
		if a == "--network" {
			n++
		}
		// --pid and --pid=host share the host's pids; --pids-limit only caps lea's own.
		if a == "--pid" || strings.HasPrefix(a, "--pid=") {
			t.Errorf("argument %q", a)
		}
		for _, bad := range []string{"--net=host", "--network=host", "--ipc", "--privileged", "docker.sock", "--userns", "--uts",
			"src=" + st + ",", "dst=/lea/state,", "src=" + st + "/clipboard,", "dst=" + w.home + "/.kube/config", "apparmor=lea"} {
			if strings.Contains(a, bad) {
				t.Errorf("%q in %q", bad, a)
			}
		}
		if a == "host" || a == "dst=/lea/state" {
			t.Errorf("argument %q", a)
		}
		// The only host-dir mount is this container's own bus directory.
		if strings.Contains(a, w.hostDir()) && a != "type=bind,src="+w.hostDir()+"/run/"+w.name()+",dst=/run/lea/bus" {
			t.Errorf("host-dir mount %q", a)
		}
	}
	if n != 1 {
		t.Errorf("%d --network arguments", n)
	}
	if !isDir(st+"/kube") || !isFifo(st+"/clipboard/copy") || !isDir(w.hostDir()+"/run/"+w.name()) {
		t.Error("the kube dir, the clipboard FIFO and the bus dir are created")
	}
	// AppArmor only when Docker uses it.
	w.env["FAKE_DOCKER_OUT_INFO___FORMAT"] = `["name=apparmor","name=seccomp,profile=builtin"]`
	if args, _ := w.launcher().runArgs(w.proj, "172.18.0.5"); !contains(args, "apparmor=lea") {
		t.Error("no apparmor=lea under AppArmor")
	}
	// A regular file at the FIFO path is replaced by a FIFO; LEA_CLIPBOARD=0 binds nothing.
	os.Remove(st + "/clipboard/copy")
	os.WriteFile(st+"/clipboard/copy", []byte("x"), 0o600)
	if _, err := w.launcher().runArgs(w.proj, "172.18.0.5"); err != nil || !isFifo(st+"/clipboard/copy") {
		t.Errorf("FIFO not recreated: %v", err)
	}
	w.env["LEA_CLIPBOARD"] = "0"
	args, _ = w.launcher().runArgs(w.proj, "172.18.0.5")
	if s := strings.Join(args, "\n"); strings.Contains(s, "/lea/clipboard") || strings.Contains(s, "WAYLAND_DISPLAY") {
		t.Error("LEA_CLIPBOARD=0 still binds the clipboard")
	}
}

// A symlink planted on the kube state dir, the clipboard dir or the bus directory refuses the
// start, and nothing is written through it.
func TestRunArgsRefusePlantedSymlinks(t *testing.T) {
	for _, rel := range []string{"state/kube", "state/clipboard", "host/run/NAME"} {
		w := newWorld(t, repoRoot(t))
		w.env["LEA_CLIPBOARD"] = "1"
		p := w.data + "/lea/" + strings.ReplaceAll(rel, "NAME", w.name())
		os.MkdirAll(filepath.Dir(p), 0o755)
		out := w.dir + "/out"
		os.MkdirAll(out, 0o755)
		os.Symlink(out, p)
		if _, err := w.launcher().runArgs(w.proj, "172.18.0.5"); err == nil || !strings.Contains(err.Error(), p+" is a symbolic link") {
			t.Errorf("%s: err = %v", rel, err)
		}
		if ents, _ := os.ReadDir(out); len(ents) != 0 {
			t.Errorf("%s: written through the symlink", rel)
		}
	}
}

// The user name passed to the container is a portable login name, else u<uid>.
func TestContainerUser(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	id := w.dir + "/bin/id"
	fallback := "u" + strconv.Itoa(os.Getuid())
	for name, want := range map[string]string{"alice": "alice", "_svc-1": "_svc-1", "John.Doe": fallback, "1abc": fallback,
		"Alice": fallback, "a.b": fallback, "a b": fallback, "abcdefghijklmnopqrstuvwxyz0123456789": fallback} {
		os.WriteFile(id, []byte("#!/bin/sh\n[ \"$1\" = -un ] && { printf '%s\\n' '"+name+"'; exit 0; }\nexec /usr/bin/id \"$@\"\n"), 0o755)
		if got := w.launcher().containerUser(); got != want {
			t.Errorf("%q: %q, want %q", name, got, want)
		}
	}
	os.WriteFile(id, []byte("#!/bin/sh\n[ \"$1\" = -un ] && { printf 'alice\\n'; exit 0; }\nexec /usr/bin/id \"$@\"\n"), 0o755)
	if args, _ := w.launcher().runArgs(w.proj, "172.18.0.5"); !contains(args, "LEA_USER=alice") {
		t.Error("LEA_USER is the container user")
	}
}

// TestClaudeMountsContents: ~/.claude inside lea is a private directory per projects dir, with the
// allow-list of host entries read-only, this projects dir's transcripts and lea's own .claude.json
// read-write, and nothing else; symlinks planted on its paths are refused.
func TestClaudeMountsContents(t *testing.T) {
	setup := func() (*world, string, string, string) {
		w := newWorld(t, repoRoot(t))
		w.proj = w.dir + "/my projects"
		os.MkdirAll(w.proj, 0o755)
		st, c := w.data+"/lea/state", w.home+"/.claude"
		for _, d := range []string{"skills", "plugins", "projects/-other"} {
			os.MkdirAll(c+"/"+d, 0o755)
		}
		for f, s := range map[string]string{"settings.json": "{}", "CLAUDE.md": "x", ".credentials.json": "secret", "history.jsonl": "h"} {
			os.WriteFile(c+"/"+f, []byte(s), 0o600)
		}
		return w, st, c, st + "/claude/" + ID(w.proj)
	}
	w, st, c, priv := setup()
	slug := Slug(w.proj)
	m, err := w.launcher().claudeMounts(w.home, w.proj, st)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"type=bind,src=" + priv + ",dst=" + c,
		"type=bind,src=" + c + "/settings.json,dst=" + c + "/settings.json,readonly",
		"type=bind,src=" + c + "/skills,dst=" + c + "/skills,readonly", "type=bind,src=" + c + "/plugins,dst=" + c + "/plugins,readonly",
		"type=bind,src=" + c + "/CLAUDE.md,dst=" + c + "/CLAUDE.md,readonly",
		"type=bind,src=" + c + "/projects/" + slug + ",dst=" + c + "/projects/" + slug,
		"type=bind,src=" + st + "/claude.json,dst=" + w.home + "/.claude.json"} {
		if !contains(m, want) {
			t.Errorf("missing %q", want)
		}
	}
	rw := 0
	for _, a := range m {
		if strings.HasPrefix(a, "type=") && !strings.HasSuffix(a, ",readonly") {
			rw++
		}
		for _, bad := range []string{".credentials.json", "history.jsonl", "-other", "/hooks", "/output-styles"} {
			if strings.Contains(a, bad) {
				t.Errorf("%q mounted: %s", bad, a)
			}
		}
	}
	if rw != 4 {
		t.Errorf("%d read-write mounts, want the private base, plugins/data, the transcripts and claude.json", rw)
	}
	if !isDir(priv+"/skills") || !isRegular(priv+"/settings.json") || !isDir(c+"/projects/"+slug) {
		t.Error("mount points and the transcripts dir are created")
	}
	other := w.dir + "/other"
	os.MkdirAll(other, 0o755)
	if m2, _ := w.launcher().claudeMounts(w.home, other, st); !contains(m2, "type=bind,src="+st+"/claude/"+ID(other)+",dst="+c) || ID(other) == ID(w.proj) {
		t.Error("two projects dirs share a private base")
	}

	// A symlinked read-only entry on the host is skipped with a warning.
	w, st, c, _ = setup()
	os.MkdirAll(w.dir+"/dotfiles/agents", 0o755)
	os.Symlink(w.dir+"/dotfiles/agents", c+"/agents")
	if m, err := w.launcher().claudeMounts(w.home, w.proj, st); err != nil || strings.Contains(strings.Join(m, "\n"), "src="+c+"/agents") ||
		!strings.Contains(w.errb.String(), "skipped "+c+"/agents") {
		t.Errorf("symlinked entry: %v %q", err, w.errb.String())
	}

	// Planted symlinks are refused, nothing written through them.
	for name, plant := range map[string]func(w *world, st, c, priv string) string{
		"claude.json": func(w *world, st, c, priv string) string {
			os.WriteFile(w.dir+"/cj", []byte("{}"), 0o600)
			os.Remove(st + "/claude.json")
			os.Symlink(w.dir+"/cj", st+"/claude.json")
			return st + "/claude.json"
		},
		"private base": func(w *world, st, c, priv string) string {
			os.MkdirAll(st+"/claude", 0o755)
			os.MkdirAll(w.dir+"/out", 0o755)
			os.Symlink(w.dir+"/out", priv)
			return priv
		},
		"placeholder": func(w *world, st, c, priv string) string {
			os.MkdirAll(priv, 0o755)
			os.Symlink(w.dir+"/planted", priv+"/settings.json")
			return priv + "/settings.json"
		},
		"transcripts": func(w *world, st, c, priv string) string {
			os.MkdirAll(w.dir+"/out", 0o755)
			os.Symlink(w.dir+"/out", c+"/projects/"+Slug(w.proj))
			return c + "/projects/" + Slug(w.proj)
		},
	} {
		w, st, c, priv := setup()
		p := plant(w, st, c, priv)
		if _, err := w.launcher().claudeMounts(w.home, w.proj, st); err == nil || !strings.Contains(err.Error(), p+" is a symbolic link") {
			t.Errorf("%s: err = %v", name, err)
		}
		if ents, _ := os.ReadDir(w.dir + "/out"); len(ents) != 0 || exists(w.dir+"/planted") {
			t.Errorf("%s: written through the symlink", name)
		}
	}
}

// TestClaudePluginsDataIsWritable: ~/.claude/plugins stays read-only, but its data/ (where Claude Code
// mkdirs each plugin's CLAUDE_PLUGIN_DATA) is a private read-write bind laid over it (#90).
func TestClaudePluginsDataIsWritable(t *testing.T) {
	setup := func() (*world, string, string, string) {
		w := newWorld(t, repoRoot(t))
		st, c := w.data+"/lea/state", w.home+"/.claude"
		os.MkdirAll(c+"/plugins", 0o755)
		return w, st, c, st + "/claude/" + ID(w.proj)
	}
	index := func(m []string, s string) int {
		for i, a := range m {
			if a == s {
				return i
			}
		}
		return -1
	}
	for _, hostHasData := range []bool{true, false} {
		w, st, c, priv := setup()
		if hostHasData {
			os.MkdirAll(c+"/plugins/data/caveman-caveman", 0o755)
		}
		m, err := w.launcher().claudeMounts(w.home, w.proj, st)
		if err != nil {
			t.Fatal(err)
		}
		ro := index(m, "type=bind,src="+c+"/plugins,dst="+c+"/plugins,readonly")
		rw := index(m, "type=bind,src="+priv+"/plugins-data,dst="+c+"/plugins/data")
		if ro < 0 || rw < 0 || rw < ro {
			t.Errorf("host data %v: plugins read-only at %d, plugins/data read-write at %d:\n%s", hostHasData, ro, rw, strings.Join(m, "\n"))
		}
		if !isDir(priv+"/plugins-data") || !isDir(c+"/plugins/data") {
			t.Errorf("host data %v: the private source and the mount point are created", hostHasData)
		}
	}

	// No plugins on the host: nothing to lay over.
	w, st, c, _ := setup()
	os.RemoveAll(c + "/plugins")
	if m, err := w.launcher().claudeMounts(w.home, w.proj, st); err != nil || strings.Contains(strings.Join(m, "\n"), "/plugins/data") || exists(c+"/plugins") {
		t.Errorf("no plugins: %v %q", err, m)
	}

	// A symlinked plugins/data is skipped with a warning; the agent still starts.
	w, st, c, _ = setup()
	os.MkdirAll(w.dir+"/out", 0o755)
	os.Symlink(w.dir+"/out", c+"/plugins/data")
	if m, err := w.launcher().claudeMounts(w.home, w.proj, st); err != nil || strings.Contains(strings.Join(m, "\n"), "dst="+c+"/plugins/data") ||
		!strings.Contains(w.errb.String(), c+"/plugins/data") {
		t.Errorf("symlinked plugins/data: %v %q", err, w.errb.String())
	}

	// A mount point lea cannot create is logged; the agent still starts.
	if os.Getuid() != 0 {
		w, st, c, _ = setup()
		os.Chmod(c+"/plugins", 0o555)
		defer os.Chmod(c+"/plugins", 0o755)
		if m, err := w.launcher().claudeMounts(w.home, w.proj, st); err != nil || strings.Contains(strings.Join(m, "\n"), "dst="+c+"/plugins/data") ||
			!strings.Contains(w.errb.String(), c+"/plugins/data") {
			t.Errorf("uncreatable plugins/data: %v %q", err, w.errb.String())
		}
	}
}

// LEA_LOCKSQL=0 leaves no trace of locksql in the container: no volume, no group, no variable.
func TestRunArgsWithoutLocksql(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_LOCKSQL=0\n")
	args, err := w.launcher().runArgs(w.proj, "172.18.0.5")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range args {
		if strings.Contains(strings.ToLower(normalize(w, a)), "locksql") || a == "10000" {
			t.Fatalf("locksql in the run args: %s", a)
		}
	}
}

// The proxy is started with the git SSH endpoints and its label; one started for others is
// replaced, one started for the same is kept.
func TestProxyFollowsTheGitSSHEndpoints(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_GIT_SSH='github.com:22 gitlab.com:22'\n")
	w.env["FAKE_DOCKER_OUT_INSPECT__F"] = "true"
	w.env["FAKE_DOCKER_OUT_INSPECT___FORMAT"] = "github.com:22 gitlab.com:22@" + w.launcher().proxyRules()
	if err := w.launcher().ensureProxy(w.proj); err != nil {
		t.Fatal(err)
	}
	if w.hasCall("docker [run]") {
		t.Fatalf("a proxy for the same endpoints was replaced:\n%s", strings.Join(w.calls(), "\n"))
	}
	w.env["FAKE_DOCKER_OUT_INSPECT___FORMAT"] = "gitlab.com:22"
	if err := w.launcher().ensureProxy(w.proj); err != nil {
		t.Fatal(err)
	}
	if !w.hasCall("[--label] [lea.git-ssh=github.com:22 gitlab.com:22]") || !w.hasCall("[LEA_GIT_SSH=github.com:22 gitlab.com:22]") {
		t.Fatalf("the proxy was not restarted for the new endpoints:\n%s", strings.Join(w.calls(), "\n"))
	}
}

// writeProviders writes the user's providers file and internal-hosts file.
func writeProviders(t *testing.T, w *world, providers, hosts string) {
	t.Helper()
	conf := filepath.Join(w.env["XDG_CONFIG_HOME"], "lea")
	if err := os.MkdirAll(conf, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conf+"/providers", []byte(providers), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conf+"/internal-hosts", []byte(hosts), 0o600); err != nil {
		t.Fatal(err)
	}
	writeUserConf(t, w, "LEA_INTERNAL_HOSTS="+conf+"/internal-hosts\n")
}

// An endpoint's key file is mounted read-only into the proxy only, at /etc/lea/keys/<name>; the
// proxy gets the rendered endpoints file and a lea.providers label; the agents' container never
// sees a key file, and gets its own ~/.codex.
func TestProvidersGoToTheProxyOnly(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	key := filepath.Join(w.dir, "keys", "vllm")
	os.MkdirAll(filepath.Dir(key), 0o700)
	if err := os.WriteFile(key, []byte("sk-secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeProviders(t, w, "lan http://10.0.0.5:11434/v1 model=qwen3\nvllm https://vllm.example.com/v1 key="+key+"\n", "10.0.0.5 11434\n")
	if err := w.launcher().ensureProxy(w.proj); err != nil {
		t.Fatal(err)
	}
	if !w.hasCall("[--mount] [type=bind,src="+key+",dst=/etc/lea/keys/vllm,readonly]") ||
		!w.hasCall("[--mount] [type=bind,src="+w.data+"/lea/proxy/endpoints,dst=/etc/lea/endpoints,readonly]") ||
		!w.hasCall("[--label] [lea.providers=") {
		t.Fatalf("proxy run:\n%s", strings.Join(w.calls(), "\n"))
	}
	b, _ := os.ReadFile(w.data + "/lea/proxy/endpoints")
	if !strings.Contains(string(b), "lan http://10.0.0.5:11434/v1\n") || !strings.Contains(string(b), "vllm https://vllm.example.com/v1 /etc/lea/keys/vllm\n") {
		t.Fatalf("endpoints:\n%s", b)
	}
	for _, c := range append(w.calls(), string(b)) {
		if strings.Contains(c, "sk-secret-value") {
			t.Fatalf("the key leaked: %s", c)
		}
	}
	args, err := w.launcher().runArgs(w.proj, "172.18.0.5")
	if err != nil {
		t.Fatal(err)
	}
	j := strings.Join(args, " ")
	if strings.Contains(j, key) || strings.Contains(j, "/etc/lea/keys") || strings.Contains(j, "sk-secret") {
		t.Fatalf("the agents' container sees a key: %s", j)
	}
	if !strings.Contains(j, "type=bind,src="+w.data+"/lea/state/codex/"+ID(w.proj)+",dst="+w.home+"/.codex") {
		t.Fatalf("no private ~/.codex: %s", j)
	}
	if fi, err := os.Stat(w.data + "/lea/state/codex/" + ID(w.proj)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("~/.codex state: %v %v", fi, err)
	}
}

// A key file replaced by a new file (another inode) changes the proxy's stamp: its single-file
// mount would keep the old key. A key rewritten in place does not.
func TestKeyReplacementChangesTheProxyStamp(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	key := filepath.Join(w.dir, "keys", "vllm")
	os.MkdirAll(filepath.Dir(key), 0o700)
	os.WriteFile(key, []byte("sk-1\n"), 0o600)
	writeProviders(t, w, "vllm https://vllm.example.com/v1 key="+key+"\n", "")
	stamp := func() string {
		fw, err := w.launcher().forwarder(w.proj)
		if err != nil {
			t.Fatal(err)
		}
		return fw.stamp
	}
	s1 := stamp()
	os.WriteFile(key, []byte("sk-2\n"), 0o600) // in place
	if s2 := stamp(); s2 != s1 {
		t.Fatal("a key rewritten in place changed the stamp")
	}
	os.WriteFile(key+".new", []byte("sk-3\n"), 0o600)
	os.Rename(key+".new", key)
	if s3 := stamp(); s3 == s1 {
		t.Fatal("a replaced key file kept the stamp")
	}
}

// A proxy started for other providers is replaced; one for the same is kept.
func TestProxyFollowsTheProviders(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeProviders(t, w, "lan http://10.0.0.5:11434/v1\n", "10.0.0.5 11434\n")
	w.env["FAKE_DOCKER_OUT_INSPECT__F"] = "true"
	w.env["FAKE_DOCKER_OUT_INSPECT___FORMAT"] = ""
	if err := w.launcher().ensureProxy(w.proj); err != nil {
		t.Fatal(err)
	}
	if !w.hasCall("docker [run]") {
		t.Fatalf("the proxy was not restarted for the providers:\n%s", strings.Join(w.calls(), "\n"))
	}
}

// The providers file is checked before anything starts: a key file in the projects directory,
// readable by others, or an upstream on the private network not listed in internal-hosts.
func TestProvidersFileIsChecked(t *testing.T) {
	for name, tc := range map[string]struct{ line, hosts string }{
		"key in projects": {"vllm https://vllm.example.com/v1 key=%P/key\n", ""},
		"key readable":    {"vllm https://vllm.example.com/v1 key=%D/open-key\n", ""},
		"key via a link":  {"vllm https://vllm.example.com/v1 key=%D/to-projects/key\n", ""},
		"key in lea data": {"vllm https://vllm.example.com/v1 key=%A/lea/key\n", ""},
		"key in .claude":  {"vllm https://vllm.example.com/v1 key=%H/.claude/key\n", ""},
		"private":         {"lan http://192.168.1.10:11434/v1\n", ""},
		"loopback":        {"lan http://127.0.0.1:11434/v1\n", "127.0.0.1 8000\n"},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t, repoRoot(t))
			os.WriteFile(w.proj+"/key", []byte("k\n"), 0o600)
			os.WriteFile(w.dir+"/open-key", []byte("k\n"), 0o644)
			os.Symlink(w.proj, w.dir+"/to-projects")
			os.WriteFile(w.data+"/lea/key", []byte("k\n"), 0o600)
			os.MkdirAll(w.home+"/.claude", 0o700)
			os.WriteFile(w.home+"/.claude/key", []byte("k\n"), 0o600)
			line := strings.NewReplacer("%P", w.proj, "%D", w.dir, "%A", w.data, "%H", w.home).Replace(tc.line)
			writeProviders(t, w, line, tc.hosts)
			if err := w.launcher().ensureProxy(w.proj); err == nil || !strings.Contains(err.Error(), "providers file") {
				t.Fatalf("err=%v", err)
			}
			if w.hasCall("docker [run]") {
				t.Fatal("the proxy started")
			}
		})
	}
}

// The proxy is started with the egress allowlist and its label; one started for another list is
// replaced.
func TestProxyFollowsTheEgressAllowlist(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_EGRESS_ALLOW=github.com,proxy.golang.org\n")
	w.env["FAKE_DOCKER_OUT_INSPECT__F"] = "true"
	w.env["FAKE_DOCKER_OUT_INSPECT___FORMAT"] = "|github.com,proxy.golang.org@" + w.launcher().proxyRules()
	if err := w.launcher().ensureProxy(w.proj); err != nil {
		t.Fatal(err)
	}
	if w.hasCall("docker [run]") {
		t.Fatalf("a proxy for the same allowlist was replaced:\n%s", strings.Join(w.calls(), "\n"))
	}
	w.env["FAKE_DOCKER_OUT_INSPECT___FORMAT"] = ""
	if err := w.launcher().ensureProxy(w.proj); err != nil {
		t.Fatal(err)
	}
	if !w.hasCall("[--label] [lea.egress-allow=github.com,proxy.golang.org]") || !w.hasCall("[LEA_EGRESS_ALLOW=github.com,proxy.golang.org]") {
		t.Fatalf("the proxy was not restarted for the new allowlist:\n%s", strings.Join(w.calls(), "\n"))
	}
}

// argAfter is the value that follows flag in args, and whether flag is there.
func argAfter(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

// The lea container has memory, swap and pids limits by default (half of Docker's memory, no
// swap beyond it), so that a runaway nested stack is OOM-killed inside lea instead of freezing
// the host (#97); each one is configurable, and 0 turns it off.
func TestRunArgsResourceLimits(t *testing.T) {
	limits := func(w *world) (mem, swap, pids string, err error) {
		args, err := w.launcher().runArgs(w.proj, "172.18.0.5")
		mem, _ = argAfter(args, "--memory")
		swap, _ = argAfter(args, "--memory-swap")
		pids, _ = argAfter(args, "--pids-limit")
		if len(args) > 0 && args[len(args)-1] != "lea:dev" {
			t.Errorf("the image is not last: %q", args[len(args)-1])
		}
		return
	}
	w := newWorld(t, repoRoot(t))
	w.env["FAKE_DOCKER_OUT_SYSTEM_INFO"] = "34359738368" // 32 GiB
	if m, s, p, err := limits(w); err != nil || m != "16384m" || s != "16384m" || p != "16384" {
		t.Errorf("defaults: --memory %q --memory-swap %q --pids-limit %q, %v", m, s, p, err)
	}
	if b, _ := os.ReadFile(w.log); !strings.Contains(string(b), "docker [system] [info] [--format] [{{.MemTotal}}]") {
		t.Error("the host memory is not asked from Docker")
	}
	for _, c := range []struct{ mem, swap, pids, wm, ws, wp string }{
		{"25%", "", "", "8192m", "8192m", "16384"},
		{"8g", "12g", "4096", "8g", "12g", "4096"},
		{"8g", "-1", "0", "8g", "-1", ""},
		{"0", "4g", "", "", "", "16384"},
	} {
		w := newWorld(t, repoRoot(t))
		w.env["FAKE_DOCKER_OUT_SYSTEM_INFO"] = "34359738368"
		writeUserConf(t, w, "LEA_MEMORY="+c.mem+"\nLEA_MEMORY_SWAP="+c.swap+"\nLEA_PIDS_LIMIT="+c.pids+"\n")
		if m, s, p, err := limits(w); err != nil || m != c.wm || s != c.ws || p != c.wp {
			t.Errorf("%+v: --memory %q --memory-swap %q --pids-limit %q, %v", c, m, s, p, err)
		}
	}
	// Podman has no {{.MemTotal}} (rc 125, "can't evaluate field MemTotal"): {{.Host.MemTotal}}.
	w = newWorld(t, repoRoot(t))
	delete(w.env, "FAKE_DOCKER_OUT_SYSTEM_INFO")
	w.env["FAKE_DOCKER_RC_SYSTEM_INFO_AT____MEMTOTAL__"] = "125"
	w.env["FAKE_DOCKER_OUT_SYSTEM_INFO_AT____HOST_MEMTOTAL__"] = "17179869184" // 16 GiB
	noMeminfo(t)
	if m, s, _, err := limits(w); err != nil || m != "8192m" || s != "8192m" {
		t.Errorf("podman: --memory %q --memory-swap %q, %v, stderr %q", m, s, err, w.errb.String())
	}
	// Neither answers: the host's /proc/meminfo (Linux, where the engine runs on the host).
	w = newWorld(t, repoRoot(t))
	w.env["FAKE_DOCKER_RC_SYSTEM_INFO"] = "125"
	mi := filepath.Join(t.TempDir(), "meminfo")
	if err := os.WriteFile(mi, []byte("MemTotal:       65536000 kB\nMemFree:  1 kB\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := meminfoPath
	meminfoPath = mi
	if m, _, _, err := limits(w); err != nil || m != "32000m" {
		t.Errorf("meminfo: --memory %q, %v, stderr %q", m, err, w.errb.String())
	}
	meminfoPath = old
	// Nothing tells the memory: no memory limit, and a warning naming LEA_MEMORY.
	w = newWorld(t, repoRoot(t))
	w.env["FAKE_DOCKER_RC_SYSTEM_INFO"] = "1"
	noMeminfo(t)
	if m, _, p, err := limits(w); err != nil || m != "" || p != "16384" ||
		!strings.Contains(w.errb.String(), "LEA_MEMORY") {
		t.Errorf("unknown memory: --memory %q --pids-limit %q, %v, stderr %q", m, p, err, w.errb.String())
	}
}

// noMeminfo hides the host's /proc/meminfo from limitArgs for the test.
func noMeminfo(t *testing.T) {
	old := meminfoPath
	meminfoPath = filepath.Join(t.TempDir(), "no-meminfo")
	t.Cleanup(func() { meminfoPath = old })
}

func TestResourceLimitsAreValidated(t *testing.T) {
	for _, line := range []string{"LEA_MEMORY=0%", "LEA_MEMORY=101%", "LEA_MEMORY=lots", "LEA_MEMORY=-1", "LEA_MEMORY=08g",
		"LEA_MEMORY_SWAP=2t", "LEA_MEMORY_SWAP=50%", "LEA_PIDS_LIMIT=-5", "LEA_PIDS_LIMIT=12k", "LEA_PIDS_LIMIT=010"} {
		w := newWorld(t, repoRoot(t))
		writeUserConf(t, w, line+"\n")
		key, _, _ := strings.Cut(line, "=")
		if _, err := LoadConfig(w.getenv, w.root, w.proj); err == nil || !strings.Contains(err.Error(), key+" must be") {
			t.Errorf("%s: err = %v", line, err)
		}
	}
}

// A running proxy started from other rules (lea.rules, a digest of proxy/: an older lea's
// squid.conf, an open egress by default) is replaced; one started from the same rules is kept.
func TestProxyFollowsItsRules(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	rules := w.launcher().proxyRules()
	if len(rules) != 12 {
		t.Fatalf("proxyRules() = %q, want 12 hex digits", rules)
	}
	w.env["FAKE_DOCKER_OUT_INSPECT__F"] = "true"
	w.env["FAKE_DOCKER_OUT_INSPECT___FORMAT"] = "@" + rules
	if err := w.launcher().ensureProxy(w.proj); err != nil {
		t.Fatal(err)
	}
	if w.hasCall("docker [run]") {
		t.Fatalf("a proxy started from the same rules was replaced:\n%s", strings.Join(w.calls(), "\n"))
	}
	w.env["FAKE_DOCKER_OUT_INSPECT___FORMAT"] = ""
	if err := w.launcher().ensureProxy(w.proj); err != nil {
		t.Fatal(err)
	}
	if !w.hasCall("[--label] [lea.rules=" + rules + "]") {
		t.Fatalf("a proxy started from older rules was kept:\n%s", strings.Join(w.calls(), "\n"))
	}
}

// The digest covers the proxy's rule files: a change to one changes it; no proxy/ directory, none.
func TestProxyRulesDigest(t *testing.T) {
	root := t.TempDir()
	l := &Launcher{Root: root}
	if got := l.proxyRules(); got != "" {
		t.Fatalf("no proxy/: proxyRules() = %q, want empty", got)
	}
	if err := os.MkdirAll(filepath.Join(root, "proxy", "errors"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(p, s string) {
		if err := os.WriteFile(filepath.Join(root, "proxy", p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("render-squid-conf.sh", "a")
	write("errors/ERR_LEA_DOH", "b")
	first := l.proxyRules()
	write("errors/ERR_LEA_DOH", "c")
	if second := l.proxyRules(); second == first || second == "" {
		t.Fatalf("an error page change left the digest at %q", second)
	}
	write("render-squid-conf.sh", "a2")
	if third := l.proxyRules(); third == first {
		t.Fatal("a render-squid-conf.sh change left the digest unchanged")
	}
}

// A running proxy started from another image than LEA_PROXY_IMAGE is now (a rebuilt or updated
// image: its rules are copied in) is replaced; when the image cannot be inspected, its labels
// decide alone.
func TestProxyFollowsItsImage(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	stamp := "@" + w.launcher().proxyRules()
	w.env["FAKE_DOCKER_OUT_INSPECT__F"] = "true"
	w.env["FAKE_DOCKER_OUT_IMAGE_INSPECT"] = "sha256:new"
	w.env["FAKE_DOCKER_OUT_INSPECT___FORMAT"] = stamp + "!sha256:new"
	if err := w.launcher().ensureProxy(w.proj); err != nil {
		t.Fatal(err)
	}
	if w.hasCall("docker [run]") {
		t.Fatalf("a proxy on the current image was replaced:\n%s", strings.Join(w.calls(), "\n"))
	}
	// Podman writes the container's image ID without the sha256: prefix.
	w.env["FAKE_DOCKER_OUT_INSPECT___FORMAT"] = stamp + "!new"
	if err := w.launcher().ensureProxy(w.proj); err != nil {
		t.Fatal(err)
	}
	if w.hasCall("docker [run]") {
		t.Fatalf("a proxy on the current image, written without sha256:, was replaced:\n%s", strings.Join(w.calls(), "\n"))
	}
	w.env["FAKE_DOCKER_RC_IMAGE_INSPECT"] = "1"
	w.env["FAKE_DOCKER_OUT_INSPECT___FORMAT"] = stamp + "!sha256:old"
	if err := w.launcher().ensureProxy(w.proj); err != nil {
		t.Fatal(err)
	}
	if w.hasCall("docker [run]") {
		t.Fatalf("a proxy was replaced although its image could not be compared:\n%s", strings.Join(w.calls(), "\n"))
	}
	w.env["FAKE_DOCKER_RC_IMAGE_INSPECT"] = "0"
	if err := w.launcher().ensureProxy(w.proj); err != nil {
		t.Fatal(err)
	}
	if !w.hasCall("docker [run]") {
		t.Fatalf("a proxy on an older image was kept:\n%s", strings.Join(w.calls(), "\n"))
	}
}

// The locksql console's own directory is read-only for the agents: a mount point cannot be
// removed, renamed or replaced, so no agent swaps it for a symbolic link that the console's bind
// mount would follow on the host (#121). Made when missing; a link or a file is refused.
func TestRunArgsLocksqlDirReadOnly(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	d := w.proj + "/.locksql"
	ro := "type=bind,src=" + d + ",dst=" + d + ",readonly"
	args, err := w.launcher().runArgs(w.proj, "172.18.0.5")
	if err != nil || !contains(args, ro) {
		t.Fatalf("missing %q: %v", ro, err)
	}
	if fi, err := os.Lstat(d); err != nil || !fi.IsDir() {
		t.Fatalf("%s not made: %v", d, err)
	}
	if err := os.Remove(d); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(w.home, d); err != nil {
		t.Fatal(err)
	}
	if _, err := w.launcher().runArgs(w.proj, "172.18.0.5"); err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("a symbolic link: %v", err)
	}
	if err := os.Remove(d); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(d, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.launcher().runArgs(w.proj, "172.18.0.5"); err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("a file: %v", err)
	}
	// Without locksql, no mount and nothing made.
	if err := os.Remove(d); err != nil {
		t.Fatal(err)
	}
	w.writeConf("lea.conf", "LEA_LOCKSQL=0\n")
	args, err = w.launcher().runArgs(w.proj, "172.18.0.5")
	if err != nil || contains(args, ro) {
		t.Fatalf("locksql off: %v %q", err, args)
	}
	if _, err := os.Lstat(d); !os.IsNotExist(err) {
		t.Fatalf("locksql off made %s: %v", d, err)
	}
}

// The lea container's CPUs are limited too (#132): 75% of the engine's CPUs by default, at least
// 2, a number of CPUs or a share; 0 turns it off.
func TestRunArgsCPULimit(t *testing.T) {
	cpus := func(w *world) (string, bool) {
		args, err := w.launcher().runArgs(w.proj, "172.18.0.5")
		if err != nil {
			t.Fatal(err)
		}
		return argAfter(args, "--cpus")
	}
	for _, c := range []struct{ conf, ncpu, want string }{
		{"", "22", "16.5"},
		{"", "16", "12"},
		{"", "2", "2"},
		{"", "1", "1"},
		{"LEA_CPUS=50%\n", "22", "11"},
		{"LEA_CPUS=10%\n", "8", "2"},
		{"LEA_CPUS=6\n", "22", "6"},
		{"LEA_CPUS=1.5\n", "22", "1.5"},
		{"LEA_CPUS=0\n", "22", ""},
	} {
		w := newWorld(t, repoRoot(t))
		w.env["FAKE_DOCKER_OUT_SYSTEM_INFO_AT____NCPU__"] = c.ncpu
		writeUserConf(t, w, c.conf)
		if got, _ := cpus(w); got != c.want {
			t.Errorf("%q on %s CPUs: --cpus %q, want %q", c.conf, c.ncpu, got, c.want)
		}
	}
	// Podman has no {{.NCPU}}: {{.Host.CPUs}}.
	w := newWorld(t, repoRoot(t))
	w.env["FAKE_DOCKER_RC_SYSTEM_INFO_AT____NCPU__"] = "125"
	w.env["FAKE_DOCKER_OUT_SYSTEM_INFO_AT____HOST_CPUS__"] = "8"
	if got, _ := cpus(w); got != "6" {
		t.Errorf("podman: --cpus %q, want 6", got)
	}
	// Neither answers: the CPUs this process sees.
	w = newWorld(t, repoRoot(t))
	w.env["FAKE_DOCKER_RC_SYSTEM_INFO"] = "125"
	old := hostCPUs
	hostCPUs = func() int { return 4 }
	defer func() { hostCPUs = old }()
	if got, _ := cpus(w); got != "3" {
		t.Errorf("host CPUs: --cpus %q, want 3", got)
	}
}

func TestCPULimitIsValidated(t *testing.T) {
	for _, line := range []string{"LEA_CPUS=0%", "LEA_CPUS=101%", "LEA_CPUS=lots", "LEA_CPUS=-1", "LEA_CPUS=06", "LEA_CPUS=1.555", "LEA_CPUS=1."} {
		w := newWorld(t, repoRoot(t))
		writeUserConf(t, w, line+"\n")
		if _, err := LoadConfig(w.getenv, w.root, w.proj); err == nil || !strings.Contains(err.Error(), "LEA_CPUS must be") {
			t.Errorf("%s: err = %v", line, err)
		}
	}
	for _, line := range []string{"LEA_CPUS=0", "LEA_CPUS=0.5", "LEA_CPUS=12", "LEA_CPUS=100%", "LEA_CPUS=2.25"} {
		w := newWorld(t, repoRoot(t))
		writeUserConf(t, w, line+"\n")
		if _, err := LoadConfig(w.getenv, w.root, w.proj); err != nil {
			t.Errorf("%s: %v", line, err)
		}
	}
}

// A container started by an older lea, or with other limits, gets the configured ones at the
// next lea without a restart (#132: a container started before #122 ran without any limit). The
// CPUs only throttle: always applied. Memory and pids are lowered only with headroom (usage under
// 80% of the new limit), never into an OOM kill or a fork failure of the agents' work. A refusal
// of the engine is remembered for that container: no retry, no warning at every lea.
func TestRunningContainerGetsTheConfiguredLimits(t *testing.T) {
	// inspect: Id NanoCpus Memory MemorySwap PidsLimit; usage: memory.current and pids.current.
	limitsWorld := func(conf, inspect, usage string) *world {
		w := newWorld(t, repoRoot(t))
		running(w)
		w.env["FAKE_DOCKER_OUT_SYSTEM_INFO_AT____NCPU__"] = "16"
		w.env["FAKE_DOCKER_OUT_SYSTEM_INFO_AT____MEMTOTAL__"] = "34359738368" // 32 GiB
		w.env["FAKE_DOCKER_OUT_INSPECT___FORMAT_AT_"+fakeKey(w.name())] = inspect
		w.env["FAKE_DOCKER_OUT_EXEC_"+fakeKey(w.name())+"_AT__SYS_FS_CGROUP_PIDS_CURRENT"] = usage
		writeUserConf(t, w, conf)
		return w
	}
	updates := func(w *world) []string {
		var u []string
		for _, c := range w.calls() {
			if strings.HasPrefix(c, "docker [update]") {
				u = append(u, c)
			}
		}
		return u
	}
	up := func(w *world) {
		t.Helper()
		l := w.launcher()
		u, err := l.up(w.proj)
		if err != nil {
			t.Fatal(err)
		}
		l.unlock(u.sid)
	}
	const gib = "1073741824"
	// No limits at all (NanoCpus=0, Memory=0), 2 GiB and 300 processes in use: every limit.
	w := limitsWorld("", "c1 0 0 0 0", "2147483648\n300")
	up(w)
	want := "docker [update] [--cpus] [12] [--memory] [16384m] [--memory-swap] [16384m] [--pids-limit] [16384] [" + w.name() + "]"
	if u := updates(w); len(u) != 1 || u[0] != want {
		t.Fatalf("updates %q, want %q", u, want)
	}
	if !w.hasCall("docker [exec] [" + w.name() + "] [cat] [/sys/fs/cgroup/memory.current] [/sys/fs/cgroup/pids.current]") {
		t.Fatalf("the usage is not read:\n%s", strings.Join(w.calls(), "\n"))
	}
	if !strings.Contains(w.errb.String(), "applied") {
		t.Errorf("the update is not reported: %q", w.errb.String())
	}
	// 20 GiB in use (the incident): the memory limit (16 GiB) waits for a restart, the CPUs do
	// not; the warning says why and how.
	w = limitsWorld("", "c1 0 0 0 16384", "21474836480\n300")
	up(w)
	if u := updates(w); len(u) != 1 || u[0] != "docker [update] [--cpus] [12] ["+w.name()+"]" {
		t.Fatalf("updates %q", u)
	}
	if e := w.errb.String(); !strings.Contains(e, "--memory 16384m") || !strings.Contains(e, "lea stop") || !strings.Contains(e, "20480 MiB") {
		t.Errorf("warning %q", e)
	}
	// Usage just under 80% of the new limits: applied; at 80% of the pids limit: not.
	w = limitsWorld("", "c1 12000000000 0 0 0", "13421772800\n13107")
	up(w)
	if u := updates(w); len(u) != 1 || u[0] != "docker [update] [--memory] [16384m] [--memory-swap] [16384m] [--pids-limit] [16384] ["+w.name()+"]" {
		t.Fatalf("updates %q", u)
	}
	w = limitsWorld("", "c1 12000000000 17179869184 17179869184 0", "1\n13108")
	up(w)
	if u := updates(w); len(u) != 0 {
		t.Fatalf("updates %q, want none", u)
	}
	if e := w.errb.String(); !strings.Contains(e, "--pids-limit 16384") || !strings.Contains(e, "13108 processes") {
		t.Errorf("warning %q", e)
	}
	// Usage unknown (no cgroup files): memory and pids are not lowered, the CPUs are.
	w = limitsWorld("", "c1 0 0 0 0", "")
	w.env["FAKE_DOCKER_RC_EXEC_"+fakeKey(w.name())+"_AT__SYS_FS_CGROUP_PIDS_CURRENT"] = "1"
	up(w)
	if u := updates(w); len(u) != 1 || u[0] != "docker [update] [--cpus] [12] ["+w.name()+"]" {
		t.Fatalf("updates %q", u)
	}
	// Raising a limit needs no headroom: 15 GiB in use, 8 GiB limit, 16 GiB wanted.
	w = limitsWorld("", "c1 12000000000 8589934592 8589934592 16384", "16106127360\n10")
	up(w)
	if u := updates(w); len(u) != 1 || u[0] != "docker [update] [--memory] [16384m] [--memory-swap] [16384m] ["+w.name()+"]" {
		t.Fatalf("updates %q", u)
	}
	// Docker answers <nil> for a container without a pids limit.
	w = limitsWorld("", "c1 12000000000 17179869184 17179869184 <nil>", gib+"\n10")
	up(w)
	if u := updates(w); len(u) != 1 || u[0] != "docker [update] [--pids-limit] [16384] ["+w.name()+"]" {
		t.Fatalf("updates %q", u)
	}
	// The limits already hold: nothing to do, the usage is not even read.
	w = limitsWorld("", "c1 12000000000 17179869184 17179869184 16384", gib+"\n10")
	up(w)
	if u := updates(w); len(u) != 0 || w.hasCall("docker [exec] ["+w.name()+"] [cat]") {
		t.Fatalf("updates %q", u)
	}
	// A limit turned off (0) is not taken away from a running container: a restart does.
	w = limitsWorld("LEA_CPUS=0\nLEA_MEMORY=0\nLEA_PIDS_LIMIT=0\n", "c1 12000000000 17179869184 17179869184 16384", gib+"\n10")
	up(w)
	if u := updates(w); len(u) != 0 {
		t.Fatalf("updates %q, want none", u)
	}
	// An engine that cannot update (rootless Podman without the cpu controller): a warning that
	// says how to apply them, and the session goes on. The next lea neither retries nor warns
	// again for that container; another container (a new Id) is tried again.
	w = limitsWorld("", "c1 0 0 0 2048", gib+"\n10")
	w.env["FAKE_DOCKER_RC_UPDATE___CPUS"] = "125"
	up(w)
	if e := w.errb.String(); !strings.Contains(e, "lea stop") || !strings.Contains(e, "--cpus 12") {
		t.Errorf("warning %q", e)
	}
	w.errb.Reset()
	os.Remove(w.log)
	up(w)
	if u := updates(w); len(u) != 0 || strings.Contains(w.errb.String(), "limits") {
		t.Fatalf("retried: updates %q, stderr %q", u, w.errb.String())
	}
	w.env["FAKE_DOCKER_OUT_INSPECT___FORMAT_AT_"+fakeKey(w.name())] = "c2 0 0 0 2048"
	up(w)
	if u := updates(w); len(u) != 1 {
		t.Fatalf("a new container is not tried: updates %q", u)
	}
	// An inspect that does not answer five fields (an engine without these fields) is left alone.
	w = limitsWorld("", "", gib+"\n10")
	up(w)
	if u := updates(w); len(u) != 0 {
		t.Fatalf("updates %q, want none", u)
	}
}
