package grants

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// hostile is a shell prelude as an agent could plant in Claude's shell snapshot: every name the
// pinned command might look up runs the agent's code instead.
const hostile = `test(){ touch "$PWNED"; return 0; }
git(){ touch "$PWNED"; }
gh(){ touch "$PWNED"; }
env(){ touch "$PWNED"; }
alias env='touch "$PWNED"'
shopt -s expand_aliases
`

func needTools(t *testing.T) {
	for _, p := range []string{gitPath, envPath, shPath, mktempPath, rmPath, "/bin/bash"} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("%s: %v", p, err)
		}
	}
}

// fixture is a repository at <root>/.lea/worktrees/lea/dev-2 with a staged file, a planted
// pre-commit hook and a clean filter that would touch <root>/pwned.
func fixture(t *testing.T) (root, wt string, run func(dir, script string) (string, error)) {
	needTools(t)
	root = t.TempDir()
	wt = filepath.Join(root, ".lea", "worktrees", "lea", "dev-2")
	pwned := filepath.Join(root, "pwned")
	run = func(dir, script string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "/bin/bash", "-c", hostile+script)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "PWNED="+pwned, "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	setup := exec.Command("/bin/sh", "-c", "git init -q "+wt+" && cd "+wt+" && git config user.name t && git config user.email t@t"+
		" && git remote add origin https://github.com/Someone/Lea.git && echo a > a && git add a && git commit -qm init"+
		" && echo b > a && git add a && printf '* filter=x\\n' > .gitattributes"+
		" && printf '#!/bin/sh\\ntouch "+pwned+"\\n' > .git/hooks/pre-commit && chmod +x .git/hooks/pre-commit")
	setup.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := setup.CombinedOutput(); err != nil {
		t.Fatalf("setup: %v %s", err, out)
	}
	return root, wt, run
}

func pinnedCaller(root, wt string) (Caller, Grant) {
	c := dev
	c.Worktree, c.Env = wt, map[string]string{"HOME": root}
	g := all(CommitIdentity)
	g.GitHub = "github.com/someone/lea"
	return c, g
}

func notPwned(t *testing.T, root string) {
	t.Helper()
	if b, err := os.ReadFile(filepath.Join(root, "pwned")); err == nil {
		t.Fatalf("the agent's code ran: %q", b)
	}
}

// TestPinnedCommitIgnoresTheAgentsShell: the pinned commit runs in a fresh shell (the agent's
// functions and aliases never run) and through plumbing (the planted hook never runs).
func TestPinnedCommitIgnoresTheAgentsShell(t *testing.T) {
	root, wt, run := fixture(t)
	c, g := pinnedCaller(root, wt)
	// The hostile shell is live: the agent's own "git" runs its code.
	run(root, "git status")
	if _, err := os.Stat(filepath.Join(root, "pwned")); err != nil {
		t.Fatal("the hostile prelude is not active")
	}
	os.Remove(filepath.Join(root, "pwned"))
	commit := Command(Request{Prog: "git", Args: []string{"commit", "-m", "it's"}}, c, g)
	if out, err := run(root, commit); err != nil || !strings.Contains(out, "[lea grant] committed") {
		t.Fatalf("pinned commit: %v %s", err, out)
	}
	notPwned(t, root)
	if out, _ := exec.Command(gitPath, "-C", wt, "log", "-1", "--format=%s").Output(); strings.TrimSpace(string(out)) != "it's" {
		t.Fatalf("log %q", out)
	}
	// The commit is the index as it was staged, and the work tree stays clean against it.
	if out, _ := exec.Command(gitPath, "-C", wt, "show", "HEAD:a").Output(); string(out) != "b\n" {
		t.Fatalf("committed a = %q", out)
	}
	if out, _ := exec.Command(gitPath, "-C", wt, "diff", "--cached", "--name-only").Output(); len(out) != 0 {
		t.Fatalf("index differs from the commit: %s", out)
	}
}

// TestPinnedCommitRunsNoFilterWhileConfigChanges is the reviewer's TOCTOU exploit: a loop toggles
// a clean filter in .git/config while commits run. The guard may refuse some; none runs it.
func TestPinnedCommitRunsNoFilterWhileConfigChanges(t *testing.T) {
	root, wt, run := fixture(t)
	c, g := pinnedCaller(root, wt)
	commit := Command(Request{Prog: "git", Args: []string{"commit", "-m", "x"}}, c, g)
	if err := os.WriteFile(filepath.Join(wt, "a"), []byte("c\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		cfg := filepath.Join(wt, ".git", "config")
		base, _ := os.ReadFile(cfg)
		evil := append(append([]byte{}, base...), []byte("[filter \"x\"]\n\tclean = touch "+filepath.Join(root, "pwned")+"\n")...)
		for i := 0; ; i++ {
			select {
			case <-stop:
				os.WriteFile(cfg, base, 0o644)
				return
			default:
			}
			if i%2 == 0 {
				os.WriteFile(cfg, evil, 0o644)
			} else {
				os.WriteFile(cfg, base, 0o644)
			}
		}
	}()
	for i := 0; i < 60; i++ {
		run(root, commit)
	}
	close(stop)
	wg.Wait()
	notPwned(t, root)
}

// TestPinnedCommitRefusesARiskyConfig: a redirecting or program-running key stops the call.
func TestPinnedCommitRefusesARiskyConfig(t *testing.T) {
	root, wt, run := fixture(t)
	c, g := pinnedCaller(root, wt)
	commit := Command(Request{Prog: "git", Args: []string{"commit", "-m", "x"}}, c, g)
	for _, kv := range [][2]string{{"url.https://evil.example/.insteadOf", "https://github.com/"},
		{"include.path", "/tmp/x"}, {"filter.x.clean", "touch " + root + "/pwned"}, {"remote.origin.pushurl", "https://evil.example/r"},
		{"credential.https://github.com.helper", "!touch " + root + "/pwned"}, {"remote.origin.push", "refs/heads/x:refs/heads/main"},
		{"remote.origin.mirror", "true"}, {"http.sslCAInfo", "/tmp/evil-ca.pem"}, {"http.https://github.com/.curloptResolve", "github.com:443:1.2.3.4"},
		{"http.cookieFile", "/tmp/c"}} {
		if out, err := exec.Command(gitPath, "-C", wt, "config", kv[0], kv[1]).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
		if out, err := run(root, commit); err == nil || !strings.Contains(out, "lea: grant not applied") {
			t.Fatalf("%s: commit ran: %v %s", kv[0], err, out)
		}
		exec.Command(gitPath, "-C", wt, "config", "--unset-all", kv[0]).Run()
	}
	notPwned(t, root)
}

// TestPinnedPushNeverReadsTheRepositoryConfig: the push runs from a bare repository of lea's,
// so a url.insteadOf in the worktree's config (planted after the guard) cannot send it to the
// agent's repository; it names the grant's URL and the full ref.
func TestPinnedPushNeverReadsTheRepositoryConfig(t *testing.T) {
	root, wt, _ := fixture(t)
	c, g := pinnedCaller(root, wt)
	evil := filepath.Join(root, "evil.git")
	if out, err := exec.Command(gitPath, "init", "-q", "--bare", evil).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	push := Command(Request{Prog: "git", Args: []string{"push", "-u", "origin", "feat/73-x"}}, c, g)
	if !strings.Contains(push, "github.com/someone/lea.git") || !strings.Contains(push, "refs/heads/feat/73-x") || !strings.Contains(push, "--git-dir") {
		t.Fatalf("push %s", push)
	}
	exec.Command(gitPath, "-C", wt, "branch", "feat/73-x").Run()
	// The guard ran on a clean config; the agent plants the redirection right after it.
	script := strings.SplitN(lastWord(t, push), "\n", 2)[1]
	if out, err := exec.Command(gitPath, "-C", wt, "config", "url."+evil+".insteadOf", "https://github.com/").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	env := []string{"PATH=/usr/bin:/bin", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "HOME=" + root}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, shPath, "-c", script)
	cmd.Env = env
	cmd.CombinedOutput() // reaches github.com or fails offline: either way not the agent's repo
	if out, _ := exec.Command(gitPath, "--git-dir", evil, "for-each-ref").Output(); len(out) != 0 {
		t.Fatalf("the push went to the agent's repository: %s", out)
	}
	notPwned(t, root)
	if !strings.Contains(lastWord(t, push), "the push repository config changed") || !strings.Contains(push, "'GIT_CONFIG_NOSYSTEM=1'") {
		t.Fatalf("push does not check the bare repository's config: %s", push)
	}
	if !strings.Contains(lastWord(t, push), `"$sha"':refs/heads/feat/73-x'`) {
		t.Fatalf("push refspec not one quoted word: %s", lastWord(t, push))
	}
	// A branch name that would break out of the script is never pinned.
	for _, b := range []string{`x";touch /tmp/p;"`, "x$(id)", "x`id`", "a b", "a..b", "-x", "x.lock", "x/"} {
		odd := c
		odd.Branch = b
		if cmd := Command(Request{Prog: "git", Args: []string{"push", "origin", b}}, odd, g); cmd != "" {
			t.Fatalf("branch %q pinned: %s", b, cmd)
		}
		if d := Decide("Bash", bash("git push origin '"+b+"'"), odd, []Grant{g}, now, false); d.Verdict == Allow {
			t.Fatalf("branch %q allowed: %+v", b, d)
		}
	}
	for _, bad := range [][]string{{"push", "origin", "main"}, {"push", "origin"}, {"push", "-u", "--delete", "origin", "x"}} {
		if cmd := Command(Request{Prog: "git", Args: bad}, c, g); cmd != "" {
			t.Fatalf("%v: %s", bad, cmd)
		}
	}
}

// lastWord is the script an outer pinned command hands to /bin/sh -c, as the shell reads it.
func lastWord(t *testing.T, outer string) string {
	t.Helper()
	out, err := exec.Command("/bin/sh", "-c", "set -- "+outer+`; eval "s=\${$#}"; printf %s "$s"`).Output()
	if err != nil {
		t.Fatalf("read %s: %v", outer, err)
	}
	return string(out)
}

// TestPinnedMergeChecksThePRHead runs the numbered-merge script against a fake gh: the merge runs
// only when gh says PR 42's head is the caller's branch, in the grant's repository.
func TestPinnedMergeChecksThePRHead(t *testing.T) {
	needTools(t)
	d := Decide("Bash", bash("gh pr merge 42 --squash"), dev, []Grant{all(Merge)}, now, false)
	if d.Verdict != Allow {
		t.Fatalf("merge: %+v", d)
	}
	for _, tc := range []struct {
		head   string
		merged bool
	}{{"false feat/73-x", true}, {"false feat/other", false}, {"true feat/73-x", false}, {"", false}} {
		dir := t.TempDir()
		fake := filepath.Join(dir, "gh")
		log := filepath.Join(dir, "log")
		body := "#!/bin/sh\necho \"$*\" >> " + log + "\nif [ \"$2\" = view ]; then printf '%s\\n' '" + tc.head + "'; fi\n"
		if err := os.WriteFile(fake, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		script := strings.ReplaceAll(lastWord(t, d.Command), "'"+ghPath+"'", "'"+fake+"'")
		out, err := exec.Command(shPath, "-c", script).CombinedOutput()
		got, _ := os.ReadFile(log)
		merged := strings.Contains(string(got), "pr merge 42 --squash -R github.com/o/lea")
		if merged != tc.merged || (err == nil) != tc.merged {
			t.Fatalf("head %q: merged %v, err %v, out %s, calls %s", tc.head, merged, err, out, got)
		}
	}
}

// TestPinnedPRCreateBodyFile runs the pr-create script against a fake gh: gh gets the body
// file's content on its stdin only when it is a plain file of the worktree, outside .git.
func TestPinnedPRCreateBodyFile(t *testing.T) {
	needTools(t)
	if _, err := os.Stat("/usr/bin/realpath"); err != nil {
		t.Skip(err)
	}
	root := t.TempDir()
	wt := filepath.Join(root, ".lea", "worktrees", "lea", "dev-2")
	secret := filepath.Join(root, "secret")
	for _, d := range []string{filepath.Join(wt, "docs"), filepath.Join(wt, ".git"), filepath.Join(wt, "dir")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for p, s := range map[string]string{filepath.Join(wt, "docs", "pr.md"): "body", secret: "token", filepath.Join(wt, ".git", "x"): "x"} {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for l, to := range map[string]string{"out.md": secret, "git.md": filepath.Join(wt, ".git", "x"), "in.md": filepath.Join(wt, "docs", "pr.md")} {
		if err := os.Symlink(to, filepath.Join(wt, l)); err != nil {
			t.Fatal(err)
		}
	}
	if err := syscall.Mkfifo(filepath.Join(wt, "fifo.md"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := dev
	c.Worktree = wt
	for _, tc := range []struct {
		file, passed string
	}{{"docs/pr.md", "body"}, {"in.md", "body"},
		{"out.md", ""}, {"git.md", ""}, {"dir", ""}, {"missing.md", ""}, {"fifo.md", ""}} {
		d := Decide("Bash", bash("gh pr create --head feat/73-x --title x --body-file "+tc.file), c, []Grant{all(PRCreate)}, now, false)
		if d.Verdict != Allow {
			t.Fatalf("%s: %+v", tc.file, d)
		}
		dir := t.TempDir()
		fake := filepath.Join(dir, "gh")
		log := filepath.Join(dir, "log")
		if err := os.WriteFile(fake, []byte("#!/bin/sh\necho \"$*\" >> "+log+"\ncat >> "+log+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		script := strings.ReplaceAll(lastWord(t, d.Command), "'"+ghPath+"'", "'"+fake+"'")
		// A FIFO must be refused, not opened: opening one blocks until a writer comes.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		out, err := exec.CommandContext(ctx, shPath, "-c", script).CombinedOutput()
		blocked := ctx.Err() != nil
		cancel()
		if blocked {
			t.Fatalf("%s: the script blocked", tc.file)
		}
		got, _ := os.ReadFile(log)
		want := ""
		if tc.passed != "" {
			want = "pr create --head feat/73-x --title x --body-file - -R github.com/o/lea\n" + tc.passed
		}
		if string(got) != want || (err == nil) != (want != "") {
			t.Fatalf("%s: calls %q, want %q, err %v, out %s", tc.file, got, want, err, out)
		}
	}
}

// TestPinnedMRMergeChecksTheSource runs the numbered glab merge script against a fake glab: the
// merge runs only when glab says MR 12 comes from the caller's branch of the project itself.
func TestPinnedMRMergeChecksTheSource(t *testing.T) {
	needTools(t)
	g := all(Merge)
	g.GitHub = "gitlab:gitlab.example.com/grp/app"
	d := Decide("Bash", bash("glab mr merge 12 --squash -y"), dev, []Grant{g}, now, false)
	if d.Verdict != Allow {
		t.Fatalf("merge: %+v", d)
	}
	for _, tc := range []struct {
		head   string
		merged bool
	}{{"true feat/73-x", true}, {"true feat/other", false}, {"false feat/73-x", false}, {"", false}} {
		dir := t.TempDir()
		fake := filepath.Join(dir, "glab")
		log := filepath.Join(dir, "log")
		// The fake glab answers glab config get token, and records the token, config dir and
		// directory of the calls after.
		body := "#!/bin/sh\nif [ \"$1 $2\" = 'config get' ]; then echo tok; exit 0; fi\necho \"$GITLAB_TOKEN|$GLAB_CONFIG_DIR|$PWD\" >> " + log + ".env\n" +
			"echo \"$*\" >> " + log + "\nif [ \"$2\" = view ]; then printf '%s\\n' '" + tc.head + "'; fi\n"
		if err := os.WriteFile(fake, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		script := strings.ReplaceAll(lastWord(t, d.Command), "'"+glabPath+"'", "'"+fake+"'")
		out, err := exec.Command(shPath, "-c", script).CombinedOutput()
		got, _ := os.ReadFile(log)
		merged := strings.Contains(string(got), "mr merge 12 --squash -y -R https://gitlab.example.com/grp/app")
		if env, _ := os.ReadFile(log + ".env"); tc.head != "" && !regexp.MustCompile(`^tok\|/tmp/tmp\.[A-Za-z0-9]+\|/\n`).Match(env) {
			t.Fatalf("head %q: glab ran with %q", tc.head, env)
		}
		if merged != tc.merged || (err == nil) != tc.merged {
			t.Fatalf("head %q: merged %v, err %v, out %s, calls %s", tc.head, merged, err, out, got)
		}
	}
}

// TestPinnedGlabIgnoresTheAgentsGlabConfig: GLAB_CONFIG_DIR, and a repository's own
// .git/glab-cli/config.yml, are the agent's to write. A config whose api_host points at a server
// of the agent's would answer the MR source check (then be put back before the merge): every
// pinned glab call runs from / with a fresh config dir and the token read from the agent's
// config, so glab only ever talks to the forge itself.
func TestPinnedGlabIgnoresTheAgentsGlabConfig(t *testing.T) {
	needTools(t)
	if _, err := os.Stat(glabPath); err != nil {
		t.Skipf("%s: %v", glabPath, err)
	}
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"iid":12,"source_branch":"feat/73-x","source_project_id":1,"target_project_id":1,"state":"opened"}`)
	}))
	t.Cleanup(srv.Close)
	api := strings.TrimPrefix(srv.URL, "http://")
	hijack := "hosts:\n    gitlab.example.com:\n        token: lea-test-not-a-token\n        api_host: " + api + "\n        api_protocol: http\n"
	dir := t.TempDir()
	cfg := filepath.Join(dir, "glab-cli")
	os.MkdirAll(cfg, 0o700)
	os.WriteFile(filepath.Join(cfg, "config.yml"), []byte(hijack), 0o600)
	repo := filepath.Join(dir, "repo")
	os.MkdirAll(filepath.Join(repo, ".git", "glab-cli"), 0o700)
	if out, err := exec.Command(gitPath, "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	os.WriteFile(filepath.Join(repo, ".git", "glab-cli", "config.yml"), []byte(hijack), 0o600)
	c := dev
	c.Env = map[string]string{"HOME": dir, "GLAB_CONFIG_DIR": cfg}
	for _, call := range []string{"glab mr merge 12 --squash -y", "glab mr merge feat/73-x --squash -y", "glab mr create -s feat/73-x -t x -y"} {
		g := all(Merge)
		if strings.Contains(call, "create") {
			g = all(PRCreate)
		}
		g.GitHub = "gitlab:gitlab.example.com/grp/app"
		d := Decide("Bash", bash(call), c, []Grant{g}, now, false)
		if d.Verdict != Allow {
			t.Fatalf("%s: %+v", call, d)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		cmd := exec.CommandContext(ctx, shPath, "-c", d.Command)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		cancel()
		mu.Lock()
		n := hits
		mu.Unlock()
		// gitlab.example.com refuses the made-up token (or cannot be reached): the call fails.
		if n != 0 || err == nil || strings.Contains(string(out), "grant not applied") {
			t.Fatalf("%s: the agent's server got %d requests, err %v, out %s", call, n, err, out)
		}
	}
	// No token in the agent's config: refused before glab talks to anyone.
	os.WriteFile(filepath.Join(cfg, "config.yml"), []byte("hosts:\n    gitlab.example.com:\n        api_protocol: https\n"), 0o600)
	g := all(Merge)
	g.GitHub = "gitlab:gitlab.example.com/grp/app"
	d := Decide("Bash", bash("glab mr merge 12 --squash -y"), c, []Grant{g}, now, false)
	if out, err := exec.Command(shPath, "-c", d.Command).CombinedOutput(); err == nil || !strings.Contains(string(out), "no GitLab token") {
		t.Fatalf("no token: err %v, out %s", err, out)
	}
}
