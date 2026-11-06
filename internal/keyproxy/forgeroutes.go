package keyproxy

// The forge forwarder's allowlist (issue #103): the token reaches only the GitHub calls lea's gh
// and git workflows make. A denylist of the calls that would hand access away (keys, webhooks)
// leaves the rest open: the /repositories/{id} and /organizations/{id} aliases of the same
// routes, collaborators, invitations, a transfer, a repository's settings, organisation
// memberships, Actions secrets and runners, GraphQL mutations. So every call on a token host is
// refused unless it is one of these:
//
//   - api.github.com, reads (GET, HEAD) of the namespaces gh reads: the root, user, users,
//     repos/{owner}/{repo}/…, orgs/{org}/…, search, rate_limit, meta, notifications and a few
//     static ones. The ID aliases (repositories, organizations, teams …) are refused.
//   - api.github.com, writes under repos/{owner}/{repo}/: issues, pulls, labels, milestones,
//     contents (PUT, DELETE), git (refs, blobs, trees, commits, tags), releases, check-runs,
//     check-suites, statuses, merge-upstream, and of Actions only a run's rerun, cancel or
//     deletion, a job's rerun and a workflow's dispatch, enabling or disabling. Not the
//     repository itself (PATCH, DELETE), its collaborators, invitations, transfer, keys, hooks,
//     settings, secrets, variables or runners; no write outside repos/.
//   - api.github.com/graphql, POST: any query; a mutation only of the fields of graphqlMutations
//     (what gh issue, gh pr and gh project send).
//   - uploads.github.com: POST repos/{owner}/{repo}/releases/{id}/assets (gh release upload).
//   - github.com (git through url.insteadOf): the smart HTTP protocol ({owner}/{repo}/info/refs,
//     git-upload-pack, git-receive-pack), Git LFS (info/lfs/…), and reads of archive, raw and
//     releases/download.
//
// The path was canonicalised first (forgePath): no empty, "." or ".." segment, no escaped slash
// or backslash, no ";", so the route matched is the route GitHub serves.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
)

// MaxGraphQLBodyBytes caps a GraphQL request, which the forwarder reads whole to check it.
const MaxGraphQLBodyBytes = 1 << 20

// apiReadTops are the first segments of the API paths gh reads.
var apiReadTops = map[string]bool{"user": true, "users": true, "repos": true, "orgs": true, "search": true,
	"rate_limit": true, "meta": true, "notifications": true, "emojis": true, "licenses": true,
	"gitignore": true, "octocat": true, "zen": true, "versions": true}

// repoWrites are the segments after repos/{owner}/{repo}/ under which every write is allowed,
// with the methods it takes ("" for any of the forwarder's).
var repoWrites = map[string]string{"issues": "", "pulls": "", "labels": "", "milestones": "", "git": "",
	"releases": "", "check-runs": "POST PATCH", "check-suites": "POST PATCH", "statuses": "POST",
	"contents": "PUT DELETE", "merge-upstream": "POST"}

// graphqlMutations are the mutation fields lea's gh workflows send.
var graphqlMutations = map[string]bool{
	// gh issue
	"createIssue": true, "updateIssue": true, "closeIssue": true, "reopenIssue": true, "deleteIssue": true,
	"pinIssue": true, "unpinIssue": true, "createLinkedBranch": true,
	// comments, reactions, labels, assignees (issues and pull requests)
	"addComment": true, "updateIssueComment": true, "deleteIssueComment": true, "minimizeComment": true,
	"unminimizeComment": true, "addReaction": true, "removeReaction": true, "addLabelsToLabelable": true,
	"removeLabelsFromLabelable": true, "clearLabelsFromLabelable": true, "addAssigneesToAssignable": true,
	"removeAssigneesFromAssignable": true, "lockLockable": true, "unlockLockable": true,
	// gh pr
	"createPullRequest": true, "updatePullRequest": true, "closePullRequest": true, "reopenPullRequest": true,
	"mergePullRequest": true, "enablePullRequestAutoMerge": true, "disablePullRequestAutoMerge": true,
	"markPullRequestReadyForReview": true, "convertPullRequestToDraft": true, "updatePullRequestBranch": true,
	"requestReviews": true, "addPullRequestReview": true, "submitPullRequestReview": true,
	"updatePullRequestReview": true, "deletePullRequestReview": true, "dismissPullRequestReview": true,
	"addPullRequestReviewComment": true, "addPullRequestReviewThread": true, "addPullRequestReviewThreadReply": true,
	"updatePullRequestReviewComment": true, "deletePullRequestReviewComment": true,
	"resolveReviewThread": true, "unresolveReviewThread": true,
	// gh project (the kanban boards)
	"addProjectV2ItemById": true, "addProjectV2DraftIssue": true, "updateProjectV2ItemFieldValue": true,
	"clearProjectV2ItemFieldValue": true, "updateProjectV2ItemPosition": true, "archiveProjectV2Item": true,
	"unarchiveProjectV2Item": true, "deleteProjectV2Item": true,
	"__typename": true,
}

// validOwner and validRepo match GitHub's account and repository names (lower-cased by forgePath).
var (
	validOwner = regexp.MustCompile(`^[a-z0-9_-]{1,100}$`)
	validRepo  = regexp.MustCompile(`^[a-z0-9._-]{1,100}$`)
)

// read reports whether method only reads.
func read(method string) bool { return method == http.MethodGet || method == http.MethodHead }

// methodIn reports whether method is one of the space-separated list ("" is any).
func methodIn(method, list string) bool {
	if list == "" {
		return true
	}
	for _, m := range bytes.Fields([]byte(list)) {
		if string(m) == method {
			return true
		}
	}
	return false
}

// repoPath reports whether segs start with repos/{owner}/{repo} and something under it (n: the
// minimum number of segments).
func repoPath(segs []string, n int) bool {
	return len(segs) >= n && segs[0] == "repos" && validOwner.MatchString(segs[1]) && validRepo.MatchString(segs[2])
}

// forgeRoute is why a call on a token host is refused, "" when the allowlist has it. r's body is
// read (and put back) for a GraphQL call.
func forgeRoute(host string, r *http.Request, segs []string) string {
	m := r.Method
	switch host {
	case "api.github.com":
		if len(segs) == 1 && segs[0] == "" {
			if read(m) {
				return ""
			}
			return "a write on the API's root"
		}
		if len(segs) == 1 && segs[0] == "graphql" {
			return graphqlRoute(r)
		}
		if read(m) {
			switch {
			case !apiReadTops[segs[0]]:
				return "a read outside the API namespaces gh reads (an ID alias such as /repositories/{id} included)"
			case segs[0] == "repos" && !repoPath(segs, 3):
				return "a repository path without an owner and a name"
			case segs[0] == "orgs" && (len(segs) < 2 || !validOwner.MatchString(segs[1])):
				return "an organisation path without its name"
			}
			return ""
		}
		if !repoPath(segs, 4) {
			return "a write outside a repository's issues, pull requests, contents, git data, releases and checks"
		}
		rest := segs[3:]
		if list, ok := repoWrites[rest[0]]; ok {
			if methodIn(m, list) {
				return ""
			}
			return "this method on " + rest[0]
		}
		if rest[0] == "actions" && actionsWrite(m, rest[1:]) {
			return ""
		}
		return "a write on a repository's " + rest[0] + " (lea's allowlist: issues, pulls, labels, milestones, contents, git, releases, checks, statuses, a run's rerun or cancel, a workflow's dispatch)"
	case "uploads.github.com":
		if m == http.MethodPost && repoPath(segs, 6) && len(segs) == 6 && segs[3] == "releases" && segs[5] == "assets" {
			return ""
		}
		return "an upload other than a release asset"
	case "github.com":
		return gitRoute(m, segs)
	}
	return "a host the token does not go to"
}

// actionsWrite reports whether a write on repos/{o}/{r}/actions/<rest> is one gh run and gh
// workflow make: a run's rerun, cancel or deletion, a job's rerun, a workflow's dispatch, enable
// or disable. Not secrets, variables, runners (registration tokens), permissions or a fork run's
// approval.
func actionsWrite(m string, rest []string) bool {
	switch {
	case len(rest) == 2 && rest[0] == "runs" && m == http.MethodDelete:
		return true
	case len(rest) == 3 && rest[0] == "runs" && m == http.MethodPost:
		switch rest[2] {
		case "rerun", "rerun-failed-jobs", "cancel", "force-cancel":
			return true
		}
	case len(rest) == 3 && rest[0] == "jobs" && rest[2] == "rerun" && m == http.MethodPost:
		return true
	case len(rest) == 3 && rest[0] == "workflows":
		switch rest[2] {
		case "dispatches":
			return m == http.MethodPost
		case "enable", "disable":
			return m == http.MethodPut
		}
	}
	return false
}

// gitRoute is why a call on github.com is refused: git's smart HTTP protocol and Git LFS on
// {owner}/{repo}[.git], and reads of a repository's archives, raw files and release downloads.
func gitRoute(m string, segs []string) string {
	if len(segs) < 3 || !validOwner.MatchString(segs[0]) || !validRepo.MatchString(segs[1]) {
		return "a github.com path other than a repository's git protocol"
	}
	rest := segs[2:]
	switch {
	case len(rest) == 2 && rest[0] == "info" && rest[1] == "refs" && read(m):
		return ""
	case len(rest) == 1 && (rest[0] == "git-upload-pack" || rest[0] == "git-receive-pack") && m == http.MethodPost:
		return ""
	case len(rest) >= 3 && rest[0] == "info" && rest[1] == "lfs" && (read(m) || m == http.MethodPost || m == http.MethodPut):
		return ""
	case read(m) && (rest[0] == "archive" || rest[0] == "raw" || (rest[0] == "releases" && len(rest) > 1 && rest[1] == "download")):
		return ""
	}
	return "a github.com path other than a repository's git protocol"
}

// graphqlRoute is why a GraphQL call is refused: anything but a POST of a JSON body {"query",
// "variables", "operationName"} (no query string, no content encoding, each key once) whose
// document's mutations select only graphqlMutations. The body is read whole and put back.
func graphqlRoute(r *http.Request) string {
	if r.Method != http.MethodPost {
		return "GraphQL other than a POST"
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		return "a GraphQL call with a query string"
	}
	if ce := r.Header.Values("Content-Encoding"); len(ce) > 0 {
		return "a GraphQL call with a content encoding"
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		if mt, _, err := mime.ParseMediaType(ct); err != nil || mt != "application/json" {
			return "a GraphQL call that is not JSON"
		}
	}
	if r.ContentLength > MaxGraphQLBodyBytes {
		return "a GraphQL call over 1 MiB"
	}
	var body []byte
	if r.Body != nil {
		b, err := io.ReadAll(io.LimitReader(r.Body, MaxGraphQLBodyBytes+1))
		if err != nil {
			return "an unreadable GraphQL call"
		}
		body = b
	}
	if len(body) > MaxGraphQLBodyBytes {
		return "a GraphQL call over 1 MiB"
	}
	r.Body, r.ContentLength = io.NopCloser(bytes.NewReader(body)), int64(len(body))
	r.Header.Del("Transfer-Encoding")
	r.TransferEncoding = nil
	q, err := graphqlQuery(body)
	if err != nil {
		return "a GraphQL call lea cannot read: " + err.Error()
	}
	bad, err := graphqlMutationFields(q)
	if err != nil {
		return "a GraphQL document lea cannot read: " + err.Error()
	}
	if bad != "" {
		return "the GraphQL mutation " + bad + " (lea's allowlist: issues, pull requests, comments, labels, reviews, project items)"
	}
	return ""
}

// graphqlQuery is the "query" of a GraphQL JSON body: an object whose keys are query (a string),
// variables (an object or null) and operationName (a string or null), each at most once.
func graphqlQuery(body []byte) (string, error) {
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	if t, err := d.Token(); err != nil || t != json.Delim('{') {
		return "", errors.New("not a JSON object")
	}
	seen := map[string]bool{}
	query, has := "", false
	for d.More() {
		t, err := d.Token()
		if err != nil {
			return "", err
		}
		k, _ := t.(string)
		if seen[k] {
			return "", errors.New("a key twice")
		}
		seen[k] = true
		var v json.RawMessage
		if err := d.Decode(&v); err != nil {
			return "", err
		}
		switch k {
		case "query":
			if err := json.Unmarshal(v, &query); err != nil {
				return "", errors.New("query is not a string")
			}
			has = true
		case "variables":
			if s := bytes.TrimSpace(v); !bytes.Equal(s, []byte("null")) && (len(s) == 0 || s[0] != '{') {
				return "", errors.New("variables is not an object")
			}
		case "operationName":
			var s *string
			if err := json.Unmarshal(v, &s); err != nil {
				return "", errors.New("operationName is not a string")
			}
		default:
			return "", errors.New("an unknown key")
		}
	}
	if t, err := d.Token(); err != nil || t != json.Delim('}') {
		return "", errors.New("not a JSON object")
	}
	if _, err := d.Token(); err != io.EOF {
		return "", errors.New("data after the object")
	}
	if !has {
		return "", errors.New("no query")
	}
	return query, nil
}

// gqlTok is a GraphQL token: a punctuator ("{", "...", …), a name, or a value (string, number).
type gqlTok struct {
	kind byte // 'p' punctuator, 'n' name, 'v' value
	s    string
}

// gqlLex splits a GraphQL document into tokens (GraphQL's lexical grammar: whitespace, commas,
// comments and a BOM ignored). Anything it does not know fails.
func gqlLex(src string) ([]gqlTok, error) {
	var out []gqlTok
	isName := func(c byte, first bool) bool {
		return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (!first && c >= '0' && c <= '9')
	}
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ',':
			i++
		case len(src)-i >= 3 && src[i:i+3] == "\xef\xbb\xbf":
			i += 3
		case c == '#':
			for i < len(src) && src[i] != '\n' && src[i] != '\r' {
				i++
			}
		case len(src)-i >= 3 && src[i:i+3] == "...":
			out = append(out, gqlTok{'p', "..."})
			i += 3
		case bytes.IndexByte([]byte("!$&():=@[]{}|"), c) >= 0:
			out = append(out, gqlTok{'p', string(c)})
			i++
		case isName(c, true):
			j := i + 1
			for j < len(src) && isName(src[j], false) {
				j++
			}
			out = append(out, gqlTok{'n', src[i:j]})
			i = j
		case c == '-' || (c >= '0' && c <= '9'):
			j := i + 1
			for j < len(src) && (src[j] >= '0' && src[j] <= '9' || src[j] == '.' || src[j] == 'e' || src[j] == 'E' || src[j] == '+' || src[j] == '-') {
				j++
			}
			out = append(out, gqlTok{'v', src[i:j]})
			i = j
		case len(src)-i >= 3 && src[i:i+3] == `"""`:
			j := i + 3
			for {
				if j+3 > len(src) {
					return nil, errors.New("an unterminated block string")
				}
				if src[j] == '\\' && j+4 <= len(src) && src[j+1:j+4] == `"""` {
					j += 4
					continue
				}
				if src[j:j+3] == `"""` {
					break
				}
				j++
			}
			out = append(out, gqlTok{'v', "string"})
			i = j + 3
		case c == '"':
			j := i + 1
			for {
				if j >= len(src) || src[j] == '\n' || src[j] == '\r' {
					return nil, errors.New("an unterminated string")
				}
				if src[j] == '\\' {
					j += 2
					continue
				}
				if src[j] == '"' {
					break
				}
				j++
			}
			out = append(out, gqlTok{'v', "string"})
			i = j + 1
		default:
			return nil, errors.New("an unexpected character")
		}
	}
	return out, nil
}

// graphqlMutationFields is the first root field of a mutation of the document q that is not in
// graphqlMutations ("" when there is none). A subscription, or a fragment spread or inline fragment
// at a mutation's root (which would hide its fields), is refused as "subscription" or "...".
func graphqlMutationFields(q string) (string, error) {
	toks, err := gqlLex(q)
	if err != nil {
		return "", err
	}
	i := 0
	peek := func() gqlTok {
		if i < len(toks) {
			return toks[i]
		}
		return gqlTok{}
	}
	// skip passes a balanced group that opens at toks[i].
	skip := func() error {
		depth := 0
		for ; i < len(toks); i++ {
			if t := toks[i]; t.kind == 'p' {
				switch t.s {
				case "{", "(", "[":
					depth++
				case "}", ")", "]":
					depth--
				}
			}
			if depth == 0 {
				i++
				return nil
			}
			if depth < 0 {
				return errors.New("unbalanced brackets")
			}
		}
		return errors.New("unbalanced brackets")
	}
	// header passes an operation's or a fragment's name, variables and directives, up to its "{".
	header := func() error {
		for {
			t := peek()
			switch {
			case t.kind == 0:
				return errors.New("a definition without a selection set")
			case t.kind == 'p' && t.s == "{":
				return nil
			case t.kind == 'p' && t.s == "(":
				if err := skip(); err != nil {
					return err
				}
			case t.kind == 'p' && (t.s == "}" || t.s == ")" || t.s == "]" || t.s == "..."):
				return errors.New("an unexpected " + t.s)
			default:
				i++
			}
		}
	}
	if len(toks) == 0 {
		return "", errors.New("an empty document")
	}
	for i < len(toks) {
		t := peek()
		switch {
		case t.kind == 'p' && t.s == "{":
			if err := skip(); err != nil {
				return "", err
			}
		case t.kind == 'n' && (t.s == "query" || t.s == "fragment"):
			i++
			if err := header(); err != nil {
				return "", err
			}
			if err := skip(); err != nil {
				return "", err
			}
		case t.kind == 'n' && t.s == "subscription":
			return "subscription", nil
		case t.kind == 'n' && t.s == "mutation":
			i++
			if err := header(); err != nil {
				return "", err
			}
			i++ // the root "{"
			for {
				f := peek()
				if f.kind == 'p' && f.s == "}" {
					i++
					break
				}
				if f.kind == 'p' && f.s == "..." {
					return "...", nil
				}
				if f.kind != 'n' {
					return "", errors.New("a mutation root without a field name")
				}
				i++
				name := f.s
				if a := peek(); a.kind == 'p' && a.s == ":" {
					i++
					if n := peek(); n.kind == 'n' {
						name = n.s
						i++
					} else {
						return "", errors.New("an alias without a field")
					}
				}
				if !graphqlMutations[name] {
					return name, nil
				}
				if a := peek(); a.kind == 'p' && a.s == "(" {
					if err := skip(); err != nil {
						return "", err
					}
				}
				for d := peek(); d.kind == 'p' && d.s == "@"; d = peek() {
					i++
					if n := peek(); n.kind != 'n' {
						return "", errors.New("a directive without a name")
					}
					i++
					if a := peek(); a.kind == 'p' && a.s == "(" {
						if err := skip(); err != nil {
							return "", err
						}
					}
				}
				if s := peek(); s.kind == 'p' && s.s == "{" {
					if err := skip(); err != nil {
						return "", err
					}
				}
				if i >= len(toks) {
					return "", errors.New("an unterminated mutation")
				}
			}
		default:
			return "", errors.New("an unexpected token")
		}
	}
	return "", nil
}
