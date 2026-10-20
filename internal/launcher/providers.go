package launcher

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"

	"github.com/DavidGodefroid/lea/internal/providers"
)

// LoadProviders reads the providers file (LEA_PROVIDERS) against the internal-hosts file
// (LEA_INTERNAL_HOSTS): a private upstream must be listed there. No key file may sit in the
// projects directory (LEA_PROJECTS_DIR), which the agents read and write, and each must be the
// user's own, readable by them only.
func LoadProviders(get func(string) string) (*providers.Registry, error) {
	o := providers.Options{Internal: providers.Hosts{}, UID: os.Getuid()}
	if f, err := os.Open(get("LEA_INTERNAL_HOSTS")); err == nil {
		o.Internal = providers.ParseHosts(f)
		f.Close()
	}
	for _, d := range []string{get("LEA_PROJECTS_DIR"), get("LEA_PROJECTS")} {
		if d != "" {
			o.Forbidden = append(o.Forbidden, d)
		}
	}
	return providers.Load(get("LEA_PROVIDERS"), o)
}

// forwarderSetup is what the proxy needs for the endpoints of the providers file: the mounts of
// the rendered endpoints file and of each key file (read-only, into the proxy only: no agent
// container ever mounts one), and a stamp of them for the proxy's lea.providers label ("" when
// there is no endpoint). The stamp is a hash of the rendered file: it changes with an endpoint or
// a key file's path, never with a key (the forwarder reads the key file at each request).
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
	sum := sha256.Sum256([]byte(b.String()))
	return forwarderSetup{
		mounts: append([]string{"--mount", "type=bind,src=" + f + ",dst=/etc/lea/endpoints,readonly"}, mounts...),
		stamp:  hex.EncodeToString(sum[:8]),
	}, nil
}
