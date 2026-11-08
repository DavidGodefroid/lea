package main

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/DavidGodefroid/lea/internal/grants"
)

// What waits for the user on the host (#135): the permission requests of lea's agents (approval
// cards) and the spawn requests routed to the user. Without the deck (LEA_DECK=0) they are
// answered here only; with it, here or in the deck. Lock requests are not lea's: lox run.
//
// The two have their own numbers, so lea requests names them p<N> (a permission) and s<N> (a
// spawn request); a bare number is a permission, as lea approve <id> allow|deny|always has always
// read it.

const decideUsage = "usage: lea approve ID [always] | lea approve ID allow|deny|always | lea deny ID [reason...]\n" +
	"       lea approve gN... [--expires 30d] | lea deny gN [reason...]\n" +
	"  ID: p<N> a permission (or N), s<N> a spawn request, g<N> a lead's grant request (lea requests lists them)"

// requestID is an ID of lea requests: kind 'p', 's' or 'g', and its number.
func requestID(s string) (kind byte, id int64, ok bool) {
	kind = 'p'
	if strings.HasPrefix(s, "p") || strings.HasPrefix(s, "s") || strings.HasPrefix(s, "g") {
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
	if kind == 'g' {
		return decideGrantRequests(verdict, args[1:], stdout, stderr)
	}
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

// decideGrantRequests answers leads' grant requests (#342): approve records each grant (for the
// lead and its children; --expires replaces the life the lead asked for), deny records none, with
// an optional reason. args are the request IDs (gN or N), then approve's --expires D or deny's
// reason words.
func decideGrantRequests(verdict string, args []string, stdout, stderr io.Writer) int {
	usage := func() int { fmt.Fprintln(stderr, decideUsage); return 2 }
	var ids []int64
	i := 0
	for ; i < len(args); i++ {
		id, err := strconv.ParseInt(strings.TrimPrefix(args[i], "g"), 10, 64)
		if err != nil || id < 1 || strings.HasPrefix(args[i], "+") {
			break
		}
		ids = append(ids, id)
	}
	rest := args[i:]
	if len(ids) == 0 {
		return usage()
	}
	p := map[string]any{}
	switch verdict {
	case "approve":
		if len(rest) == 2 && rest[0] == "--expires" {
			d, ok := expiry(rest[1])
			if !ok {
				return usage()
			}
			p["ttl_s"] = int64(d / time.Second)
		} else if len(rest) != 0 {
			return usage()
		}
	case "deny":
		if len(ids) > 1 && len(rest) > 0 {
			return usage()
		}
		p["reason"] = strings.Join(rest, " ")
	default:
		return usage()
	}
	p["verdict"] = verdict
	code := 0
	for _, id := range ids {
		p["id"] = id
		var g grants.Grant
		if err := hostCall("grant_request_decide", p, &g); err != nil {
			fmt.Fprintf(stderr, "lea: g%d: %s\n", id, clean(err.Error(), false))
			code = 1
			continue
		}
		if verdict == "approve" {
			fmt.Fprintf(stdout, "g%d approved: grant #%d: %s\n", id, g.ID, describeGrant(g))
		} else {
			fmt.Fprintf(stdout, "g%d denied\n", id)
		}
	}
	return code
}

// expiry reads a grant's life: a Go duration (8h, 90m) or a number of days (30d).
func expiry(s string) (time.Duration, bool) {
	if n, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil || days < 1 || days > 366 || strings.HasPrefix(n, "+") {
			return 0, false
		}
		return time.Duration(days) * 24 * time.Hour, true
	}
	d, err := time.ParseDuration(s)
	return d, err == nil && d > 0
}

// grantRequestLines prints the pending grant requests, one line each; it returns how many.
func grantRequestLines(stdout io.Writer) (int, error) {
	var gs []struct {
		ID                                            int64
		Container, From, Action, Repo, Branch, GitHub string
		Tag                                           string
		TTLS                                          int64 `json:"ttl_s"`
		Reason                                        string
	}
	if err := hostCall("grant_request_list", nil, &gs); err != nil {
		return 0, err
	}
	for _, g := range gs {
		where := ""
		if g.Branch != "" && g.Branch != "*" {
			where = " branch " + oneLine(g.Branch, 128)
		}
		if g.Tag != "" {
			where += " tag " + oneLine(g.Tag, 64)
		}
		fmt.Fprintf(stdout, "g%d %s (lead in %s) asks for a %s grant on repo %s (gh %s)%s for itself and its children, %s\n", g.ID,
			oneLine(g.From, 64), oneLine(g.Container, 64), oneLine(g.Action, 32), oneLine(g.Repo, 100), oneLine(g.GitHub, 160), where,
			time.Duration(g.TTLS)*time.Second)
		if g.Reason != "" {
			fmt.Fprintf(stdout, "    reason: %s\n", oneLine(g.Reason, 200))
		}
	}
	return len(gs), nil
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
	var gb bytes.Buffer
	ng, err := grantRequestLines(&gb)
	if err != nil {
		return fail(err)
	}
	if len(as) == 0 && len(rs) == 0 && ng == 0 {
		fmt.Fprintln(stdout, "nothing waits for you: no permission, spawn or grant request (Lock requests: lox run)")
		return 0
	}
	defer func() {
		if ng > 0 {
			stdout.Write(gb.Bytes())
			fmt.Fprintln(stdout, "grant requests: lea approve gN [gM...] [--expires 30d], lea deny gN [reason]")
		}
	}()
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
	if len(as)+len(rs) > 0 {
		fmt.Fprintln(stdout, "answer: lea approve ID [always] (a spawn request: lea approve sN), lea deny ID [reason]")
	}
	return 0
}
