package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/DavidGodefroid/lea/internal/grants"
)

const grantUsage = `usage: lea grant add <action> --container NAME [--repo R] [--github OWNER/NAME] [--branch GLOB] [--agent NAME] [--expires 8h]
       (merge and close-issue need --github: every gh call they allow is pinned to it)
       lea grant ls [--all]
       lea grant revoke <id>
actions: ` + "merge, stop-finished, close-issue, delete-own-branch, commit-identity, pr-create"

// runGrant is lea grant: the user's only channel to record, list and revoke grants (issue #73).
// It talks to the host API, which no container can reach.
func runGrant(args []string, stdout, stderr io.Writer) int {
	fail := func(err error) int { fmt.Fprintln(stderr, "lea:", clean(err.Error(), false)); return 1 }
	bad := func() int { fmt.Fprintln(stderr, grantUsage); return 2 }
	if len(args) < 2 {
		return bad()
	}
	switch args[1] {
	case "add":
		if len(args) < 3 || !grants.ValidAction(args[2]) {
			return bad()
		}
		p := map[string]any{"action": args[2]}
		for i := 3; i < len(args); i++ {
			if i+1 >= len(args) {
				return bad()
			}
			v := args[i+1]
			switch args[i] {
			case "--container", "--lea":
				p["container"] = v
			case "--repo":
				p["repo"] = v
			case "--github":
				p["github"] = v
			case "--branch":
				p["branch"] = v
			case "--agent":
				p["agent"] = v
			case "--expires":
				d, err := time.ParseDuration(v)
				if err != nil || d <= 0 {
					return bad()
				}
				p["ttl_s"] = int64(d / time.Second)
			default:
				return bad()
			}
			i++
		}
		if p["container"] == nil {
			return bad()
		}
		var g grants.Grant
		if err := hostCall("grant_add", p, &g); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "grant #%d: %s\n", g.ID, describeGrant(g))
		return 0
	case "ls":
		all := len(args) == 3 && args[2] == "--all"
		if len(args) > 3 || (len(args) == 3 && !all) {
			return bad()
		}
		var r struct {
			Grants []grants.Grant
			Uses   []struct {
				Grant                 int64
				Agent, Action, Target string
				At                    int64
			}
			Now int64
		}
		if err := hostCall("grant_list", map[string]any{"all": all}, &r); err != nil {
			return fail(err)
		}
		if len(r.Grants) == 0 {
			fmt.Fprintln(stdout, "no active grant: agents ask as usual")
		} else {
			tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tSTATE\tGRANT")
			for _, g := range r.Grants {
				state := "active"
				switch {
				case g.Revoked != 0:
					state = "revoked"
				case g.Expires <= r.Now:
					state = "expired"
				case g.IsStanding():
					state = "standing"
				}
				fmt.Fprintf(tw, "#%d\t%s\t%s\n", g.ID, state, describeGrant(g))
			}
			tw.Flush()
		}
		if len(r.Uses) > 0 {
			fmt.Fprintln(stdout, "\nrecent uses:")
			for _, u := range r.Uses {
				fmt.Fprintf(stdout, "  %s #%d %s %s: %s\n", stamp(u.At), u.Grant, oneLine(u.Agent, 64), oneLine(u.Action, 32), oneLine(u.Target, 200))
			}
		}
		return 0
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
	if g.GitHub != "" {
		s += " gh " + oneLine(g.GitHub, 160)
	}
	if g.Branch != "" && g.Branch != "*" {
		s += " branch " + oneLine(g.Branch, 128)
	}
	if g.Agent != "" {
		s += " for " + oneLine(g.Agent, 129) + " and its children"
	}
	if g.IsStanding() {
		return s + " (lea delivery: until lea delivery off)"
	}
	return s + " until " + stamp(g.Expires)
}
