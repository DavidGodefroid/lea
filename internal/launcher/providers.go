package launcher

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/DavidGodefroid/lea/internal/providers"
)

// LoadProviders reads the providers file (LEA_PROVIDERS) against the internal-hosts file
// (LEA_INTERNAL_HOSTS): a private upstream must be listed there. No key file may sit in a host
// directory the agents' container mounts (the projects directory, lea's data, ~/.claude,
// ~/.codex), real paths compared, and each must be the user's own, readable by them only.
// on and several are Config.LockTools' (LockServers).
func LoadProviders(on map[string]bool, several map[string][]string, get func(string) string) (*providers.Registry, error) {
	o := providers.Options{Internal: providers.Hosts{}, UID: os.Getuid()}
	if f, err := os.Open(get("LEA_INTERNAL_HOSTS")); err == nil {
		o.Internal = providers.ParseHosts(f)
		f.Close()
	}
	// Every host directory the agents' container mounts: the projects directory, lea's data
	// (its state dirs: git, gh, go, ~/.codex ...) and ~/.claude (shared entries, the private one).
	home := get("HOME")
	data := nonEmpty(get("XDG_DATA_HOME"), home+"/.local/share") + "/lea"
	for _, d := range []string{get("LEA_PROJECTS_DIR"), get("LEA_PROJECTS"), data, home + "/.claude", home + "/.codex"} {
		if d != "" && d != "/.claude" && d != "/.codex" {
			o.Forbidden = append(o.Forbidden, d)
		}
	}
	r, err := providers.Load(get("LEA_PROVIDERS"), o)
	if err != nil {
		return nil, err
	}
	r.SetLocks(LockServers(on, several, get))
	return r, nil
}

// LockServers are the enabled Locks a Codex agent gets as MCP servers (on and several:
// Config.LockTools, in the order locksql, lockmq, lockfile), each with Codex's tool timeout
// LEA_LOCK_TOOL_TIMEOUT seconds (1500 when unset or invalid; 60 to 86400): a Lock call waits for
// a human. A tool lox up runs several locks of gets one server per instance its agents reach
// (none when they reach none); otherwise the tool's one server.
func LockServers(on map[string]bool, several map[string][]string, get func(string) string) []providers.Lock {
	t := 1500
	if n, err := strconv.Atoi(get("LEA_LOCK_TOOL_TIMEOUT")); err == nil && n >= 60 && n <= 86400 {
		t = n
	}
	var out []providers.Lock
	for _, name := range []string{"locksql", "lockmq", "lockfile"} {
		if !on[name] {
			continue
		}
		insts, ok := several[name]
		if !ok {
			out = append(out, providers.Lock{Name: name, TimeoutSec: t})
			continue
		}
		for _, inst := range insts {
			out = append(out, providers.Lock{Name: name, Instance: inst, TimeoutSec: t})
		}
	}
	return out
}

// forwarderSetup is what the proxy needs for the endpoints of the providers file: the mounts of
// the rendered endpoints file and of each key file (read-only, into the proxy only: no agent
// container ever mounts one), and a stamp of them for the proxy's lea.providers label ("" when
// there is no endpoint). The stamp is a hash of the rendered file and of each key file's identity
// (device and inode, never its content): a key rewritten in place is read at the next request,
// and a key file replaced by a new one (mv, most editors), which a single-file bind mount would
// not show, restarts the proxy at the next lea.
type forwarderSetup struct {
	mounts []string
	stamp  string
}

func (l *Launcher) forwarder(proj string) (forwarderSetup, error) {
	get := func(k string) string {
		if k == "LEA_PROJECTS" {
			return proj
		}
		return l.Cfg.Get(k)
	}
	on, several := l.Cfg.LockTools(proj, func(string, ...any) {})
	reg, err := LoadProviders(on, several, get)
	if err != nil {
		return forwarderSetup{}, die("%v", err)
	}
	if _, err := reg.Get(l.Cfg.Get("LEA_PROVIDER")); l.Cfg.Get("LEA_PROVIDER") != "" && err != nil {
		return forwarderSetup{}, die("LEA_PROVIDER: %v", err)
	}
	eps := reg.Endpoints()
	if len(eps) == 0 {
		return forwarderSetup{}, nil
	}
	var b strings.Builder
	var mounts []string
	b.WriteString("# Rendered by lea from " + l.Cfg.Get("LEA_PROVIDERS") + ": <name> <base-url> [<key file in the proxy>]\n")
	for _, p := range eps {
		b.WriteString(p.Name + " " + p.Endpoint.URL.String())
		if kf := p.Endpoint.KeyFile; kf != "" {
			if err := noComma(kf); err != nil {
				return forwarderSetup{}, err
			}
			dst := providers.KeyDir + "/" + p.Name
			b.WriteString(" " + dst)
			mounts = append(mounts, "--mount", "type=bind,src="+kf+",dst="+dst+",readonly")
		}
		b.WriteString("\n")
	}
	dir := l.Cfg.DataDir() + "/proxy"
	f := dir + "/endpoints"
	if err := Mkdirs(l.Cfg.DataDir(), dir); err != nil {
		return forwarderSetup{}, die("%v", err)
	}
	if err := WriteFile(l.Cfg.DataDir(), f, []byte(b.String())); err != nil {
		return forwarderSetup{}, die("%v", err)
	}
	if err := noComma(f); err != nil {
		return forwarderSetup{}, err
	}
	id := b.String()
	for _, p := range eps {
		if kf := p.Endpoint.KeyFile; kf != "" {
			id += "\x00" + fileIdentity(kf)
		}
	}
	sum := sha256.Sum256([]byte(id))
	return forwarderSetup{
		mounts: append([]string{"--mount", "type=bind,src=" + f + ",dst=/etc/lea/endpoints,readonly"}, mounts...),
		stamp:  hex.EncodeToString(sum[:8]),
	}, nil
}

// fileIdentity is a file's device and inode, "" when it cannot be read.
func fileIdentity(p string) string {
	fi, err := os.Stat(p)
	if err != nil {
		return ""
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return strconv.FormatUint(uint64(st.Dev), 10) + ":" + strconv.FormatUint(uint64(st.Ino), 10)
	}
	return ""
}
