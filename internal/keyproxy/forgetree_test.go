package keyproxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// treeRig is a forge forwarder whose GitHub is a fake: a repository someone/shop with a default
// branch and a .lea/production per ref, which records the writes that reach it.
type treeRig struct {
	fw     *httptest.Server
	keyDir string
	key    []byte
	mu     sync.Mutex
	def    string            // the default branch
	prod   map[string]string // ref -> .lea/production
	prodSt int               // the status of every contents read when not 0 (an error)
	repoSt int               // the status of the repository read when not 0
	writes []string          // the writes that reached GitHub: "METHOD path"
	bodies []string
	auths  []string
	git    http.Handler // when set, serves the git protocol (e2e)
}

const (
	oid1 = "1111111111111111111111111111111111111111"
	oid2 = "2222222222222222222222222222222222222222"
	zero = "0000000000000000000000000000000000000000"
)

func newTreeRig(t *testing.T, tree bool) *treeRig {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "")
	r := &treeRig{keyDir: t.TempDir(), def: "main", prod: map[string]string{}}
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		r.mu.Lock()
		git := r.git
		r.mu.Unlock()
		if git != nil && strings.Contains(q.URL.Path, ".git/") {
			git.ServeHTTP(w, q)
			return
		}
		b, _ := io.ReadAll(q.Body)
		r.mu.Lock()
		defer r.mu.Unlock()
		switch {
		case q.Method == http.MethodGet && q.URL.Path == "/repos/someone/shop":
			if r.repoSt != 0 {
				w.WriteHeader(r.repoSt)
				return
			}
			fmt.Fprintf(w, `{"default_branch":%q}`, r.def)
		case q.Method == http.MethodGet && q.URL.Path == "/repos/someone/shop/contents/.lea/production":
			if r.prodSt != 0 {
				w.WriteHeader(r.prodSt)
				return
			}
			if q.Header.Get("Accept") != "application/vnd.github.raw" {
				w.WriteHeader(http.StatusNotAcceptable)
				return
			}
			p, ok := r.prod[q.URL.Query().Get("ref")]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			io.WriteString(w, p)
		case q.URL.Path == "/graphql" && bytes.Contains(b, []byte("node(id:")):
			if bytes.Contains(b, []byte(`"PR_7"`)) {
				io.WriteString(w, `{"data":{"node":{"number":7,"repository":{"nameWithOwner":"someone/shop"}}}}`)
				return
			}
			io.WriteString(w, `{"data":{"node":null}}`)
		default:
			r.writes = append(r.writes, q.Method+" "+q.URL.Path)
			r.bodies = append(r.bodies, string(b))
			r.auths = append(r.auths, q.Header.Get("Authorization")+"|"+q.Header.Get(GrantHeader))
			io.WriteString(w, "ok")
		}
	}))
	t.Cleanup(up.Close)
	via, _ := url.Parse(newFwdProxy(t).URL)
	f, err := New(Config{Via: via, RootCAs: certPool(up), Log: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	upURL, _ := url.Parse(up.URL)
	g := f.Forge(r.keyDir, func(string) *url.URL { u := *upURL; return &u }).(*forge)
	g.self = testSelf
	r.fw = httptest.NewServer(g)
	t.Cleanup(r.fw.Close)
	writeKey(t, r.keyDir, GitHubTokenFile, testToken)
	if r.key, err = LoadGrantKey(r.keyDir); err != nil {
		t.Fatal(err)
	}
	if err := WriteTree(r.keyDir, Tree{Addrs: map[string]TreeAddr{"127.0.0.1": {Container: "lea-a", Tree: tree}}}); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *treeRig) mint(t *testing.T, g Grant) string {
	t.Helper()
	tok, err := MintGrant(r.key, g, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func (r *treeRig) pushGrant(t *testing.T, ref string) string {
	return r.mint(t, Grant{Container: "lea-a", Repo: "someone/shop", Kind: GrantPush, Ref: ref})
}

func (r *treeRig) nwrites() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.writes)
}

// do sends method host path with body and headers to the forwarder.
func (r *treeRig) do(t *testing.T, method, host, path string, body []byte, hdr map[string]string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, r.fw.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultTransport.(*http.Transport).Clone().RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

// pkt is git's pkt-line of s.
func pkt(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }

// pushBody is a receive-pack body: the commands (capabilities on the first), a flush, a pack.
func pushBody(cmds ...string) []byte {
	var b strings.Builder
	for i, c := range cmds {
		if i == 0 {
			c += "\x00report-status side-band-64k agent=git/2.43"
		}
		b.WriteString(pkt(c + "\n"))
	}
	b.WriteString("0000PACK\x00\x00\x00\x02\x00\x00\x00\x00fakepack")
	return []byte(b.String())
}

func (r *treeRig) push(t *testing.T, body []byte, tok string) (int, string) {
	h := map[string]string{"Content-Type": "application/x-git-receive-pack-request"}
	if tok != "" {
		h[GrantHeader] = tok
	}
	return r.do(t, http.MethodPost, "lea-proxy:4401", "/someone/shop.git/git-receive-pack", body, h)
}

func TestTreePushToTheOwnBranchNeedsItsGrantToken(t *testing.T) {
	r := newTreeRig(t, true)
	cmd := oid1 + " " + oid2 + " refs/heads/feat/x"
	if st, b := r.push(t, pushBody(cmd), ""); st != http.StatusForbidden || !strings.Contains(b, "no grant token") || r.nwrites() != 0 {
		t.Fatalf("a push without a token: %d %s, %d writes", st, b, r.nwrites())
	}
	tok := r.pushGrant(t, "refs/heads/feat/x")
	if st, b := r.push(t, pushBody(cmd), tok); st != http.StatusOK || r.nwrites() != 1 {
		t.Fatalf("a pinned push: %d %s", st, b)
	}
	r.mu.Lock()
	body, auth := r.bodies[0], r.auths[0]
	r.mu.Unlock()
	if body != string(pushBody(cmd)) {
		t.Fatalf("the body did not reach GitHub whole: %q", body)
	}
	if strings.Contains(auth, grantPrefix) || !strings.HasPrefix(auth, "Basic ") {
		t.Fatalf("the grant token reached GitHub, or the token did not: %q", auth)
	}
	// Replayed.
	if st, b := r.push(t, pushBody(cmd), tok); st != http.StatusForbidden || !strings.Contains(b, "already used") || r.nwrites() != 1 {
		t.Fatalf("a replayed token: %d %s", st, b)
	}
}

func TestTreePushRefusesAWrongToken(t *testing.T) {
	r := newTreeRig(t, true)
	cmd := oid1 + " " + oid2 + " refs/heads/feat/x"
	other, _ := MintGrant(bytes.Repeat([]byte{7}, 32), Grant{Container: "lea-a", Repo: "someone/shop", Kind: GrantPush, Ref: "refs/heads/feat/x"}, time.Now())
	expired, _ := MintGrant(r.key, Grant{Container: "lea-a", Repo: "someone/shop", Kind: GrantPush, Ref: "refs/heads/feat/x"}, time.Now().Add(-GrantTTL-time.Minute))
	for name, tok := range map[string]string{
		"wrong ref":       r.pushGrant(t, "refs/heads/feat/y"),
		"wrong repo":      r.mint(t, Grant{Container: "lea-a", Repo: "someone/other", Kind: GrantPush, Ref: "refs/heads/feat/x"}),
		"wrong container": r.mint(t, Grant{Container: "lea-b", Repo: "someone/shop", Kind: GrantPush, Ref: "refs/heads/feat/x"}),
		"merge token":     r.mint(t, Grant{Container: "lea-a", Repo: "someone/shop", Kind: GrantMerge, PR: 7}),
		"another key":     other,
		"expired":         expired,
		"garbage":         "lea-grant.e30.AAAA",
	} {
		if st, b := r.push(t, pushBody(cmd), tok); st != http.StatusForbidden || r.nwrites() != 0 {
			t.Fatalf("%s: %d %s", name, st, b)
		}
	}
}

func TestTreePushRefusesProductionBranches(t *testing.T) {
	r := newTreeRig(t, true)
	r.def = "develop"
	r.prod["develop"] = "# production\r\nrelease\r\n\n"
	r.prod["hotfix"] = "hotfix\n"
	for _, b := range []string{"main", "master", "MAIN", "develop", "Develop", "release", "hotfix"} {
		ref := "refs/heads/" + b
		for _, cmd := range []string{oid1 + " " + oid2 + " " + ref, oid1 + " " + zero + " " + ref, zero + " " + oid2 + " " + ref} {
			st, body := r.push(t, pushBody(cmd), r.pushGrant(t, ref))
			if st != http.StatusForbidden || !strings.Contains(body, "production branch") {
				t.Fatalf("%s: %d %s", cmd, st, body)
			}
		}
	}
	if r.nwrites() != 0 {
		t.Fatal("a production push reached GitHub")
	}
	// The default branch's list counts, not a list only another branch holds.
	r.prod["feat/x"] = "feat/y\n"
	if st, b := r.push(t, pushBody(oid1+" "+oid2+" refs/heads/feat/y"), r.pushGrant(t, "refs/heads/feat/y")); st != http.StatusOK {
		t.Fatalf("feat/y is not a production branch: %d %s", st, b)
	}
}

func TestTreePushFailsClosed(t *testing.T) {
	r := newTreeRig(t, true)
	ref := "refs/heads/feat/x"
	cmd := oid1 + " " + oid2 + " " + ref
	r.prodSt = http.StatusInternalServerError
	if st, b := r.push(t, pushBody(cmd), r.pushGrant(t, ref)); st != http.StatusForbidden || !strings.Contains(b, "cannot read .lea/production") {
		t.Fatalf("a 500 on .lea/production: %d %s", st, b)
	}
	r.prodSt = http.StatusForbidden
	if st, b := r.push(t, pushBody(cmd), r.pushGrant(t, ref)); st != http.StatusForbidden {
		t.Fatalf("a 403 on .lea/production: %d %s", st, b)
	}
	r.prodSt, r.repoSt = 0, http.StatusBadGateway
	if st, b := r.push(t, pushBody(cmd), r.pushGrant(t, ref)); st != http.StatusForbidden || !strings.Contains(b, "default branch") {
		t.Fatalf("no default branch: %d %s", st, b)
	}
	r.repoSt = 0
	tok := r.pushGrant(t, ref)
	for name, body := range map[string][]byte{
		"two refs":       pushBody(cmd, oid1+" "+oid2+" refs/heads/feat/z"),
		"a tag":          pushBody(oid1 + " " + oid2 + " refs/tags/v1"),
		"no flush":       []byte(pkt(cmd + "\x00report-status\n")),
		"bad length":     []byte("zzzz" + cmd),
		"short oid":      pushBody("abc " + oid2 + " " + ref),
		"push-cert":      pushBody("push-cert"),
		"flush and data": []byte("0000PACKdata"),
		"empty":          nil,
	} {
		if st, b := r.push(t, body, tok); st != http.StatusForbidden {
			t.Fatalf("%s: %d %s", name, st, b)
		}
	}
	h := map[string]string{GrantHeader: tok, "Content-Encoding": "gzip"}
	if st, b := r.do(t, http.MethodPost, "lea-proxy:4401", "/someone/shop.git/git-receive-pack", pushBody(cmd), h); st != http.StatusForbidden {
		t.Fatalf("gzip: %d %s", st, b)
	}
	if r.nwrites() != 0 {
		t.Fatal("an unreadable push reached GitHub")
	}
	// git's probe before a large push: a lone flush.
	if st, b := r.push(t, []byte("0000"), ""); st != http.StatusOK {
		t.Fatalf("the probe: %d %s", st, b)
	}
	// The token was not spent by the refusals that came before its check.
	if st, b := r.push(t, pushBody(cmd), tok); st != http.StatusOK {
		t.Fatalf("the pinned push: %d %s", st, b)
	}
}

func TestTreePushRulesApplyToTreeContainersOnly(t *testing.T) {
	r := newTreeRig(t, false)
	if st, b := r.push(t, pushBody(oid1+" "+oid2+" refs/heads/main"), ""); st != http.StatusOK {
		t.Fatalf("outside a tree: %d %s", st, b)
	}
	// No tree file: no daemon wrote one.
	os.Remove(filepath.Join(r.keyDir, TreeFile))
	if st, b := r.push(t, pushBody(oid1+" "+oid2+" refs/heads/main"), ""); st != http.StatusOK {
		t.Fatalf("no tree file: %d %s", st, b)
	}
	// A malformed one fails closed; so does an address it does not name while a tree exists.
	writeKey(t, r.keyDir, TreeFile, "{")
	if st, _ := r.push(t, pushBody(oid1+" "+oid2+" refs/heads/feat/x"), ""); st != http.StatusForbidden {
		t.Fatalf("a malformed tree file: %d", st)
	}
	WriteTree(r.keyDir, Tree{Addrs: map[string]TreeAddr{"172.18.0.9": {Container: "lea-b", Tree: true}}})
	if st, _ := r.push(t, pushBody(oid1+" "+oid2+" refs/heads/feat/x"), r.pushGrant(t, "refs/heads/feat/x")); st != http.StatusForbidden {
		t.Fatalf("an address the tree file does not name: %d", st)
	}
}

func TestTreeRefusesTokenlessMerges(t *testing.T) {
	r := newTreeRig(t, true)
	gh := map[string]string{"Authorization": "token lea-proxy-adds-the-token", "Content-Type": "application/json"}
	if st, b := r.do(t, http.MethodPut, "api.github.com", "/repos/someone/shop/pulls/7/merge", []byte(`{}`), gh); st != http.StatusForbidden || !strings.Contains(b, "no grant token") {
		t.Fatalf("REST merge: %d %s", st, b)
	}
	mut := func(q string, vars any) []byte {
		b, _ := json.Marshal(map[string]any{"query": q, "variables": vars})
		return b
	}
	ghMerge := mut("mutation PullRequestMerge($input:MergePullRequestInput!){mergePullRequest(input:$input){clientMutationId}}",
		map[string]any{"input": map[string]string{"pullRequestId": "PR_7", "mergeMethod": "SQUASH"}})
	if st, b := r.do(t, http.MethodPost, "api.github.com", "/graphql", ghMerge, gh); st != http.StatusForbidden || !strings.Contains(b, "no grant token") {
		t.Fatalf("GraphQL merge: %d %s", st, b)
	}
	for name, body := range map[string][]byte{
		"auto-merge": mut("mutation($i:EnablePullRequestAutoMergeInput!){enablePullRequestAutoMerge(input:$i){clientMutationId}}", map[string]any{"i": map[string]string{"pullRequestId": "PR_7"}}),
		"aliased":    mut("mutation{m: mergePullRequest(input:{pullRequestId:\"PR_7\"}){clientMutationId}}", nil),
		"two vars":   mut("mutation($input:MergePullRequestInput!,$x:MergePullRequestInput!){mergePullRequest(input:$x){clientMutationId}}", map[string]any{"input": map[string]string{"pullRequestId": "PR_7"}, "x": map[string]string{"pullRequestId": "PR_8"}}),
		"with other": mut("mutation($input:MergePullRequestInput!){mergePullRequest(input:$input){clientMutationId} addComment(input:{}){clientMutationId}}", map[string]any{"input": map[string]string{"pullRequestId": "PR_7"}}),
	} {
		tok := r.mint(t, Grant{Container: "lea-a", Repo: "someone/shop", Kind: GrantMerge, PR: 7})
		h := map[string]string{"Authorization": "token " + tok, "Content-Type": "application/json"}
		if st, b := r.do(t, http.MethodPost, "api.github.com", "/graphql", body, h); st != http.StatusForbidden {
			t.Fatalf("%s: %d %s", name, st, b)
		}
	}
	for _, p := range []string{"/repos/someone/shop/merges", "/repos/someone/shop/merge-upstream"} {
		if st, b := r.do(t, http.MethodPost, "api.github.com", p, []byte(`{}`), gh); st != http.StatusForbidden {
			t.Fatalf("%s: %d %s", p, st, b)
		}
	}
	for _, c := range [][2]string{{http.MethodPut, "/repos/someone/shop/contents/.lea/production"}, {http.MethodPatch, "/repos/someone/shop/git/refs/heads/main"},
		{http.MethodPost, "/repos/someone/shop/git/refs"}, {http.MethodDelete, "/repos/someone/shop/git/refs/heads/main"}, {http.MethodDelete, "/repos/someone/shop/git/refs/tags/v1"}} {
		if st, b := r.do(t, c[0], "api.github.com", c[1], []byte(`{}`), gh); st != http.StatusForbidden {
			t.Fatalf("%v: %d %s", c, st, b)
		}
	}
	if r.nwrites() != 0 {
		t.Fatalf("a merge reached GitHub: %v", r.writes)
	}
	// What gh pr merge --delete-branch does next, and what is no merge, go through.
	if st, b := r.do(t, http.MethodDelete, "api.github.com", "/repos/someone/shop/git/refs/heads/Feat/X", nil, gh); st != http.StatusOK {
		t.Fatalf("delete own branch: %d %s", st, b)
	}
	comment := mut("mutation($input:AddCommentInput!){addComment(input:$input){clientMutationId}}", map[string]any{"input": map[string]string{"subjectId": "PR_7", "body": "hi"}})
	if st, b := r.do(t, http.MethodPost, "api.github.com", "/graphql", comment, gh); st != http.StatusOK {
		t.Fatalf("a comment: %d %s", st, b)
	}
}

func TestTreeMergeWithItsGrantToken(t *testing.T) {
	r := newTreeRig(t, true)
	body, _ := json.Marshal(map[string]any{"query": "mutation PullRequestMerge($input:MergePullRequestInput!){mergePullRequest(input:$input){clientMutationId}}",
		"variables": map[string]any{"input": map[string]string{"pullRequestId": "PR_7", "mergeMethod": "SQUASH", "expectedHeadOid": oid1}}})
	hdr := func(tok string) map[string]string {
		return map[string]string{"Authorization": "token " + tok, "Content-Type": "application/json"}
	}
	merge7 := func() string {
		return r.mint(t, Grant{Container: "lea-a", Repo: "someone/shop", Kind: GrantMerge, PR: 7})
	}
	// Another PR's token.
	if st, b := r.do(t, http.MethodPost, "api.github.com", "/graphql", body, hdr(r.mint(t, Grant{Container: "lea-a", Repo: "someone/shop", Kind: GrantMerge, PR: 8}))); st != http.StatusForbidden {
		t.Fatalf("PR 8's token: %d %s", st, b)
	}
	tok := merge7()
	if st, b := r.do(t, http.MethodPost, "api.github.com", "/graphql", body, hdr(tok)); st != http.StatusOK || r.nwrites() != 1 {
		t.Fatalf("the pinned merge: %d %s", st, b)
	}
	if st, _ := r.do(t, http.MethodPost, "api.github.com", "/graphql", body, hdr(tok)); st != http.StatusForbidden {
		t.Fatal("a replayed merge token was accepted")
	}
	r.mu.Lock()
	auth := r.auths[0]
	r.mu.Unlock()
	if auth != "Bearer "+testToken+"|" {
		t.Fatalf("GitHub got %q", auth)
	}
	if st, b := r.do(t, http.MethodPut, "api.github.com", "/repos/someone/shop/pulls/7/merge", []byte(`{}`), hdr(merge7())); st != http.StatusOK {
		t.Fatalf("REST merge with its token: %d %s", st, b)
	}
	// A node that is no PR of the token's.
	other, _ := json.Marshal(map[string]any{"query": "mutation($input:MergePullRequestInput!){mergePullRequest(input:$input){clientMutationId}}",
		"variables": map[string]any{"input": map[string]string{"pullRequestId": "PR_9"}}})
	if st, b := r.do(t, http.MethodPost, "api.github.com", "/graphql", other, hdr(merge7())); st != http.StatusForbidden {
		t.Fatalf("an unknown node: %d %s", st, b)
	}
}

func TestReadReceivePack(t *testing.T) {
	body := pkt("shallow "+oid1+"\n") + string(pushBody(oid1+" "+oid2+" refs/heads/a", zero+" "+oid2+" refs/heads/b"))
	br := bufio.NewReader(strings.NewReader(body))
	cmds, head, err := readReceivePack(br)
	if err != nil || len(cmds) != 2 || cmds[0].ref != "refs/heads/a" || cmds[1].old != zero || !strings.HasSuffix(string(head), "0000") {
		t.Fatalf("%v %q %v", cmds, head, err)
	}
	rest, _ := io.ReadAll(br)
	if string(head)+string(rest) != body {
		t.Fatal("the body is not whole again")
	}
	var many []string
	for i := 0; i <= maxPushCommands; i++ {
		many = append(many, fmt.Sprintf("%s %s refs/heads/b%d", oid1, oid2, i))
	}
	if _, _, err := readReceivePack(bufio.NewReader(bytes.NewReader(pushBody(many...)))); err == nil {
		t.Fatal("too many commands")
	}
}
