package keyproxy

// The forge forwarder's rules for the containers of a tower's tree (#110, see grant.go): it
// checks every push and merge itself, on what git and gh send, and reads the production branches
// on GitHub itself.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// maxPushCommands caps the command list of a receive-pack from a tree container.
	maxPushCommands = 64
	// maxPktLen is git's largest pkt-line.
	maxPktLen = 65520
	// maxTreeRead caps a forge answer the forwarder reads for its checks.
	maxTreeRead = 1 << 20
)

// pushCmd is one command of a receive-pack request: <old> <new> <ref>.
type pushCmd struct{ old, new, ref string }

// zeroOID says whether s is git's null object id (a create or a delete).
func zeroOID(s string) bool { return strings.Trim(s, "0") == "" }

// hexOID says whether s is a full object id.
func hexOID(s string) bool {
	return (len(s) == 40 || len(s) == 64) && strings.Trim(s, "0123456789abcdef") == ""
}

// readReceivePack reads the command list at the start of a receive-pack body (git's
// pkt-lines up to the first flush): optional "shallow <oid>" lines, then "<old> <new> <ref>"
// lines, capabilities after a NUL on the first. It returns the bytes it read, which go to the
// upstream first. A push certificate, a malformed line or more than maxPushCommands fail.
func readReceivePack(br *bufio.Reader) ([]pushCmd, []byte, error) {
	var raw bytes.Buffer
	var cmds []pushCmd
	for first := true; ; {
		var hdr [4]byte
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			return nil, nil, errors.New("a truncated command list")
		}
		raw.Write(hdr[:])
		n, err := strconv.ParseUint(string(hdr[:]), 16, 16)
		if err != nil {
			return nil, nil, errors.New("a malformed pkt-line")
		}
		if n == 0 {
			return cmds, raw.Bytes(), nil
		}
		if n <= 4 || n > maxPktLen {
			return nil, nil, errors.New("a malformed pkt-line")
		}
		line := make([]byte, n-4)
		if _, err := io.ReadFull(br, line); err != nil {
			return nil, nil, errors.New("a truncated command list")
		}
		raw.Write(line)
		s := strings.TrimSuffix(string(line), "\n")
		if first {
			s, _, _ = strings.Cut(s, "\x00")
		}
		if o, ok := strings.CutPrefix(s, "shallow "); ok && first && hexOID(o) {
			continue
		}
		first = false
		f := strings.Split(s, " ")
		if len(f) != 3 || !hexOID(f[0]) || !hexOID(f[1]) || f[2] == "" || strings.ContainsAny(f[2], "\x00\n\r\t") {
			// push-cert included: lea reads no certificate.
			return nil, nil, errors.New("a command lea cannot read")
		}
		cmds = append(cmds, pushCmd{f[0], f[1], f[2]})
		if len(cmds) > maxPushCommands {
			return nil, nil, errors.New("too many commands")
		}
	}
}

// grantToken is the grant token r carries: GrantHeader (a pinned git push), or gh's
// Authorization (the GH_TOKEN of a pinned gh merge). "" for none.
func grantToken(r *http.Request) string {
	if t := strings.TrimSpace(r.Header.Get(GrantHeader)); t != "" {
		return t
	}
	a := r.Header.Get("Authorization")
	for _, p := range []string{"token ", "Bearer ", "bearer "} {
		if t, ok := strings.CutPrefix(a, p); ok && strings.HasPrefix(t, grantPrefix) {
			return strings.TrimSpace(t)
		}
	}
	return ""
}

// treeCtx is what the tree checks of one request need.
type treeCtx struct {
	g         *forge
	r         *http.Request
	container string // the calling container (TreeFile)
	source    string // the gate's source, "" without a gate
	token     string // lea's GitHub token
}

// treeCheck is why a request of a tree container to a token host is refused, "" to forward it.
// It may read r's body (and put it back).
func (g *forge) treeCheck(r *http.Request, host string, segs []string, raw, container, source, token string) string {
	t := &treeCtx{g: g, r: r, container: container, source: source, token: token}
	m := r.Method
	switch host {
	case "github.com":
		if len(segs) == 3 && segs[2] == "git-receive-pack" && m == http.MethodPost {
			return t.receivePack(strings.TrimSuffix(segs[0]+"/"+segs[1], ".git"))
		}
		return ""
	case "api.github.com":
		if len(segs) == 1 && segs[0] == "graphql" {
			return t.graphql()
		}
		if read(m) || !repoPath(segs, 4) {
			return ""
		}
		repo, rest := segs[1]+"/"+segs[2], segs[3:]
		switch rest[0] {
		case "pulls":
			if len(rest) == 3 && rest[2] == "merge" {
				n, err := strconv.ParseInt(rest[1], 10, 64)
				if err != nil || n < 1 {
					return "in a tower's tree, a merge of a PR lea cannot read"
				}
				return t.spend(GrantMerge, repo, "", n)
			}
		case "merges", "merge-upstream":
			return "in a tower's tree, a merge of branches through the API (it would move a production branch past the production-merge policy)"
		case "contents":
			return "in a tower's tree, a file write through the contents API (it could rewrite .lea/production or a production branch)"
		case "git":
			if len(rest) >= 2 && rest[1] == "refs" {
				// The delete of a branch that is no production branch: gh pr merge --delete-branch.
				if m == http.MethodDelete && len(rest) >= 4 && rest[2] == "heads" {
					b := rawTail(raw, 7)
					if b == "" {
						return "in a tower's tree, a branch delete lea cannot read"
					}
					return t.notProduction(repo, b)
				}
				return "in a tower's tree, a ref write through the git API (push to your own branch with git, under the tree's push grant)"
			}
		}
	}
	return ""
}

// rawTail is the raw path's segments from the n-th on, unescaped, with their case: the branch
// of repos/o/r/git/refs/heads/<branch>.
func rawTail(raw string, n int) string {
	s := strings.SplitN(strings.TrimPrefix(raw, "/"), "/", n+1)
	if len(s) <= n {
		return ""
	}
	u, err := url.PathUnescape(s[n])
	if err != nil {
		return ""
	}
	return u
}

// receivePack checks a push of repo from a tree container: one command, on a branch that is no
// production branch, with a grant token for exactly that ref. A body that is a lone flush (git's
// probe before a large push) updates nothing and goes through.
func (t *treeCtx) receivePack(repo string) string {
	r := t.r
	if len(r.Header.Values("Content-Encoding")) > 0 {
		return "in a tower's tree, a push whose command list lea cannot read (a content encoding)"
	}
	if r.Body == nil {
		return "in a tower's tree, a push without a command list"
	}
	br := bufio.NewReaderSize(r.Body, 64<<10)
	cmds, head, err := readReceivePack(br)
	if err != nil {
		return "in a tower's tree, a push whose command list lea cannot read: " + err.Error()
	}
	r.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(head), br), r.Body}
	if len(cmds) == 0 {
		if r.ContentLength == int64(len(head)) {
			return ""
		}
		return "in a tower's tree, a push whose command list lea cannot read (no command, then data)"
	}
	if len(cmds) != 1 {
		return "in a tower's tree, a push of more than one ref (lea pins one push of your own branch at a time)"
	}
	ref := cmds[0].ref
	b, ok := strings.CutPrefix(ref, "refs/heads/")
	if !ok || b == "" {
		return "in a tower's tree, a push of " + ref + ", which is not a branch"
	}
	if why := t.notProduction(repo, b); why != "" {
		return why
	}
	return t.spend(GrantPush, repo, ref, 0)
}

// spend is why the request's grant token does not allow kind on repo (ref or pr), "" when it
// does; then the token is spent.
func (t *treeCtx) spend(kind, repo, ref string, pr int64) string {
	what := "a push"
	if kind == GrantMerge {
		what = "a merge"
	}
	tok := grantToken(t.r)
	if tok == "" {
		return "in a tower's tree, " + what + " runs only as lea pins it (git push origin <your branch>, gh pr merge N under the tree's grants): " +
			"this one carries no grant token (a script, a raw git or API call?)"
	}
	now := time.Now()
	gr, err := parseGrant(readGrantKey(t.g.keyDir), tok, now)
	if err != nil {
		return "in a tower's tree, " + what + " with " + err.Error()
	}
	if gr.Kind != kind || gr.Container != t.container || !strings.EqualFold(gr.Repo, repo) || gr.Ref != ref || gr.PR != pr {
		return "in a tower's tree, " + what + " other than the one its grant token names"
	}
	if !t.g.spent.spend(gr, now) {
		return "in a tower's tree, " + what + " with a grant token already used"
	}
	return ""
}

// notProduction is why branch of repo is a production branch, or cannot be told not to be one;
// "" when it is not one. Production: main, master, the default branch, a branch .lea/production
// names on the default branch or on branch itself. Names compare without case.
func (t *treeCtx) notProduction(repo, branch string) string {
	why := "in a tower's tree, a push to (or delete of) the production branch " + branch +
		" skips the production-merge policy; push to a branch of your own and merge through the tree's merge grant"
	if strings.EqualFold(branch, "main") || strings.EqualFold(branch, "master") {
		return why
	}
	def, err := t.defaultBranch(repo)
	if err != nil {
		return "in a tower's tree, lea cannot read the default branch of " + repo + " (" + err.Error() + "), so it refuses the push"
	}
	if strings.EqualFold(branch, def) {
		return why + " (the default branch)"
	}
	refs := []string{def}
	if branch != def {
		refs = append(refs, branch)
	}
	for _, ref := range refs {
		list, err := t.production(repo, ref)
		if err != nil {
			return "in a tower's tree, lea cannot read .lea/production on " + ref + " (" + err.Error() + "), so it refuses the push"
		}
		for _, p := range list {
			if strings.EqualFold(p, branch) {
				return why + " (.lea/production on " + ref + " names it)"
			}
		}
	}
	return ""
}

// get is a read of the API at path (escaped) with query, for the checks.
func (t *treeCtx) get(path string, q url.Values, accept string) (int, []byte, error) {
	return t.do(http.MethodGet, path, q, accept, nil)
}

func (t *treeCtx) do(method, path string, q url.Values, accept string, body []byte) (int, []byte, error) {
	base := t.g.upstream("api.github.com")
	if base == nil {
		return 0, nil, errors.New("no upstream")
	}
	u := *base
	u.Path = strings.TrimSuffix(base.Path, "/") + path
	u.RawPath, u.RawQuery = "", q.Encode()
	ctx, cancel := context.WithTimeout(t.r.Context(), 30*time.Second)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.WithValue(ctx, keyCtx{}, route{target: &u, source: t.source}), method, u.String(), rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "lea-keyproxy")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if t.token != "" {
		req.Header.Set("Authorization", "Bearer "+t.token)
	}
	if t.source != "" {
		req.Header.Set(SourceHeader, t.source)
	}
	res, err := t.g.f.roundTrip(req)
	if err != nil {
		return 0, nil, errors.New("the forge did not answer")
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, maxTreeRead+1))
	if err != nil || len(b) > maxTreeRead {
		return 0, nil, errors.New("an unreadable answer")
	}
	return res.StatusCode, b, nil
}

// defaultBranch is repo's default branch on GitHub.
func (t *treeCtx) defaultBranch(repo string) (string, error) {
	st, b, err := t.get("/repos/"+repo, nil, "application/vnd.github+json")
	if err != nil {
		return "", err
	}
	if st != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", st)
	}
	var v struct {
		Default string `json:"default_branch"`
	}
	if json.Unmarshal(b, &v) != nil || v.Default == "" || strings.ContainsAny(v.Default, " \t\r\n") {
		return "", errors.New("no default branch")
	}
	return v.Default, nil
}

// production is the list .lea/production holds on ref (one branch per line, CRs and blanks cut,
// blank and # lines dropped); empty when the file is missing, which only an HTTP 404 says.
func (t *treeCtx) production(repo, ref string) ([]string, error) {
	st, b, err := t.get("/repos/"+repo+"/contents/.lea/production", url.Values{"ref": {ref}}, "application/vnd.github.raw")
	if err != nil {
		return nil, err
	}
	switch st {
	case http.StatusNotFound:
		return nil, nil
	case http.StatusOK:
		return ProductionList(string(b)), nil
	}
	return nil, fmt.Errorf("HTTP %d", st)
}

// ProductionList is the branches a .lea/production names.
func ProductionList(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(strings.TrimSuffix(l, "\r"))
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		out = append(out, l)
	}
	return out
}

// graphql checks a GraphQL call of a tree container (graphqlRoute read its body and allowed it):
// a mutation that merges is refused unless it is the one mergePullRequest of gh pr merge, with a
// merge token for that PR; an auto-merge is refused.
func (t *treeCtx) graphql() string {
	body, err := io.ReadAll(t.r.Body)
	if err != nil {
		return "an unreadable GraphQL call"
	}
	t.r.Body = io.NopCloser(bytes.NewReader(body))
	q, err := graphqlQuery(body)
	if err != nil {
		return "a GraphQL call lea cannot read"
	}
	fields, err := graphqlMutationRoots(q)
	if err != nil {
		return "a GraphQL document lea cannot read"
	}
	merge := false
	for _, f := range fields {
		switch f {
		case "enablePullRequestAutoMerge":
			return "in a tower's tree, an auto-merge (GitHub would merge later, past the production-merge policy)"
		case "mergePullRequest":
			merge = true
		}
	}
	if !merge {
		return ""
	}
	if len(fields) != 1 {
		return "in a tower's tree, a merge with other mutations in the same call"
	}
	// gh's form only: mergePullRequest(input: $input), the PR in the variables, so that the PR
	// the forwarder reads is the one GitHub merges.
	if strings.Contains(q, "pullRequestId") {
		return "in a tower's tree, a merge whose PR is not in the variables (gh pr merge's form)"
	}
	var v struct {
		Variables map[string]json.RawMessage `json:"variables"`
	}
	var in struct {
		PullRequestID string `json:"pullRequestId"`
	}
	if json.Unmarshal(body, &v) != nil || len(v.Variables) != 1 || v.Variables["input"] == nil ||
		json.Unmarshal(v.Variables["input"], &in) != nil || in.PullRequestID == "" {
		return "in a tower's tree, a merge whose PR lea cannot read (gh pr merge's form: one input variable)"
	}
	repo, n, err := t.pullRequest(in.PullRequestID)
	if err != nil {
		return "in a tower's tree, lea cannot read the PR of a merge (" + err.Error() + "), so it refuses it"
	}
	return t.spend(GrantMerge, repo, "", n)
}

// pullRequest is the repository (owner/name) and number of the PR whose node id is id.
func (t *treeCtx) pullRequest(id string) (string, int64, error) {
	b, _ := json.Marshal(map[string]any{
		"query":     "query($id:ID!){node(id:$id){... on PullRequest{number repository{nameWithOwner}}}}",
		"variables": map[string]string{"id": id},
	})
	st, res, err := t.do(http.MethodPost, "/graphql", nil, "application/json", b)
	if err != nil {
		return "", 0, err
	}
	if st != http.StatusOK {
		return "", 0, fmt.Errorf("HTTP %d", st)
	}
	var v struct {
		Data struct {
			Node *struct {
				Number     int64 `json:"number"`
				Repository struct {
					NameWithOwner string `json:"nameWithOwner"`
				} `json:"repository"`
			} `json:"node"`
		} `json:"data"`
	}
	if json.Unmarshal(res, &v) != nil || v.Data.Node == nil || v.Data.Node.Number < 1 || v.Data.Node.Repository.NameWithOwner == "" {
		return "", 0, errors.New("not a pull request")
	}
	return strings.ToLower(v.Data.Node.Repository.NameWithOwner), v.Data.Node.Number, nil
}

// localAddr is the proxy's address the request arrived on.
func localAddr(r *http.Request) net.IP {
	if a, ok := r.Context().Value(http.LocalAddrContextKey).(*net.TCPAddr); ok {
		return a.IP
	}
	return nil
}
