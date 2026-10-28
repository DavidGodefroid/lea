package launcher

import (
	"net"
	"os"
	"strings"
	"testing"

	"github.com/DavidGodefroid/lea/internal/lockhttp"
)

// testDigest is the image digest the fake launcher pins.
const testDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// fakeLockHTTPLauncher is the head of lockhttp's launcher (scripts/lockhttp) as install.sh writes it.
func fakeLockHTTPLauncher(version, digest string) string {
	return "#!/bin/sh\n# lockhttp: the launcher.\nset -eu\n\nlauncher_version=" + version + "\nimage_digest=" + digest +
		"\n\nrepo=\"DavidGodefroid/lockhttp\"\nregistry=\"ghcr.io/davidgodefroid/lockhttp\"\n"
}

// lockHTTPWorld adds what lockhttp's install.sh leaves to lockHostWorld: its launcher, root-owned
// in /usr/local/bin, pinning testDigest.
func lockHTTPWorld(t *testing.T) (*world, string) {
	t.Helper()
	root := lockHostWorld(t)
	if err := os.WriteFile(root+"/usr/local/bin/lockhttp", []byte(fakeLockHTTPLauncher("0.2.0", testDigest)), 0o755); err != nil {
		t.Fatal(err)
	}
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_LOCKHTTP=1\n")
	return w, root
}

// gateRuns are the docker run calls of the gate container.
func gateRuns(w *world) []string {
	var r []string
	for _, c := range w.calls() {
		if strings.HasPrefix(c, "docker [run]") && strings.Contains(c, "[lea-lockhttp-gate]") {
			r = append(r, c)
		}
	}
	return r
}

func TestLockHTTPOffByDefault(t *testing.T) {
	lockHTTPWorld(t)
	w := newWorld(t, repoRoot(t))
	args := lockArgs(t, w)
	if v, _ := argAfter(args, "-e"); strings.Contains(strings.Join(args, " "), "LOCKHTTP") || strings.Contains(strings.Join(args, " "), ":3130") {
		t.Fatalf("LockHTTP off leaves a trace: %v (%s)", args, v)
	}
	if err := w.launcher().ensureProxy(w.proj); err != nil {
		t.Fatal(err)
	}
	if w.hasCall("lockhttp") {
		t.Fatalf("the proxy got LockHTTP:\n%s", strings.Join(w.calls(), "\n"))
	}
}

// The proxy keeps squid and keyproxy; the gate runs beside it, from the launcher's pinned image,
// in the proxy's network namespace, as gate, with the console's socket volume and nothing else
// of the console's.
func TestLockHTTPProxyGetsTheGate(t *testing.T) {
	w, _ := lockHTTPWorld(t)
	if err := w.launcher().ensureProxy(w.proj); err != nil {
		t.Fatal(err)
	}
	data := w.launcher().Cfg.DataDir()
	key, err := lockhttp.Key(data)
	if err != nil {
		t.Fatal(err)
	}
	var proxy string
	for _, c := range w.calls() {
		if strings.HasPrefix(c, "docker [run]") && strings.Contains(c, "[lea-proxy]") {
			proxy = c
		}
	}
	for _, want := range []string{
		"[--label] [lea.lockhttp=1]",
		"[LEA_LOCKHTTP=1]",
		"[LEA_KEYPROXY_TOKEN=" + lockhttp.KeyproxyToken(key) + "]",
		"[type=bind,src=" + lockhttp.GateDir(data) + ",dst=/etc/lockhttp-gate,readonly]",
	} {
		if !strings.Contains(proxy, want) {
			t.Errorf("proxy run lacks %s:\n%s", want, proxy)
		}
	}
	for _, no := range []string{"/usr/local/bin/lockhttp", "/run/lockhttp", "--group-add", "LEA_LOCKHTTP_GID", "/var/log/lockhttp"} {
		if strings.Contains(proxy, no) {
			t.Errorf("proxy run has %s:\n%s", no, proxy)
		}
	}
	runs := gateRuns(w)
	if len(runs) != 1 {
		t.Fatalf("gate runs: %d\n%s", len(runs), strings.Join(w.calls(), "\n"))
	}
	g := runs[0]
	image := "ghcr.io/davidgodefroid/lockhttp@" + testDigest
	for _, want := range []string{
		"[--network] [container:lea-proxy]", "[--user] [gate]", "[--restart] [unless-stopped]",
		"[--label] [lea.prefix=lea]", "[--label] [lea.role=lockhttp-gate]", "[--label] [lea.lockhttp-image=" + image + "]",
		"[--cap-drop] [ALL]", "[--security-opt] [no-new-privileges]", "[--read-only]",
		"[type=volume,src=lockhttp-run,dst=/run/lockhttp]",
		"[type=bind,src=" + lockhttp.GateDir(data) + ",dst=/etc/lockhttp-gate,readonly]",
		"[type=volume,src=lea-lockhttp-audit,dst=/var/log/lockhttp]",
		"[type=bind,src=" + lockhttp.StatusDir(data) + ",dst=/run/lockhttp-gate]",
		"[" + image + "] [gate] [--listen] [0.0.0.0:3130] [--upstream] [http://127.0.0.1:3128] [--profile] [default] " +
			"[--sources] [/etc/lockhttp-gate/sources.json] [--audit-dir] [/var/log/lockhttp] [--status-file] [/run/lockhttp-gate/status.json]",
	} {
		if !strings.Contains(g, want) {
			t.Errorf("gate run lacks %s:\n%s", want, g)
		}
	}
	for _, no := range []string{"lockhttp-home", "docker.sock", "-p", "--publish"} {
		if strings.Contains(g, "["+no) {
			t.Errorf("gate run has %s:\n%s", no, g)
		}
	}
	// The gate (uid 10003) writes its status where lea reads it.
	if fi, err := os.Stat(lockhttp.StatusDir(data)); err != nil || fi.Mode().Perm() != 0o777 {
		t.Errorf("status dir: %v %v", fi, err)
	}
}

// The gate exits on a missing sources.json: the launcher writes one naming nobody before the
// daemon's first, and never replaces the daemon's.
func TestLockHTTPGateStartsWithSources(t *testing.T) {
	w, _ := lockHTTPWorld(t)
	data := w.launcher().Cfg.DataDir()
	if err := w.launcher().ensureProxy(w.proj); err != nil {
		t.Fatal(err)
	}
	p := lockhttp.SourcesPath(data)
	b, err := os.ReadFile(p)
	if err != nil || !strings.Contains(string(b), `"keyproxy"`) || strings.Contains(string(b), `"local_ip"`) {
		t.Fatalf("sources.json: %s %v", b, err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o644 {
		t.Errorf("sources.json mode %v", fi.Mode())
	}
	key, _ := lockhttp.Key(data)
	lockhttp.WriteJSON(p, lockhttp.Build(key, []lockhttp.Network{{LocalIP: "10.89.1.2", Container: "lea-x"}}, nil))
	if err := w.launcher().ensureProxy(w.proj); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), "10.89.1.2") {
		t.Fatalf("the daemon's sources.json was replaced: %s", b)
	}
}

// The gate lives in the proxy's network namespace: a proxy started again (another network
// namespace) or another pinned image replaces it; a running gate of this image, started after the
// proxy, is kept.
func TestLockHTTPGateFollowsTheProxy(t *testing.T) {
	image := "ghcr.io/davidgodefroid/lockhttp@" + testDigest
	for name, c := range map[string]struct {
		inspect string
		rc      string
		replace bool
	}{
		"current":          {"true|2026-10-28T10:00:00.5Z|\ntrue|2026-10-28T10:00:01Z|" + image, "", false},
		"podman times":     {"true|2026-10-28 10:00:00.5 +0000 UTC|\ntrue|2026-10-28 10:00:01 +0000 UTC|" + image, "", false},
		"older than proxy": {"true|2026-10-28T10:00:02Z|\ntrue|2026-10-28T10:00:01Z|" + image, "", true},
		"other image":      {"true|2026-10-28T10:00:00Z|\ntrue|2026-10-28T10:00:01Z|ghcr.io/davidgodefroid/lockhttp@sha256:ff", "", true},
		"stopped":          {"true|2026-10-28T10:00:00Z|\nfalse|2026-10-28T10:00:01Z|" + image, "", true},
		"absent":           {"true|2026-10-28T10:00:00Z|", "1", true},
		"unreadable time":  {"true|soon|\ntrue|later|" + image, "", true},
	} {
		w, _ := lockHTTPWorld(t)
		// The proxy runs with the gate's label: kept.
		w.env["FAKE_DOCKER_OUT_INSPECT__F"] = "true"
		w.env["FAKE_DOCKER_OUT_INSPECT___FORMAT"] = "@" + w.launcher().proxyRules() + "^1"
		w.env["FAKE_DOCKER_OUT_INSPECT___TYPE"] = c.inspect
		if c.rc != "" {
			w.env["FAKE_DOCKER_RC_INSPECT___TYPE"] = c.rc
		}
		if err := w.launcher().ensureProxy(w.proj); err != nil {
			t.Fatal(err)
		}
		if w.hasCall("[lea-proxy] [--network]") || w.hasCall("[--name] [lea-proxy]") {
			t.Errorf("%s: the proxy was replaced", name)
		}
		if got := len(gateRuns(w)) == 1; got != c.replace {
			t.Errorf("%s: gate replaced = %v:\n%s", name, got, strings.Join(w.calls(), "\n"))
		}
		if c.replace && !w.hasCall("docker [rm] [-f] [lea-lockhttp-gate]") {
			t.Errorf("%s: the old gate was not removed", name)
		}
	}
}

// A proxy started again always gets a new gate: the old one is in the old network namespace.
func TestLockHTTPNewProxyNewGate(t *testing.T) {
	w, _ := lockHTTPWorld(t)
	w.env["FAKE_DOCKER_OUT_INSPECT__F"] = "true"
	w.env["FAKE_DOCKER_OUT_INSPECT___FORMAT"] = "@" + w.launcher().proxyRules()
	w.env["FAKE_DOCKER_OUT_INSPECT___TYPE"] = "true|2026-10-28T10:00:00Z|\ntrue|2026-10-28T10:00:01Z|ghcr.io/davidgodefroid/lockhttp@" + testDigest
	if err := w.launcher().ensureProxy(w.proj); err != nil {
		t.Fatal(err)
	}
	if len(gateRuns(w)) != 1 || !w.hasCall("docker [rm] [-f] [lea-lockhttp-gate]") {
		t.Fatalf("a new proxy kept the old gate:\n%s", strings.Join(w.calls(), "\n"))
	}
}

// LockHTTP turned off: the proxy restarts without the gate, and the gate goes.
func TestLockHTTPOffRemovesTheGate(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["FAKE_DOCKER_OUT_INSPECT__F"] = "true"
	w.env["FAKE_DOCKER_OUT_INSPECT___FORMAT"] = "@" + w.launcher().proxyRules() + "^1"
	if err := w.launcher().ensureProxy(w.proj); err != nil {
		t.Fatal(err)
	}
	if !w.hasCall("docker [rm] [-f] [lea-lockhttp-gate]") || len(gateRuns(w)) != 0 {
		t.Fatalf("LockHTTP off:\n%s", strings.Join(w.calls(), "\n"))
	}
}

// A launcher run from a clone runs the local image lockhttp, as the launcher itself does.
func TestLockHTTPDevLauncherRunsTheLocalImage(t *testing.T) {
	w, root := lockHTTPWorld(t)
	if err := os.WriteFile(root+"/usr/local/bin/lockhttp", []byte(fakeLockHTTPLauncher("dev", "")), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := w.launcher().ensureProxy(w.proj); err != nil {
		t.Fatal(err)
	}
	if r := gateRuns(w); len(r) != 1 || !strings.Contains(r[0], "[lea.lockhttp-image=lockhttp] ") || !strings.Contains(r[0], " [lockhttp] [gate] ") {
		t.Fatalf("dev launcher:\n%s", strings.Join(r, "\n"))
	}
}

func TestLockHTTPRefusesAnUnsetHost(t *testing.T) {
	for what, undo := range map[string]func(root string){
		"is absent":        func(r string) { os.Remove(r + "/usr/local/bin/lockhttp") },
		"writable by root": func(r string) { os.Chmod(r+"/usr/local/bin/lockhttp", 0o777) },
		"no image digest": func(r string) {
			os.WriteFile(r+"/usr/local/bin/lockhttp", []byte(fakeLockHTTPLauncher("0.2.0", "")), 0o755)
		},
		"not lockhttp's launcher": func(r string) {
			os.WriteFile(r+"/usr/local/bin/lockhttp", []byte("\x7fELF\x02\x01\x01 a v0.1.0 host binary"), 0o755)
		},
		"image digest": func(r string) {
			os.WriteFile(r+"/usr/local/bin/lockhttp", []byte(fakeLockHTTPLauncher("0.2.0", "sha256:$(id)")), 0o755)
		},
	} {
		w, root := lockHTTPWorld(t)
		undo(root)
		err := w.launcher().ensureProxy(w.proj)
		if err == nil || !strings.Contains(err.Error(), strings.TrimSpace(what)) || !strings.Contains(err.Error(), "LEA_LOCKHTTP=1") {
			t.Errorf("%s: err = %v", what, err)
		}
		if w.hasCall("docker [run]") {
			t.Errorf("%s: a container was started", what)
		}
	}
}

func TestLockHTTPAgentsGoThroughTheGate(t *testing.T) {
	w, _ := lockHTTPWorld(t)
	args := lockArgs(t, w)
	key, err := lockhttp.Key(w.launcher().Cfg.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	want := "http://shell:" + lockhttp.Token(key, w.name(), "shell") + "@172.18.0.5:3130"
	joined := strings.Join(args, "\n") + "\n"
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		if !strings.Contains(joined, "\n"+k+"="+want+"\n") {
			t.Errorf("%s is not %s", k, want)
		}
	}
	for _, e := range []string{"LEA_LOCKHTTP=1", "LEA_NESTED_PROXY_TOKEN=" + lockhttp.Token(key, w.name(), "nested"),
		"NO_PROXY=localhost,127.0.0.1,::1,lea-proxy,172.18.0.5"} {
		if !strings.Contains(joined, "\n"+e+"\n") {
			t.Errorf("run args lack %s", e)
		}
	}
	if strings.Contains(joined, "/run/lockhttp") {
		t.Error("an agent container got the console's socket directory")
	}
}

// An address the proxy had on a removed network never names the next container that gets it.
func TestLockHTTPForgetsReleasedNetworks(t *testing.T) {
	w, _ := lockHTTPWorld(t)
	data := w.launcher().Cfg.DataDir()
	key, err := lockhttp.Key(data)
	if err != nil {
		t.Fatal(err)
	}
	p := lockhttp.SourcesPath(data)
	lockhttp.WriteJSON(p, lockhttp.Build(key, []lockhttp.Network{{LocalIP: "10.89.1.2", Container: w.name()},
		{LocalIP: "10.89.2.2", Container: "lea-other"}, {LocalIP: "10.89.3.2", Container: "lea-gone"}}, nil))
	w.launcher().releaseNetwork(w.id())
	w.env["FAKE_DOCKER_OUT_CONTAINER_INSPECT"] = "10.89.3.2"
	if _, err := w.launcher().proxyAttach(w.id()); err != nil {
		t.Fatal(err)
	}
	s := lockhttp.NewSourcer(p)
	for ip, want := range map[string]string{"10.89.1.2": "", "10.89.2.2": "lea-other", "10.89.3.2": ""} {
		if c, _ := s.Source(net.ParseIP(ip)); c != want {
			t.Errorf("%s names %q, want %q", ip, c, want)
		}
	}
}
