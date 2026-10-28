package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/DavidGodefroid/lea/internal/grants"
)

const deliveryUse = "usage: lea delivery on [--yes]|off|status"

// runDelivery is the core form of lea delivery (lea delivery --container NAME --projects DIR
// on|off|status), issue #92: the launcher names the container and the projects directory. on
// reads, on the host and at that moment, the origin of each repository of DIR, and records
// standing grants (no expiry) so every agent of the lea pushes its own branch, opens and merges
// its PR there without a prompt; off revokes them. The host API only: no agent turns it on.
func runDelivery(args []string, stdout, stderr io.Writer) int {
	fail := func(err error) int { fmt.Fprintln(stderr, "lea:", clean(err.Error(), false)); return 1 }
	yes := len(args) == 6 && args[4] == "on" && args[5] == "--yes"
	if (len(args) != 5 && !yes) || args[0] != "--container" || args[2] != "--projects" || !filepath.IsAbs(args[3]) {
		fmt.Fprintln(stderr, deliveryUse)
		return 2
	}
	container, proj, verb := args[1], args[3], args[4]
	switch verb {
	case "on":
		ents, err := os.ReadDir(proj)
		if err != nil {
			return fail(err)
		}
		type repo struct {
			Repo   string `json:"repo"`
			GitHub string `json:"github"`
		}
		repos := []repo{}
		var skipped []string
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), ".") || !e.IsDir() {
				continue
			}
			url := repoOrigin(filepath.Join(proj, e.Name()))
			switch gh := originGitHub(url); {
			case url == "":
				skipped = append(skipped, e.Name()+"  skipped: no origin")
			case gh == "":
				skipped = append(skipped, e.Name()+"  skipped: origin is not on github.com")
			default:
				repos = append(repos, repo{e.Name(), gh})
			}
		}
		// An agent can write a repository's .git/config (or add a repository): a pin is recorded
		// only once the user has seen it. Without --yes, on applies only when every pin is the
		// last one recorded for that repository of this lea (active, revoked or expired);
		// otherwise it lists the pins and records nothing.
		var cur struct{ Grants []grants.Grant }
		if err := hostCall("grant_list", map[string]any{"all": true}, &cur); err != nil {
			return fail(err)
		}
		was := map[string]string{}
		for _, g := range cur.Grants { // newest first
			if _, seen := was[g.Repo]; !seen && g.Container == container && g.IsStanding() {
				was[g.Repo] = g.GitHub
			}
		}
		unseen := false
		for _, x := range repos {
			switch w, ok := was[x.Repo]; {
			case !ok:
				fmt.Fprintf(stderr, "lea: %s: new pin github.com/%s\n", oneLine(x.Repo, 100), oneLine(x.GitHub, 160))
				unseen = true
			case w != "github.com/"+x.GitHub:
				fmt.Fprintf(stderr, "lea: %s: origin changed from %s to github.com/%s\n", oneLine(x.Repo, 100), oneLine(w, 160), oneLine(x.GitHub, 160))
				unseen = true
			}
		}
		if unseen && !yes {
			for _, sk := range skipped {
				fmt.Fprintf(stderr, "lea: %s\n", oneLine(sk, 200))
			}
			fmt.Fprintln(stderr, "lea: nothing recorded: check those repositories (agents can rewrite .git/config), then run lea delivery on --yes")
			return 1
		}
		var r struct{ Grants []grants.Grant }
		if err := hostCall("delivery_set", map[string]any{"container": container, "on": true, "repos": repos}, &r); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "delivery is on for %s: its agents push their own branch, open and merge their PR without asking\n", oneLine(container, 64))
		for _, x := range repos {
			fmt.Fprintf(stdout, "  %s  github.com/%s\n", oneLine(x.Repo, 100), oneLine(x.GitHub, 160))
		}
		for _, s := range skipped {
			fmt.Fprintf(stdout, "  %s\n", oneLine(s, 200))
		}
		fmt.Fprintln(stdout, "run lea delivery on again after adding a repository or changing an origin")
		return 0
	case "off":
		if err := hostCall("delivery_set", map[string]any{"container": container, "on": false}, nil); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "delivery is off for %s: its agents ask as usual\n", oneLine(container, 64))
		return 0
	case "status":
		var r struct {
			Grants []grants.Grant
			Now    int64
		}
		if err := hostCall("grant_list", map[string]any{}, &r); err != nil {
			return fail(err)
		}
		var on []string
		for _, g := range r.Grants {
			if g.Container == container && g.IsStanding() && g.Active(r.Now) && g.Action == grants.CommitIdentity {
				on = append(on, oneLine(g.Repo, 100)+" ("+oneLine(g.GitHub, 160)+")")
			}
		}
		if len(on) == 0 {
			fmt.Fprintf(stdout, "delivery is off for %s\n", oneLine(container, 64))
			return 0
		}
		fmt.Fprintf(stdout, "delivery is on: %s\n", strings.Join(on, ", "))
		return 0
	}
	fmt.Fprintln(stderr, deliveryUse)
	return 2
}

// githubOriginRe: an origin URL on github.com over https or ssh, OWNER/NAME.
var githubOriginRe = regexp.MustCompile(`^(?:https://(?:[A-Za-z0-9._-]+@)?github\.com/|git@github\.com:|ssh://git@github\.com/)` +
	`([A-Za-z0-9][A-Za-z0-9-]{0,38})/([A-Za-z0-9._-]{1,100}?)(?:\.git)?/?$`)

// originGitHub is OWNER/NAME of a github.com origin URL, or "".
func originGitHub(url string) string {
	m := githubOriginRe.FindStringSubmatch(url)
	if m == nil || m[2] == "." || m[2] == ".." {
		return ""
	}
	return m[1] + "/" + m[2]
}

// repoOrigin is the url of remote "origin" in dir's .git/config, read as a file: no git runs on
// the host against a repository its agents write. "" when there is none, more than one, or the
// file is not plain enough to read (a continued line).
func repoOrigin(dir string) string {
	fi, err := os.Lstat(filepath.Join(dir, ".git"))
	if err != nil || !fi.IsDir() {
		return ""
	}
	cfg := filepath.Join(dir, ".git", "config")
	if fi, err := os.Lstat(cfg); err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	// O_NOFOLLOW and O_NONBLOCK: the file swapped for a symlink or a FIFO after the Lstat is
	// refused, not followed or waited on.
	f, err := os.OpenFile(cfg, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return ""
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	b, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil || len(b) > 1<<20 {
		return ""
	}
	var urls []string
	in := false
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasSuffix(line, `\`) {
			return ""
		}
		if strings.HasPrefix(line, "[") {
			in = line == `[remote "origin"]` || strings.EqualFold(strings.TrimSuffix(strings.TrimPrefix(line, "["), `"origin"]`), `remote `)
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !in || !ok || !strings.EqualFold(strings.TrimSpace(k), "url") {
			continue
		}
		v = strings.TrimSpace(v)
		if strings.HasPrefix(v, `"`) {
			end := strings.IndexByte(v[1:], '"')
			if end < 0 {
				return ""
			}
			v = v[1 : 1+end]
		} else if i := strings.IndexAny(v, ";#"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		urls = append(urls, v)
	}
	if len(urls) != 1 {
		return ""
	}
	return urls[0]
}
