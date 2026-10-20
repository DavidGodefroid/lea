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
func LoadProviders(get func(string) string) (*providers.Registry, error) {
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
	return providers.Load(get("LEA_PROVIDERS"), o)
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
	reg, err := LoadProviders(get)
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
