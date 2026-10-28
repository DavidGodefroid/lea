// Package lockhttp is lea's side of LockHTTP, the Lock Family proxy that decides each outbound
// HTTP request of the agents (contract: https://github.com/DavidGodefroid/lockhttp/blob/main/docs/lea.md,
// version 1). lea runs the gate in the proxy's network namespace and tells it who calls: sources.json maps
// the proxy's address on each internal network to a lea container, and the hash of each proxy
// token to the agent it was given to. Tokens are derived from a host key that never leaves the
// host's lea data directory: the launcher (container env) and the daemon (agents, sources.json)
// compute the same ones without sharing state.
//
// The network is the trust boundary: an agent token only attributes a request to an agent. The
// agents of one container share a uid and can read each other's environment (and so tokens), so
// one can pass for another of the same container, never for another container.
package lockhttp

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Version is the contract version lea writes and reads.
const Version = 1

// Port is the gate's port in the proxy's network namespace; squid stays on 127.0.0.1:3128 behind it.
const Port = "3130"

// Agent names that are not lea agents: the user's shells and anything else started in a lea
// container outside an agent ("shell"), and the nested Podman's containers ("nested").
const (
	ShellAgent  = "shell"
	NestedAgent = "nested"
)

// Dir is the host directory of lea's LockHTTP files under the lea data directory (the parent of
// the daemon's host directory): the token key, the gate's input files (gate/), its audit
// (audit/) and its status (status/).
func Dir(dataDir string) string { return filepath.Join(dataDir, "lockhttp") }

// GateDir holds sources.json and credentials.json, mounted read-only in the gate and proxy containers at
// /etc/lockhttp-gate. A directory, not the files: lea replaces them by rename, which a file bind
// mount would not see.
func GateDir(dataDir string) string { return filepath.Join(Dir(dataDir), "gate") }

// AuditDir is the gate's JSONL audit, mounted at /var/log/lockhttp.
func AuditDir(dataDir string) string { return filepath.Join(Dir(dataDir), "audit") }

// StatusDir is where the gate writes status.json, mounted at /run/lockhttp-gate.
func StatusDir(dataDir string) string { return filepath.Join(Dir(dataDir), "status") }

func keyPath(dataDir string) string { return filepath.Join(Dir(dataDir), "token.key") }

// Key is the host key tokens are derived from: 32 random bytes in token.key (0600), created on
// first use. A key file of another size is an error, never silently replaced: every running
// agent holds tokens derived from it.
func Key(dataDir string) ([]byte, error) {
	p := keyPath(dataDir)
	b, err := os.ReadFile(p)
	if err == nil {
		if len(b) != 32 {
			return nil, fmt.Errorf("%s: %d bytes, want 32 (remove it to make a new key, then restart lea's containers)", p, len(b))
		}
		return b, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(Dir(dataDir), 0o700); err != nil {
		return nil, err
	}
	// MkdirAll leaves a directory made earlier as it was.
	if err := os.Chmod(Dir(dataDir), 0o700); err != nil {
		return nil, err
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return Key(dataDir) // another process made it first
	}
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(k); err != nil {
		f.Close()
		os.Remove(p)
		return nil, err
	}
	return k, f.Close()
}

// Token is the proxy password of agent in container: 32 hex digits of HMAC-SHA256(key).
func Token(key []byte, container, agent string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("agent\x00" + container + "\x00" + agent))
	return hex.EncodeToString(m.Sum(nil))[:32]
}

// KeyproxyToken is the password lea-keyproxy presents to the gate.
func KeyproxyToken(key []byte) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("keyproxy"))
	return hex.EncodeToString(m.Sum(nil))[:32]
}

// Hash is a token as sources.json holds it: the hex SHA-256.
func Hash(token string) string {
	s := sha256.Sum256([]byte(token))
	return hex.EncodeToString(s[:])
}

// Sources is sources.json (contract §2.3).
type Sources struct {
	Version  int            `json:"version"`
	Networks []Network      `json:"networks"`
	Keyproxy KeyproxySource `json:"keyproxy"`
	Agents   []AgentSource  `json:"agents"`
}

// Network says that a connection arriving on the proxy's address LocalIP comes from Container.
type Network struct {
	LocalIP   string `json:"local_ip"`
	Container string `json:"container"`
	Project   string `json:"project,omitempty"`
}

type KeyproxySource struct {
	TokenSHA256 string `json:"token_sha256"`
}

// AgentSource attributes a token to an agent (audit only: the network decides the container).
type AgentSource struct {
	TokenSHA256 string `json:"token_sha256"`
	Container   string `json:"container"`
	Agent       string `json:"agent"`
	Role        string `json:"role,omitempty"`
	Parent      string `json:"parent,omitempty"`
	TaskID      string `json:"task_id,omitempty"`
	Branch      string `json:"branch,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
}

// Build is the sources.json of the containers on networks, with the shell and nested tokens of
// each and the tokens of agents (those of a container no network names are left out: the gate
// would refuse their connections anyway). The result is sorted: an unchanged world gives the
// same bytes.
func Build(key []byte, networks []Network, agents []AgentSource) Sources {
	s := Sources{Version: Version, Networks: append([]Network{}, networks...), Agents: []AgentSource{},
		Keyproxy: KeyproxySource{TokenSHA256: Hash(KeyproxyToken(key))}}
	sort.Slice(s.Networks, func(i, j int) bool { return s.Networks[i].LocalIP < s.Networks[j].LocalIP })
	known := map[string]bool{}
	for _, n := range s.Networks {
		if known[n.Container] {
			continue
		}
		known[n.Container] = true
		for _, a := range []string{ShellAgent, NestedAgent} {
			s.Agents = append(s.Agents, AgentSource{TokenSHA256: Hash(Token(key, n.Container, a)), Container: n.Container, Agent: a})
		}
	}
	for _, a := range agents {
		if !known[a.Container] || a.Agent == ShellAgent || a.Agent == NestedAgent {
			continue
		}
		a.TokenSHA256 = Hash(Token(key, a.Container, a.Agent))
		s.Agents = append(s.Agents, a)
	}
	sort.SliceStable(s.Agents, func(i, j int) bool {
		if s.Agents[i].Container != s.Agents[j].Container {
			return s.Agents[i].Container < s.Agents[j].Container
		}
		return s.Agents[i].Agent < s.Agents[j].Agent
	})
	return s
}

// WriteJSON writes v to path atomically (a temporary file in the same directory, then rename),
// 0644: the gate reads it as its own uid. It reports whether the file changed; an identical file
// is left alone, so the gate does not reload for nothing.
func WriteJSON(path string, v any) (bool, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return false, err
	}
	b = append(b, '\n')
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, b) {
		return false, nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return false, err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(b); err != nil {
		f.Close()
		return false, err
	}
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return false, err
	}
	if err := f.Close(); err != nil {
		return false, err
	}
	return true, os.Rename(tmp, path)
}

// Sourcer names the container of a local address from a sources.json it reads again whenever the
// file changes (lea-keyproxy, in the proxy container). An unreadable or invalid file names none:
// fail-closed, like the gate.
type Sourcer struct {
	path string
	mu   sync.Mutex
	mod  time.Time
	size int64
	ips  map[string]string
}

func NewSourcer(path string) *Sourcer { return &Sourcer{path: path} }

// Source is the container whose network the proxy's address ip is on.
func (s *Sourcer) Source(ip net.IP) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fi, err := os.Stat(s.path)
	if err != nil {
		s.ips, s.mod = nil, time.Time{}
		return "", false
	}
	if s.ips == nil || !fi.ModTime().Equal(s.mod) || fi.Size() != s.size {
		s.ips, s.mod, s.size = nil, fi.ModTime(), fi.Size()
		var src Sources
		if b, err := os.ReadFile(s.path); err == nil && json.Unmarshal(b, &src) == nil && src.Version == Version {
			s.ips = map[string]string{}
			for _, n := range src.Networks {
				if p := net.ParseIP(n.LocalIP); p != nil {
					s.ips[p.String()] = n.Container
				}
			}
		}
	}
	c, ok := s.ips[ip.String()]
	return c, ok
}

// DropNetworks removes from the sources.json at path the networks drop names, at once: the
// launcher calls it when the proxy leaves a container's network and when it joins a new one, so
// that an address a removed network leaves behind never names the next container that gets it
// (the daemon rebuilds the whole file from Docker within lockHTTPEvery anyway). A missing or
// unreadable file is left alone: the gate refuses every source without it.
func DropNetworks(path string, drop func(Network) bool) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var s Sources
	if json.Unmarshal(b, &s) != nil || s.Version != Version {
		return nil
	}
	kept := s.Networks[:0]
	for _, n := range s.Networks {
		if !drop(n) {
			kept = append(kept, n)
		}
	}
	if len(kept) == len(s.Networks) {
		return nil
	}
	s.Networks = kept
	_, err = WriteJSON(path, s)
	return err
}

// Credentials is credentials.json (contract §2.4): the hashes of the secrets each container (and
// lea-keyproxy) may send to the model APIs.
type Credentials struct {
	Version    int                 `json:"version"`
	Containers map[string][]string `json:"containers"`
	Keyproxy   []string            `json:"keyproxy"`
}

// CredentialsPath is the credentials.json lea writes for the gate.
func CredentialsPath(dataDir string) string {
	return filepath.Join(GateDir(dataDir), "credentials.json")
}

// SourcesPath is the sources.json lea writes for the gate.
func SourcesPath(dataDir string) string { return filepath.Join(GateDir(dataDir), "sources.json") }
