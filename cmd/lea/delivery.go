package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/DavidGodefroid/lea/internal/grants"
)

const deliveryUse = "usage: lea delivery on [--yes DIGEST] [--gitlab HOST]...|off|status"

// runDelivery is the core form of lea delivery (lea delivery --container NAME --projects DIR
// on [--yes DIGEST] [--gitlab HOST]...|off|status), issue #92: the launcher names the container and the
// projects directory. on reads, on the host and at that moment, the origin of each repository of
// DIR, and records standing grants (no expiry) so every agent of the lea pushes its own branch,
// opens and merges its PR (GitHub) or MR (GitLab) there without a prompt; off revokes them. The
// host API only: no agent turns it on. GitLab is gitlab.com, each --gitlab HOST, and the hosts
// of the lea's GitLab pins already recorded.
func runDelivery(args []string, stdout, stderr io.Writer) int {
	fail := func(err error) int { fmt.Fprintln(stderr, "lea:", clean(err.Error(), false)); return 1 }
	if len(args) < 5 || args[0] != "--container" || args[2] != "--projects" || !filepath.IsAbs(args[3]) {
		fmt.Fprintln(stderr, deliveryUse)
		return 2
	}
	container, proj, verb := args[1], args[3], args[4]
	yes, confirmed := false, ""
	gitlab := map[string]bool{"gitlab.com": true}
	for i := 5; i < len(args); i++ {
		switch {
		case verb == "on" && args[i] == "--yes" && i+1 < len(args) && digestRe.MatchString(args[i+1]) && !yes:
			yes, confirmed = true, args[i+1]
			i++
		case verb == "on" && args[i] == "--gitlab" && i+1 < len(args) && gitlabHostRe.MatchString(strings.ToLower(args[i+1])):
			gitlab[strings.ToLower(args[i+1])] = true
			i++
		default:
			fmt.Fprintln(stderr, deliveryUse)
			return 2
		}
	}
	switch verb {
	case "on":
		ents, err := os.ReadDir(proj)
		if err != nil {
			return fail(err)
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
			if g.Container != container || !g.IsStanding() {
				continue
			}
			if _, seen := was[g.Repo]; !seen {
				was[g.Repo] = g.GitHub
			}
			if h, ok := strings.CutPrefix(g.GitHub, "gitlab:"); ok {
				h, _, _ = strings.Cut(h, "/")
				gitlab[h] = true
			}
		}
		type repo struct {
			Repo string `json:"repo"`
			Pin  string `json:"github"`
		}
		repos := []repo{}
		var skipped []string
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), ".") || !e.IsDir() {
				continue
			}
			url := repoOrigin(filepath.Join(proj, e.Name()))
			switch pin := originPin(url, gitlab); {
			case url == "":
				skipped = append(skipped, e.Name()+"  skipped: no origin")
			case pin == "":
				skipped = append(skipped, e.Name()+"  skipped: origin is not on github.com, gitlab.com or a --gitlab host")
			default:
				repos = append(repos, repo{e.Name(), pin})
			}
		}
		unseen := false
		for _, x := range repos {
			switch w, ok := was[x.Repo]; {
			case !ok:
				fmt.Fprintf(stderr, "lea: %s: new pin %s\n", oneLine(x.Repo, 100), oneLine(x.Pin, 200))
				unseen = true
			case w != x.Pin:
				fmt.Fprintf(stderr, "lea: %s: origin changed from %s to %s\n", oneLine(x.Repo, 100), oneLine(w, 200), oneLine(x.Pin, 200))
				unseen = true
			}
		}
		// --yes names the digest of the pins the user was shown: pins read again since (an agent
		// rewrote an origin or added a repository between the two commands) do not match it.
		sum := sha256.New()
		for _, x := range repos {
			fmt.Fprintf(sum, "%s\x00%s\n", x.Repo, x.Pin)
		}
		digest := hex.EncodeToString(sum.Sum(nil))[:16]
		if yes && confirmed != digest {
			fmt.Fprintln(stderr, "lea: nothing recorded: the pins changed since they were listed; run lea delivery on again and check them")
			return 1
		}
		if unseen && !yes {
			for _, sk := range skipped {
				fmt.Fprintf(stderr, "lea: %s\n", oneLine(sk, 200))
			}
			fmt.Fprintln(stderr, "lea: nothing recorded: check those repositories (agents can rewrite .git/config), then run lea delivery on --yes "+digest)
			return 1
		}
		var r struct{ Grants []grants.Grant }
		if err := hostCall("delivery_set", map[string]any{"container": container, "on": true, "repos": repos}, &r); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "delivery is on for %s: its agents push their own branch, open and merge their PR without asking\n", oneLine(container, 64))
		for _, x := range repos {
			fmt.Fprintf(stdout, "  %s  %s\n", oneLine(x.Repo, 100), oneLine(x.Pin, 200))
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
				on = append(on, oneLine(g.Repo, 100)+" ("+oneLine(g.GitHub, 200)+")")
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

var digestRe = regexp.MustCompile(`^[0-9a-f]{16}$`)

// gitlabHostRe: a GitLab host name (no port: pushes and glab go over https on 443).
var gitlabHostRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?)+$`)

// gitlabOriginRe: an origin URL over https or ssh, HOST and PROJECT/PATH.
var gitlabOriginRe = regexp.MustCompile(`^(?:https://(?:[A-Za-z0-9._-]+@)?([A-Za-z0-9.-]+)/|git@([A-Za-z0-9.-]+):|ssh://git@([A-Za-z0-9.-]+)/)` +
	`([A-Za-z0-9_][A-Za-z0-9._/-]{0,1000}?)(?:\.git)?/?$`)

// originPin is the forge pin of an origin URL: github.com/OWNER/NAME, gitlab:HOST/PATH when its
// host is one of gitlab, or "".
func originPin(url string, gitlab map[string]bool) string {
	if gh := originGitHub(url); gh != "" {
		return "github.com/" + gh
	}
	m := gitlabOriginRe.FindStringSubmatch(url)
	if m == nil {
		return ""
	}
	host := strings.ToLower(m[1] + m[2] + m[3])
	pin := "gitlab:" + host + "/" + m[4]
	if !gitlab[host] || !grants.ValidPin(pin) {
		return ""
	}
	return pin
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
