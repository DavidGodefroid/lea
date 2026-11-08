package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/DavidGodefroid/lea/internal/grants"
	"github.com/DavidGodefroid/lea/internal/store"
)

// allowFixture is a projects directory with repositories whose origins the test sets.
type allowFixture struct {
	t    *testing.T
	proj string
	host map[string]string
}

func (f allowFixture) origin(repo, url string) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Join(f.proj, repo, ".git"), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.proj, repo, ".git", "config"), []byte("[remote \"origin\"]\n\turl = "+url+"\n"), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// allow runs the core form of lea allow, as the launcher calls it, without a terminal.
func (f allowFixture) allow(args ...string) (int, string, string) {
	f.t.Helper()
	return runEnv(f.t, f.host, append([]string{"allow", "--container", "lea-a", "--projects", f.proj}, args...)...)
}

// digest is the --yes DIGEST a refused lea allow asks for.
func (f allowFixture) digest(args ...string) string {
	f.t.Helper()
	rc, _, e := f.allow(args...)
	m := regexp.MustCompile(`--yes ([0-9a-f]{16})\n`).FindStringSubmatch(e)
	if rc != 1 || m == nil {
		f.t.Fatalf("%v without --yes: rc=%d err=%q", args, rc, e)
	}
	return m[1]
}

// worktree makes the dev agent's worktree with one commit on branch.
func worktree(t *testing.T, proj, repo, name, branch string) string {
	t.Helper()
	wt := filepath.Join(proj, ".lea", "worktrees", repo, name)
	for _, args := range [][]string{{"init", "-q", "-b", branch, wt}, {"-C", wt, "commit", "-q", "--allow-empty", "-m", "x"}} {
		g := exec.Command("git", args...)
		g.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := g.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return wt
}

// TestAllowCommand is #319's lea allow (and #92's one-time delivery setup): lea allow deliver in
// a projects directory covers each repository whose origin is on a forge, until revoked; a pin the
// user has not seen is recorded only with --yes DIGEST off a terminal; lea allow lists this lea's
// sets; lea allow off revokes them.
func TestAllowCommand(t *testing.T) {
	api, bus, st := startDaemonWithStore(t)
	if _, err := st.Register(store.NewAgent{Container: "lea-a", Name: "dev", Profile: "claude", Workdir: "/p", Role: "dev",
		Repo: "fixture", Branch: "feat/a"}, store.Caps{}, 1); err != nil {
		t.Fatal(err)
	}
	f := allowFixture{t, t.TempDir(), map[string]string{"LEA_DAEMON_SOCK": api}}
	f.origin("fixture", "git@github.com:o/fixture.git")
	f.origin("web", "https://github.com/o/web.git")
	f.origin("gl", "https://gitlab.com/o/gl.git")
	f.origin("corp", "git@git.corp.example:team/sub/corp.git")
	f.origin(".lea", "https://github.com/o/hidden.git")
	if err := os.MkdirAll(filepath.Join(f.proj, "plain"), 0o755); err != nil {
		t.Fatal(err)
	}
	if rc, out, _ := f.allow(); rc != 0 || !strings.Contains(out, "no allowance: agents of lea-a ask as usual") {
		t.Fatalf("list before: rc=%d %q", rc, out)
	}
	push := cmd("git push -u origin feat/a")
	wt := worktree(t, f.proj, "fixture", "dev", "feat/a")
	if d := preToolUseIn(t, bus, "dev", "Bash", push, wt); d != "" {
		t.Fatalf("before lea allow: %q", d)
	}
	// The first run only lists the pins and their digest: the user confirms exactly those with
	// --yes DIGEST. A pin that changed in between makes the digest stale: nothing is recorded.
	if rc, _, e := f.allow("deliver"); rc != 1 || !strings.Contains(e, "fixture: new pin github.com/o/fixture") ||
		!strings.Contains(e, "then run lea allow deliver --yes ") {
		t.Fatalf("first run: rc=%d err=%q", rc, e)
	}
	digest := f.digest("deliver")
	f.origin("web", "https://github.com/swap/web.git")
	if rc, _, e := f.allow("deliver", "--yes", digest); rc != 1 || !strings.Contains(e, "the pins changed since they were listed") {
		t.Fatalf("stale digest: rc=%d err=%q", rc, e)
	}
	f.origin("web", "https://github.com/o/web.git")
	if rc, _, _ := f.allow("deliver", "--yes", "0000000000000000"); rc != 1 {
		t.Fatal("a made-up digest")
	}
	if d := preToolUseIn(t, bus, "dev", "Bash", push, wt); d != "" {
		t.Fatalf("after unconfirmed runs: %q", d)
	}
	rc, out, e := f.allow("deliver", "--gitlab", "git.corp.example", "--yes", f.digest("deliver", "--gitlab", "git.corp.example"))
	if rc != 0 || !strings.Contains(out, "allowance #1: agents of lea-a may push their branch, open PRs, merge PRs and delete their branch on corp (gitlab:git.corp.example/team/sub/corp), fixture (github.com/o/fixture), gl (gitlab:gitlab.com/o/gl), web (github.com/o/web), until lea allow off 1") ||
		strings.Contains(out, "hidden") || !strings.Contains(out, "plain  skipped: no origin") {
		t.Fatalf("deliver: rc=%d out=%q err=%q", rc, out, e)
	}
	for _, c := range []string{"git push -u origin feat/a", "gh pr create --head feat/a --title t --body b", "gh pr merge 3 --squash"} {
		if d := preToolUseAs(t, bus, "dev", "Bash", cmd(c)); d != "allow" {
			t.Fatalf("%s with deliver: %q", c, d)
		}
	}
	if d := preToolUseAs(t, bus, "dev", "Bash", cmd("git push origin main")); d != "deny" {
		t.Fatalf("push main with deliver: %q", d)
	}
	// The same pins again: no question, the same set (the GitLab host is remembered).
	if rc, out, e := f.allow("deliver"); rc != 0 || !strings.Contains(out, "allowance #1 (unchanged)") {
		t.Fatalf("again: rc=%d out=%q err=%q", rc, out, e)
	}
	// Another capability set, on one repository, for a while, for one agent's subtree.
	if rc, out, e := f.allow("issues", "merge", "--repo", "web", "--for", "2h", "--agent", "dev"); rc != 0 ||
		!strings.Contains(out, "allowance #2: lea-a/dev and its children may merge PRs and close, reopen and comment on issues on web (github.com/o/web), until ") ||
		!strings.Contains(out, "(2h0m left; lea allow off 2 ends it sooner)") {
		t.Fatalf("issues merge: rc=%d out=%q err=%q", rc, out, e)
	}
	// One line per set, this lea's only.
	if _, err := st.AddGrant(grantFor("lea-b")); err != nil {
		t.Fatal(err)
	}
	rc, out, _ = f.allow()
	if rc != 0 || !regexp.MustCompile(`#1 +deliver +corp \(gitlab:git.corp.example/team/sub/corp\), fixture \(github.com/o/fixture\), gl \(gitlab:gitlab.com/o/gl\), web \(github.com/o/web\) +revoked +\d{4}-`).MatchString(out) ||
		!regexp.MustCompile(`#2 +merge issues +web \(github.com/o/web\), for lea-a/dev and its children +\d{4}-\d\d-\d\d \d\d:\d\d \(2h0m left\) +-`).MatchString(out) ||
		strings.Contains(out, "lea-b") || !strings.Contains(out, "last uses (lea allow log: all):") || !strings.Contains(out, "#1 lea-a/dev push: fixture") {
		t.Fatalf("list: rc=%d %q", rc, out)
	}
	if rc, out, _ := f.allow("log"); rc != 0 || !strings.Contains(out, "#1 lea-a/dev merge: github.com/o/fixture pr 3") {
		t.Fatalf("log: rc=%d %q", rc, out)
	}
	// An origin that changed since it was recorded (an agent can write .git/config) is not pinned
	// without the user's word.
	f.origin("web", "https://github.com/evil/web.git")
	if rc, _, e := f.allow("deliver"); rc != 1 || !strings.Contains(e, "web: origin changed from github.com/o/web to github.com/evil/web") {
		t.Fatalf("changed origin: rc=%d err=%q", rc, e)
	}
	if rc, out, e := f.allow("deliver", "--yes", f.digest("deliver")); rc != 0 || !strings.Contains(out, "web (github.com/evil/web)") ||
		!strings.Contains(out, "allowance #1:") {
		t.Fatalf("deliver --yes: rc=%d out=%q err=%q", rc, out, e)
	}
	// A capability off: in every set of the lea; the set that covered it alone ends.
	if rc, out, _ := f.allow("off", "merge"); rc != 0 || !strings.Contains(out, "revoked merge in every allowance of lea-a") {
		t.Fatalf("off merge: rc=%d %q", rc, out)
	}
	if d := preToolUseAs(t, bus, "dev", "Bash", cmd("gh pr merge 3 --squash")); d != "" {
		t.Fatalf("merge after off merge: %q", d)
	}
	if d := preToolUseIn(t, bus, "dev", "Bash", push, wt); d != "allow" {
		t.Fatalf("push after off merge: %q", d)
	}
	rc, out, _ = f.allow()
	if rc != 0 || !regexp.MustCompile(`#1 +push pr delete-branch `).MatchString(out) || !regexp.MustCompile(`#2 +issues `).MatchString(out) {
		t.Fatalf("list after off merge: %q", out)
	}
	// Re-running deliver brings merge back into the same set.
	if rc, out, _ := f.allow("deliver"); rc != 0 || !strings.Contains(out, "allowance #1:") || !strings.Contains(out, "merge PRs") {
		t.Fatalf("deliver after off merge: rc=%d %q", rc, out)
	}
	if rc, out, _ := f.allow("off", "2"); rc != 0 || !strings.Contains(out, "revoked allowance #2") {
		t.Fatalf("off 2: rc=%d %q", rc, out)
	}
	if rc, _, e := f.allow("off", "2"); rc != 1 || !strings.Contains(e, "allowance #2 is already off") {
		t.Fatalf("off 2 again: rc=%d %q", rc, e)
	}
	if rc, out, _ := f.allow("off", "deliver"); rc != 0 || !strings.Contains(out, "revoked deliver") {
		t.Fatalf("off deliver: rc=%d %q", rc, out)
	}
	if d := preToolUseIn(t, bus, "dev", "Bash", push, wt); d != "" {
		t.Fatalf("after off: %q", d)
	}
	// Off forgets no pin: an origin changed while nothing was allowed still needs the user's
	// word, and an unchanged set applies with a plain run.
	f.origin("web", "https://github.com/evil2/web.git")
	if rc, _, e := f.allow("deliver"); rc != 1 || !strings.Contains(e, "web: origin changed from github.com/evil/web to github.com/evil2/web") {
		t.Fatalf("changed while off: rc=%d err=%q", rc, e)
	}
	f.origin("web", "https://github.com/evil/web.git")
	if rc, out, e := f.allow("deliver"); rc != 0 || !strings.Contains(out, "allowance #4:") {
		t.Fatalf("same pins after off: rc=%d out=%q err=%q", rc, out, e)
	}
	if rc, out, _ := f.allow("off"); rc != 0 || !strings.Contains(out, "revoked every allowance of lea-a") {
		t.Fatalf("off: rc=%d %q", rc, out)
	}
	if rc, out, _ := f.allow(); rc != 0 || !strings.Contains(out, "no allowance") {
		t.Fatalf("list after off: %q", out)
	}
	// A named repository without a forge origin is an error; stop-finished names none.
	if rc, _, e := f.allow("merge", "--repo", "plain"); rc != 1 || !strings.Contains(e, "--repo plain: no origin") {
		t.Fatalf("plain: rc=%d %q", rc, e)
	}
	if rc, out, e := f.allow("stop-finished"); rc != 0 || !strings.Contains(out, "may stop their finished children, until lea allow off") {
		t.Fatalf("stop-finished: rc=%d out=%q err=%q", rc, out, e)
	}
	for _, args := range [][]string{{"maybe"}, {"deliver", "--yes"}, {"deliver", "--yes", "xyz"}, {"deliver", "-y"},
		{"deliver", "--yes", "0123456789abcdef", "--yes", "0123456789abcdef"}, {"deliver", "--gitlab"}, {"deliver", "--gitlab", "bad host"},
		{"merge", "--for", "soon"}, {"merge", "--github", "o/x"}, {"merge", "--container", "lea-b"}, {"log", "x"}, {"off", "1", "2"},
		{"check"}, {"stop-finished", "--repo", "web"}} {
		if rc, _, _ := f.allow(args...); rc != 2 {
			t.Errorf("%v: rc=%d", args, rc)
		}
	}
	for _, args := range [][]string{{"merge", "--branch", "main", "--repo", "web"}, {"merge", "--repo", "web", "--for", "1000h"},
		{"merge", "--repo", "web", "--agent", "nobody"}, {"off", "99"}, {"off", "rm-rf"}, {"merge", "--repo", "../x"}} {
		if rc, _, _ := f.allow(args...); rc != 1 {
			t.Errorf("%v: rc=%d", args, rc)
		}
	}
	for _, args := range [][]string{{"allow"}, {"allow", "--container", "lea-a"}, {"allow", "--container", "lea-a", "--projects", "relative"}} {
		if rc, _, _ := runEnv(t, f.host, args...); rc != 2 {
			t.Errorf("%v: rc=%d", args, rc)
		}
	}
}

func grantFor(container string) grants.Grant {
	return grants.Grant{Container: container, Action: grants.Merge, Repo: "x", Pin: "github.com/o/x", Created: 1, Expires: grants.Standing}
}

// syncBuffer is a buffer two goroutines share.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}
func (b *syncBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

// TestAllowCheckPin: lea grant add --github (the alias) names a pin; lea allow checks it against
// the origin and refuses a different one, naming the origin.
func TestAllowCheckPin(t *testing.T) {
	api, _, _ := startDaemonWithStore(t)
	f := allowFixture{t, t.TempDir(), map[string]string{"LEA_DAEMON_SOCK": api}}
	f.origin("web", "https://github.com/o/web.git")
	if rc, _, e := f.allow("merge", "--repo", "web", "--check-pin", "evil/web"); rc != 1 ||
		!strings.Contains(e, "--github evil/web is not the origin of web, which is github.com/o/web") {
		t.Fatalf("mismatch: rc=%d %q", rc, e)
	}
	d := f.digest("merge", "--repo", "web", "--check-pin", "o/web")
	if rc, out, e := f.allow("merge", "--repo", "web", "--check-pin", "github.com/o/web", "--yes", d); rc != 0 || !strings.Contains(out, "may merge PRs on web") {
		t.Fatalf("match: rc=%d out=%q err=%q", rc, out, e)
	}
}

// openPty is a pseudo-terminal: what the user types goes to master, the command reads slave.
func openPty(t *testing.T) (master, slave *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pty: %v", err)
	}
	if err := unix.IoctlSetPointerInt(int(m.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		m.Close()
		t.Skipf("unlockpt: %v", err)
	}
	n, err := unix.IoctlGetInt(int(m.Fd()), unix.TIOCGPTN)
	if err != nil {
		m.Close()
		t.Skipf("ptsname: %v", err)
	}
	s, err := os.OpenFile("/dev/pts/"+strconv.Itoa(n), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		m.Close()
		t.Skipf("open pts: %v", err)
	}
	t.Cleanup(func() { m.Close(); s.Close() })
	return m, s
}

// allowOnTTY runs lea allow with a terminal on stdin, the user typing answer.
func (f allowFixture) allowOnTTY(answer string, args ...string) (int, string, string) {
	f.t.Helper()
	for k, v := range f.host {
		f.t.Setenv(k, v)
	}
	m, s := openPty(f.t)
	if _, err := m.WriteString(answer); err != nil {
		f.t.Fatal(err)
	}
	var out, errb bytes.Buffer
	rc := run(append([]string{"allow", "--container", "lea-a", "--projects", f.proj}, args...), s, &out, &errb)
	return rc, out.String(), errb.String()
}

// TestAllowFirstRun is #319's first run from zero, on a terminal: lea allow deliver lists the new
// pins and asks; y records them, and lea allow shows one set. An origin an agent rewrites is asked
// about again, and lea doctor warns until the user confirms it.
func TestAllowFirstRun(t *testing.T) {
	api, bus, st := startDaemonWithStore(t)
	if _, err := st.Register(store.NewAgent{Container: "lea-a", Name: "dev", Profile: "claude", Workdir: "/p", Role: "dev",
		Repo: "fixture", Branch: "feat/a"}, store.Caps{}, 1); err != nil {
		t.Fatal(err)
	}
	f := allowFixture{t, t.TempDir(), map[string]string{"LEA_DAEMON_SOCK": api}}
	state := t.TempDir()
	f.origin("fixture", "https://github.com/o/fixture.git")
	wt := worktree(t, f.proj, "fixture", "dev", "feat/a")
	rc, out, e := f.allowOnTTY("n\n", "deliver")
	if rc != 1 || !strings.Contains(e, "fixture: new pin github.com/o/fixture") || !strings.Contains(e, "record these? [y/N]") ||
		!strings.Contains(e, "nothing recorded") || out != "" {
		t.Fatalf("no: rc=%d out=%q err=%q", rc, out, e)
	}
	// A pin an agent rewrites while the question waits is not the one the user saw: nothing is
	// recorded.
	m, sl := openPty(t)
	var errb syncBuffer
	done := make(chan int)
	go func() {
		done <- run([]string{"allow", "--container", "lea-a", "--projects", f.proj, "deliver"}, sl, &bytes.Buffer{}, &errb)
	}()
	for i := 0; !strings.Contains(errb.String(), "record these?"); i++ {
		if i > 500 {
			t.Fatalf("no question: %q", errb.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.origin("fixture", "https://github.com/swap/fixture.git")
	m.WriteString("y\n")
	if rc := <-done; rc != 1 || !strings.Contains(errb.String(), "the pins changed since they were listed") {
		t.Fatalf("rewritten while asked: rc=%d %q", rc, errb.String())
	}
	f.origin("fixture", "https://github.com/o/fixture.git")
	rc, out, e = f.allowOnTTY("y\n", "deliver")
	if rc != 0 || !strings.Contains(out, "allowance #1: agents of lea-a may push their branch") || strings.Contains(e, "--yes") {
		t.Fatalf("yes: rc=%d out=%q err=%q", rc, out, e)
	}
	if rc, out, _ := f.allow(); rc != 0 || strings.Count(out, "\n#") != 1 || !strings.Contains(out, "#1  deliver") {
		t.Fatalf("one set: %q", out)
	}
	if d := preToolUseIn(t, bus, "dev", "Bash", cmd("git push -u origin feat/a"), wt); d != "allow" {
		t.Fatalf("push: %q", d)
	}
	doctor := func() string {
		t.Helper()
		_, out, _ := f.allow("check", "--state", state)
		return out
	}
	if out := doctor(); !strings.Contains(out, "ok    allowances fixture (github.com/o/fixture)") {
		t.Fatalf("doctor: %q", out)
	}
	// An agent rewrites the origin: doctor warns, lea allow asks again.
	f.origin("fixture", "https://github.com/evil/fixture.git")
	if out := doctor(); !strings.Contains(out, "warn  allowances fixture: origin is now github.com/evil/fixture, still pinned to github.com/o/fixture") {
		t.Fatalf("doctor after the rewrite: %q", out)
	}
	if rc, _, e := f.allowOnTTY("\n", "deliver"); rc != 1 || !strings.Contains(e, "origin changed from github.com/o/fixture to github.com/evil/fixture") {
		t.Fatalf("asked again: rc=%d err=%q", rc, e)
	}
	if out := doctor(); !strings.Contains(out, "warn  allowances fixture") {
		t.Fatalf("doctor before the user confirms: %q", out)
	}
	if rc, _, e := f.allowOnTTY("y\n", "deliver"); rc != 0 {
		t.Fatalf("confirm: rc=%d %q", rc, e)
	}
	if out := doctor(); !strings.Contains(out, "ok    allowances fixture (github.com/evil/fixture)") {
		t.Fatalf("doctor after the user confirmed: %q", out)
	}
}
