package launcher

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/DavidGodefroid/lea/internal/lockhttp"
	"github.com/DavidGodefroid/lea/internal/locks"
)

// LockHTTP (LEA_LOCKHTTP=1): a LockHTTP gate beside the proxy container decides every web request
// of the agents, asking the human on the hub of `lox run` (contract: lockhttp's docs/lea.md, and
// lockhub's docs/lox-run.md; lockhubgate.go). The gate is a sidecar container in the proxy's
// network namespace, from the image lockhttp's launcher pins; it alone gets the console's socket
// volume. The agents get a proxy URL with their
// own token (internal/lockhttp), and the daemon tells the gate who holds which (sources.json).

// lockHTTPLaunchers are where lockhttp's launcher may be on the host, in the order lea looks:
// /usr/local/bin (a root-owned, system-wide install), the user's ~/.local/bin (lockfamily's
// install.sh), then brew's bin (brew install davidgodefroid/tap/lockfamily: HOMEBREW_PREFIX, else
// brew's default prefixes on macOS and Linux).
func (l *Launcher) lockHTTPLaunchers() []string {
	c := []string{lockHostRoot + "/usr/local/bin/lockhttp"}
	if h := l.Cfg.Get("HOME"); h != "" {
		c = append(c, h+"/.local/bin/lockhttp")
	}
	if p := l.Cfg.Get("HOMEBREW_PREFIX"); p != "" {
		c = append(c, p+"/bin/lockhttp")
	}
	return append(c, lockHostRoot+"/opt/homebrew/bin/lockhttp", lockHostRoot+"/home/linuxbrew/.linuxbrew/bin/lockhttp")
}

// lockHTTPLauncher is lockhttp's launcher on the host, the first of lockHTTPLaunchers that is a
// file (or a symbolic link to one, as brew's), else the first of them: lea runs the image it pins.
func (l *Launcher) lockHTTPLauncher() string {
	c := l.lockHTTPLaunchers()
	for _, p := range c {
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			return p
		}
	}
	return c[0]
}

// lockHTTPInstall is how to install lockhttp's launcher, for lea's messages.
const lockHTTPInstall = "brew install davidgodefroid/tap/lockfamily (or brew upgrade), then lox run"

// lockHTTPIssuer is the OIDC issuer of the certificates that sign lockhttp's releases.
const lockHTTPIssuer = "https://token.actions.githubusercontent.com"

// lockHTTPRelease is a launcher_version of a release: X.Y.Z, a pre-release X.Y.Z-rc.1 too.
var lockHTTPRelease = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`)

// lockHTTPRegistry is where lockhttp's release images are; the launcher pins one by digest.
const lockHTTPRegistry = "ghcr.io/davidgodefroid/lockhttp"

var (
	lockHTTPVersionLine = regexp.MustCompile(`(?m)^launcher_version=([0-9A-Za-z.+-]+)$`)
	lockHTTPDigestLine  = regexp.MustCompile(`(?m)^image_digest=(.*)$`)
	lockHTTPDigest      = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// lockHTTPImage is the gate's image: the one the launcher runs, read from its launcher_version
// and image_digest lines (docs/docker.md, "Version pinning"): a release by digest, never by a tag;
// the local image lockhttp for a root-owned launcher run from a clone (launcher_version=dev). An
// error when there is no launcher, when it pins no digest, or when others than root may write it
// (the user's, brew's) and its digest is not the one lockhttp's release signed (lockHTTPSigned).
func (l *Launcher) lockHTTPImage() (string, error) {
	p := l.lockHTTPLauncher()
	if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
		return "", die("LEA_LOCKHTTP=1: no lockhttp launcher (%s): %s", strings.Join(l.lockHTTPLaunchers(), ", "), lockHTTPInstall)
	}
	// Only /usr/local/bin is root's: the user may replace a file in ~/.local/bin or brew's bin.
	trusted := p == l.lockHTTPLaunchers()[0] && rootOwned(p, false) == ""
	f, err := os.Open(p)
	if err != nil {
		return "", die("LEA_LOCKHTTP=1: %v", err)
	}
	defer f.Close()
	// The two lines sit in the launcher's first lines; a host binary of v0.1.0 has neither.
	head := make([]byte, 64<<10)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	v := lockHTTPVersionLine.FindSubmatch(head)
	if v == nil {
		return "", die("LEA_LOCKHTTP=1: %s is not lockhttp's launcher (a v0.1.0 host install?): %s", p, lockHTTPInstall)
	}
	if string(v[1]) == "dev" {
		if !trusted {
			return "", die("LEA_LOCKHTTP=1: %s runs from a clone (launcher_version=dev) and is not owned and writable by root only: lea runs a clone's local image only from a root-owned /usr/local/bin/lockhttp", p)
		}
		return "lockhttp", nil
	}
	d := lockHTTPDigestLine.FindSubmatch(head)
	if d == nil || len(d[1]) == 0 {
		return "", die("LEA_LOCKHTTP=1: %s pins no image digest: %s", p, lockHTTPInstall)
	}
	if !lockHTTPDigest.Match(d[1]) {
		return "", die("LEA_LOCKHTTP=1: %s: invalid image digest '%s'", p, d[1])
	}
	if !trusted {
		if err := l.lockHTTPSigned(p, string(v[1]), string(d[1])); err != nil {
			return "", err
		}
	}
	return lockHTTPRegistry + "@" + string(d[1]), nil
}

// lockHTTPSigned is an error unless digest is the image lockhttp's release ver signed. A root-owned
// launcher is trusted as it is: an agent cannot rewrite it. One that others than root may write
// (the user's ~/.local/bin, brew's bin) could pin any image, so lea runs its digest only when it
// is the set's (config/locks.lock, checked with cosign when lea pinned it, at the same release),
// or when cosign verifies it against lockhttp's release workflow at that very tag, as lox run
// checks the images it pins.
func (l *Launcher) lockHTTPSigned(p, ver, digest string) error {
	v := "v" + strings.TrimPrefix(ver, "v")
	if !lockHTTPRelease.MatchString(v) {
		return die("LEA_LOCKHTTP=1: %s names no lockhttp release (launcher_version=%s): %s", p, ver, lockHTTPInstall)
	}
	if set, err := l.lockSet(); err == nil {
		if pin := set.Pins["lockhttp"]; pin.Version == v && locks.Digest(pin.Image) == digest {
			return nil
		}
	}
	ref := lockHTTPRegistry + "@" + digest
	msg, err := l.errOutput("cosign", "verify", "--certificate-oidc-issuer", lockHTTPIssuer,
		"--certificate-identity", locks.Identity("lockhttp", v), ref)
	if errors.Is(err, exec.ErrNotFound) {
		return die("LEA_LOCKHTTP=1: cosign not found: brew install cosign (lea checks the image %s pins: others than root may write it)", p)
	}
	if err != nil {
		if msg != "" {
			msg = " (cosign: " + strings.SplitN(msg, "\n", 2)[0] + ")"
		}
		return die("LEA_LOCKHTTP=1: %s pins %s, which cosign does not verify as lockhttp's release %s%s: others than root may write that launcher, and lea runs only a signed image. Reinstall it (brew reinstall davidgodefroid/tap/lockhttp), then lea", p, ref, v, msg)
	}
	return nil
}

// lockHTTPProxyArgs are the docker run arguments of the proxy container under LockHTTP (squid on
// the loopback, keyproxy through the gate, sources.json to name the calling container), and the
// gate's image; an error when the host is not set up for it: the user asked for LockHTTP, so
// lea never starts the agents without it.
func (l *Launcher) lockHTTPProxyArgs() ([]string, string, error) {
	image, err := l.lockHTTPImage()
	if err != nil {
		return nil, "", err
	}
	data := l.Cfg.DataDir()
	key, err := lockhttp.Key(data)
	if err != nil {
		return nil, "", die("LEA_LOCKHTTP=1: %v", err)
	}
	gate, status := lockhttp.GateDir(data), lockhttp.StatusDir(data)
	if err := Mkdirs(data, gate, status); err != nil {
		return nil, "", die("%v", err)
	}
	// The gate (uid 10003) reads its inputs and writes its status, which lea reads: neither is
	// secret, and both sit in lea's own data directory.
	if err := os.Chmod(gate, 0o755); err != nil {
		return nil, "", die("%v", err)
	}
	if err := os.Chmod(status, 0o777); err != nil {
		return nil, "", die("%v", err)
	}
	if err := noComma(gate, status); err != nil {
		return nil, "", err
	}
	// The gate exits on a missing sources.json: until the daemon writes its own, one that names no
	// container (every agent refused, E_SOURCE_UNKNOWN) and only keyproxy's token.
	if p := lockhttp.SourcesPath(data); !isRegular(p) {
		if _, err := lockhttp.WriteJSON(p, lockhttp.Build(key, nil, nil)); err != nil {
			return nil, "", die("LEA_LOCKHTTP=1: %v", err)
		}
	}
	a := []string{"-e", "LEA_LOCKHTTP=1", "-e", "LEA_KEYPROXY_TOKEN=" + lockhttp.KeyproxyToken(key),
		"--mount", "type=bind,src=" + gate + ",dst=/etc/lockhttp-gate,readonly"}
	if l.Cfg.LockHTTPMITM() {
		ca, err := l.lockHTTPCA()
		if err != nil {
			return nil, "", err
		}
		// The certificate, never the key, next to the gate's inputs: the gate and keyproxy trust it.
		b, err := os.ReadFile(ca + "/ca.pem")
		if err != nil {
			return nil, "", die("LEA_LOCKHTTP_MITM=1: %v", err)
		}
		p := gate + "/ca.pem"
		if err := WriteFile(data, p, b); err != nil {
			return nil, "", die("%v", err)
		}
		if err := os.Chmod(p, 0o644); err != nil {
			return nil, "", die("%v", err)
		}
		a = append(a, "-e", "LEA_LOCKHTTP_MITM=1")
	}
	return a, image, nil
}

func (l *Launcher) lockHTTPGateName() string   { return l.prefix() + "-lockhttp-gate" }
func (l *Launcher) lockHTTPSignerName() string { return l.prefix() + "-lockhttp-signer" }

// lockHTTPConfine is the confinement lockhttp's launcher gives its own containers.
var lockHTTPConfine = []string{"--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--read-only",
	"--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=64m", "--pids-limit", "256", "--memory", "1g"}

// ensureLockHTTPGate runs the gate container beside the proxy (docs/lea.md, §2.1), and under MITM
// the signer (§3), unless fresh is false and they are current: a running gate of image (and CA)
// started after the proxy, and a running signer of the same. A gate older than the proxy is in a
// network namespace the proxy left. The gate asks hub (lox run's lockhttp lock, lockhubgate.go),
// and only the proxy joins the hub's network.
func (l *Launcher) ensureLockHTTPGate(fresh bool, image string, hub *gateHub, proj string) error {
	proxy, name, signer := l.prefix()+"-proxy", l.lockHTTPGateName(), l.lockHTTPSignerName()
	mitm, stamp, hubStamp := l.Cfg.LockHTTPMITM(), l.lockHTTPCAStamp(), hub.stamp()
	want, names := image, []string{proxy, name}
	if mitm {
		want += " ca:" + stamp
		names = append(names, signer)
	}
	want += " hub:" + hubStamp
	// The proxy leaves any other hub network first; it joins hub's only once the gate in its
	// namespace refuses that network's addresses: a current gate (its stamp names them), or after
	// the old gate is gone, before the new one starts.
	if err := l.joinHubNetwork(proxy, hub, false); err != nil {
		return err
	}
	if !fresh {
		out, err := l.output(false, "docker", append([]string{"inspect", "--type", "container", "-f",
			`{{.State.Running}}|{{.State.StartedAt}}|{{index .Config.Labels "lea.lockhttp-image"}}{{with index .Config.Labels "lea.lockhttp-ca"}} ca:{{.}}{{end}}{{with index .Config.Labels "lea.lockhttp-hub"}} hub:{{.}}{{end}}`},
			names...)...)
		if err == nil && lockHTTPGateCurrent(out, want, len(names)) {
			// Its inputs as its stamp says they are.
			if _, err := l.writeGateHub(hub); err != nil {
				return die("LEA_LOCKHTTP=1: %v", err)
			}
			return l.joinHubNetwork(proxy, hub, true)
		}
	}
	if err := l.quiet("docker", "rm", "-f", name, signer); err != nil {
		return die("LEA_LOCKHTTP=1: could not remove the gate %s before it asks the hub", name)
	}
	// The new gate's inputs, once the old one, which read the same directory, is gone.
	hubDir, err := l.writeGateHub(hub)
	if err != nil {
		return die("LEA_LOCKHTTP=1: %v", err)
	}
	if err := l.joinHubNetwork(proxy, hub, true); err != nil {
		return err
	}
	data := l.Cfg.DataDir()
	labels := []string{"--label", "lea.prefix=" + l.prefix(), "--label", "lea.lockhttp-image=" + image,
		"--label", "lea.lockhttp-hub=" + hubStamp, "--label", "lea.lockhttp-for=" + proj}
	var gateMITM, gateCA []string
	if mitm {
		labels = append(labels, "--label", "lea.lockhttp-ca="+stamp)
		if err := l.lockHTTPSigner(image, labels); err != nil {
			return err
		}
		gateMITM = []string{"--mount", "type=volume,src=" + l.prefix() + "-lockhttp-signer,dst=/run/lockhttp-signer"}
		gateCA = []string{"--ca-cert", "/etc/lockhttp-gate/ca.pem", "--signer", "/run/lockhttp-signer/signer.sock"}
	}
	a := []string{"run", "-d", "--name", name, "--restart", "unless-stopped",
		"--network", "container:" + proxy, "--user", "gate", "--label", "lea.role=lockhttp-gate"}
	// The console's socket (lox run's): the gate's container only, never an agent's.
	gateMITM = append(gateMITM, "--mount", "type=volume,src="+hub.home+",dst=/lockhttp-home",
		"--mount", "type=bind,src="+hubDir+",dst="+gateHubDir+",readonly")
	a = append(append(append(a, labels...), lockHTTPConfine...),
		"--mount", "type=volume,src="+hub.socket+",dst=/run/lockhttp",
		"--mount", "type=bind,src="+lockhttp.GateDir(data)+",dst=/etc/lockhttp-gate,readonly",
		// The audit stays in the engine's volume, the gate's own (0700 in the image).
		"--mount", "type=volume,src="+l.prefix()+"-lockhttp-audit,dst=/var/log/lockhttp",
		"--mount", "type=bind,src="+lockhttp.StatusDir(data)+",dst=/run/lockhttp-gate")
	a = append(append(a, gateMITM...),
		image, "gate", "--listen", "0.0.0.0:"+lockhttp.Port, "--upstream", "http://127.0.0.1:3128",
		"--profile", hub.profile, "--sources", "/etc/lockhttp-gate/sources.json",
		// The daemon's credentials.json (§2.4): a Claude API key not of the calling container is denied.
		"--credentials", "/etc/lockhttp-gate/credentials.json",
		"--audit-dir", "/var/log/lockhttp", "--status-file", "/run/lockhttp-gate/status.json")
	return l.run("docker", append(append(a, gateCA...), hub.args()...)...)
}

// lockHTTPSigner hands the CA key to the signer's volume and runs the signer (docs/lea.md, §3): a
// one-off root container of the image copies the key, owned by signer (10004) and 0600, and the
// certificate into <prefix>-lockhttp-ca, which only the signer's container mounts; the signer,
// without a network, serves leaf certificates to the gate's uid on a socket in
// <prefix>-lockhttp-signer, a volume of the image's 2750 signer:lockhttp-clients directory.
func (l *Launcher) lockHTTPSigner(image string, labels []string) error {
	ca, vol := lockhttp.CADir(l.Cfg.DataDir()), l.prefix()+"-lockhttp-ca"
	if err := l.run("docker", "run", "--rm", "--user", "0", "--entrypoint", "sh", "--network", "none",
		"--mount", "type=bind,src="+ca+",dst=/src,readonly", "--mount", "type=volume,src="+vol+",dst=/ca",
		image, "-c", "install -m 0600 -o signer /src/ca-key.pem /ca/ca-key.pem && install -m 0644 /src/ca.pem /ca/ca.pem"); err != nil {
		return die("LEA_LOCKHTTP_MITM=1: could not hand the CA key to the signer's volume %s", vol)
	}
	a := []string{"run", "-d", "--name", l.lockHTTPSignerName(), "--restart", "unless-stopped",
		"--network", "none", "--user", "signer", "--label", "lea.role=lockhttp-signer"}
	a = append(append(append(a, labels...), lockHTTPConfine...),
		"--mount", "type=volume,src="+vol+",dst=/etc/lockhttp-ca,readonly",
		"--mount", "type=volume,src="+l.prefix()+"-lockhttp-signer,dst=/run/lockhttp-signer",
		image, "signer", "--ca-cert", "/etc/lockhttp-ca/ca.pem", "--ca-key", "/etc/lockhttp-ca/ca-key.pem",
		"--listen", "/run/lockhttp-signer/signer.sock", "--allow-uid", "10003")
	return l.run("docker", a...)
}

// lockHTTPGateCurrent reads the "<running>|<started>|<labels>" lines of the proxy, the gate and,
// under MITM, the signer (n lines): the gate and the signer run with labels want, the gate
// started after the proxy.
func lockHTTPGateCurrent(out, want string, n int) bool {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != n {
		return false
	}
	var f [][]string
	for _, l := range lines {
		x := strings.SplitN(l, "|", 3)
		if len(x) != 3 || x[0] != "true" {
			return false
		}
		f = append(f, x)
	}
	for _, x := range f[1:] {
		if x[2] != want {
			return false
		}
	}
	ps, err1 := parseStarted(f[0][1])
	gs, err2 := parseStarted(f[1][1])
	return err1 == nil && err2 == nil && !gs.Before(ps)
}

// parseStarted reads a .State.StartedAt: RFC 3339 from Docker, Go's time format from Podman.
func parseStarted(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02 15:04:05.999999999 -0700 MST", s)
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

// lockHTTPCA is the MITM CA's directory, made on first use through lockhttp's launcher (lockhttp
// ca init: P-256, 90 days, files owned by the user). lea never rotates it by itself: lea doctor
// warns a week before it ends (lockhttp ca init --force --dir <it> rotates it; then lea stop
// --all, and lea).
func (l *Launcher) lockHTTPCA() (string, error) {
	dir := lockhttp.CADir(l.Cfg.DataDir())
	if err := noComma(dir); err != nil {
		return "", err
	}
	if !isRegular(dir+"/ca.pem") || !isRegular(dir+"/ca-key.pem") {
		if err := Mkdirs(l.Cfg.DataDir(), lockhttp.Dir(l.Cfg.DataDir())); err != nil {
			return "", die("%v", err)
		}
		if err := l.run(l.lockHTTPLauncher(), "ca", "init", "--dir", dir); err != nil {
			return "", die("LEA_LOCKHTTP_MITM=1: lockhttp ca init --dir %s failed", dir)
		}
	}
	if !isRegular(dir+"/ca.pem") || !isRegular(dir+"/ca-key.pem") {
		return "", die("LEA_LOCKHTTP_MITM=1: no ca.pem and ca-key.pem in %s", dir)
	}
	return dir, nil
}

// lockHTTPCAStamp is a digest of the MITM CA's certificate (12 hex digits), "" without MITM.
func (l *Launcher) lockHTTPCAStamp() string {
	if !l.Cfg.LockHTTPMITM() {
		return ""
	}
	b, _ := os.ReadFile(lockhttp.CADir(l.Cfg.DataDir()) + "/ca.pem")
	return lockhttp.Hash(string(b))[:12]
}

// lockHTTPStamp is the proxy's lea.lockhttp label: "1", or under MITM "mitm-" and the CA's stamp,
// so that a new CA restarts the proxy (keyproxy reads the certificate at start).
func (l *Launcher) lockHTTPStamp() string {
	if !l.Cfg.LockHTTPMITM() {
		return "1"
	}
	return "mitm-" + l.lockHTTPCAStamp()
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

// lockHostRoot and lockHostOwner: the host's root directory and the uid that must own its system
// files (tests only change them).
var (
	lockHostRoot  = ""
	lockHostOwner = 0
)

// rootOwned is why path is not a root-owned file (dir: directory) that only root may write, or "".
// An agent able to edit the system file could point the client at a console of its own.
func rootOwned(path string, dir bool) string {
	fi, err := os.Lstat(path)
	if err != nil {
		return path + " is absent"
	}
	st, _ := fi.Sys().(*syscall.Stat_t)
	if fi.IsDir() != dir || (!dir && !fi.Mode().IsRegular()) || st == nil || int(st.Uid) != lockHostOwner || fi.Mode().Perm()&0o022 != 0 {
		return path + " is not owned and writable by root only"
	}
	return ""
}

// gateSwitchRefused is an error when the gate name asks for another projects directory's hub
// than gateFor (or none: a gate of an earlier lea, gateNoHub) and lea agents of a directory other
// than proj run: their
// requests would be approved elsewhere. Only a gate that does not exist switches nothing; one lea
// cannot read, or that asks a hub without saying whose, is refused the same way.
func (l *Launcher) gateSwitchRefused(name, gateFor, proj string) error {
	ex, err := l.output(false, "docker", "ps", "-a", "--filter", "name=^/"+name+"$", "--format", "{{.Names}}")
	if err != nil {
		return die("LEA_LOCKHTTP=1: cannot tell whether the gate %s exists", name)
	}
	if strings.TrimSpace(ex) == "" {
		return nil
	}
	o, err := l.output(false, "docker", "inspect", "--type", "container", "-f",
		`for={{index .Config.Labels "lea.lockhttp-for"}}|hub={{index .Config.Labels "lea.lockhttp-hub"}}`, name)
	f, h, ok := strings.Cut(strings.TrimSpace(o), "|")
	old, ok1 := strings.CutPrefix(f, "for=")
	hub, ok2 := strings.CutPrefix(h, "hub=")
	switch {
	case err != nil || !ok || !ok1 || !ok2 || strings.ContainsAny(o, "\n"):
		old = "?" // unreadable: its hub is unknown
	case old == "" && hub == "":
		old = gateNoHub // a gate of an earlier lea that asked a console of its own
	case old == "":
		old = "?" // a hub, but whose is unknown
	}
	if old == gateFor {
		return nil
	}
	ps, err := l.output(false, "docker", "ps", "--filter", "label=lea.prefix="+l.prefix(), "--filter", "label=lea.role=session",
		"--format", `{{.Label "lea.projects"}}`)
	if err != nil {
		return die("LEA_LOCKHTTP=1: cannot list lea's running containers before the gate changes hub")
	}
	for _, p := range strings.Split(ps, "\n") {
		if p = strings.TrimSpace(p); p != "" && p != proj {
			return die("LEA_LOCKHTTP=1: the LockHTTP gate serves every projects directory, and the agents of %s run: it would ask the hub of %s for them. Stop them first (lea stop in %s), or open that directory", p, gateFor, p)
		}
	}
	// A start in another projects directory holds that directory's lock until its container
	// runs: its agents come.
	locks, err := os.ReadDir(l.Cfg.HostDir() + "/locks")
	if err != nil && !os.IsNotExist(err) {
		return die("LEA_LOCKHTTP=1: %v", err)
	}
	for _, e := range locks {
		if lockSID.MatchString(e.Name()) && e.Name() != ID(proj) {
			return die("LEA_LOCKHTTP=1: the LockHTTP gate serves every projects directory, and lea is starting in another one (%s/locks/%s): it would change how its agents are approved. Retry once it runs", l.Cfg.HostDir(), e.Name())
		}
	}
	return nil
}

var lockSID = regexp.MustCompile(`^[0-9a-f]{10}$`)

// gateNoHub is the lea.lockhttp-for of a gate of an earlier lea that asked a console of its own
// (the gate is hub-attached only now): one still running is switched like another hub's.
const gateNoHub = "-"
