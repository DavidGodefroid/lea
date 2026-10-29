package launcher

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// repoRoot is the lea checkout the tests run in (internal/launcher -> ../..).
func repoRoot(t *testing.T) string {
	t.Helper()
	r, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// copyRoot copies the checkout's bin, libexec, config and image into a temporary root, so a test
// can give it a fake build/lea (and a fake libexec/lea-sh) without touching the checkout.
func copyRoot(t *testing.T) string {
	t.Helper()
	src, dst := repoRoot(t), t.TempDir()
	for _, d := range []string{"bin", "libexec", "config", "image"} {
		if _, err := os.Stat(filepath.Join(src, d)); err != nil {
			continue
		}
		err := filepath.WalkDir(filepath.Join(src, d), func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(src, p)
			if e.IsDir() {
				return os.MkdirAll(filepath.Join(dst, rel), 0o755)
			}
			info, err := e.Info()
			if err != nil {
				return err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dst, rel), b, info.Mode().Perm())
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dst, "build"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dst
}

// shellEval runs script in sh after sourcing every libexec/lib/*.sh of root, as libexec/lea-sh
// does, and returns stdout, stderr and the exit status.
func shellEval(t *testing.T, root string, env []string, dir, script string) (string, string, int) {
	t.Helper()
	pre := "set -eu; LEA_ROOT='" + root + "'; export LEA_ROOT; for f in \"$LEA_ROOT\"/libexec/lib/*.sh; do . \"$f\"; done\n"
	c := exec.Command("sh", "-c", pre+script)
	c.Dir, c.Env = dir, env
	var o, e bytes.Buffer
	c.Stdout, c.Stderr = &o, &e
	code := 0
	if err := c.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		code = ee.ExitCode()
	}
	return o.String(), e.String(), code
}

// fakeCore logs its calls; a started daemon logs to $FAKE_LOG.daemon instead, in one write: it
// runs alongside the launcher's polls, and its line would land anywhere among them.
const fakeCore = `#!/bin/sh
if [ "${1:-}" = daemon ]; then printf 'core [daemon]\n' >>"$FAKE_LOG.daemon"; exit 0; fi
{ printf 'core'; for a in "$@"; do printf ' [%s]' "$a"; done; printf '\n'; } >>"$FAKE_LOG"
case ${1:-} in launcher-check) echo go launcher ;; attach-container) echo /run/lea/bus/bus.sock ;; esac
sub=$(printf '%s' "${1:-}" | tr -c 'a-z' '_')
eval "rc=\${FAKE_CORE_RC_$sub:-\${FAKE_CORE_RC:-0}}"
exit "$rc"
`

const fakeTmux = `#!/bin/sh
{ printf 'tmux'; for a in "$@"; do printf ' [%s]' "$a"; done; printf '\n'; } >>"$FAKE_LOG"
if [ "${1:-}" = -V ]; then echo "${FAKE_TMUX_VERSION:-tmux 3.4}"; fi
exit "${FAKE_TMUX_RC:-0}"
`

// world is a test host: HOME, XDG dirs, a projects directory, tests/fakes and a fake tmux on
// PATH, a fake core binary, lea initialised (state/claude.json) and daemon records that match
// the default configuration, so ensureDaemon finds a running daemon.
type world struct {
	t                                *testing.T
	root, dir, home, data, proj, log string
	core, tmux                       string
	env                              map[string]string
	out, errb                        bytes.Buffer
	execs                            [][]string
}

func newWorld(t *testing.T, root string) *world {
	t.Helper()
	dir := t.TempDir()
	w := &world{t: t, root: root, dir: dir, home: dir + "/home", data: dir + "/data", proj: dir + "/projects",
		log: dir + "/calls.log", core: root + "/build/lea", tmux: dir + "/bin/tmux"}
	for _, d := range []string{w.home, w.proj, dir + "/bin", w.data + "/lea/state", w.data + "/lea/host"} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, s string, mode os.FileMode) {
		if err := os.WriteFile(p, []byte(s), mode); err != nil {
			t.Fatal(err)
		}
	}
	if root == repoRoot(t) {
		// Unit tests run on the checkout: never overwrite its build/lea; their core is a fake
		// next to the world. Tests on a copyRoot get root/build/lea, the launcher's own path.
		w.core = dir + "/bin/core"
	}
	write(w.core, fakeCore, 0o755)
	write(w.tmux, fakeTmux, 0o755)
	write(w.log, "", 0o644)
	write(w.data+"/lea/state/claude.json", "{}\n", 0o600)
	write(w.data+"/lea/host/daemon.tmux", w.tmux, 0o600)
	write(w.data+"/lea/host/daemon.approvals", "deck 60", 0o600)
	write(w.data+"/lea/host/daemon.resume", "on 60", 0o600)
	write(w.data+"/lea/host/daemon.lockhttp", "off", 0o600)
	write(w.data+"/lea/host/daemon.stacks", "10 0 4", 0o600)
	w.env = map[string]string{
		"HOME": w.home, "XDG_DATA_HOME": w.data, "XDG_CONFIG_HOME": dir + "/config", "FAKE_LOG": w.log,
		"LEA_PROJECTS_DIR": w.proj, "LEA_TMUX_BIN": w.tmux, "LEA_TMUX_BUILT": dir + "/no-built-tmux",
		"LEA_CLIPBOARD": "0", "LEA_LOCK_TIMEOUT": "2", "LC_ALL": "C",
		// Docker's memory (docker system info): 32 GiB, so the lea container gets a 16 GiB limit.
		"FAKE_DOCKER_OUT_SYSTEM_INFO": "34359738368",
		// Docker's CPUs: 16, so the lea container gets 12.
		"FAKE_DOCKER_OUT_SYSTEM_INFO_AT____NCPU__": "16",
		"PATH": repoRoot(t) + "/tests/fakes:" + dir + "/bin:/usr/local/bin:/usr/bin:/bin",
	}
	// What a daemon started with the default provider settings records.
	write(w.data+"/lea/host/daemon.providers", DaemonProvidersStamp(stampMap{"LEA_PROVIDER": "claude",
		"LEA_PROVIDERS": dir + "/config/lea/providers", "LEA_INTERNAL_HOSTS": root + "/config/internal-hosts"}.get), 0o600)
	return w
}

type stampMap map[string]string

func (m stampMap) get(k string) string { return m[k] }

func (w *world) getenv(k string) string { return w.env[k] }

func (w *world) environ() []string {
	var out []string
	for k, v := range w.env {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// launcher is a Go launcher on this world. PATH is set for the process too: exec.Command looks
// programs up in the parent's PATH.
func (w *world) launcher() *Launcher {
	w.t.Helper()
	w.t.Setenv("PATH", w.env["PATH"])
	cfg, err := LoadConfig(w.getenv, w.root, w.proj)
	if err != nil {
		w.t.Fatal(err)
	}
	return &Launcher{Root: w.root, Core: w.core, Cwd: w.proj, Cfg: cfg, Environ: w.environ(),
		Stdin: strings.NewReader(""), Stdout: &w.out, Stderr: &w.errb,
		Exec: w.recordExec, Sleep: func(time.Duration) {}, Getpid: os.Getpid}
}

// recordExec stands for syscall.Exec: it logs the argv as the fakes do.
func (w *world) recordExec(argv0 string, argv, env []string) error {
	w.execs = append(w.execs, argv)
	f, err := os.OpenFile(w.log, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	var b strings.Builder
	b.WriteString(filepath.Base(argv[0]))
	for _, a := range argv[1:] {
		b.WriteString(" [" + a + "]")
	}
	_, err = f.WriteString(b.String() + "\n")
	return err
}

// calls is the log the fakes and recordExec append to, one call per line.
func (w *world) calls() []string {
	b, err := os.ReadFile(w.log)
	if err != nil {
		w.t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

func (w *world) hasCall(sub string) bool {
	for _, c := range w.calls() {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

func (w *world) hostDir() string { return w.data + "/lea/host" }
func (w *world) id() string      { return ID(w.proj) }
func (w *world) name() string    { return "lea-" + ID(w.proj) }
