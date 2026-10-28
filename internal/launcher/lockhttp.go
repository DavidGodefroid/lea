package launcher

import (
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/DavidGodefroid/lea/internal/lockhttp"
)

// LockHTTP (LEA_LOCKHTTP=1): a LockHTTP gate beside the proxy container decides every web request
// of the agents, asking the human in a lockhttp console (contract: lockhttp's docs/lea.md). The
// gate is a sidecar container in the proxy's network namespace, from the image lockhttp's
// launcher pins; it alone gets the console's socket volume. The agents get a proxy URL with their
// own token (internal/lockhttp), and the daemon tells the gate who holds which (sources.json).

// lockHTTPLauncher is lockhttp's launcher on the host (scripts/install.sh puts it there, root-owned):
// lea runs the image it pins.
func lockHTTPLauncher() string { return lockHostRoot + "/usr/local/bin/lockhttp" }

// lockHTTPRegistry is where lockhttp's release images are; the launcher pins one by digest.
const lockHTTPRegistry = "ghcr.io/davidgodefroid/lockhttp"

var (
	lockHTTPVersionLine = regexp.MustCompile(`(?m)^launcher_version=([0-9A-Za-z.+-]+)$`)
	lockHTTPDigestLine  = regexp.MustCompile(`(?m)^image_digest=(.*)$`)
	lockHTTPDigest      = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// lockHTTPImage is the gate's image: the one the launcher runs, read from its launcher_version
// and image_digest lines (docs/docker.md, "Version pinning"): a release by digest, never by a tag;
// the local image lockhttp for a launcher run from a clone (launcher_version=dev). An error when
// the launcher is missing, writable by others than root, or pins no digest.
func lockHTTPImage() (string, error) {
	p := lockHTTPLauncher()
	if why := rootOwned(p, false); why != "" {
		return "", die("LEA_LOCKHTTP=1: %s (install lockhttp's launcher: scripts/install.sh, lockhttp's docs/docker.md)", why)
	}
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
		return "", die("LEA_LOCKHTTP=1: %s is not lockhttp's launcher (a v0.1.0 host install?): install the launcher (lockhttp's docs/docker.md)", p)
	}
	if string(v[1]) == "dev" {
		return "lockhttp", nil
	}
	d := lockHTTPDigestLine.FindSubmatch(head)
	if d == nil || len(d[1]) == 0 {
		return "", die("LEA_LOCKHTTP=1: %s pins no image digest (installed with LOCKHTTP_SKIP_COSIGN=1?): install it again with cosign", p)
	}
	if !lockHTTPDigest.Match(d[1]) {
		return "", die("LEA_LOCKHTTP=1: %s: invalid image digest '%s'", p, d[1])
	}
	return lockHTTPRegistry + "@" + string(d[1]), nil
}

// lockHTTPProxyArgs are the docker run arguments of the proxy container under LockHTTP (squid on
// the loopback, keyproxy through the gate, sources.json to name the calling container), and the
// gate's image; an error when the host is not set up for it: the user asked for LockHTTP, so
// lea never starts the agents without it.
func (l *Launcher) lockHTTPProxyArgs() ([]string, string, error) {
	image, err := lockHTTPImage()
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
	return []string{"-e", "LEA_LOCKHTTP=1", "-e", "LEA_KEYPROXY_TOKEN=" + lockhttp.KeyproxyToken(key),
		"--mount", "type=bind,src=" + gate + ",dst=/etc/lockhttp-gate,readonly"}, image, nil
}

func (l *Launcher) lockHTTPGateName() string { return l.prefix() + "-lockhttp-gate" }

// ensureLockHTTPGate runs the gate container beside the proxy (docs/lea.md, §2.1), unless fresh
// is false and a running gate of image was started after the proxy: a gate older than the proxy
// is in a network namespace the proxy left.
func (l *Launcher) ensureLockHTTPGate(fresh bool, image string) error {
	proxy, name := l.prefix()+"-proxy", l.lockHTTPGateName()
	if !fresh {
		out, err := l.output(false, "docker", "inspect", "--type", "container", "-f",
			`{{.State.Running}}|{{.State.StartedAt}}|{{index .Config.Labels "lea.lockhttp-image"}}`, proxy, name)
		if err == nil && lockHTTPGateCurrent(out, image) {
			return nil
		}
	}
	_ = l.quiet("docker", "rm", "-f", name)
	data := l.Cfg.DataDir()
	return l.run("docker", "run", "-d", "--name", name, "--restart", "unless-stopped",
		"--network", "container:"+proxy, "--user", "gate",
		"--label", "lea.prefix="+l.prefix(), "--label", "lea.role=lockhttp-gate", "--label", "lea.lockhttp-image="+image,
		// The confinement lockhttp's launcher gives its own containers.
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--read-only",
		"--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=64m", "--pids-limit", "256", "--memory", "1g",
		// The console's socket: the gate's container only, never an agent's.
		"--mount", "type=volume,src=lockhttp-run,dst=/run/lockhttp",
		"--mount", "type=bind,src="+lockhttp.GateDir(data)+",dst=/etc/lockhttp-gate,readonly",
		// The audit stays in the engine's volume, the gate's own (0700 in the image).
		"--mount", "type=volume,src="+l.prefix()+"-lockhttp-audit,dst=/var/log/lockhttp",
		"--mount", "type=bind,src="+lockhttp.StatusDir(data)+",dst=/run/lockhttp-gate",
		image, "gate", "--listen", "0.0.0.0:"+lockhttp.Port, "--upstream", "http://127.0.0.1:3128",
		"--profile", "default", "--sources", "/etc/lockhttp-gate/sources.json",
		"--audit-dir", "/var/log/lockhttp", "--status-file", "/run/lockhttp-gate/status.json")
}

// lockHTTPGateCurrent reads the proxy's and the gate's "<running>|<started>|<image label>" lines.
func lockHTTPGateCurrent(out, image string) bool {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		return false
	}
	p, g := strings.SplitN(lines[0], "|", 3), strings.SplitN(lines[1], "|", 3)
	if len(p) != 3 || len(g) != 3 || p[0] != "true" || g[0] != "true" || g[2] != image {
		return false
	}
	ps, err1 := parseStarted(p[1])
	gs, err2 := parseStarted(g[1])
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
