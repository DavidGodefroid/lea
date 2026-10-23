// Package grants decides, deterministically, whether a tool call an agent is about to make is
// covered by a grant the user recorded on the host (lea grant add), or is one of the hard denies
// no grant can cover. It never sees who granted what beyond the rows it is given: the daemon
// stores grants and only the host API writes them (issue #73).
//
// Fail closed: a command the parser does not fully understand is never allowed. Hard denies are
// checked first, on the raw command as well as on its words, so shell chaining cannot hide them.
package grants

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// The actions a grant can name: a closed list, each mapped to concrete tool calls below.
const (
	Merge           = "merge"
	StopFinished    = "stop-finished"
	CloseIssue      = "close-issue"
	DeleteOwnBranch = "delete-own-branch"
	CommitIdentity  = "commit-identity"
)

// Actions lists every action a grant may name, in display order.
var Actions = []string{Merge, StopFinished, CloseIssue, DeleteOwnBranch, CommitIdentity}

// ValidAction says whether a is one of Actions.
func ValidAction(a string) bool {
	for _, x := range Actions {
		if x == a {
			return true
		}
	}
	return false
}

// StopTool is the MCP tool stop-finished covers.
const StopTool = "mcp__lea__agents_stop"

// Grant is one user decision. Container is the lea it applies to; Repo the repository ("" any
// repository of that lea); Branch a glob on the caller's branch ("" or "*" any); Agent the key of
// the agent whose subtree it covers ("" every agent of the lea). Expires and Revoked are unix
// seconds, Revoked 0 while active.
type Grant struct {
	ID                              int64
	Container, Action, Repo, Branch string
	Agent                           string
	Created, Expires, Revoked       int64
}

// Active says whether g still applies at now.
func (g Grant) Active(now int64) bool {
	return g.Revoked == 0 && g.Expires > now
}

// Caller is the agent making the call, as the daemon knows it (never as the agent says).
// Lineage is its key followed by its ancestors' keys, nearest first.
type Caller struct {
	Key, Container, Repo, Branch string
	Lineage                      []string
}

// Request is a tool call recognised as one action. Target names what it acts on, for the audit
// ("pr 42", "issue 7", a branch, a child agent's name): never the tool input itself.
type Request struct {
	Action, Target string
}

// Verdicts of Decide.
const (
	Allow = "allow"
	Deny  = "deny"
)

// Decision is Decide's answer. Verdict "" means no opinion: the call takes the normal path
// (Claude's own checks, the deck).
type Decision struct {
	Verdict, Reason string
	Grant           int64
	Request         Request
}

// Classify parses a tool call: hard is the reason of a hard deny ("" when none); req is the
// action the call is, when it is exactly one the grants know (ok false otherwise). It never
// allows anything by itself.
func Classify(tool string, input json.RawMessage, c Caller) (hard string, req Request, ok bool) {
	switch tool {
	case "Bash":
		var in struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(input, &in) != nil {
			return "", Request{}, false
		}
		if h := hardBash(in.Command, c); h != "" {
			return h, Request{}, false
		}
		words, wok := Words(in.Command)
		if !wok {
			return "", Request{}, false
		}
		req, ok = bashRequest(words, c)
		return "", req, ok
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		var in struct {
			FilePath     string `json:"file_path"`
			NotebookPath string `json:"notebook_path"`
		}
		_ = json.Unmarshal(input, &in)
		for _, p := range []string{in.FilePath, in.NotebookPath} {
			if protectedPath(p) {
				return "editing Claude's or lea's own settings from inside an agent is never allowed", Request{}, false
			}
		}
		return "", Request{}, false
	case StopTool:
		var in struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(input, &in) != nil || in.Name == "" || len(in.Name) > 129 {
			return "", Request{}, false
		}
		return "", Request{Action: StopFinished, Target: in.Name}, true
	}
	return "", Request{}, false
}

// Match returns the first active grant of gs that covers req for c, or ok false.
func Match(req Request, c Caller, gs []Grant, now int64) (Grant, bool) {
	for _, g := range gs {
		if g.Action != req.Action || !g.Active(now) || g.Container != c.Container {
			continue
		}
		if g.Repo != "" && g.Repo != c.Repo {
			continue
		}
		if g.Branch != "" && !Glob(g.Branch, c.Branch) {
			continue
		}
		if g.Agent != "" && !contains(c.Lineage, g.Agent) {
			continue
		}
		return g, true
	}
	return Grant{}, false
}

// Decide is Classify then Match, for every action whose conditions the call itself carries.
// stop-finished also needs the target to be the caller's finished child: the daemon checks that
// before it calls Decide with finished true (false otherwise).
func Decide(tool string, input json.RawMessage, c Caller, gs []Grant, now int64, finished bool) Decision {
	hard, req, ok := Classify(tool, input, c)
	if hard != "" {
		return Decision{Verdict: Deny, Reason: "lea hard deny: " + hard}
	}
	if !ok || (req.Action == StopFinished && !finished) {
		return Decision{}
	}
	g, ok := Match(req, c, gs, now)
	if !ok {
		return Decision{}
	}
	return Decision{Verdict: Allow, Grant: g.ID, Request: req,
		Reason: fmt.Sprintf("lea grant #%d (%s) recorded by the user", g.ID, g.Action)}
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// Glob matches s against pattern, where * matches any run of characters (slashes included) and
// every other character itself.
func Glob(pattern, s string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	last := len(parts) - 1
	if last == 0 {
		return s == ""
	}
	for _, p := range parts[1:last] {
		i := strings.Index(s, p)
		if i < 0 {
			return false
		}
		s = s[i+len(p):]
	}
	return strings.HasSuffix(s, parts[last])
}

// Protected says whether a branch is one no agent may push to or delete: the default branch.
func Protected(b string) bool {
	b = strings.TrimPrefix(b, "refs/heads/")
	return b == "main" || b == "master" || b == "HEAD" || b == ""
}

var protectedPathRe = regexp.MustCompile(`(^|/)\.claude/(settings[^/]*\.json|hooks(/|$))|(^|[\s'"=])/usr/local/share/lea(/|$)|(^|[\s'"=])/lea/state(/|$)|(^|/)\.config/lea(/|$)`)

// protectedPath says whether p is one of Claude's settings or hook files, lea's settings for Claude
// (/usr/local/share/lea), lea's state in the container (/lea/state: git identity, gh login, SSH)
// or lea's config.
func protectedPath(p string) bool {
	return p != "" && protectedPathRe.MatchString(p)
}
