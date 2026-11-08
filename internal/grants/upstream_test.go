package grants

import (
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// rmWord is the rm program as a word of a script: Claude Code's safety check refuses a shell -c
// script that runs rm, even when a hook allowed the call (#381).
var rmWord = regexp.MustCompile(`(^|[^A-Za-z0-9_.-])rm([^A-Za-z0-9_.-]|$)`)

// TestPinnedPushScriptRunsNoRm: the script a granted push, deletion or release tag runs holds no
// rm, so Claude Code runs the rewritten command instead of refusing it (#381).
func TestPinnedPushScriptRunsNoRm(t *testing.T) {
	c := dev
	g := all(CommitIdentity)
	for _, args := range [][]string{{"push", "-u", "origin", "feat/73-x"}, {"push", "origin", "feat/73-x"}, {"push", "--delete", "origin", "feat/73-x"}} {
		cmd := Command(Request{Prog: "git", Args: args}, c, g)
		if cmd == "" {
			t.Fatalf("%v not pinned", args)
		}
		if rmWord.MatchString(cmd) {
			t.Fatalf("%v runs rm: %s", args, cmd)
		}
	}
	if s := tagScript("v1.2.3", c.Worktree, g.Pin, c.Proxy); s == "" || rmWord.MatchString(s) {
		t.Fatalf("tag script runs rm (or is empty): %s", s)
	}
}

// TestPinnedPushSetsTheUpstream: -u records origin/<branch> as the branch's upstream once the push
// succeeded; a plain push leaves the upstream as it is.
func TestPinnedPushSetsTheUpstream(t *testing.T) {
	g := all(CommitIdentity)
	up := pushScript([]string{"push", "-u", "origin", "feat/73-x"}, dev.Worktree, g.Pin, dev.Proxy)
	if !strings.Contains(up, "'branch.feat/73-x.merge' 'refs/heads/feat/73-x'") || !strings.Contains(up, "'branch.feat/73-x.remote' 'origin'") {
		t.Fatalf("-u does not set the upstream: %s", up)
	}
	plain := pushScript([]string{"push", "origin", "feat/73-x"}, dev.Worktree, g.Pin, dev.Proxy)
	if strings.Contains(plain, "branch.feat/73-x.") {
		t.Fatalf("a push without -u sets the upstream: %s", plain)
	}
	for _, s := range []string{up, plain} {
		if !strings.Contains(s, "update-ref 'refs/remotes/origin/feat/73-x' \"$sha\"") {
			t.Fatalf("the push does not record origin/feat/73-x: %s", s)
		}
	}
}

// TestPushFromAWorktreeTrackingMain is #381 end to end on lea's side: an agent creates its
// worktree branch from origin/main (git sets the upstream to origin/main), then runs the plain
// git push -u origin <branch>. The hook allows it, the rewritten command holds no rm, pushes the
// branch to the grant's repository (a local git http-backend standing in for lea's forge
// forwarder) at the first try, and the branch's upstream ends as origin/<branch>. A second push
// reuses lea's push repository.
func TestPushFromAWorktreeTrackingMain(t *testing.T) {
	needTools(t)
	execPath, err := exec.Command(gitPath, "--exec-path").Output()
	if err != nil {
		t.Skipf("git --exec-path: %v", err)
	}
	backend := filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skipf("%s: %v", backend, err)
	}
	root := t.TempDir()
	srv := filepath.Join(root, "srv")
	remote := filepath.Join(srv, "someone", "lea.git")
	repo := filepath.Join(root, "lea")
	wt := filepath.Join(root, ".lea", "worktrees", "lea", "dev-2")
	sh := func(dir, script string) string {
		t.Helper()
		x := exec.Command("/bin/sh", "-c", script)
		x.Dir = dir
		x.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "HOME="+root)
		out, err := x.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v %s", script, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	sh(root, "git init -q --bare "+remote+" && git -C "+remote+" config http.receivepack true"+
		" && git init -q -b main "+repo+" && cd "+repo+" && git config user.name t && git config user.email t@t"+
		" && echo a > a && git add a && git commit -qm init && git push -q "+remote+" main"+
		" && git remote add origin https://github.com/someone/lea.git && git fetch -q "+remote+" main:refs/remotes/origin/main")
	sh(repo, "git worktree add -q -b feat/x "+wt+" origin/main")
	if u := sh(wt, "git rev-parse --abbrev-ref feat/x@{upstream}"); u != "origin/main" {
		t.Fatalf("setup: upstream %q, want origin/main", u)
	}
	server := httptest.NewServer(&cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + srv, "GIT_HTTP_EXPORT_ALL=1"}})
	defer server.Close()

	c := dev
	c.Branch, c.Worktree, c.Cwd, c.Env = "feat/x", wt, wt, map[string]string{"HOME": root}
	g := all(CommitIdentity)
	g.Pin = "github.com/someone/lea"
	push := func() {
		t.Helper()
		d := Decide("Bash", bash("git push -u origin feat/x"), c, []Grant{g}, now, false)
		if d.Verdict != Allow || d.Command == "" {
			t.Fatalf("push not allowed: %+v", d)
		}
		if rmWord.MatchString(d.Command) {
			t.Fatalf("the rewritten push runs rm: %s", d.Command)
		}
		// The forge forwarder's address, lea-proxy:4401, is the test server's here.
		forge := strings.TrimSuffix(forgePushURL(c.Proxy, g.Pin), "someone/lea.git")
		cmd := strings.ReplaceAll(d.Command, forge, server.URL+"/")
		sh(wt, cmd)
		if got, want := sh(root, "git -C "+remote+" rev-parse refs/heads/feat/x"), sh(wt, "git rev-parse HEAD"); got != want {
			t.Fatalf("remote feat/x at %s, want %s", got, want)
		}
		if u := sh(wt, "git rev-parse --abbrev-ref feat/x@{upstream}"); u != "origin/feat/x" {
			t.Fatalf("upstream %q, want origin/feat/x", u)
		}
	}
	push()
	sh(wt, "git config user.name t && git config user.email t@t && echo b > a && git commit -qam two")
	push()
	if out := sh(wt, "git status -sb"); !strings.Contains(out, "...origin/feat/x") || strings.Contains(out, "ahead") {
		t.Fatalf("status after the push: %s", out)
	}
	// What an agent leaves in the reused push repository makes the next push refuse, not follow it.
	gd := sh(wt, "git rev-parse --absolute-git-dir")
	d := Decide("Bash", bash("git push -u origin feat/x"), c, []Grant{g}, now, false)
	for _, plant := range []string{"echo " + filepath.Join(repo, ".git") + " > " + filepath.Join(gd, pushRepo, "commondir"),
		"mv " + filepath.Join(gd, pushRepo) + " " + gd + "/elsewhere && ln -s elsewhere " + filepath.Join(gd, pushRepo)} {
		sh(wt, plant)
		x := exec.Command("/bin/sh", "-c", d.Command)
		x.Dir = wt
		if out, err := x.CombinedOutput(); err == nil || !strings.Contains(string(out), "the push repository was changed") {
			t.Fatalf("%s: push ran: %v %s", plant, err, out)
		}
		sh(wt, "rm -f "+filepath.Join(gd, pushRepo, "commondir"))
	}
}
