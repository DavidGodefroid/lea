package launcher

import (
	"os"
	"strconv"

	"github.com/DavidGodefroid/lea/internal/lockhttp"
)

// LockHTTP (LEA_LOCKHTTP=1): a LockHTTP gate in the proxy container decides every web request of
// the agents, asking the human in a lockhttp console on the host (contract: lockhttp's
// docs/lea.md). The proxy container alone gets the console's socket directory; the agents get
// a proxy URL with their own token (internal/lockhttp), and the daemon tells the gate who holds
// which (sources.json).

// lockHTTPBin is the host's lockhttp, bind-mounted read-only into the proxy container: a static
// binary, the same version as the console.
func lockHTTPBin() string { return lockHostRoot + "/usr/local/bin/lockhttp" }

// lockHTTPProxyArgs are the docker run arguments of the proxy container for the gate, and its
// label stamp; an error when the host is not set up for it: the user asked for LockHTTP, so lea
// never starts the agents without it.
func (l *Launcher) lockHTTPProxyArgs() ([]string, error) {
	if why := rootOwned(lockHTTPBin(), false); why != "" {
		return nil, die("LEA_LOCKHTTP=1: %s (install lockhttp, then: sudo lockhttp install --client %s --gate-uid 10003%s)",
			why, l.userName(), l.rootlessFlag())
	}
	run := lockHostRoot + "/run/lockhttp"
	if fi, err := os.Stat(run); err != nil || !fi.IsDir() {
		return nil, die("LEA_LOCKHTTP=1: %s is absent (sudo lockhttp install --client %s --gate-uid 10003%s)", run, l.userName(), l.rootlessFlag())
	}
	gid, ok := lookupID(lockHostRoot+"/etc/group", "lockhttp-clients", 2)
	if !ok {
		return nil, die("LEA_LOCKHTTP=1: no lockhttp-clients group in /etc/group (sudo lockhttp install --client %s)", l.userName())
	}
	if !inGroup(lockHostRoot+"/etc/group", "lockhttp-clients", l.userName()) {
		l.logf("warning: lockhttp: you are not in the lockhttp-clients group: the gate cannot reach the console (sudo usermod -aG lockhttp-clients %s, then log in again)", l.userName())
	}
	data := l.Cfg.DataDir()
	key, err := lockhttp.Key(data)
	if err != nil {
		return nil, die("LEA_LOCKHTTP=1: %v", err)
	}
	gate, audit, status := lockhttp.GateDir(data), lockhttp.AuditDir(data), lockhttp.StatusDir(data)
	if err := Mkdirs(data, gate, audit, status); err != nil {
		return nil, die("%v", err)
	}
	for _, d := range []string{gate, status} {
		// The gate's uid reads its inputs and lea reads its status: neither is secret.
		if err := os.Chmod(d, 0o755); err != nil {
			return nil, die("%v", err)
		}
	}
	if err := noComma(gate, audit, status, run, lockHTTPBin()); err != nil {
		return nil, err
	}
	var mitm []string
	if l.Cfg.LockHTTPMITM() {
		ca, err := l.lockHTTPCA()
		if err != nil {
			return nil, err
		}
		// The whole CA directory, read-only: the entrypoint hands the key to the signer's uid
		// alone; the gate gets the certificate only.
		mitm = []string{"-e", "LEA_LOCKHTTP_MITM=1", "--mount", "type=bind,src=" + ca + ",dst=/etc/lockhttp-ca,readonly"}
	}
	a := []string{"-e", "LEA_LOCKHTTP=1", "-e", "LEA_KEYPROXY_TOKEN=" + lockhttp.KeyproxyToken(key),
		"--mount", "type=bind,src=" + lockHTTPBin() + ",dst=/usr/local/bin/lockhttp,readonly",
		"--mount", "type=bind,src=" + run + ",dst=/run/lockhttp",
		"--mount", "type=bind,src=" + gate + ",dst=/etc/lockhttp-gate,readonly",
		"--mount", "type=bind,src=" + audit + ",dst=/var/log/lockhttp",
		"--mount", "type=bind,src=" + status + ",dst=/run/lockhttp-gate"}
	a = append(a, mitm...)
	if l.rootlessPodman() {
		// crun hands the container the user's own groups, lockhttp-clients among them.
		return append(a, "--group-add", "keep-groups"), nil
	}
	g := strconv.Itoa(gid)
	return append(a, "--group-add", g, "-e", "LEA_LOCKHTTP_GID="+g), nil
}

// rootlessPodman reports a Podman engine run by a user other than root.
func (l *Launcher) rootlessPodman() bool {
	engine, _ := l.hostEngine()
	return engine == "podman" && lockHostSelf() != 0
}

func (l *Launcher) rootlessFlag() string {
	if l.rootlessPodman() {
		return " --rootless"
	}
	return ""
}

// lockHTTPAgentEnv is the proxy environment of a lea container under LockHTTP: the gate on the
// proxy's address pip, with the container's "shell" token (its processes outside an agent; each
// agent gets its own from the daemon), and the nested Podman's token.
func (l *Launcher) lockHTTPAgentEnv(container, pip string) (proxy string, nestedToken string, err error) {
	key, err := lockhttp.Key(l.Cfg.DataDir())
	if err != nil {
		return "", "", die("LEA_LOCKHTTP=1: %v", err)
	}
	return "http://" + lockhttp.ShellAgent + ":" + lockhttp.Token(key, container, lockhttp.ShellAgent) + "@" + pip + ":" + lockhttp.Port,
		lockhttp.Token(key, container, lockhttp.NestedAgent), nil
}

// lockHTTPCA is the MITM CA's directory, made on first use with the host's lockhttp (lockhttp ca
// init: P-256, 90 days). lea never rotates it by itself: lea doctor warns a week before it ends
// (lockhttp ca init --force rotates it; then lea stop --all, and lea).
func (l *Launcher) lockHTTPCA() (string, error) {
	dir := lockhttp.CADir(l.Cfg.DataDir())
	if err := noComma(dir); err != nil {
		return "", err
	}
	if !isRegular(dir+"/ca.pem") || !isRegular(dir+"/ca-key.pem") {
		if err := Mkdirs(l.Cfg.DataDir(), lockhttp.Dir(l.Cfg.DataDir())); err != nil {
			return "", die("%v", err)
		}
		if err := l.run(lockHTTPBin(), "ca", "init", "--dir", dir); err != nil {
			return "", die("LEA_LOCKHTTP_MITM=1: lockhttp ca init --dir %s failed", dir)
		}
	}
	if !isRegular(dir+"/ca.pem") || !isRegular(dir+"/ca-key.pem") {
		return "", die("LEA_LOCKHTTP_MITM=1: no ca.pem and ca-key.pem in %s", dir)
	}
	return dir, nil
}

// lockHTTPStamp is the proxy's lea.lockhttp label: "1", or under MITM "mitm-" and a digest of the
// CA's certificate, so that a new CA restarts the proxy.
func (l *Launcher) lockHTTPStamp() string {
	if !l.Cfg.LockHTTPMITM() {
		return "1"
	}
	b, _ := os.ReadFile(lockhttp.CADir(l.Cfg.DataDir()) + "/ca.pem")
	return "mitm-" + lockhttp.Hash(string(b))[:12]
}

// lockHTTPAgentCA are the run arguments that make lea's agents trust the MITM CA: its
// certificate only (never the key), which the entrypoint adds to the system trust store, and the
// variables of the tools that keep their own (Node, Python requests, pip).
func (l *Launcher) lockHTTPAgentCA() []string {
	if !l.Cfg.LockHTTPMITM() {
		return nil
	}
	const bundle = "/etc/pki/tls/certs/ca-bundle.crt"
	return []string{"-e", "LEA_LOCKHTTP_MITM=1",
		"--mount", "type=bind,src=" + lockhttp.CADir(l.Cfg.DataDir()) + "/ca.pem,dst=/etc/lea/lockhttp-ca.pem,readonly",
		"-e", "NODE_EXTRA_CA_CERTS=/etc/lea/lockhttp-ca.pem", "-e", "SSL_CERT_FILE=" + bundle,
		"-e", "REQUESTS_CA_BUNDLE=" + bundle, "-e", "PIP_CERT=" + bundle}
}
