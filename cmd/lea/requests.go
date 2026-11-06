package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

// What waits for the user on the host (#135): the permission requests of lea's agents (approval
// cards) and the spawn requests routed to the user. Without the deck (LEA_DECK=0) they are
// answered here only; with it, here or in the deck. Lock requests are not lea's: lox run.
//
// The two have their own numbers, so lea requests names them p<N> (a permission) and s<N> (a
// spawn request); a bare number is a permission, as lea approve <id> allow|deny|always has always
// read it.

const decideUsage = "usage: lea approve ID [always] | lea approve ID allow|deny|always | lea deny ID [reason...]\n" +
	"  ID: p<N> a permission (or N), s<N> a spawn request (lea requests lists them)"

// requestID is an ID of lea requests: kind 'p' or 's', and its number.
func requestID(s string) (kind byte, id int64, ok bool) {
	kind = 'p'
	if strings.HasPrefix(s, "p") || strings.HasPrefix(s, "s") {
		kind, s = s[0], s[1:]
	}
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id < 1 || strings.HasPrefix(s, "+") {
		return 0, 0, false
	}
	return kind, id, true
}

// runDecide is lea approve and lea deny.
func runDecide(args []string, stdout, stderr io.Writer) int {
	usage := func() int { fmt.Fprintln(stderr, decideUsage); return 2 }
	if len(args) < 2 {
		return usage()
	}
	kind, id, ok := requestID(args[1])
	if !ok {
		return usage()
	}
	verdict, rest := args[0], args[2:]
	if kind == 's' {
		// A spawn request: accepted (lea approve sN) or declined with a reason (lea deny sN why).
		if verdict == "approve" && len(rest) > 0 {
			return usage()
		}
		v := map[string]string{"approve": "accept", "deny": "decline"}[verdict]
		var r struct{ Name string }
		if err := hostCallT("request_decide", map[string]any{"id": id, "verdict": v, "reason": strings.Join(rest, " ")},
			&r, agentTimeout); err != nil {
			fmt.Fprintln(stderr, "lea:", clean(err.Error(), false))
			return 1
		}
		if v == "accept" {
			fmt.Fprintf(stdout, "started %s for spawn request s%d\n", clean(r.Name, false), id)
		} else {
			fmt.Fprintf(stdout, "declined spawn request s%d\n", id)
		}
		return 0
	}
	switch {
	case verdict == "approve" && len(rest) == 0:
		verdict = "allow"
	case verdict == "approve" && len(rest) == 1 && (rest[0] == "allow" || rest[0] == "deny" || rest[0] == "always"):
		verdict = rest[0]
	case verdict == "deny" && len(rest) == 0:
	default:
		return usage()
	}
	if err := hostCall("approval_decide", map[string]any{"id": id, "verdict": verdict}, nil); err != nil {
		fmt.Fprintln(stderr, "lea:", clean(err.Error(), false))
		return 1
	}
	fmt.Fprintf(stdout, "%s: p%d\n", map[string]string{"allow": "allowed", "always": "allowed, and always", "deny": "denied"}[verdict], id)
	return 0
}

// runRequests is lea requests: every permission and spawn request that waits for the user.
func runRequests(stdout, stderr io.Writer) int {
	fail := func(err error) int { fmt.Fprintln(stderr, "lea:", clean(err.Error(), false)); return 1 }
	var as []struct {
		ID                 int64
		Agent, Tool, Input string
		Always             bool
	}
	if err := hostCall("approval_list", nil, &as); err != nil {
		return fail(err)
	}
	var rs []struct {
		ID                                  int64
		Container, From, Role, Repo, Branch string
		Brief, Reason                       string
		Size                                int
	}
	if err := hostCall("request_list", nil, &rs); err != nil {
		return fail(err)
	}
	if len(as) == 0 && len(rs) == 0 {
		fmt.Fprintln(stdout, "nothing waits for you: no permission or spawn request (Lock requests: lox run)")
		return 0
	}
	for _, a := range as {
		fmt.Fprintf(stdout, "p%d %s wants %s: %s\n", a.ID, oneLine(a.Agent, 64), oneLine(a.Tool, 64), oneLine(a.Input, 200))
	}
	for _, r := range rs {
		where := ""
		if r.Repo != "" {
			where += " repo " + r.Repo
		}
		if r.Branch != "" {
			where += " branch " + r.Branch
		}
		fmt.Fprintf(stdout, "s%d %s asks for a %s%s: %s (%d bytes)\n", r.ID, oneLine(r.From, 64), oneLine(r.Role, 16), oneLine(where, 200),
			oneLine(firstLine(r.Brief), 120), r.Size)
		if r.Reason != "" {
			fmt.Fprintf(stdout, "    reason: %s\n", oneLine(r.Reason, 200))
		}
	}
	fmt.Fprintln(stdout, "answer: lea approve ID [always] (a spawn request: lea approve sN), lea deny ID [reason]")
	return 0
}
