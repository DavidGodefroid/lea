package launcher

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/DavidGodefroid/lea/internal/lockhttp"
)

// Networks and the egress proxy (ported from lib/net.sh). Each container has its own internal network
// (<prefix>-internal-<id>), shared only with the proxy.

func (l *Launcher) prefix() string { return l.Cfg.Get("LEA_PREFIX") }

// containerRunning is lea_container_running.
func (l *Launcher) containerRunning(name string) bool {
	out, _ := l.output(false, "docker", "inspect", "-f", "{{.State.Running}}", name)
	return out == "true"
}

// ensureNetworks is lea_ensure_networks: the egress network and the container's internal one.
func (l *Launcher) ensureNetworks(id string) error {
	inet, egr := l.Cfg.InternalNetwork(id), l.prefix()+"-egress"
	if l.quiet("docker", "network", "inspect", egr) != nil {
		if err := l.run("docker", "network", "create", "--label", "lea.prefix="+l.prefix(), egr); err != nil {
			return err
		}
	}
	if internal, err := l.output(false, "docker", "network", "inspect", "-f", "{{.Internal}}", inet); err == nil {
		if internal != "true" {
			return die("network %s exists but is not an internal network; remove it (docker network rm %s) and retry", inet, inet)
		}
		return nil
	}
	if l.quiet("docker", "network", "create", "--label", "lea.prefix="+l.prefix(), "--label", "lea.id="+id, "--internal",
		"-o", "com.docker.network.bridge.gateway_mode_ipv4=isolated", inet) != nil {
		l.logf("warning: Docker refused gateway_mode_ipv4=isolated; %s is created without it (check with: make test-isolation)", inet)
		return l.run("docker", "network", "create", "--label", "lea.prefix="+l.prefix(), "--label", "lea.id="+id, "--internal", inet)
	}
	return nil
}

// proxyRules is a digest (12 hex digits) of the files the proxy renders its rules and refusal
// pages from (the lea root's proxy/), "" when there is none: a lea whose rules changed (a new
// default allowlist, a new refusal) replaces a proxy that an older one started.
func (l *Launcher) proxyRules() string {
	dir := filepath.Join(l.Root, "proxy")
	files, _ := filepath.Glob(filepath.Join(dir, "errors", "*"))
	files = append(files, filepath.Join(dir, "render-squid-conf.sh"), filepath.Join(dir, "sni-matches.sh"),
		filepath.Join(dir, "entrypoint.sh"))
	sort.Strings(files)
	h, n := sha256.New(), 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(dir, f)
		sum := sha256.Sum256(b)
		h.Write([]byte(rel + "\x00" + hex.EncodeToString(sum[:]) + "\n"))
		n++
	}
	if n == 0 {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// ensureProxy is lea_ensure_proxy. A running proxy started for other git SSH endpoints (its
// lea.git-ssh label), another egress allowlist (lea.egress-allow), other providers' endpoints
// (lea.providers), other rules (lea.rules: proxyRules) or another image than LEA_PROXY_IMAGE is now
// (a rebuilt or updated image: the rules it runs are copied in) is replaced: its forwarders and its
// squid.conf are set at start. proj is the projects directory being opened: no key file may sit in
// it.
func (l *Launcher) ensureProxy(proj string) error {
	name, allow, rules := l.prefix()+"-proxy", l.Cfg.Get("LEA_EGRESS_ALLOW"), l.proxyRules()
	fw, err := l.forwarder(proj)
	if err != nil {
		return err
	}
	var lh []string
	var gateImage string
	if l.Cfg.LockHTTP() {
		if lh, gateImage, err = l.lockHTTPProxyArgs(); err != nil {
			return err
		}
	}
	if l.containerRunning(name) {
		got, _ := l.output(false, "docker", "inspect", "--format",
			`{{index .Config.Labels "lea.git-ssh"}}{{with index .Config.Labels "lea.egress-allow"}}|{{.}}{{end}}`+
				`{{with index .Config.Labels "lea.providers"}}#{{.}}{{end}}{{with index .Config.Labels "lea.rules"}}@{{.}}{{end}}`+
				`{{with index .Config.Labels "lea.lockhttp"}}^{{.}}{{end}}!{{.Image}}`, name)
		got, image, _ := strings.Cut(got, "!")
		want := l.Cfg.GitSSH()
		if allow != "" {
			want += "|" + allow
		}
		if fw.stamp != "" {
			want += "#" + fw.stamp
		}
		if rules != "" {
			want += "@" + rules
		}
		if lh != nil {
			want += "^1"
		}
		// An image that cannot be inspected (not pulled yet) leaves the labels to decide.
		current, err := l.output(false, "docker", "image", "inspect", "-f", "{{.Id}}", l.Cfg.Get("LEA_PROXY_IMAGE"))
		if err != nil || current == "" {
			current = image
		}
		// Docker writes the IDs sha256:<hex>, Podman <hex>.
		if got == want && strings.TrimPrefix(image, "sha256:") == strings.TrimPrefix(current, "sha256:") {
			if lh != nil {
				return l.ensureLockHTTPGate(false, gateImage)
			}
			return nil
		}
		if lh == nil && strings.Contains(got, "^1") {
			// LockHTTP turned off: its gate goes with the proxy it served.
			_ = l.quiet("docker", "rm", "-f", l.lockHTTPGateName())
		}
		l.logf("restarting %s for the git SSH endpoints '%s', the egress allowlist '%s', the providers of %s, the rules %s, LockHTTP %s and the image %s",
			name, l.Cfg.GitSSH(), allow, l.Cfg.Get("LEA_PROVIDERS"), rules, onOff(lh != nil), l.Cfg.Get("LEA_PROXY_IMAGE"))
	}
	hosts := l.Cfg.Get("LEA_INTERNAL_HOSTS")
	if err := noComma(hosts); err != nil {
		return err
	}
	_ = l.quiet("docker", "rm", "-f", name)
	a := []string{"run", "-d", "--name", name, "--restart", "unless-stopped", "--network", l.prefix() + "-egress",
		"--label", "lea.prefix=" + l.prefix(), "--label", "lea.role=proxy", "--label", "lea.git-ssh=" + l.Cfg.GitSSH(),
		"--label", "lea.egress-allow=" + allow}
	if fw.stamp != "" {
		a = append(a, "--label", "lea.providers="+fw.stamp)
	}
	if rules != "" {
		a = append(a, "--label", "lea.rules="+rules)
	}
	if lh != nil {
		a = append(a, "--label", "lea.lockhttp=1")
	}
	a = append(a, "--mount", "type=bind,src="+hosts+",dst=/etc/lea/internal-hosts,readonly")
	a = append(a, fw.mounts...)
	a = append(a, lh...)
	a = append(a, "-e", "LEA_GIT_SSH="+l.Cfg.GitSSH(), "-e", "LEA_OTEL_UPSTREAM="+l.Cfg.Get("LEA_OTEL_UPSTREAM"),
		"-e", "LEA_EGRESS_ALLOW="+allow,
		l.Cfg.Get("LEA_PROXY_IMAGE"))
	if err := l.run("docker", a...); err != nil || lh == nil {
		return err
	}
	return l.ensureLockHTTPGate(true, gateImage)
}

// proxyIP is lea__proxy_ip: the proxy's IPv4 address on network, "" when it is not attached.
func (l *Launcher) proxyIP(network string) string {
	out, _ := l.output(false, "docker", "container", "inspect", "--format",
		`{{with index .NetworkSettings.Networks "`+network+`"}}{{.IPAddress}}{{end}}`, l.prefix()+"-proxy")
	return out
}

// proxyAttach is lea_proxy_attach: connects the proxy to the container's network (alias
// lea-proxy) if needed, and returns its address there.
func (l *Launcher) proxyAttach(id string) (string, error) {
	n := l.Cfg.InternalNetwork(id)
	ip := l.proxyIP(n)
	if ip == "" {
		if l.run("docker", "network", "connect", "--alias", "lea-proxy", n, l.prefix()+"-proxy") != nil {
			return "", die("could not connect %s-proxy to %s", l.prefix(), n)
		}
		ip = l.proxyIP(n)
	}
	if ip == "" || strings.Trim(ip, "0123456789.") != "" || ip[0] == '.' || ip[len(ip)-1] == '.' || strings.Contains(ip, "..") {
		return "", die("could not read the address of %s-proxy on %s (got '%s')", l.prefix(), n, ip)
	}
	// LockHTTP: the address may have named another container's (removed) network until the
	// daemon's next rewrite: forget it before this container can use it.
	l.lockHTTPForget(func(nw lockhttp.Network) bool { return nw.LocalIP == ip })
	return ip, nil
}

// lockHTTPForget drops networks from the gate's sources.json (LEA_LOCKHTTP=1), best effort.
func (l *Launcher) lockHTTPForget(drop func(lockhttp.Network) bool) {
	if !l.Cfg.LockHTTP() {
		return
	}
	if err := lockhttp.DropNetworks(lockhttp.SourcesPath(l.Cfg.DataDir()), drop); err != nil {
		l.logf("warning: lockhttp: %v", err)
	}
}

// releaseNetwork is lea_release_network: best effort, once the container is stopped.
func (l *Launcher) releaseNetwork(id string) {
	n := l.Cfg.InternalNetwork(id)
	_ = l.quiet("docker", "network", "disconnect", "-f", n, l.prefix()+"-proxy")
	c := l.prefix() + "-" + id
	l.lockHTTPForget(func(nw lockhttp.Network) bool { return nw.Container == c })
	_ = l.quiet("docker", "network", "rm", n)
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}
