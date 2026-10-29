package grants

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// tagRepo is a remote (bare) and an agent's linked worktree of a repository, on branch
// feat/73-x, with a lightweight tag v1 and an annotated tag v2 on its commit.
func tagRepo(t *testing.T) (root, remote, wt string) {
	needTools(t)
	root = t.TempDir()
	remote = filepath.Join(root, "remote.git")
	main := filepath.Join(root, "lea")
	wt = filepath.Join(root, ".lea", "worktrees", "lea", "dev-2")
	git(t, root, "init", "-q", "--bare", remote)
	git(t, root, "init", "-q", main)
	git(t, main, "commit", "-q", "--allow-empty", "-m", "init")
	git(t, main, "remote", "add", "origin", remote)
	git(t, main, "worktree", "add", "-q", "-b", "feat/73-x", wt)
	git(t, wt, "tag", "v1")
	git(t, wt, "tag", "-a", "v2", "-m", "v2")
	return root, remote, wt
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(gitPath, append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return string(out)
}

// shell runs cmd in dir as the agent would, with git's own defaults.
func shell(t *testing.T, dir, cmd string) {
	t.Helper()
	x := exec.Command("/bin/sh", "-c", cmd)
	x.Dir = dir
	x.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	x.CombinedOutput() // a push the remote refuses fails: what lands there is what counts
}

func remoteRefs(t *testing.T, remote string) string {
	t.Helper()
	return git(t, remote, "for-each-ref", "--format=%(refname)")
}

func caller(wt, cwd string) Caller {
	c := dev
	c.Worktree, c.Cwd, c.OwnWorktree = wt, cwd, cwd == wt
	return c
}

// TestRawPushNeverMakesATag: git infers an unqualified push destination from the source, so a
// tag pushed under the branch's name lands as refs/tags/<branch>. Every refspec lea lets through
// (no hard deny, no RawPush deny) is pushed with real git to a bare remote: none ever creates a
// tag there. The refspecs lea refuses are shown to make one.
func TestRawPushNeverMakesATag(t *testing.T) {
	for _, tc := range []struct {
		name, cmd string
		setup     func(t *testing.T, wt string)
		denied    bool
		// tag: git, left to run, makes refs/tags/… on the remote (what the deny prevents).
		tag bool
	}{
		{"own branch", "git push origin feat/73-x", nil, false, false},
		{"own branch, -u", "git push -u origin feat/73-x", nil, false, false},
		{"own branch ref", "git push origin refs/heads/feat/73-x", nil, false, false},
		{"own branch ref to own name", "git push origin refs/heads/feat/73-x:feat/73-x", nil, false, false},
		{"lightweight tag to own full ref", "git push origin v1:refs/heads/feat/73-x", nil, false, false},
		{"annotated tag to own full ref", "git push origin v2:refs/heads/feat/73-x", nil, false, false},
		{"tag to own name", "git push origin v1:feat/73-x", nil, true, true},
		{"tag ref to own name", "git push origin refs/tags/v1:feat/73-x", nil, true, true},
		{"HEAD on a tag to own name", "git push origin HEAD:feat/73-x", func(t *testing.T, wt string) {
			git(t, wt, "symbolic-ref", "HEAD", "refs/tags/v1")
		}, true, true},
		{"only a tag of the branch's name", "git push origin feat/73-x", func(t *testing.T, wt string) {
			git(t, wt, "tag", "feat/73-x")
			git(t, wt, "checkout", "-q", "--detach")
			git(t, wt, "branch", "-q", "-D", "feat/73-x")
		}, true, true},
		{"only a packed tag of the branch's name", "git push origin feat/73-x", func(t *testing.T, wt string) {
			git(t, wt, "tag", "feat/73-x")
			git(t, wt, "checkout", "-q", "--detach")
			git(t, wt, "branch", "-q", "-D", "feat/73-x")
			git(t, wt, "pack-refs", "--all")
		}, true, true},
		{"a branch and a tag of its name", "git push origin feat/73-x", func(t *testing.T, wt string) {
			git(t, wt, "tag", "feat/73-x")
		}, true, false},
		{"tag made by the same command", "git checkout -q --detach && git branch -D feat/73-x && git tag feat/73-x && git push origin feat/73-x", nil, true, true},
		{"tag made by a script first", "sh -c 'git tag feat/73-x' ; git push origin feat/73-x", nil, true, false},
		{"commit then push", "git add -A && git commit -q --allow-empty -m x && git push origin feat/73-x", nil, false, false},
		{"tag made by the same command, qualified", "git tag v3 && git push origin refs/heads/feat/73-x", nil, false, false},
		{"packed branch", "git push origin feat/73-x", func(t *testing.T, wt string) {
			git(t, wt, "pack-refs", "--all")
		}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, remote, wt := tagRepo(t)
			if tc.setup != nil {
				tc.setup(t, wt)
			}
			c := caller(wt, wt)
			in := bash(tc.cmd)
			hard, _, _ := Classify("Bash", in, c)
			why := hard
			if why == "" {
				why = RawPush("Bash", in, c)
			}
			if (why != "") != tc.denied {
				t.Fatalf("%s: denied %q, want denied %v", tc.cmd, why, tc.denied)
			}
			shell(t, wt, tc.cmd)
			tags := strings.Contains(remoteRefs(t, remote), "refs/tags/")
			if !tc.denied && tags {
				t.Fatalf("%s: allowed, and it made a tag: %s", tc.cmd, remoteRefs(t, remote))
			}
			if tags != tc.tag {
				t.Fatalf("%s: git made a tag %v, want %v: %s", tc.cmd, tags, tc.tag, remoteRefs(t, remote))
			}
		})
	}
}

// TestRawPushReadsTheDirectoryItRunsIn: the repository checked is the one git runs in: the
// hook's cwd, moved by cd and git -C; one lea cannot tell is denied.
func TestRawPushReadsTheDirectoryItRunsIn(t *testing.T) {
	root, _, wt := tagRepo(t)
	other := filepath.Join(root, "other")
	git(t, root, "init", "-q", other)
	git(t, other, "commit", "-q", "--allow-empty", "-m", "x")
	git(t, other, "tag", "feat/73-x") // a tag of the branch's name, no branch
	os.Mkdir(filepath.Join(wt, "sub"), 0o755)
	for _, tc := range []struct {
		name, cwd, cmd string
		denied         bool
	}{
		{"worktree", wt, "git push origin feat/73-x", false},
		{"subdirectory of the worktree", filepath.Join(wt, "sub"), "git push origin feat/73-x", false},
		{"cd into the worktree", root, "cd " + wt + " && git push origin feat/73-x", false},
		{"cd relative", root, "cd .lea/worktrees/lea/dev-2 && git push origin feat/73-x", false},
		{"git -C the worktree", root, "git -C " + wt + " push origin feat/73-x", false},
		{"other repository", other, "git push origin feat/73-x", true},
		{"cd to the other repository", wt, "cd " + other + " && git push origin feat/73-x", true},
		{"git -C the other repository", wt, "git -C " + other + " push origin feat/73-x", true},
		{"cd computed", wt, "cd $D && git push origin feat/73-x", true},
		{"cd home", wt, "cd && git push origin feat/73-x", true},
		{"cd back", wt, "cd - && git push origin feat/73-x", true},
		{"no repository", root, "git push origin feat/73-x", true},
		{"no cwd", "", "git push origin feat/73-x", true},
		{"push in a substitution", wt, "echo $(git push origin feat/73-x)", true},
		{"not a push", root, "git status && echo push", false},
		{"other branch name is hard denied already", wt, "git log", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			why := RawPush("Bash", bash(tc.cmd), caller(wt, tc.cwd))
			if (why != "") != tc.denied {
				t.Fatalf("%s in %s: %q, want denied %v", tc.cmd, tc.cwd, why, tc.denied)
			}
		})
	}
	if why := RawPush("Read", []byte(`{"file_path":"x"}`), caller(wt, wt)); why != "" {
		t.Fatalf("Read: %q", why)
	}
}

// TestRawPushRefusesWhatItCannotRead: a repository lea cannot read the refs of (reftable, a
// symlinked gitdir or refs file) is denied, never guessed.
func TestRawPushRefusesWhatItCannotRead(t *testing.T) {
	_, _, wt := tagRepo(t)
	common := strings.TrimSpace(git(t, wt, "rev-parse", "--path-format=absolute", "--git-common-dir"))
	push := bash("git push origin feat/73-x")
	if why := RawPush("Bash", push, caller(wt, wt)); why != "" {
		t.Fatalf("clean: %q", why)
	}
	// A symlinked packed-refs could hide a tag lea would not see.
	os.Symlink("/dev/null", filepath.Join(common, "packed-refs"))
	if why := RawPush("Bash", push, caller(wt, wt)); why == "" {
		t.Fatal("symlinked packed-refs allowed")
	}
	os.Remove(filepath.Join(common, "packed-refs"))
	os.Mkdir(filepath.Join(common, "reftable"), 0o755)
	if why := RawPush("Bash", push, caller(wt, wt)); why == "" {
		t.Fatal("reftable allowed")
	}
}
