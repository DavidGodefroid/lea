// Package providers is what lea knows of the coding agents it runs: which CLI an agent is
// (Claude Code or Codex), the model it asks for, and, for a provider of the user's providers file,
// the OpenAI-compatible endpoint it reaches through the proxy's key forwarder (lea-keyproxy).
//
// Security model (issue #21): an endpoint's API key stays on the host side. The providers file
// names a key file, never a key; that file is mounted into the proxy container only, which adds
// the key to the agent's requests. An agent sees http://lea-proxy:4400/<name>, never the key, the
// key file or the upstream's credentials. The forwarder reaches the upstream through the proxy's
// squid, so the egress rules are the same as for any request of the agents: a private or local
// upstream is refused unless the user listed it in the internal-hosts file.
package providers

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
)

const (
	// Claude and Codex are the CLIs lea runs; they are also the built-in providers' names.
	Claude = "claude"
	Codex  = "codex"
	// Default is the provider of an agent started without one and without LEA_PROVIDER.
	Default = Claude
	// ForwarderPort is the port of lea-keyproxy in the proxy container (lea-proxy:4400/<name>).
	ForwarderPort = 4400
	// ForwarderHost is the proxy's name on every internal network.
	ForwarderHost = "lea-proxy"
	// MaxEndpoints caps the providers file.
	MaxEndpoints = 16
	// MaxKeyBytes caps a key file.
	MaxKeyBytes = 4 << 10
	// MaxNameLen and MaxModelLen cap what an agent may name.
	MaxNameLen  = 16
	MaxModelLen = 100
	// KeyDir is where the proxy container sees the key files (one per endpoint, by name).
	KeyDir = "/etc/lea/keys"
)

var (
	nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,15}$`)
	// modelRe is a model id as the CLIs take it: gpt-5.1-codex, claude-opus-5-5, qwen3:32b,
	// meta-llama/Llama-3.3-70B-Instruct, opus[1m]. Never a flag (no leading '-'), no space, quote
	// or shell or TOML syntax.
	modelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+\[\]-]{0,99}$`)
	hostRe  = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)
	pathRe  = regexp.MustCompile(`^(/[A-Za-z0-9._~-]+)*/?$`)
)

// reserved are names no endpoint takes: the built-in providers, and kimi (to come, #62). Codex's
// own provider ids (openai, ollama, lmstudio) are free: lea's id of an endpoint is lea-<name>.
var reserved = map[string]bool{Claude: true, Codex: true, "kimi": true}

// Endpoint is an OpenAI-compatible server (Responses API) of the providers file.
type Endpoint struct {
	// URL is the upstream's base URL (http or https, no user info, query or fragment), for
	// example http://10.0.0.5:11434/v1: the forwarder appends the agent's request path.
	URL *url.URL
	// KeyFile is the host path of the API key file, "" for an endpoint without a key.
	KeyFile string
}

// Provider is one provider an agent can be started on.
type Provider struct {
	Name string
	// CLI is the program the agent runs: Claude or Codex.
	CLI string
	// Model is the provider's default model, "" for the CLI's own default.
	Model string
	// Endpoint is set for a provider of the providers file (run through Codex).
	Endpoint *Endpoint
}

// Registry is the built-in providers and the endpoints of the providers file.
type Registry struct {
	list []Provider
}

// Builtin is the registry without a providers file.
func Builtin() *Registry {
	return &Registry{list: []Provider{{Name: Claude, CLI: Claude}, {Name: Codex, CLI: Codex}}}
}

// Names lists the providers, built-in ones first.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.list))
	for _, p := range r.list {
		out = append(out, p.Name)
	}
	return out
}

// Endpoints are the providers of the providers file, in its order.
func (r *Registry) Endpoints() []Provider {
	var out []Provider
	for _, p := range r.list {
		if p.Endpoint != nil {
			out = append(out, p)
		}
	}
	return out
}

// Get is the provider called name. The error never echoes an overlong or unsafe name.
func (r *Registry) Get(name string) (Provider, error) {
	for _, p := range r.list {
		if p.Name == name {
			return p, nil
		}
	}
	if !nameRe.MatchString(name) {
		return Provider{}, errors.New("unknown provider: a provider name is lowercase letters, digits and '-', at most 16")
	}
	return Provider{}, fmt.Errorf("unknown provider %q: the providers are %s", name, strings.Join(r.Names(), ", "))
}

// CheckModel accepts "" (the provider's default) or a model id.
func CheckModel(m string) error {
	if m == "" || modelRe.MatchString(m) {
		return nil
	}
	if len(m) > MaxModelLen {
		return fmt.Errorf("model is %d bytes, at most %d", len(m), MaxModelLen)
	}
	return errors.New("invalid model: letters, digits and . _ : / @ + [ ] -, starting with a letter or digit")
}

// Hosts is what the internal-hosts file grants: the private hosts the proxy may reach, with their ports.
type Hosts map[string]map[int]bool

// Allows reports whether host:port is granted.
func (h Hosts) Allows(host string, port int) bool { return h[strings.ToLower(host)][port] }

// ParseHosts reads an internal-hosts file (<host|IPv4> <ports,...> per line, # comments), as
// proxy/render-squid-conf.sh does. Malformed lines are the proxy's to refuse: they are skipped.
func ParseHosts(r io.Reader) Hosts {
	h := Hosts{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line, _, _ := strings.Cut(sc.Text(), "#")
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		ports := map[int]bool{}
		for _, p := range strings.Split(f[1], ",") {
			if n, err := strconv.Atoi(p); err == nil && n > 0 && n < 65536 {
				ports[n] = true
			}
		}
		k := strings.ToLower(f[0])
		if h[k] == nil {
			h[k] = map[int]bool{}
		}
		for p := range ports {
			h[k][p] = true
		}
	}
	return h
}

// Options are the host-side facts the providers file is checked against.
type Options struct {
	// Internal is the internal-hosts file's grants: a private upstream must be listed there.
	Internal Hosts
	// UID owns the key files (os.Getuid() on the host; -1 skips the owner check, for tests).
	UID int
	// Forbidden are directories no key file may sit in: the projects directory, which every
	// agent reads and writes.
	Forbidden []string
}

// Load reads the providers file at path; a missing file is no endpoint.
func Load(path string, o Options) (*Registry, error) {
	r := Builtin()
	if path == "" {
		return r, nil
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, fmt.Errorf("providers file: %w", err)
	}
	defer f.Close()
	eps, err := Parse(f, o)
	if err != nil {
		return nil, fmt.Errorf("providers file %s: %w", path, err)
	}
	r.list = append(r.list, eps...)
	return r, nil
}

// Parse reads the lines of a providers file:
//
//	<name> <base-url> [key=<key file>] [model=<default model>]
//
// '#' starts a comment. Each endpoint is checked (CheckEndpoint) and so is its key file (CheckKeyFile).
func Parse(rd io.Reader, o Options) ([]Provider, error) {
	var out []Provider
	seen := map[string]bool{}
	sc := bufio.NewScanner(rd)
	n := 0
	for sc.Scan() {
		n++
		line, _, _ := strings.Cut(sc.Text(), "#")
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if len(f) < 2 || len(f) > 4 {
			return nil, fmt.Errorf("line %d: expected '<name> <base-url> [key=<file>] [model=<model>]'", n)
		}
		p := Provider{Name: f[0], CLI: Codex}
		if !nameRe.MatchString(p.Name) {
			return nil, fmt.Errorf("line %d: invalid name: lowercase letters, digits and '-', starting with a letter, at most 16", n)
		}
		if reserved[p.Name] {
			return nil, fmt.Errorf("line %d: %s is a reserved provider name", n, p.Name)
		}
		if seen[p.Name] {
			return nil, fmt.Errorf("line %d: %s is listed twice", n, p.Name)
		}
		seen[p.Name] = true
		u, err := url.Parse(f[1])
		if err != nil {
			return nil, fmt.Errorf("line %d: %s: invalid base URL", n, p.Name)
		}
		ep := &Endpoint{URL: u}
		for _, kv := range f[2:] {
			k, v, ok := strings.Cut(kv, "=")
			switch {
			case ok && k == "key" && ep.KeyFile == "":
				ep.KeyFile = v
			case ok && k == "model" && p.Model == "":
				if v == "" || CheckModel(v) != nil {
					return nil, fmt.Errorf("line %d: %s: invalid model", n, p.Name)
				}
				p.Model = v
			default:
				return nil, fmt.Errorf("line %d: %s: unknown or repeated field (key=<file> or model=<model>)", n, p.Name)
			}
		}
		if err := CheckEndpoint(ep, o.Internal); err != nil {
			return nil, fmt.Errorf("line %d: %s: %w", n, p.Name, err)
		}
		if ep.KeyFile != "" {
			if err := CheckKeyFile(ep.KeyFile, o.UID, o.Forbidden); err != nil {
				return nil, fmt.Errorf("line %d: %s: %w", n, p.Name, err)
			}
		}
		p.Endpoint = ep
		out = append(out, p)
		if len(out) > MaxEndpoints {
			return nil, fmt.Errorf("at most %d providers", MaxEndpoints)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Port is the upstream's port, the scheme's default when the URL has none.
func (e *Endpoint) Port() int {
	if p := e.URL.Port(); p != "" {
		n, _ := strconv.Atoi(p)
		return n
	}
	if e.URL.Scheme == "https" {
		return 443
	}
	return 80
}

// CheckEndpoint applies the egress rules to an upstream: http or https; a plain host name or an
// IPv4 literal (no wildcard, no user info, query or fragment); a private, loopback or link-local
// address (or localhost) only when internal-hosts grants it with that port; and a key travels
// over https only, unless internal-hosts grants the host (a server of the user's own network).
func CheckEndpoint(e *Endpoint, internal Hosts) error {
	u := e.URL
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("the base URL must be http:// or https://")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return errors.New("the base URL takes no user info, query or fragment")
	}
	host := u.Hostname()
	if host == "" || strings.Contains(u.Host, "[") {
		return errors.New("the base URL needs a host name or an IPv4 address")
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != p {
			return errors.New("invalid port in the base URL")
		}
	}
	if !pathRe.MatchString(u.EscapedPath()) || strings.Contains(u.Path, "/./") || strings.Contains(u.Path, "/../") ||
		strings.HasSuffix(u.Path, "/..") || strings.HasSuffix(u.Path, "/.") {
		return errors.New("the base URL's path must be plain segments (letters, digits, . _ ~ -)")
	}
	ip := net.ParseIP(host)
	switch {
	case ip != nil && ip.To4() == nil:
		return errors.New("the base URL needs a host name or an IPv4 address")
	case ip == nil && !hostRe.MatchString(host):
		return errors.New("the base URL's host must be a plain host name (no wildcard)")
	}
	granted := internal.Allows(host, e.Port())
	if (ip != nil && Private(ip)) || isLocalName(host) {
		if !granted {
			return fmt.Errorf("%s is a private or local address: list '%s %d' in the internal-hosts file to allow it", host, host, e.Port())
		}
	}
	if e.KeyFile != "" && u.Scheme != "https" && !granted {
		return errors.New("an endpoint with a key must use https (or be a host of the internal-hosts file)")
	}
	return nil
}

func isLocalName(h string) bool {
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	return h == "localhost" || strings.HasSuffix(h, ".localhost") || h == "lea-proxy"
}

// private are the ranges the proxy refuses (proxy/render-squid-conf.sh private_dst).
var private = func() []*net.IPNet {
	var out []*net.IPNet
	for _, c := range []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
		"172.16.0.0/12", "192.168.0.0/16", "224.0.0.0/4", "240.0.0.0/4"} {
		_, n, _ := net.ParseCIDR(c)
		out = append(out, n)
	}
	return out
}()

// Private reports whether ip is in a range the proxy refuses by default.
func Private(ip net.IP) bool {
	for _, n := range private {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// CheckKeyFile checks a key file without ever returning its content: an absolute path, a regular
// file (no symbolic link), owned by uid, readable by its owner only (mode 0600 or 0400), at most
// MaxKeyBytes, outside every forbidden directory.
func CheckKeyFile(path string, uid int, forbidden []string) error {
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, ",\n") || cleanPath(path) != path {
		return errors.New("the key file must be a clean absolute path without a comma")
	}
	for _, d := range forbidden {
		if d != "" && (path == d || strings.HasPrefix(path, strings.TrimSuffix(d, "/")+"/")) {
			return fmt.Errorf("the key file must not be in %s: the agents can read it", d)
		}
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return errors.New("the key file cannot be read")
	}
	if !fi.Mode().IsRegular() {
		return errors.New("the key file must be a regular file, not a symbolic link")
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("the key file is readable by others (mode %04o): chmod 600 it", fi.Mode().Perm())
	}
	if fi.Size() == 0 || fi.Size() > MaxKeyBytes {
		return fmt.Errorf("the key file must hold one key of at most %d bytes", MaxKeyBytes)
	}
	if uid >= 0 && fileOwner(fi) != uid {
		return errors.New("the key file must belong to you")
	}
	return nil
}

func cleanPath(p string) string {
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	parts := strings.Split(p, "/")
	for _, s := range parts {
		if s == "." || s == ".." {
			return ""
		}
	}
	if len(p) > 1 {
		p = strings.TrimSuffix(p, "/")
	}
	return p
}

// ReadKey reads a key file's single key (surrounding blanks trimmed); the error never holds it.
func ReadKey(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("the key file cannot be read")
	}
	if len(b) > MaxKeyBytes {
		return "", errors.New("the key file is too large")
	}
	k := strings.TrimSpace(string(b))
	if k == "" {
		return "", errors.New("the key file is empty")
	}
	for _, c := range k {
		if c <= ' ' || c == 0x7f || c > '~' {
			return "", errors.New("the key file must hold one key: printable ASCII, no space")
		}
	}
	return k, nil
}

// ForwarderBase is the base URL an agent reaches an endpoint provider at.
func ForwarderBase(name string) string {
	return fmt.Sprintf("http://%s:%d/%s", ForwarderHost, ForwarderPort, name)
}

// WithEndpoints is r with the providers eps (Parse's) added after its own.
func WithEndpoints(r *Registry, eps []Provider) *Registry {
	return &Registry{list: append(append([]Provider{}, r.list...), eps...)}
}
