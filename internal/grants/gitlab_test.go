package grants

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	glAPI = "https://gitlab.example.com/api/v4/projects/grp%2Fapp"
	glSHA = "0123456789abcdef0123456789abcdef01234567"
)

// glMR is the API's JSON of MR 12 of grp/app.
func glMR(src, tgt, state, sha, branch string) string {
	return `{"iid":12,"source_project_id":` + src + `,"target_project_id":` + tgt + `,"state":"` + state + `","sha":"` + sha +
		`","source_branch":"` + branch + `","web_url":"https://gitlab.example.com/grp/app/-/merge_requests/12"}`
}

// gitlabRun runs the pinned command of call (a grant pinned to gitlab.example.com/grp/app)
// against a fake glab, which prints token for glab config get token, and a fake curl, which
// answers the API calls from responses ("METHOD URL" -> body; anything else fails as curl
// --fail-with-body does). It returns the output, the error and the curl calls, one per line.
func gitlabRun(t *testing.T, call, token string, responses map[string]string) (string, error, string) {
	t.Helper()
	needTools(t)
	if _, err := os.Stat(jqPath); err != nil {
		t.Skipf("%s: %v", jqPath, err)
	}
	g := all(Merge)
	if strings.Contains(call, " create") {
		g = all(PRCreate)
	}
	g.GitHub = "gitlab:gitlab.example.com/grp/app"
	d := Decide("Bash", bash(call), dev, []Grant{g}, now, false)
	if d.Verdict != Allow {
		t.Fatalf("%s: %+v", call, d)
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	glab := filepath.Join(dir, "glab")
	if err := os.WriteFile(glab, []byte("#!/bin/sh\n[ \"$*\" = 'config get token --host gitlab.example.com' ] && [ \"$PWD\" = / ] && printf '%s\\n' '"+token+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cases := ""
	for k, v := range responses {
		cases += "'" + k + "') printf '%s' '" + v + "' ;;\n"
	}
	curl := "#!/bin/sh\nm=GET; p=; for a; do [ \"$p\" = -X ] && m=$a; p=$a; u=$a; done\n" +
		"for a; do printf '[%s]' \"$a\"; done >> " + log + "; echo >> " + log + "\n" +
		"case \"$m $u\" in\n" + cases + "*) printf '{\"message\":\"404 Not found\"}'; exit 22 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "curl"), []byte(curl), 0o755); err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(lastWord(t, d.Command), "'"+glabPath+"'", "'"+glab+"'")
	script = strings.ReplaceAll(script, "'"+curlPath+"'", "'"+filepath.Join(dir, "curl")+"'")
	// HOME is the agent's: a ~/.jq there would be loaded into every jq program. A broken one
	// fails any jq that reads it.
	home := filepath.Join(dir, "home")
	os.MkdirAll(home, 0o700)
	if err := os.WriteFile(filepath.Join(home, ".jq"), []byte("this is not jq\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(shPath, "-c", script)
	cmd.Env = append(os.Environ(), "HOME="+home)
	out, err := cmd.CombinedOutput()
	calls, _ := os.ReadFile(log)
	return string(out), err, string(calls)
}

// TestPinnedGitLabMergeChecksTheMR: a pinned glab mr merge is one check and one merge on the
// GitLab API, with curl: the MR must be open, from the caller's branch of the project itself,
// and the merge names the head it checked (sha), so a push in between makes GitLab refuse it.
func TestPinnedGitLabMergeChecksTheMR(t *testing.T) {
	merge := "PUT " + glAPI + "/merge_requests/12/merge"
	done := `{"state":"merged","web_url":"https://gitlab.example.com/grp/app/-/merge_requests/12"}`
	for _, tc := range []struct {
		name, mr string
		merged   bool
	}{
		{"own branch", glMR("1", "1", "opened", glSHA, "feat/73-x"), true},
		{"other branch", glMR("1", "1", "opened", glSHA, "feat/other"), false},
		{"a fork's branch of the same name", glMR("2", "1", "opened", glSHA, "feat/73-x"), false},
		{"closed", glMR("1", "1", "closed", glSHA, "feat/73-x"), false},
		{"not a sha", glMR("1", "1", "opened", "HEAD", "feat/73-x"), false},
		{"a branch with more words", glMR("1", "1", "opened", glSHA, "feat/73-x feat/73-x"), false},
		{"a branch named after it and a sha", glMR("1", "1", "opened", glSHA, "feat/73-x|"+glSHA), false},
		{"no MR", "", false},
	} {
		resp := map[string]string{merge: done}
		if tc.mr != "" {
			resp["GET "+glAPI+"/merge_requests/12"] = tc.mr
		}
		out, err, calls := gitlabRun(t, "glab mr merge 12 --squash -y", "glpat-tok_1", resp)
		merged := strings.Contains(calls, "[-X][PUT]")
		if merged != tc.merged || (err == nil) != tc.merged {
			t.Fatalf("%s: merged %v, err %v, out %s, calls %s", tc.name, merged, err, out, calls)
		}
		if !strings.HasPrefix(calls, "[-q][--proto][=https]") || !strings.Contains(calls, "[-H][PRIVATE-TOKEN: glpat-tok_1]") {
			t.Fatalf("%s: curl calls %s", tc.name, calls)
		}
		if tc.merged && (!strings.Contains(calls, "[--data-urlencode][sha="+glSHA+"][--data][squash=true]["+glAPI+"/merge_requests/12/merge]") ||
			strings.Contains(calls, "should_remove_source_branch") || !strings.Contains(out, "merged https://gitlab.example.com/grp/app/-/merge_requests/12")) {
			t.Fatalf("%s: out %s, calls %s", tc.name, out, calls)
		}
	}
	// A pipeline still running: GitLab merges once it passes (glab's default, auto-merge).
	running := strings.Replace(glMR("1", "1", "opened", glSHA, "feat/73-x"), `"iid":12,`, `"iid":12,"head_pipeline":{"status":"running"},`, 1)
	if out, err, calls := gitlabRun(t, "glab mr merge 12 -y", "tok", map[string]string{"GET " + glAPI + "/merge_requests/12": running, merge: done}); err != nil ||
		!strings.Contains(calls, "[--data][auto_merge=true][--data][merge_when_pipeline_succeeds=true]") {
		t.Fatalf("running pipeline: err %v, out %s, calls %s", err, out, calls)
	}
	if _, _, calls := gitlabRun(t, "glab mr merge 12 -y", "tok", map[string]string{"GET " + glAPI + "/merge_requests/12": glMR("1", "1", "opened", glSHA, "feat/73-x"), merge: done}); strings.Contains(calls, "auto_merge") {
		t.Fatalf("no pipeline: calls %s", calls)
	}
	// By its branch: the one open MR of that branch of the project itself (a fork's MR of a
	// branch of the same name is not it); none or two: refused.
	list := "GET " + glAPI + "/merge_requests"
	for _, tc := range []struct {
		name, list string
		merged     bool
	}{
		{"one", `[{"iid":12,"source_project_id":1,"target_project_id":1},{"iid":9,"source_project_id":5,"target_project_id":1}]`, true},
		{"none", `[{"iid":9,"source_project_id":5,"target_project_id":1}]`, false},
		{"two", `[{"iid":12,"source_project_id":1,"target_project_id":1},{"iid":13,"source_project_id":1,"target_project_id":1}]`, false},
	} {
		out, err, calls := gitlabRun(t, "glab mr merge feat/73-x --remove-source-branch -y", "tok", map[string]string{
			list: tc.list, "GET " + glAPI + "/merge_requests/12": glMR("1", "1", "opened", glSHA, "feat/73-x"), merge: done})
		merged := strings.Contains(calls, "[-X][PUT]")
		if merged != tc.merged || (err == nil) != tc.merged {
			t.Fatalf("by branch, %s: merged %v, err %v, out %s, calls %s", tc.name, merged, err, out, calls)
		}
		if !strings.Contains(calls, "[-G][--data-urlencode][state=opened][--data-urlencode][source_branch=feat/73-x]["+glAPI+"/merge_requests]") ||
			(tc.merged && !strings.Contains(calls, "[--data][should_remove_source_branch=true]")) {
			t.Fatalf("by branch, %s: calls %s", tc.name, calls)
		}
	}
	// No token, or not a token: refused before curl runs.
	for _, tok := range []string{"", "a b", "x\"; touch /tmp/p; \""} {
		out, err, calls := gitlabRun(t, "glab mr merge 12 -y", tok, nil)
		if err == nil || calls != "" || !strings.Contains(out, "no GitLab token for gitlab.example.com") {
			t.Fatalf("token %q: err %v, out %s, calls %s", tok, err, out, calls)
		}
	}
}

// TestPinnedGitLabCreate: a pinned glab mr create is one POST on the GitLab API; the target is
// the named one or the project's default branch, and the agent's text is data (curl
// --data-urlencode name=value never reads a file).
func TestPinnedGitLabCreate(t *testing.T) {
	post := "POST " + glAPI + "/merge_requests"
	made := `{"iid":13,"web_url":"https://gitlab.example.com/grp/app/-/merge_requests/13"}`
	out, err, calls := gitlabRun(t, `glab mr create -s feat/73-x -t x --draft -d "@/etc/passwd & co" --remove-source-branch -y`, "tok",
		map[string]string{"GET " + glAPI: `{"default_branch":"main"}`, post: made})
	if err != nil || !strings.Contains(out, "https://gitlab.example.com/grp/app/-/merge_requests/13") ||
		!strings.Contains(calls, "[-X][POST][--data-urlencode][source_branch=feat/73-x][--data-urlencode][target_branch=main][--data-urlencode][title=Draft: x]"+
			"[--data-urlencode][description=@/etc/passwd & co][--data][remove_source_branch=true]["+glAPI+"/merge_requests]") {
		t.Fatalf("create: err %v, out %s, calls %s", err, out, calls)
	}
	out, err, calls = gitlabRun(t, `glab mr create --source-branch feat/73-x --title y --target-branch dev`, "tok", map[string]string{post: made})
	if err != nil || strings.Contains(calls, "["+glAPI+"]") ||
		!strings.Contains(calls, "[-X][POST][--data-urlencode][source_branch=feat/73-x][--data-urlencode][target_branch=dev][--data-urlencode][title=y]["+glAPI+"/merge_requests]") {
		t.Fatalf("create with a target: err %v, out %s, calls %s", err, out, calls)
	}
	if out, err, calls := gitlabRun(t, `glab mr create -s feat/73-x -t y`, "tok", map[string]string{"GET " + glAPI: `{}`, post: made}); err == nil ||
		strings.Contains(calls, "[POST]") {
		t.Fatalf("no default branch: err %v, out %s, calls %s", err, out, calls)
	}
}

// TestPinnedGitLabIgnoresTheAgentsGlabConfig: GLAB_CONFIG_DIR, and a repository's own
// .git/glab-cli/config.yml, are the agent's to write; its api_host, proxy or TLS settings could
// send glab's requests to a server of its own. Pinned GitLab calls take only the token from it
// (glab config get, from /): curl talks to the forge itself.
func TestPinnedGitLabIgnoresTheAgentsGlabConfig(t *testing.T) {
	needTools(t)
	for _, p := range []string{glabPath, curlPath, jqPath} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("%s: %v", p, err)
		}
	}
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, glMR("1", "1", "opened", glSHA, "feat/73-x"))
	}))
	t.Cleanup(srv.Close)
	api := strings.TrimPrefix(srv.URL, "http://")
	hijack := "hosts:\n    gitlab.example.com:\n        token: lea-test-not-a-token\n        api_host: " + api + "\n        api_protocol: http\n" +
		"        proxy: " + srv.URL + "\n        skip_tls_verify: true\n"
	dir := t.TempDir()
	cfg := filepath.Join(dir, "glab-cli")
	os.MkdirAll(cfg, 0o700)
	os.WriteFile(filepath.Join(cfg, "config.yml"), []byte(hijack), 0o600)
	repo := filepath.Join(dir, "repo")
	if out, err := exec.Command(gitPath, "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	os.MkdirAll(filepath.Join(repo, ".git", "glab-cli"), 0o700)
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
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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
}
