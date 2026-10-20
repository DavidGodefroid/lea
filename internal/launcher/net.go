package launcher

import "strings"

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

// ensureProxy is lea_ensure_proxy. A running proxy started for other git SSH endpoints (its
// lea.git-ssh label), another egress allowlist (lea.egress-allow) or other providers' endpoints
// (lea.providers) is replaced: its forwarders and its squid.conf are set at start. proj is the
// projects directory being opened: no key file may sit in it.
func (l *Launcher) ensureProxy(proj string) error {
	name, allow := l.prefix()+"-proxy", l.Cfg.Get("LEA_EGRESS_ALLOW")
	fw, err := l.forwarder(proj)
	if err != nil {
		return err
	}
	if l.containerRunning(name) {
		got, _ := l.output(false, "docker", "inspect", "--format",
			`{{index .Config.Labels "lea.git-ssh"}}{{with index .Config.Labels "lea.egress-allow"}}|{{.}}{{end}}`+
				`{{with index .Config.Labels "lea.providers"}}#{{.}}{{end}}`, name)
		want := l.Cfg.GitSSH()
		if allow != "" {
			want += "|" + allow
		}
		if fw.stamp != "" {
			want += "#" + fw.stamp
		}
		if got == want {
			return nil
		}
		l.logf("restarting %s for the git SSH endpoints '%s', the egress allowlist '%s' and the providers of %s",
			name, l.Cfg.GitSSH(), allow, l.Cfg.Get("LEA_PROVIDERS"))
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
	a = append(a, "--mount", "type=bind,src="+hosts+",dst=/etc/lea/internal-hosts,readonly")
	a = append(a, fw.mounts...)
	a = append(a, "-e", "LEA_GIT_SSH="+l.Cfg.GitSSH(), "-e", "LEA_OTEL_UPSTREAM="+l.Cfg.Get("LEA_OTEL_UPSTREAM"),
		"-e", "LEA_EGRESS_ALLOW="+allow,
		l.Cfg.Get("LEA_PROXY_IMAGE"))
	return l.run("docker", a...)
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
	return ip, nil
}

// releaseNetwork is lea_release_network: best effort, once the container is stopped.
func (l *Launcher) releaseNetwork(id string) {
	n := l.Cfg.InternalNetwork(id)
	_ = l.quiet("docker", "network", "disconnect", "-f", n, l.prefix()+"-proxy")
	_ = l.quiet("docker", "network", "rm", n)
}
