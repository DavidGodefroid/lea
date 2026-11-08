package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/DavidGodefroid/lea/internal/grants"
)

const grantUsage = `usage: lea grant revoke <id>   (one grant row; lea allow off N revokes a whole allowance)
       lea grant requests | lea grant approve <id>... [--expires 30d] | lea grant deny <id> [reason...]
       (a lead's grant requests, g<id>: approving one records the grant for the lead and its children)
actions: ` + "merge, stop-finished, close-issue, delete-own-branch, commit-identity, pr-create, release-tag"

// runGrant is lea grant: a lead's grant requests (#342) and revoking one grant row. lea grant
// add and ls are aliases of lea allow (#319), which the launcher translates: it names the
// container. It talks to the host API, which no container can reach.
func runGrant(args []string, stdout, stderr io.Writer) int {
	fail := func(err error) int { fmt.Fprintln(stderr, "lea:", clean(err.Error(), false)); return 1 }
	bad := func() int { fmt.Fprintln(stderr, grantUsage); return 2 }
	if len(args) < 2 {
		return bad()
	}
	switch args[1] {
	case "requests":
		if len(args) != 2 {
			return bad()
		}
		n, err := grantRequestLines(stdout)
		if err != nil {
			return fail(err)
		}
		if n == 0 {
			fmt.Fprintln(stdout, "no grant request waits for you")
		}
		return 0
	case "approve", "deny":
		if len(args) < 3 {
			return bad()
		}
		return decideGrantRequests(args[1], args[2:], stdout, stderr)
	case "revoke":
		if len(args) != 3 {
			return bad()
		}
		id, err := strconv.ParseInt(strings.TrimPrefix(args[2], "#"), 10, 64)
		if err != nil || id < 1 {
			return bad()
		}
		if err := hostCall("grant_revoke", map[string]any{"id": id}, nil); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "grant #%d revoked\n", id)
		fmt.Fprintln(stderr, "lea: note: lea grant revoke goes away in the next minor release: lea allow lists allowances, lea allow off N revokes one")
		return 0
	}
	return bad()
}

func describeGrant(g grants.Grant) string {
	s := g.Action + " in " + oneLine(g.Container, 64)
	if g.Repo != "" {
		s += " repo " + oneLine(g.Repo, 100)
	} else {
		s += " (every repo)"
	}
	if g.Pin != "" {
		if strings.HasPrefix(g.Pin, "gitlab:") {
			s += " " + oneLine(g.Pin, 200)
		} else {
			s += " gh " + oneLine(g.Pin, 160)
		}
	}
	if g.Branch != "" && g.Branch != "*" {
		s += " branch " + oneLine(g.Branch, 128)
	}
	if g.Tag != "" {
		s += " tag " + oneLine(g.Tag, 64)
	}
	if g.Agent != "" {
		s += " for " + oneLine(g.Agent, 129) + " and its children"
	}
	if g.Expires == grants.Standing {
		return s + fmt.Sprintf(" until lea allow off %d", g.SetID)
	}
	return s + " until " + stamp(g.Expires)
}
