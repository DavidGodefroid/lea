package main

// The origins of the projects directory's repositories, read on the host as files (#92): what
// lea allow pins every forge capability to (#319).

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/DavidGodefroid/lea/internal/grants"
)

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
	b, ok := readPlain(cfg)
	if !ok {
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

// readPlain is the content of path, a regular file of at most 1 MiB that agents may write: a
// symlink, a FIFO or a device is refused, not followed or waited on (O_NOFOLLOW and O_NONBLOCK,
// even when swapped in after a Lstat).
func readPlain(path string) ([]byte, bool) { return readPlainMax(path, 1<<20) }

// readPlainMax is readPlain with a size cap of max bytes.
func readPlainMax(path string, max int64) ([]byte, bool) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return nil, false
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil || int64(len(b)) > max {
		return nil, false
	}
	return b, true
}

// forgeTokenIn says whether lea's GitHub token is in the proxy's key directory, next to the state
// dir state (DATA/proxy/forge/github.com).
func forgeTokenIn(state string) bool {
	b, ok := readPlain(filepath.Join(filepath.Dir(state), "proxy", "forge", "github.com"))
	return ok && strings.TrimSpace(string(b)) != ""
}

// yamlKey says whether the YAML file at path has the mapping key host (gh's hosts.yml keys its
// hosts at the top level, glab's config.yml under hosts:).
func yamlKey(path, host string) bool {
	b, ok := readPlain(path)
	if !ok {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == host+":" {
			return true
		}
	}
	return false
}

// storeHas says whether git's credential store at path has an entry for https://host.
func storeHas(path, host string) bool {
	b, ok := readPlain(path)
	if !ok {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "https://")
		at := strings.LastIndexByte(rest, '@')
		if !ok || at < 0 {
			continue
		}
		h, _, _ := strings.Cut(rest[at+1:], "/")
		if strings.EqualFold(h, host) {
			return true
		}
	}
	return false
}
