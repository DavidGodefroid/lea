package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/DavidGodefroid/lea/internal/agents"
	"github.com/DavidGodefroid/lea/internal/launcher"
)

const lockUse = "usage: lea lock open|close TOOL [PROFILE] | lea lock list"

// runLock is the core form of lea lock (lea lock --container NAME open|close|list ...): the
// launcher names the container. The host API only: no agent opens, closes or lists a console.
func runLock(args []string, stdout, stderr io.Writer) int {
	if len(args) < 3 || args[0] != "--container" {
		fmt.Fprintln(stderr, "lea lock: run it as `lea lock`: the launcher names the container")
		return 2
	}
	container, verb, rest := args[1], args[2], args[3:]
	switch {
	case verb == "list" && len(rest) == 0:
		var rows []agents.LockRow
		if err := hostCall("lock_list", map[string]any{"container": container}, &rows); err != nil {
			fmt.Fprintln(stderr, "lea:", clean(err.Error(), false))
			return 1
		}
		if len(rows) == 0 {
			fmt.Fprintln(stdout, "no lock console in this deck")
		}
		for _, r := range rows {
			where := "parked"
			if r.Shown {
				where = "shown"
			}
			fmt.Fprintf(stdout, "%s\t%s\n", clean(r.ID, false), where)
		}
		return 0
	case (verb == "open" || verb == "close") && (len(rest) == 1 || len(rest) == 2):
		profile := ""
		if len(rest) == 2 {
			profile = rest[1]
		}
		var r struct{ Status string }
		if err := hostCallT("lock_"+verb, map[string]any{"container": container, "tool": rest[0], "profile": profile},
			&r, agentTimeout); err != nil {
			fmt.Fprintln(stderr, "lea:", clean(err.Error(), false))
			return 1
		}
		fmt.Fprintln(stdout, clean(strings.TrimSpace(rest[0]+" "+profile), false)+": "+clean(r.Status, false))
		return 0
	}
	fmt.Fprintln(stderr, lockUse)
	return 2
}

// lockSpec reads a lock tool's console configuration from lea's config files at each open, as
// lea locksql does: config/lea.conf of the checkout the daemon runs from, then the user's.
func lockSpec(exe string) func(tool string) (agents.LockSpec, error) {
	root := filepath.Dir(filepath.Dir(exe)) // <root>/build/lea
	return func(tool string) (agents.LockSpec, error) {
		cwd, err := os.Getwd()
		if err != nil {
			return agents.LockSpec{}, err
		}
		cfg, err := launcher.LoadConfig(os.Getenv, root, cwd)
		if err != nil {
			return agents.LockSpec{}, err
		}
		switch tool {
		case "locksql":
			return agents.LockSpec{Enabled: cfg.Locksql(), Image: cfg.Get("LEA_LOCKSQL_IMAGE"),
				Projects: cfg.Get("LEA_PROJECTS_DIR"), VaultAddr: cfg.Get("LEA_LOCKSQL_VAULT_ADDR")}, nil
		}
		return agents.LockSpec{}, fmt.Errorf("no lock console for %q", tool)
	}
}
