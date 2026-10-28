// Package launcher is lea's host launcher in Go. It prepares a projects directory's container
// (networks, proxy, mounts, bus), the daemon and the deck. It was ported from the shell launcher
// (bin/lea and lib/*.sh, now retired): comments name the shell function each part came from
// (lea_*), every path, format and docker argument is the shell's, and the golden files under
// testdata/golden, recorded from the last parity run against the shell, lock that behaviour. The
// shell still running on the host (libexec/lea-sh: init, update, doctor, kube refresh, the scans)
// shares the same host files.
package launcher

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/DavidGodefroid/lea/internal/providers"
)

// protectedKeys: a non-empty value in the environment beats both configuration files
// (lea_load_config saves them before reading the files).
var protectedKeys = map[string]bool{
	"LEA_PROJECTS_DIR": true, "LEA_APPROVALS": true,
	"LEA_APPROVAL_TIMEOUT": true,
}

// ExportedKeys are the keys lea_load_config exports to every child: the daemon, docker, the
// core subcommands and libexec/lea-sh.
var ExportedKeys = []string{
	"LEA_PREFIX", "LEA_IMAGE", "LEA_PROXY_IMAGE", "LEA_GIT_SSH", "LEA_GIT_KNOWN_HOSTS", "LEA_LOCKSQL", "LEA_LOCKSQL_IMAGE", "LEA_LOCKSQL_REF", "LEA_LOCKSQL_VAULT_ADDR",
	"LEA_LOCKMQ", "LEA_LOCKFILE", "LEA_LOCKMQ_REF", "LEA_LOCKFILE_REF", "LEA_LOCKHTTP", "LEA_LOCKHTTP_MITM",
	"LEA_OTEL_UPSTREAM", "LEA_NESTED_NETWORKS", "LEA_K8S_NAMESPACE", "LEA_INTERNAL_HOSTS", "LEA_EGRESS_ALLOW", "LEA_PROJECTS_DIR",
	"LEA_APPROVALS", "LEA_APPROVAL_TIMEOUT", "LEA_SOUND", "LEA_SOUND_ATTENTION", "LEA_SOUND_DONE",
	"LEA_PROVIDER", "LEA_MODEL", "LEA_PROVIDERS", "LEA_AUTO_RESUME", "LEA_RESUME_DELAY",
	"LEA_STACK_IDLE",
}

// Config is what lea_load_config gives the shell: the environment, overlaid by the assignments of
// config/lea.conf and of the user configuration, then resolved and validated.
type Config struct {
	getenv func(string) string
	file   map[string]string // assignments of both files, the later one winning
	from   map[string]string // the file each assignment of file comes from
	final  map[string]string // resolved keys
	extra  []string          // keys exported by the launcher at run time (LEA_TMUX_BIN)
}

// Get is the value the shell would see for key after lea_load_config: a resolved key, else the
// files' assignment, else the environment.
func (c *Config) Get(key string) string {
	if v, ok := c.final[key]; ok {
		return v
	}
	if v, ok := c.file[key]; ok {
		return v
	}
	return c.getenv(key)
}

// Export sets key for this run and its children (the shell's `X=v; export X`).
func (c *Config) Export(key, value string) {
	c.final[key] = value
	for _, k := range c.extra {
		if k == key {
			return
		}
	}
	c.extra = append(c.extra, key)
}

// Environ is base as the children of a shell that ran lea_load_config see it: a variable of base
// that a configuration file assigned carries the file's value, and the exported keys carry theirs.
func (c *Config) Environ(base []string) []string {
	exported := map[string]bool{}
	for _, k := range append(append([]string{}, ExportedKeys...), c.extra...) {
		exported[k] = true
	}
	out := make([]string, 0, len(base)+len(exported)+1)
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case exported[k] || k == EnvKeysVar:
		case hasKey(c.file, k):
			out = append(out, k+"="+c.file[k])
		default:
			out = append(out, kv)
		}
	}
	for _, k := range append(append([]string{}, ExportedKeys...), c.extra...) {
		out = append(out, k+"="+c.Get(k))
	}
	return append(out, EnvKeysVar+"="+strings.Join(c.EnvKeys(), " "))
}

// EnvKeysVar names, in the launcher's children, the protected keys the environment set (a
// child cannot tell them apart: it sees every exported key in its environment).
const EnvKeysVar = "LEA_ENV_KEYS"

// EnvKeys are the protected keys whose non-empty value in the environment beats both files.
func (c *Config) EnvKeys() []string {
	var ks []string
	for _, k := range ExportedKeys {
		if protectedKeys[k] && c.getenv(k) != "" {
			ks = append(ks, k)
		}
	}
	return ks
}

// Protected reports whether a non-empty value of key in the environment beats both files.
func Protected(key string) bool { return protectedKeys[key] }

// Assignment is the value a configuration file assigns to key and that file's path (the later
// file winning); ok is false when neither assigns it.
func (c *Config) Assignment(key string) (value, file string, ok bool) {
	v, ok := c.file[key]
	return v, c.from[key], ok
}

func hasKey(m map[string]string, k string) bool { _, ok := m[k]; return ok }

// LoadConfig reads root/config/lea.conf, then the user configuration ($LEA_CONFIG, else
// $XDG_CONFIG_HOME/lea/lea.conf, else ~/.config/lea/lea.conf; a relative path is relative to
// cwd), and validates the result as lea_load_config does, with the same messages.
func LoadConfig(getenv func(string) string, root, cwd string) (*Config, error) {
	c := &Config{getenv: getenv, file: map[string]string{}, from: map[string]string{}, final: map[string]string{}}
	if err := c.readFile(filepath.Join(root, "config", "lea.conf")); err != nil {
		return nil, err
	}
	user := c.UserConfigFile(cwd)
	if c.Get("LEA_CONFIG") != "" || isRegular(user) {
		if err := c.readFile(user); err != nil {
			return nil, err
		}
	}
	if err := c.resolve(root); err != nil {
		return nil, err
	}
	return c, nil
}

// UserConfigPath is lea_user_config_path.
func (c *Config) UserConfigPath() string {
	if p := c.Get("LEA_CONFIG"); p != "" {
		return p
	}
	return nonEmpty(c.Get("XDG_CONFIG_HOME"), c.Get("HOME")+"/.config") + "/lea/lea.conf"
}

// UserConfigFile is the user configuration's path, a relative one taken from cwd (as LoadConfig
// reads it).
func (c *Config) UserConfigFile(cwd string) string {
	p := c.UserConfigPath()
	if !strings.HasPrefix(p, "/") {
		p = filepath.Join(cwd, p)
	}
	return p
}

// CheckGitSSH validates a LEA_GIT_SSH value as LoadConfig does.
func CheckGitSSH(v string) error {
	_, err := gitEndpoints(v)
	return err
}

func (c *Config) resolve(root string) error {
	pick := func(k, def string) string {
		if protectedKeys[k] {
			if v := c.getenv(k); v != "" {
				return v
			}
		}
		return nonEmpty(c.Get(k), def)
	}
	c.final["LEA_PROJECTS_DIR"] = pick("LEA_PROJECTS_DIR", "")
	appr, at := pick("LEA_APPROVALS", "deck"), pick("LEA_APPROVAL_TIMEOUT", "60")
	c.final["LEA_APPROVALS"], c.final["LEA_APPROVAL_TIMEOUT"] = appr, at
	if appr != "deck" && appr != "pane" {
		return fmt.Errorf("LEA_APPROVALS must be deck or pane, not '%s'", appr)
	}
	if n, ok := plainNumber(at, 3); !ok || n < 1 || n > 600 {
		return fmt.Errorf("LEA_APPROVAL_TIMEOUT must be a number of seconds from 1 to 600 without leading zeros, not '%s'", at)
	}
	c.final["LEA_INTERNAL_HOSTS"] = nonEmpty(c.Get("LEA_INTERNAL_HOSTS"), root+"/config/internal-hosts")
	ea, err := egressAllow(c.Get("LEA_EGRESS_ALLOW"))
	if err != nil {
		return err
	}
	c.final["LEA_EGRESS_ALLOW"] = ea
	// The git SSH endpoints (none by default: git over HTTPS needs none). LEA_GITLAB_SSH and
	// LEA_GITLAB_KNOWN_HOSTS are the keys of lea before any forge could be used: read when the
	// new ones are unset.
	c.final["LEA_GIT_SSH"] = strings.Join(strings.Fields(nonEmpty(c.Get("LEA_GIT_SSH"), c.Get("LEA_GITLAB_SSH"))), " ")
	if _, err := gitEndpoints(c.final["LEA_GIT_SSH"]); err != nil {
		return err
	}
	conf := nonEmpty(c.Get("XDG_CONFIG_HOME"), c.Get("HOME")+"/.config") + "/lea"
	kh := nonEmpty(c.Get("LEA_GIT_KNOWN_HOSTS"), c.Get("LEA_GITLAB_KNOWN_HOSTS"))
	if kh == "" {
		kh = conf + "/git_known_hosts"
		if _, err := os.Stat(kh); err != nil {
			if _, err := os.Stat(conf + "/gitlab_known_hosts"); err == nil {
				kh = conf + "/gitlab_known_hosts"
			}
		}
	}
	c.final["LEA_GIT_KNOWN_HOSTS"] = kh
	// The providers (internal/providers): the default one and its model, and the user's
	// providers file (its endpoints are checked when the proxy starts and at each spawn).
	c.final["LEA_PROVIDERS"] = nonEmpty(c.Get("LEA_PROVIDERS"), conf+"/providers")
	prov := nonEmpty(c.Get("LEA_PROVIDER"), providers.Default)
	if !providerName.MatchString(prov) {
		return fmt.Errorf("LEA_PROVIDER must be a provider name (claude, codex or a name of the providers file), not '%s'", prov)
	}
	c.final["LEA_PROVIDER"] = prov
	if err := providers.CheckModel(c.Get("LEA_MODEL")); err != nil {
		return fmt.Errorf("LEA_MODEL: %v", err)
	}
	c.final["LEA_MODEL"] = c.Get("LEA_MODEL")
	if err := CheckOtelUpstream(c.Get("LEA_OTEL_UPSTREAM")); err != nil {
		return err
	}
	ls := nonEmpty(c.Get("LEA_LOCKSQL"), "1")
	if ls != "0" && ls != "1" {
		return fmt.Errorf("LEA_LOCKSQL must be 1 (wire lea for a locksql console) or 0, not '%s'", ls)
	}
	c.final["LEA_LOCKSQL"] = ls
	for _, t := range lockHostTools {
		on := nonEmpty(c.Get(t.env), "0")
		if on != "0" && on != "1" {
			return fmt.Errorf("%s must be 1 (reach a %s console on this host) or 0, not '%s'", t.env, t.name, on)
		}
		c.final[t.env] = on
		// Empty: no pinned release (config/lea.conf pins one), and lea init installs nothing.
		ref := c.Get(t.env + "_REF")
		if ref != "" && !releaseVersion.MatchString(ref) {
			return fmt.Errorf("%s_REF must be a %s release (vX.Y.Z), not '%s'", t.env, t.name, ref)
		}
		if t.name == "lockfile" && ref != "" && !lockfileSafe(ref) {
			return fmt.Errorf("LEA_LOCKFILE_REF %s is refused: lockfile v0.1.0 lets a plugin escape its sandbox (v0.1.1 or later)", ref)
		}
	}
	lh := nonEmpty(c.Get("LEA_LOCKHTTP"), "0")
	if lh != "0" && lh != "1" {
		return fmt.Errorf("LEA_LOCKHTTP must be 1 (agents' web egress through a LockHTTP gate) or 0, not '%s'", lh)
	}
	if lh == "1" && c.Get("LEA_GIT_SSH") != "" {
		return fmt.Errorf("LEA_LOCKHTTP=1 refuses LEA_GIT_SSH: its SSH forwarders would bypass the LockHTTP gate (use git over HTTPS, or LEA_LOCKHTTP=0)")
	}
	if lh == "1" && c.Get("LEA_OTEL_UPSTREAM") != "" {
		return fmt.Errorf("LEA_LOCKHTTP=1 refuses LEA_OTEL_UPSTREAM: its TCP forwarder (port 4318) would bypass the LockHTTP gate (leave it empty, or LEA_LOCKHTTP=0)")
	}
	c.final["LEA_LOCKHTTP"] = lh
	mitm := nonEmpty(c.Get("LEA_LOCKHTTP_MITM"), "0")
	if mitm != "0" && mitm != "1" {
		return fmt.Errorf("LEA_LOCKHTTP_MITM must be 1 (the LockHTTP gate decrypts HTTPS with a local CA) or 0, not '%s'", mitm)
	}
	if mitm == "1" && lh != "1" {
		return fmt.Errorf("LEA_LOCKHTTP_MITM=1 needs LEA_LOCKHTTP=1")
	}
	c.final["LEA_LOCKHTTP_MITM"] = mitm
	if err := c.resolveLimits(); err != nil {
		return err
	}
	au := nonEmpty(c.Get("LEA_AUTOUPDATE"), "1")
	if au != "0" && au != "1" {
		return fmt.Errorf("LEA_AUTOUPDATE must be 1 (check for a new release every day) or 0, not '%s'", au)
	}
	c.final["LEA_AUTOUPDATE"] = au
	if sk := c.Get("LEA_AUTOUPDATE_SKIP"); sk != "" && !releaseVersion.MatchString(sk) {
		return fmt.Errorf("LEA_AUTOUPDATE_SKIP must be empty or a release version (vX.Y.Z, set by lea rollback), not '%s'", sk)
	}
	return nil
}

// Resource limits of the lea container: a share of Docker's memory (N%) or a docker size, a
// docker size for memory plus swap (or -1, swap without limit), a number of processes and threads.
var (
	memoryPercent = regexp.MustCompile(`^([1-9][0-9]?|100)%$`)
	cpuCount      = regexp.MustCompile(`^([1-9][0-9]{0,3}(\.[0-9]{1,2})?|0\.[0-9]{1,2})$`)
	dockerSize    = regexp.MustCompile(`^[1-9][0-9]{0,15}[bkmg]?$`)
)

// resolveLimits checks LEA_CPUS (default 75%, 0: no limit), LEA_MEMORY (default 50%, 0: no
// limit), LEA_MEMORY_SWAP (empty: as LEA_MEMORY, no swap) and LEA_PIDS_LIMIT (default 16384, 0: no
// limit).
func (c *Config) resolveLimits() error {
	cpus := nonEmpty(c.Get("LEA_CPUS"), "75%")
	if cpus != "0" && !memoryPercent.MatchString(cpus) && !cpuCount.MatchString(cpus) {
		return fmt.Errorf("LEA_CPUS must be a share of Docker's CPUs (1%%-100%%), a number of CPUs (8, 1.5) or 0 (no limit), not '%s'", cpus)
	}
	c.final["LEA_CPUS"] = cpus
	mem := nonEmpty(c.Get("LEA_MEMORY"), "50%")
	if mem != "0" && !memoryPercent.MatchString(mem) && !dockerSize.MatchString(mem) {
		return fmt.Errorf("LEA_MEMORY must be a share of Docker's memory (1%%-100%%), a size (8g, 512m) or 0 (no limit), not '%s'", mem)
	}
	swap := c.Get("LEA_MEMORY_SWAP")
	if swap != "" && swap != "-1" && !dockerSize.MatchString(swap) {
		return fmt.Errorf("LEA_MEMORY_SWAP must be empty (no swap), a size of memory plus swap (12g) or -1 (no limit), not '%s'", swap)
	}
	pids := nonEmpty(c.Get("LEA_PIDS_LIMIT"), "16384")
	if _, ok := plainNumber(pids, 7); !ok && pids != "0" {
		return fmt.Errorf("LEA_PIDS_LIMIT must be a number of processes without leading zeros or 0 (no limit), not '%s'", pids)
	}
	c.final["LEA_MEMORY"], c.final["LEA_MEMORY_SWAP"], c.final["LEA_PIDS_LIMIT"] = mem, swap, pids
	return nil
}

// lockProfile is a lock tool's profile name (agents.lockProfileRe).
var lockProfile = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// maxLockAgentProfiles caps an allow-list of profiles agents may ask for.
const maxLockAgentProfiles = 32

// lockAgentProfiles parses an allow-list of lock profiles agents may ask the user to open:
// profile names separated by spaces or commas; empty (the default) allows none.
func lockAgentProfiles(v string) ([]string, error) {
	ps := strings.FieldsFunc(v, func(r rune) bool { return r == ' ' || r == ',' || r == '\t' })
	if len(ps) > maxLockAgentProfiles {
		return nil, fmt.Errorf("at most %d profiles", maxLockAgentProfiles)
	}
	for _, p := range ps {
		if !lockProfile.MatchString(p) {
			return nil, fmt.Errorf("invalid profile name '%s' (lowercase letters, digits, _ and -, at most 32)", p)
		}
	}
	return ps, nil
}

// LocksqlAgentProfiles are the locksql profiles agents may ask the user to open
// (LEA_LOCKSQL_AGENT_PROFILES); none by default. Only the daemon reads it, when an agent asks: an
// invalid list refuses every request (and nothing else).
func (c *Config) LocksqlAgentProfiles() ([]string, error) {
	ps, err := lockAgentProfiles(c.Get("LEA_LOCKSQL_AGENT_PROFILES"))
	if err != nil {
		return nil, fmt.Errorf("LEA_LOCKSQL_AGENT_PROFILES: %v", err)
	}
	return ps, nil
}

var providerName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,15}$`)

// releaseVersion is a release's version, vMAJOR.MINOR.PATCH (libexec/lib/host.sh lea__semver).
var releaseVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// maxGitEndpoints caps LEA_GIT_SSH: one proxy forwarder (port 2222 + index) per endpoint.
const maxGitEndpoints = 16

// gitEndpoints splits LEA_GIT_SSH into host:port endpoints: a plain host name (it is written into
// the image's ssh configuration) and a port from 1 to 65535.
func gitEndpoints(v string) ([]string, error) {
	eps := strings.Fields(v)
	if len(eps) > maxGitEndpoints {
		return nil, fmt.Errorf("LEA_GIT_SSH lists %d endpoints, at most %d", len(eps), maxGitEndpoints)
	}
	seen := map[string]bool{}
	for _, ep := range eps {
		h, port, ok := strings.Cut(ep, ":")
		n, pok := plainNumber(port, 5)
		if !ok || h == "" || strings.Trim(h, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789.-") != "" ||
			!pok || n < 1 || n > 65535 {
			return nil, fmt.Errorf("LEA_GIT_SSH must list host:port endpoints with plain host names, got '%s'", ep)
		}
		if seen[h] {
			return nil, fmt.Errorf("LEA_GIT_SSH lists %s twice", h)
		}
		seen[h] = true
	}
	return eps, nil
}

// CheckOtelUpstream validates LEA_OTEL_UPSTREAM as lea__check_otel_upstream does: empty, or
// host:port with a plain host name and a port from 1 to 65535. The proxy hands it to socat as
// TCP:<value>, where a comma or a second colon would add address options.
func CheckOtelUpstream(v string) error {
	if v == "" {
		return nil
	}
	h, port, _ := strings.Cut(v, ":")
	n, ok := plainNumber(port, 5)
	if h == "" || strings.Trim(h, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789.-") != "" ||
		!ok || n > 65535 {
		return fmt.Errorf("LEA_OTEL_UPSTREAM must be host:port with a plain host name, not '%s'", v)
	}
	return nil
}

// maxEgressDomains caps LEA_EGRESS_ALLOW.
const maxEgressDomains = 64

// egressAllow is LEA_EGRESS_ALLOW normalized as lea__check_egress_allow does: domain names
// separated by commas or blanks, lower-cased, joined by commas. Each one also stands for its
// subdomains; an IP literal or a wildcard is refused. Empty is the proxy's default allowlist, and
// "default" in the list stands for it; "*" alone opens the web to any domain.
func egressAllow(v string) (string, error) {
	names := strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' })
	if slices.Contains(names, "*") {
		if len(names) > 1 {
			return "", fmt.Errorf("LEA_EGRESS_ALLOW: '*' opens the web to any domain and cannot be combined with domain names")
		}
		return "*", nil
	}
	if len(names) > maxEgressDomains {
		return "", fmt.Errorf("LEA_EGRESS_ALLOW lists %d domains, at most %d", len(names), maxEgressDomains)
	}
	for i, n := range names {
		if strings.Trim(n, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789.-") != "" || n[0] == '.' ||
			n[len(n)-1] == '.' || strings.Contains(n, "..") || strings.Trim(n, "0123456789.") == "" {
			return "", fmt.Errorf("LEA_EGRESS_ALLOW must list domain names separated by commas, got '%s'", n)
		}
		names[i] = strings.ToLower(n)
	}
	return strings.Join(names, ","), nil
}

// plainNumber accepts what the shell's case patterns accept: digits only, no leading zero, at
// most max digits.
func plainNumber(s string, max int) (int, bool) {
	if s == "" || len(s) > max || s[0] == '0' || strings.Trim(s, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

func (c *Config) readFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("cannot read the configuration %s: %v", path, err)
	}
	for i, line := range strings.Split(string(data), "\n") {
		k, v, ok, err := parseLine(line, c.Get("HOME"))
		if err != nil {
			return fmt.Errorf("%s line %d: %v (the Go launcher reads KEY=value lines and does not run them as shell): "+
				"rewrite the line as KEY='value'", path, i+1, err)
		}
		if ok {
			c.file[k], c.from[k] = v, path
		}
	}
	return nil
}

var keyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// parseLine reads one line: blank, a comment, or KEY=VALUE, optionally after "export ".
func parseLine(line, home string) (key, val string, ok bool, err error) {
	s := strings.TrimLeft(line, " \t")
	if s == "" || s[0] == '#' {
		return "", "", false, nil
	}
	s = strings.TrimPrefix(s, "export ")
	k, rest, found := strings.Cut(s, "=")
	if !found || !keyRe.MatchString(k) {
		return "", "", false, errors.New("not a KEY=value line")
	}
	v, err := parseValue(rest, home)
	if err != nil {
		return "", "", false, err
	}
	return k, v, true, nil
}

// bare are the characters a value may hold outside quotes, plus any non-ASCII byte.
const bare = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_./:@%+,=-"

// parseValue reads what sh would assign, for the subset lea accepts: bare characters, '...'
// (literal), "..." without backquotes, \ or any $ other than $HOME and ${HOME}, \c (the
// character c), $HOME and ${HOME} (expanded to home, also inside "..."), and a leading ~ or ~/
// (home), up to blanks and an optional comment. Everything else, in particular any other $,
// $(...) and backquotes, is refused, never guessed.
func parseValue(s, home string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return "", errors.New("a ' is not closed")
			}
			b.WriteString(s[i+1 : i+1+j])
			i += j + 2
		case c == '"':
			j := strings.IndexByte(s[i+1:], '"')
			if j < 0 {
				return "", errors.New(`a " is not closed`)
			}
			in := s[i+1 : i+1+j]
			if strings.ContainsAny(in, "`\\") {
				return "", errors.New(`backquotes and \ are not read inside "...": use '...'`)
			}
			x, err := expandHome(in, home)
			if err != nil {
				return "", err
			}
			b.WriteString(x)
			i += j + 2
		case c == '\\':
			if i+1 >= len(s) {
				return "", errors.New(`a line ending in \ is not read`)
			}
			b.WriteByte(s[i+1])
			i += 2
		case c == '$':
			n, err := homeVar(s[i:])
			if err != nil {
				return "", err
			}
			b.WriteString(home)
			i += n
		case c == '~' && i == 0:
			if len(s) > 1 && s[1] != '/' && s[1] != ' ' && s[1] != '\t' {
				return "", errors.New("only ~ and ~/... are read (~user is not)")
			}
			b.WriteString(home)
			i++
		case c == ' ' || c == '\t':
			rest := strings.TrimLeft(s[i:], " \t")
			if rest == "" || rest[0] == '#' {
				return b.String(), nil
			}
			return "", errors.New("one value per line (a blank outside quotes ends it)")
		case c >= 0x80 || strings.IndexByte(bare, c) >= 0:
			b.WriteByte(c)
			i++
		default:
			return "", fmt.Errorf("%q outside quotes is shell syntax lea does not read: quote the value with '...'", c)
		}
	}
	return b.String(), nil
}

// homeVar returns the length of the $HOME or ${HOME} that starts s; any other use of $ is an error.
func homeVar(s string) (int, error) {
	for _, v := range []string{"${HOME}", "$HOME"} {
		if strings.HasPrefix(s, v) {
			if v == "$HOME" && len(s) > len(v) && isNameByte(s[len(v)]) {
				break
			}
			return len(v), nil
		}
	}
	return 0, errors.New("only $HOME and ${HOME} are read: other variables, $(...) and arithmetic are shell syntax (use '...' for a literal $)")
}

func isNameByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// expandHome replaces $HOME and ${HOME} in the inside of a double-quoted string.
func expandHome(s, home string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '$' {
			b.WriteByte(s[i])
			i++
			continue
		}
		n, err := homeVar(s[i:])
		if err != nil {
			return "", err
		}
		b.WriteString(home)
		i += n
	}
	return b.String(), nil
}

// GitSSH is LEA_GIT_SSH: the git SSH endpoints, space separated ("" when git uses HTTPS only).
func (c *Config) GitSSH() string { return c.Get("LEA_GIT_SSH") }

// LockHTTP reports whether the agents' web egress goes through a LockHTTP gate (LEA_LOCKHTTP=1;
// off by default).
func (c *Config) LockHTTP() bool { return c.Get("LEA_LOCKHTTP") == "1" }

// LockHTTPMITM reports whether the LockHTTP gate decrypts HTTPS (LEA_LOCKHTTP_MITM=1, with
// LEA_LOCKHTTP=1; off by default).
func (c *Config) LockHTTPMITM() bool { return c.LockHTTP() && c.Get("LEA_LOCKHTTP_MITM") == "1" }

// Locksql reports whether lea is wired for a locksql console (LEA_LOCKSQL=1, the default).
func (c *Config) Locksql() bool { return c.Get("LEA_LOCKSQL") != "0" }

// LockHost reports whether lea's agents may reach the host console of the lock tool whose switch
// is env (LEA_LOCKMQ, LEA_LOCKFILE; off by default).
func (c *Config) LockHost(env string) bool { return c.Get(env) == "1" }

// gitStateDirs are the state sub-directories (relative to the state dir) where the forge tools
// inside lea keep their credentials: git's credential store, gh and glab.
var gitStateDirs = []string{"git", "gh", "glab-cli"}
