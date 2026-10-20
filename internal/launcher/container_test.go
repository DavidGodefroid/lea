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
		for _, bad := range []string{"--net=host", "--network=host", "--pid", "--ipc", "--privileged", "docker.sock", "--userns", "--uts",
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
	if rw != 3 {
		t.Errorf("%d read-write mounts, want the private base, the transcripts and claude.json", rw)
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
	w.env["FAKE_DOCKER_OUT_INSPECT___FORMAT"] = "github.com:22 gitlab.com:22"
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
		"private":         {"lan http://192.168.1.10:11434/v1\n", ""},
		"loopback":        {"lan http://127.0.0.1:11434/v1\n", "127.0.0.1 8000\n"},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t, repoRoot(t))
			os.WriteFile(w.proj+"/key", []byte("k\n"), 0o600)
			os.WriteFile(w.dir+"/open-key", []byte("k\n"), 0o644)
			line := strings.NewReplacer("%P", w.proj, "%D", w.dir).Replace(tc.line)
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
	w.env["FAKE_DOCKER_OUT_INSPECT___FORMAT"] = "|github.com,proxy.golang.org"
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
