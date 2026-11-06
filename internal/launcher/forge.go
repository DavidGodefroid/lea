package launcher

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/DavidGodefroid/lea/internal/keyproxy"
	"github.com/DavidGodefroid/lea/internal/providers"
)

// The GitHub token of a lea (issue #103) lives in the proxy container only: lea's data dir
// proxy/forge/github.com (0600, the user's), mounted read-only on /etc/lea/forge in the proxy,
// which no agent container mounts. lea-keyproxy's forge forwarder (port 4401) adds it to the
// agents' gh and git requests; the agents' gh config and git store hold no token.

// forgeDir is the forge forwarder's key directory on the host.
func (l *Launcher) forgeDir() string { return l.Cfg.DataDir() + "/proxy/forge" }

// forgeToken is the GitHub token's file on the host.
func (l *Launcher) forgeToken() string { return l.forgeDir() + "/" + keyproxy.GitHubTokenFile }

// forgeMount is the proxy's mount of the forge key directory, created (0700) when missing. The
// directory, not the file: a token replaced by lea github login is seen at the next request.
func (l *Launcher) forgeMount() ([]string, error) {
	d := l.forgeDir()
	if err := noComma(d); err != nil {
		return nil, err
	}
	if err := Mkdirs(l.Cfg.DataDir(), d); err != nil {
		return nil, die("%v", err)
	}
	if err := os.Chmod(d, 0o700); err != nil {
		return nil, die("%v", err)
	}
	return []string{"--mount", "type=bind,src=" + d + ",dst=/etc/lea/forge,readonly"}, nil
}

// setGitHubToken writes tok as lea's GitHub token (0600).
func (l *Launcher) setGitHubToken(tok string) error {
	if _, err := l.forgeMount(); err != nil {
		return err
	}
	if err := WriteFile(l.Cfg.DataDir(), l.forgeToken(), []byte(tok+"\n")); err != nil {
		return die("%v", err)
	}
	return nil
}

// validToken says whether tok can be a token: what lea-keyproxy reads (providers.ReadKey).
func validToken(tok string) bool {
	if tok == "" || len(tok) > 4096 {
		return false
	}
	for _, c := range tok {
		if c <= ' ' || c == 0x7f || c > '~' {
			return false
		}
	}
	return true
}

// migrateGitHubToken moves a GitHub token an agent could read out of lea's state into the proxy:
// gh's (STATE/gh/hosts.yml: every oauth_token of its github.com entry; a gh auth login in lea, or
// an earlier lea) and git's store (STATE/git/credentials: its https://…@github.com lines). The
// newest wins: gh's, then the store's, replace the proxy's. What it removes is gone from the agents'
// state, and the token is never printed. It says what it moved, "" for nothing.
func (l *Launcher) migrateGitHubToken() (string, error) {
	st := l.Cfg.StateDir()
	var moved []string
	tok := ""
	hosts := st + "/gh/hosts.yml"
	if b, err := os.ReadFile(hosts); err == nil {
		out, t, n := stripGHToken(string(b))
		if n > 0 {
			if err := WriteFile(l.Cfg.DataDir(), hosts, []byte(out)); err != nil {
				return "", die("%v", err)
			}
			moved = append(moved, "gh's config")
			tok = t
			if err := l.ghConfigVersioned(st + "/gh/config.yml"); err != nil {
				return "", err
			}
		}
	}
	store := st + "/git/credentials"
	if b, err := os.ReadFile(store); err == nil {
		out, t, n := stripStoreToken(string(b))
		if n > 0 {
			if err := WriteFile(l.Cfg.DataDir(), store, []byte(out)); err != nil {
				return "", die("%v", err)
			}
			moved = append(moved, "git's credential store")
			if tok == "" {
				tok = t
			}
		}
	}
	if len(moved) == 0 {
		return "", nil
	}
	if validToken(tok) {
		if err := l.setGitHubToken(tok); err != nil {
			return "", err
		}
	}
	return strings.Join(moved, " and "), nil
}

// ghConfigVersioned gives gh's config.yml at path a version when it has none: without one, gh
// migrates hosts.yml to its multi-account form, and refuses to when a host has no token.
func (l *Launcher) ghConfigVersioned(path string) error {
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return die("%v", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "version:") {
			return nil
		}
	}
	if len(b) > 0 && b[len(b)-1] != '\n' {
		b = append(b, '\n')
	}
	if err := WriteFile(l.Cfg.DataDir(), path, append(b, "version: \"1\"\n"...)); err != nil {
		return die("%v", err)
	}
	return nil
}

// stripGHToken removes the oauth_token lines of the github.com entry of gh's hosts.yml (the
// entry's own and its users'), and returns the file without them, the entry's token (its own
// first, else a user's) and how many lines it removed.
func stripGHToken(s string) (string, string, int) {
	var out []string
	in, tok, userTok, n := false, "", "", 0
	for _, line := range strings.SplitAfter(s, "\n") {
		trim := strings.TrimSpace(line)
		if line != "" && line[0] != ' ' && line[0] != '\t' && trim != "" && !strings.HasPrefix(trim, "#") {
			in = strings.TrimSuffix(strings.Trim(strings.TrimSuffix(trim, ":"), `"'`), ".") == "github.com" && strings.HasSuffix(trim, ":")
		}
		if in {
			if k, v, ok := strings.Cut(trim, ":"); ok && strings.Trim(k, `"'`) == "oauth_token" {
				v = strings.Trim(strings.TrimSpace(v), `"'`)
				// The entry's own key is the least indented one.
				if indent(line) <= 4 && tok == "" {
					tok = v
				} else if userTok == "" {
					userTok = v
				}
				n++
				continue
			}
		}
		out = append(out, line)
	}
	if tok == "" {
		tok = userTok
	}
	return strings.Join(out, ""), tok, n
}

func indent(line string) int { return len(line) - len(strings.TrimLeft(line, " \t")) }

// stripStoreToken removes git's store lines for https://…@github.com, and returns the store
// without them, the first one's password and how many it removed.
func stripStoreToken(s string) (string, string, int) {
	var out []string
	tok, n := "", 0
	for _, line := range strings.SplitAfter(s, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "https://")
		at := strings.LastIndexByte(rest, '@')
		if ok && at >= 0 {
			h, _, _ := strings.Cut(rest[at+1:], "/")
			if strings.EqualFold(strings.TrimSuffix(h, ":443"), "github.com") {
				if _, p, ok := strings.Cut(rest[:at], ":"); ok && tok == "" {
					tok = unescapeStore(p)
				}
				n++
				continue
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, ""), tok, n
}

// unescapeStore is a store password, percent-decoded.
func unescapeStore(p string) string {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		if p[i] == '%' && i+2 < len(p) && isHexByte(p[i+1]) && isHexByte(p[i+2]) {
			b.WriteByte(hexVal(p[i+1])<<4 | hexVal(p[i+2]))
			i += 2
			continue
		}
		b.WriteByte(p[i])
	}
	return b.String()
}

func isHexByte(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

func hexVal(c byte) byte {
	switch {
	case c <= '9':
		return c - '0'
	case c <= 'F':
		return c - 'A' + 10
	}
	return c - 'a' + 10
}

// github is lea github login|logout|status: lea's GitHub token, which only the proxy holds.
//
//	gh auth token | lea github login   (or: lea github login < token-file)
func (l *Launcher) github(args []string) error {
	if len(args) != 1 {
		l.logf("error: usage: lea github login|logout|status (login reads the token on its standard input)")
		return exitError{2}
	}
	switch args[0] {
	case "login":
		if f, ok := l.Stdin.(*os.File); ok && isTerminal(f) {
			l.logf("paste a GitHub token, then Enter (it is not shown again; gh auth token | lea github login also works)")
		}
		line, err := bufio.NewReader(io.LimitReader(l.Stdin, 8192)).ReadString('\n')
		if err != nil && err != io.EOF {
			return die("could not read the token")
		}
		tok := strings.TrimSpace(line)
		if !validToken(tok) {
			l.logf("error: no token on the standard input (one line: printable ASCII, no space)")
			return exitError{1}
		}
		if err := l.setGitHubToken(tok); err != nil {
			return err
		}
		if _, err := l.migrateGitHubToken(); err != nil {
			return err
		}
		// The token just given wins over one the migration found.
		if err := l.setGitHubToken(tok); err != nil {
			return err
		}
		l.logf("GitHub token saved for the proxy (%s): agents use it through gh and git and never see it; running leas use it at their next request", l.forgeToken())
		return nil
	case "logout":
		if err := os.Remove(l.forgeToken()); err != nil && !os.IsNotExist(err) {
			return die("%v", err)
		}
		l.logf("GitHub token removed: agents reach GitHub anonymously")
		return nil
	case "status":
		if _, err := providers.ReadKey(l.forgeToken()); err == nil {
			fmt.Fprintf(l.Stdout, "github: a token is in the proxy (%s), never in an agent's container\n", l.forgeToken())
		} else {
			fmt.Fprintln(l.Stdout, "github: no token (lea github login): agents reach GitHub anonymously")
		}
		return nil
	}
	l.logf("error: usage: lea github login|logout|status")
	return exitError{2}
}

// isTerminal says whether f is a terminal.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
