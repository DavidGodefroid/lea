package launcher

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
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

// isolatedGatewayDocker is the first Docker Engine release with the bridge driver's
// gateway_mode_ipv4=isolated: an internal network on which the host has no address. An older
// Engine either refuses the option or ignores it (before gateway_mode_ipv4 existed), leaving the
// host's gateway address, and every host service bound to 0.0.0.0, reachable from the agents.
const isolatedGatewayDocker = "28.0"

// allowGateway is LEA_ALLOW_GATEWAY=1: the user accepts an internal network without
// gateway_mode_ipv4=isolated (an older Docker Engine, Podman). Without it, lea fails closed (#105).
func (l *Launcher) allowGateway() bool { return l.Cfg.Get("LEA_ALLOW_GATEWAY") == "1" }

// isolatedUnavailable says why this engine cannot be trusted to give an internal network an
// isolated gateway, "" only for a Docker Engine known to have gateway_mode_ipv4=isolated. An
// engine or a version lea cannot tell is unavailable too: lea fails closed (#105), it never lets
// the option string alone decide.
func (l *Launcher) isolatedUnavailable() string {
	engine, v := l.hostEngine()
	switch {
	case engine == "podman":
		return "Podman " + v + "'s bridge has no gateway_mode_ipv4=isolated"
	case engine != "docker" || v == "":
		return "lea cannot tell the container engine's version (docker version names no Docker Engine)"
	case !versionAtLeast(v, isolatedGatewayDocker):
		return "Docker Engine " + v + " is older than " + isolatedGatewayDocker + ", the first with gateway_mode_ipv4=isolated"
	}
	return ""
}

// networkFormat is what ensureNetworks reads of an internal network (parseNetwork): whether it is
// internal and its gateway_mode_ipv4, then its ID and its bridge name option after a |. An engine
// may print an option it does not hold as <no value>, with a space: the | keeps the fields apart.
const networkFormat = `{{.Internal}} {{index .Options "com.docker.network.bridge.gateway_mode_ipv4"}}` +
	`|{{.Id}}|{{index .Options "com.docker.network.bridge.name"}}`

// parseNetwork reads networkFormat's output: internal is "true" for an internal network, mode is
// "" or <no value> when the option is unset.
func parseNetwork(info string) (internal, mode, id, bridge string) {
	head, rest, _ := strings.Cut(info, "|")
	internal, mode, _ = strings.Cut(head, " ")
	id, bridge, _ = strings.Cut(rest, "|")
	return internal, strings.TrimSpace(mode), strings.TrimSpace(id), strings.TrimSpace(bridge)
}

// hostBridgeAddrs are the IPv4 addresses the host holds on the interface name; found is false
// when the host has no such interface (the bridge lives in another network namespace: rootless
// Docker, Docker Desktop's VM). It is Launcher.bridgeAddrs when that is nil.
func hostBridgeAddrs(name string) (addrs []string, found bool, err error) {
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		return nil, false, nil
	}
	as, err := ifc.Addrs()
	if err != nil {
		return nil, true, err
	}
	for _, a := range as {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
			addrs = append(addrs, ipn.String())
		}
	}
	return addrs, true, nil
}

// hostOnBridge says how the host is reachable on the bridge of the internal network id (bridge,
// its com.docker.network.bridge.name option, or Docker's br-<12 first digits of id>), "" when lea
// sees the bridge without an IPv4 address or cannot see it at all. It checks the isolation itself
// rather than trusting gateway_mode_ipv4: a bridge with a host address is refused whatever the
// option says. A bridge whose addresses cannot be read is refused too.
func (l *Launcher) hostOnBridge(id, bridge string) string {
	if bridge == "" || bridge == "<no value>" {
		if len(id) < 12 {
			return ""
		}
		bridge = "br-" + id[:12]
	}
	get := l.bridgeAddrs
	if get == nil {
		get = hostBridgeAddrs
	}
	addrs, found, err := get(bridge)
	switch {
	case !found:
		return ""
	case err != nil:
		return "lea cannot read the host's addresses on its bridge " + bridge + " (" + err.Error() + ")"
	case len(addrs) > 0:
		return "the host holds " + strings.Join(addrs, ", ") + " on its bridge " + bridge
	}
	return ""
}

// ensureNetworks is lea_ensure_networks: the egress network and the container's internal one. The
// internal network must have gateway_mode_ipv4=isolated on an engine known to honour it, and the
// host must hold no address on its bridge: otherwise lea refuses to start the container, unless
// LEA_ALLOW_GATEWAY=1 (#105). The same checks hold for a network that already exists as for one
// lea creates.
func (l *Launcher) ensureNetworks(id string) error {
	inet, egr := l.Cfg.InternalNetwork(id), l.prefix()+"-egress"
	if l.quiet("docker", "network", "inspect", egr) != nil {
		if err := l.run("docker", "network", "create", "--label", "lea.prefix="+l.prefix(), egr); err != nil {
			return err
		}
	}
	if info, err := l.output(false, "docker", "network", "inspect", "-f", networkFormat, inet); err == nil {
		internal, mode, nid, bridge := parseNetwork(info)
		if internal != "true" {
			return die("network %s exists but is not an internal network; remove it (docker network rm %s) and retry", inet, inet)
		}
		if mode != "isolated" {
			if mode == "" || mode == "<no value>" {
				mode = "unset"
			}
			if !l.allowGateway() {
				return die("network %s exists without gateway_mode_ipv4=isolated (%s): the host is reachable from it; stop its container (lea stop), remove it (docker network rm %s) and retry, or set LEA_ALLOW_GATEWAY=1 to accept it", inet, mode, inet)
			}
			l.logf("warning: %s has gateway_mode_ipv4=%s, not isolated: host services are reachable from the agents (LEA_ALLOW_GATEWAY=1)", inet, mode)
			return nil
		}
		// The option alone proves nothing: an engine that predates it may hold it and ignore it.
		if why := l.isolatedUnavailable(); why != "" {
			if !l.allowGateway() {
				return die("network %s has gateway_mode_ipv4=isolated but %s: lea will not start an agent on a network where the host may be reachable (needs Docker Engine %s or later; set LEA_ALLOW_GATEWAY=1 to accept it)", inet, why, isolatedGatewayDocker)
			}
			l.logf("warning: %s: %s may leave host services reachable from the agents (LEA_ALLOW_GATEWAY=1)", why, inet)
			return nil
		}
		return l.refuseHostOnBridge(inet, nid, bridge)
	}
	why := l.isolatedUnavailable()
	if why == "" {
		if l.quiet("docker", "network", "create", "--label", "lea.prefix="+l.prefix(), "--label", "lea.id="+id, "--internal",
			"-o", "com.docker.network.bridge.gateway_mode_ipv4=isolated", inet) == nil {
			info, _ := l.output(false, "docker", "network", "inspect", "-f", networkFormat, inet)
			_, _, nid, bridge := parseNetwork(info)
			return l.refuseHostOnBridge(inet, nid, bridge)
		}
		why = "Docker refused gateway_mode_ipv4=isolated"
	}
	if !l.allowGateway() {
		return die("%s: lea will not start an agent on a network where the host is reachable (needs Docker Engine %s or later; set LEA_ALLOW_GATEWAY=1 to accept a plain internal network)", why, isolatedGatewayDocker)
	}
	l.logf("warning: %s; %s is created without it (LEA_ALLOW_GATEWAY=1): host services are reachable from the agents", why, inet)
	return l.run("docker", "network", "create", "--label", "lea.prefix="+l.prefix(), "--label", "lea.id="+id, "--internal", inet)
}

// refuseHostOnBridge fails when the host holds an address on the bridge of inet (hostOnBridge),
// unless LEA_ALLOW_GATEWAY=1, which only warns.
func (l *Launcher) refuseHostOnBridge(inet, id, bridge string) error {
	reach := l.hostOnBridge(id, bridge)
	if reach == "" {
		return nil
	}
	if !l.allowGateway() {
		return die("network %s has gateway_mode_ipv4=isolated but %s: the host is reachable from it; stop its container (lea stop), remove it (docker network rm %s) and retry, or set LEA_ALLOW_GATEWAY=1 to accept it", inet, reach, inet)
	}
	l.logf("warning: %s: %s; host services are reachable from the agents (LEA_ALLOW_GATEWAY=1)", inet, reach)
	return nil
}

// proxyConfine are the docker run arguments that confine the proxy, the one container on both the
// agents' networks and the egress network, which holds the provider keys: no capability but SETUID
// and SETGID (its entrypoint starts each service under a user of its own with su-exec: none of
// them runs as root), no-new-privileges, a read-only root with tmpfs where it writes, and pids and
// memory limits. Only an image whose entrypoint runs so (its label lea.confine=1) gets it
// (proxyConfinable), and rootless Podman without a cgroup controller gets it without that
// controller's limit (proxyConfinement). The arguments passed are part of proxyRules: a lea that
// confines it otherwise replaces a running proxy.
var proxyConfine = []string{"--cap-drop", "ALL", "--cap-add", "SETUID", "--cap-add", "SETGID",
	"--security-opt", "no-new-privileges", "--read-only",
	"--tmpfs", "/run:rw,nosuid,nodev,noexec,size=16m", "--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=64m",
	"--tmpfs", "/var/cache/squid:rw,nosuid,nodev,noexec,size=16m",
	"--pids-limit", "512", "--memory", "1g"}

// proxyConfinable tells whether the proxy image runs under proxyConfine: its label lea.confine
// is 1. An older image (a release before the confinement, a pinned LEA_PROXY_IMAGE) writes its
// configuration in its root and starts squid as root: it runs as before (ensureProxy warns when it
// starts it). An image that cannot be inspected gets the confinement (docker run then pulls or
// fails on it).
func (l *Launcher) proxyConfinable(image string) bool {
	out, err := l.output(false, "docker", "inspect", "--type", "image", "-f", `{{index .Config.Labels "lea.confine"}}`, image)
	return err != nil || out == "1"
}

// proxyConfinement is the confinement the proxy is started with: proxyConfine for a confinable
// image, nil for an older one. On rootless Podman without the memory or the pids cgroup controller
// (missingControllers), podman run refuses that controller's limit (#132): the proxy then runs
// without it, as the lea container does, and dropped names the limits left out. The capabilities,
// no-new-privileges and the read-only root need no controller: they are always kept.
func (l *Launcher) proxyConfinement(image string) (confine, dropped []string) {
	if !l.proxyConfinable(image) {
		return nil, nil
	}
	miss := l.missingControllers()
	for i := 0; i < len(proxyConfine); i++ {
		a := proxyConfine[i]
		if i+1 < len(proxyConfine) && (a == "--memory" && miss["memory"] || a == "--pids-limit" && miss["pids"]) {
			dropped = append(dropped, a+" "+proxyConfine[i+1])
			i++
			continue
		}
		confine = append(confine, a)
	}
	return confine, dropped
}

// proxyRules is a digest (12 hex digits) of the files the proxy renders its rules and refusal
// pages from (the lea root's proxy/) and of the confinement arguments the proxy is started with
// (proxyConfinement), "" when there is no rule file: a lea whose rules changed (a new default
// allowlist, a new refusal, another confinement, a cgroup controller now delegated) replaces a
// proxy that an older one started.
func (l *Launcher) proxyRules(confine []string) string {
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
	h.Write([]byte("confine\x00" + strings.Join(confine, "\x00") + "\n"))
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// ensureProxy is lea_ensure_proxy. A running proxy started for other git SSH endpoints (its
// lea.git-ssh label), another egress allowlist (lea.egress-allow), other providers' endpoints
// (lea.providers), other rules (lea.rules: proxyRules) or another image than LEA_PROXY_IMAGE is now
// (a rebuilt or updated image: the rules it runs are copied in) is replaced: its forwarders and its
// squid.conf are set at start. proj is the projects directory being opened: no key file may sit in
// it.
func (l *Launcher) ensureProxy(proj string) error {
	image := l.Cfg.Get("LEA_PROXY_IMAGE")
	confine, dropped := l.proxyConfinement(image)
	name, allow, rules := l.prefix()+"-proxy", l.Cfg.Get("LEA_EGRESS_ALLOW"), l.proxyRules(confine)
	fw, err := l.forwarder(proj)
	if err != nil {
		return err
	}
	var lh []string
	var gateImage string
	var hub *gateHub
	if l.Cfg.LockHTTP() {
		if lh, gateImage, err = l.lockHTTPProxyArgs(); err != nil {
			return err
		}
		// lox run's lockhttp lock for this projects directory: the gate asks its hub, and only it.
		if hub, err = l.lockUpGate(proj); err != nil {
			return l.staleGate(err)
		}
		// One gate serves every projects directory of this prefix: it asks the hub of the last one
		// started under lox run. A switch of whose hub under the agents of another directory would
		// change how their requests are approved: lea stops, before it changes anything (the
		// proxy, the gate's inputs, its networks).
		if err := l.gateSwitchRefused(l.lockHTTPGateName(), proj, proj); err != nil {
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
			want += "^" + l.lockHTTPStamp()
		}
		// An image that cannot be inspected (not pulled yet) leaves the labels to decide.
		current, err := l.output(false, "docker", "image", "inspect", "-f", "{{.Id}}", l.Cfg.Get("LEA_PROXY_IMAGE"))
		if err != nil || current == "" {
			current = image
		}
		// Docker writes the IDs sha256:<hex>, Podman <hex>.
		if got == want && strings.TrimPrefix(image, "sha256:") == strings.TrimPrefix(current, "sha256:") {
			if lh != nil {
				return l.ensureLockHTTPGate(false, gateImage, hub, proj)
			}
			return nil
		}
		if lh == nil && strings.Contains(got, "^1") {
			// LockHTTP turned off: its gate goes with the proxy it served.
			_ = l.quiet("docker", "rm", "-f", l.lockHTTPGateName(), l.lockHTTPSignerName())
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
	switch {
	case confine == nil:
		l.logf("warning: the proxy image %s predates the proxy's confinement (no capability, read-only, no root service): it runs without it; use a newer lea-proxy image (lea update, or make build)", image)
	case len(dropped) > 0:
		l.logf("warning: rootless Podman does not get the cgroup controller of %s: %s runs without it; to apply it: %s",
			strings.Join(dropped, ", "), name, delegateFix)
	}
	a = append(a, confine...)
	if fw.stamp != "" {
		a = append(a, "--label", "lea.providers="+fw.stamp)
	}
	if rules != "" {
		a = append(a, "--label", "lea.rules="+rules)
	}
	if lh != nil {
		a = append(a, "--label", "lea.lockhttp="+l.lockHTTPStamp())
	}
	a = append(a, "--mount", "type=bind,src="+hosts+",dst=/etc/lea/internal-hosts,readonly")
	a = append(a, fw.mounts...)
	fm, err := l.forgeMount()
	if err != nil {
		return err
	}
	a = append(a, fm...)
	a = append(a, lh...)
	a = append(a, "-e", "LEA_GIT_SSH="+l.Cfg.GitSSH(), "-e", "LEA_OTEL_UPSTREAM="+l.Cfg.Get("LEA_OTEL_UPSTREAM"),
		"-e", "LEA_EGRESS_ALLOW="+allow,
		l.Cfg.Get("LEA_PROXY_IMAGE"))
	if err := l.run("docker", a...); err != nil {
		if l.isInterrupted() {
			return errInterrupted
		}
		return die("could not start the proxy %s (docker's message above): lea doctor checks the setup", name)
	}
	if lh == nil {
		return nil
	}
	return l.ensureLockHTTPGate(true, gateImage, hub, proj)
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
