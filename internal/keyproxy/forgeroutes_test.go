package keyproxy

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// post sends body to the forge forwarder with Host host, through Go's client (headers hdr).
func (r *forgeRig) post(t *testing.T, host, target, body string, hdr map[string]string) (int, string) {
	t.Helper()
	q, err := http.NewRequest(http.MethodPost, r.fw.URL+target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	q.Host = host
	q.Header.Set("Content-Type", "application/json; charset=utf-8")
	for k, v := range hdr {
		if v == "" {
			q.Header.Del(k)
		} else {
			q.Header.Set(k, v)
		}
	}
	res, err := http.DefaultTransport.RoundTrip(q)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

// The allowlist (#103): what lea's gh and git workflows call reaches GitHub with the token.
func TestForgeAllowsLeasGitHubCalls(t *testing.T) {
	r := newForgeRig(t)
	writeKey(t, r.keyDir, GitHubTokenFile, testToken)
	for _, c := range []struct{ method, host, target string }{
		// Reads gh makes.
		{"GET", "api.github.com", "/"},
		{"GET", "api.github.com", "/user"},
		{"GET", "api.github.com", "/rate_limit"},
		{"GET", "api.github.com", "/repos/DavidGodefroid/lea"},
		{"GET", "api.github.com", "/repos/o/r/actions/runs?per_page=1"},
		{"GET", "api.github.com", "/repos/o/r/actions/jobs/1/logs"},
		{"GET", "api.github.com", "/repos/o/r/contents/docs/a%20b.md"},
		{"GET", "api.github.com", "/repos/o/r/git/ref/heads/feat/x"},
		{"HEAD", "api.github.com", "/repos/o/r/pulls/1"},
		{"GET", "api.github.com", "/orgs/o/repos"},
		{"GET", "api.github.com", "/users/o"},
		{"GET", "api.github.com", "/search/issues?q=x"},
		// Writes on a repository's issues, pull requests, contents, git data, releases, checks.
		{"POST", "api.github.com", "/repos/o/r/issues"},
		{"PATCH", "api.github.com", "/repos/o/r/issues/1"},
		{"POST", "api.github.com", "/repos/o/r/issues/1/comments"},
		{"PATCH", "api.github.com", "/repos/o/r/issues/comments/9"},
		{"DELETE", "api.github.com", "/repos/o/r/issues/1/labels/bug"},
		{"POST", "api.github.com", "/repos/o/r/labels"},
		{"POST", "api.github.com", "/repos/o/r/pulls"},
		{"PUT", "api.github.com", "/repos/o/r/pulls/1/merge"},
		{"POST", "api.github.com", "/repos/o/r/pulls/1/requested_reviewers"},
		{"PUT", "api.github.com", "/repos/o/r/contents/a.txt"},
		{"DELETE", "api.github.com", "/repos/o/r/contents/a.txt"},
		{"POST", "api.github.com", "/repos/o/r/git/refs"},
		{"DELETE", "api.github.com", "/repos/o/r/git/refs/heads/feat/x"},
		{"POST", "api.github.com", "/repos/o/r/releases"},
		{"POST", "api.github.com", "/repos/o/r/check-runs"},
		{"POST", "api.github.com", "/repos/o/r/statuses/abc"},
		{"POST", "api.github.com", "/repos/o/r/actions/runs/1/rerun"},
		{"POST", "api.github.com", "/repos/o/r/actions/runs/1/rerun-failed-jobs"},
		{"POST", "api.github.com", "/repos/o/r/actions/runs/1/cancel"},
		{"DELETE", "api.github.com", "/repos/o/r/actions/runs/1"},
		{"POST", "api.github.com", "/repos/o/r/actions/jobs/1/rerun"},
		{"POST", "api.github.com", "/repos/o/r/actions/workflows/ci.yml/dispatches"},
		{"PUT", "api.github.com", "/repos/o/r/actions/workflows/ci.yml/enable"},
		// gh release upload.
		{"POST", "uploads.github.com", "/repos/o/r/releases/1/assets?name=a.tgz"},
		// git and Git LFS through url.insteadOf.
		{"GET", "lea-proxy:4401", "/o/r.git/info/refs?service=git-receive-pack"},
		{"GET", "lea-proxy:4401", "/o/r/info/refs?service=git-upload-pack"},
		{"POST", "lea-proxy:4401", "/o/r.git/git-receive-pack"},
		{"POST", "lea-proxy:4401", "/o/r.git/info/lfs/objects/batch"},
		{"GET", "github.com", "/o/r/archive/refs/tags/v1.tar.gz"},
		{"GET", "github.com", "/o/r/releases/download/v1/a.tgz"},
	} {
		n := r.count()
		if code, _, body := r.send(t, c.method, c.host, c.target, map[string]string{"Content-Length": "0"}); code != 200 {
			t.Errorf("%s %s%s: %d %s", c.method, c.host, c.target, code, body)
			continue
		}
		if r.count() != n+1 {
			t.Errorf("%s %s%s did not reach the upstream", c.method, c.host, c.target)
			continue
		}
		if req, _, _ := r.last(t); req.Header.Get("Authorization") == "" {
			t.Errorf("%s %s%s: no token", c.method, c.host, c.target)
		}
	}
	// GraphQL: queries, and the mutations of gh issue, gh pr and gh project.
	for _, q := range []string{
		`query { viewer { login } }`,
		`{ repository(owner: "o", name: "r") { pullRequests(first: 1) { nodes { title } } } }`,
		`query PullRequestForBranch($owner: String!) { repository(owner: $owner, name: "r") { id } } fragment F on PullRequest { id }`,
		`mutation PullRequestCreate($input: CreatePullRequestInput!) { createPullRequest(input: $input) { pullRequest { id url } } }`,
		`mutation { a: addComment(input: {subjectId: "x", body: "mutation { transferIssue }"}) { clientMutationId } __typename }`,
		"mutation M($i: MergePullRequestInput!) @x { mergePullRequest(input: $i) @include(if: true) { pullRequest { ... on PullRequest { number } } } }",
		"# transferRepository\nmutation { updateProjectV2ItemFieldValue(input: {value: {text: \"\"\"a } b\"\"\"}}) { clientMutationId } }",
	} {
		body, _ := json.Marshal(map[string]any{"query": q, "variables": map[string]any{"input": map[string]string{"x": "y"}}})
		if code, out := r.post(t, "api.github.com", "/graphql", string(body), nil); code != 200 {
			t.Errorf("graphql %q: %d %s", q, code, out)
			continue
		}
		if _, got, _ := r.last(t); got != string(body) {
			t.Errorf("graphql %q: the upstream got %q", q, got)
		}
	}
}

// Every call outside the allowlist is refused before it reaches GitHub: the bypasses of the
// former denylist (ID aliases, empty segments, collaborators, invitations, transfer, a
// repository's settings, memberships, Actions secrets and runners, GraphQL mutations) included.
func TestForgeRefusesWhatTheAllowlistLacks(t *testing.T) {
	r := newForgeRig(t)
	writeKey(t, r.keyDir, GitHubTokenFile, testToken)
	for _, c := range []struct {
		method, host, target string
		code                 int
	}{
		// ID aliases of the same routes.
		{"POST", "api.github.com", "/repositories/123/keys", 403},
		{"POST", "api.github.com", "/repositories/123/hooks", 403},
		{"PUT", "api.github.com", "/repositories/123/collaborators/evil", 403},
		{"GET", "api.github.com", "/repositories/123", 403},
		{"POST", "api.github.com", "/organizations/9/hooks", 403},
		{"PUT", "api.github.com", "/organizations/9/memberships/evil", 403},
		{"PUT", "api.github.com", "/teams/1/repos/o/r", 403},
		// Empty segments, dot segments, escaped slashes, ";".
		{"POST", "api.github.com", "//repos/o/r/keys", 400},
		{"POST", "api.github.com", "/repos//o/r/keys", 400},
		{"POST", "api.github.com", "/repos/o/r//keys", 400},
		{"POST", "api.github.com", "/repos/o/r/issues/", 400},
		{"POST", "api.github.com", "/repos/o/r/issues/../keys", 400},
		{"POST", "api.github.com", "/repos/o/r/issues/%2e%2e/keys", 400},
		{"POST", "api.github.com", "/repos/o/r/issues%2f..%2fkeys", 400},
		{"POST", "api.github.com", "/repos/o/r/issues;x/../keys", 400},
		{"POST", "api.github.com", "/repos/o/r/issues%3b/x", 400},
		{"GET", "lea-proxy:4401", "//o/r.git/info/refs", 400},
		// Access to the repository or the organisation.
		{"PUT", "api.github.com", "/repos/o/r/collaborators/evil", 403},
		{"DELETE", "api.github.com", "/repos/o/r/collaborators/me", 403},
		{"PATCH", "api.github.com", "/repos/o/r/invitations/1", 403},
		{"PATCH", "api.github.com", "/user/repository_invitations/1", 403},
		{"PATCH", "api.github.com", "/user/memberships/orgs/o", 403},
		{"POST", "api.github.com", "/repos/o/r/transfer", 403},
		{"PATCH", "api.github.com", "/repos/o/r", 403},
		{"DELETE", "api.github.com", "/repos/o/r", 403},
		{"PUT", "api.github.com", "/repos/o/r/branches/main/protection", 403},
		{"DELETE", "api.github.com", "/repos/o/r/branches/main/protection", 403},
		{"POST", "api.github.com", "/repos/o/r/forks", 403},
		{"PUT", "api.github.com", "/orgs/o/memberships/evil", 403},
		{"POST", "api.github.com", "/orgs/o/invitations", 403},
		{"PUT", "api.github.com", "/orgs/o/teams/t/repos/o/r", 403},
		{"POST", "api.github.com", "/orgs/o/repos", 403},
		{"POST", "api.github.com", "/user/repos", 403},
		// Actions: secrets, variables, runners, permissions, a fork run's approval.
		{"PUT", "api.github.com", "/repos/o/r/actions/secrets/X", 403},
		{"PUT", "api.github.com", "/orgs/o/actions/secrets/X", 403},
		{"POST", "api.github.com", "/repos/o/r/actions/variables", 403},
		{"POST", "api.github.com", "/repos/o/r/actions/runners/registration-token", 403},
		{"POST", "api.github.com", "/orgs/o/actions/runners/registration-token", 403},
		{"PUT", "api.github.com", "/repos/o/r/actions/permissions", 403},
		{"POST", "api.github.com", "/repos/o/r/actions/runs/1/approve", 403},
		{"PUT", "api.github.com", "/repos/o/r/environments/prod/secrets/X", 403},
		{"PUT", "api.github.com", "/repos/o/r/dependabot/secrets/X", 403},
		// Keys, webhooks, authorizations (the former denylist).
		{"POST", "api.github.com", "/user/keys", 403},
		{"POST", "api.github.com", "/user/gpg_keys", 403},
		{"POST", "api.github.com", "/user/ssh_signing_keys", 403},
		{"POST", "api.github.com", "/repos/o/r/keys", 403},
		{"POST", "api.github.com", "/Repos/o/r/Keys", 403},
		{"POST", "api.github.com", "/repos/o/r/hooks", 403},
		{"PATCH", "api.github.com", "/repos/o/r/hooks/1/config", 403},
		{"POST", "api.github.com", "/orgs/o/hooks", 403},
		{"POST", "api.github.com", "/authorizations", 403},
		{"PATCH", "api.github.com", "/applications/x/token", 403},
		// The wrong method on an allowed namespace.
		{"POST", "api.github.com", "/repos/o/r/contents/a.txt", 403},
		{"PUT", "api.github.com", "/repos/o/r/statuses/abc", 403},
		{"POST", "api.github.com", "/", 403},
		{"GET", "api.github.com", "/graphql?query=mutation%7BtransferIssue%7D", 403},
		// A repository path without its owner and name.
		{"GET", "api.github.com", "/repos/o", 403},
		{"POST", "api.github.com", "/repos/o/r", 403},
		// uploads.github.com: a release asset only.
		{"POST", "uploads.github.com", "/repos/o/r/keys", 403},
		{"GET", "uploads.github.com", "/user", 403},
		// github.com: git's protocol only, not the web.
		{"POST", "github.com", "/o/r/settings/collaboration", 403},
		{"POST", "lea-proxy:4401", "/login/oauth/access_token", 403},
		{"GET", "lea-proxy:4401", "/settings/tokens", 403},
		{"POST", "lea-proxy:4401", "/o/r.git/info/refs", 403},
		{"DELETE", "lea-proxy:4401", "/o/r.git/info/lfs/locks/1", 403},
	} {
		code, hdr, body := r.send(t, c.method, c.host, c.target, map[string]string{"Content-Length": "0"})
		if code != c.code {
			t.Errorf("%s %s%s: %d, want %d (%s)", c.method, c.host, c.target, code, c.code, body)
		}
		if code == 403 && (!strings.Contains(body, `"message":"lea refuses this GitHub call`) || !strings.HasPrefix(hdr.Get("Content-Type"), "application/json")) {
			t.Errorf("%s %s%s: refusal %q (%s)", c.method, c.host, c.target, body, hdr.Get("Content-Type"))
		}
	}
	if n := r.count(); n != 0 {
		t.Errorf("%d refused requests reached the upstream", n)
	}
	// GraphQL: any mutation field not on the list, however it is hidden.
	for _, c := range []struct{ name, body string }{
		{"a mutation not on the list", `{"query":"mutation { transferRepository(input: {}) { clientMutationId } }"}`},
		{"aliased", `{"query":"mutation { addComment: transferIssue(input: {}) { clientMutationId } }"}`},
		{"after an allowed one", `{"query":"mutation { addComment(input: {}) { clientMutationId } b: updateTeamsRepository(input: {}) { clientMutationId } }"}`},
		{"after a query", `{"query":"query Q { viewer { login } } mutation M { createRepository(input: {}) { clientMutationId } }","operationName":"M"}`},
		{"a fragment spread at the root", `{"query":"mutation { ...F } fragment F on Mutation { transferIssue(input: {}) { clientMutationId } }"}`},
		{"an inline fragment at the root", `{"query":"mutation { ... on Mutation { transferIssue(input: {}) { clientMutationId } } }"}`},
		{"in a string-closing trick", `{"query":"mutation { addComment(input: {body: \"x\"}) { id } } mutation { inviteEnterpriseAdmin(input: {}) { id } }"}`},
		{"a subscription", `{"query":"subscription { x }"}`},
		{"the query key twice", `{"query":"query { viewer { login } }","query":"mutation { transferIssue(input: {}) { id } }"}`},
		{"a batch", `[{"query":"mutation { transferIssue(input: {}) { id } }"}]`},
		{"an unknown key", `{"query":"query { viewer { login } }","extensions":{}}`},
		{"no query", `{"variables":{}}`},
		{"a query that is not a string", `{"query":{"a":1}}`},
		{"an unknown character", `{"query":"mutation { addComment transferIssue }"}`},
		{"an unterminated string", `{"query":"mutation { addComment(input: {body: \"x}) { id } }"}`},
		{"unbalanced", `{"query":"mutation { addComment(input: {) { id } }"}`},
		{"trailing data", `{"query":"query { viewer { login } }"} {"query":"mutation { transferIssue }"}`},
		{"not JSON", `query=mutation+%7B+transferIssue+%7D`},
	} {
		if code, out := r.post(t, "api.github.com", "/graphql", c.body, nil); code != 403 || !strings.Contains(out, "lea refuses") {
			t.Errorf("graphql, %s: %d %s", c.name, code, out)
		}
	}
	ok := `{"query":"query { viewer { login } }"}`
	for _, c := range []struct {
		name, target string
		hdr          map[string]string
	}{
		{"a query string", "/graphql?query=mutation%7BtransferIssue%7D", nil},
		{"a form body", "/graphql", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}},
		{"a content encoding", "/graphql", map[string]string{"Content-Encoding": "gzip"}},
	} {
		if code, out := r.post(t, "api.github.com", c.target, ok, c.hdr); code != 403 {
			t.Errorf("graphql, %s: %d %s", c.name, code, out)
		}
	}
	if n := r.count(); n != 0 {
		t.Errorf("%d refused GraphQL calls reached the upstream", n)
	}
	// Hosts without the token are not on the allowlist: no credential goes there.
	if code, _, _ := r.send(t, "POST", "ghes.example.com", "/api/v3/repos/o/r/keys", map[string]string{"Content-Length": "0"}); code != 200 {
		t.Errorf("a host without the token: %d", code)
	}
}

func TestGraphQLMutationFields(t *testing.T) {
	for q, want := range map[string]string{
		`query { a }`:                                       "",
		`{ a(x: "mutation { transferIssue }") }`:            "",
		`mutation { addComment(input: {}) { id } }`:         "",
		`mutation { createIssue { id } closeIssue { id } }`: "",
		`mutation { transferIssue { id } }`:                 "transferIssue",
		`mutation { x: transferIssue { id } }`:              "transferIssue",
		`mutation { addComment: transferIssue }`:            "transferIssue",
		`mutation { ...F }`:                                 "...",
		`subscription { a }`:                                "subscription",
		`query { a } mutation { b }`:                        "b",
	} {
		if got, err := graphqlMutationFields(q); err != nil || got != want {
			t.Errorf("graphqlMutationFields(%q) = %q, %v; want %q", q, got, err, want)
		}
	}
	for _, q := range []string{"", "mutation", "mutation {", "mutation { a(", `mutation { a(x: "`, "mutation { 1 }", "foo { a }", "mutation { a: }", "}"} {
		if got, err := graphqlMutationFields(q); err == nil && got == "" {
			t.Errorf("graphqlMutationFields(%q) accepted", q)
		}
	}
}

func TestForgePathCanonical(t *testing.T) {
	for _, p := range []string{"//x", "/x//y", "/x/", "/x/./y", "/x/%2E/y", "/x/%2f", "/x/%5c", "/x;y", "/x/%3b", "/x\\y", "x", "/x y"} {
		if _, ok := forgePath(p); ok {
			t.Errorf("forgePath(%q) accepted", p)
		}
	}
	if segs, ok := forgePath("/Repos/O/R/contents/a%20b"); !ok || strings.Join(segs, "|") != "repos|o|r|contents|a b" {
		t.Errorf("forgePath: %q %v", segs, ok)
	}
	if segs, ok := forgePath("/"); !ok || len(segs) != 1 || segs[0] != "" {
		t.Errorf("forgePath(/): %q %v", segs, ok)
	}
}
