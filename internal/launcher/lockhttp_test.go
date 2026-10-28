package launcher

import (
	"net"
	"os"
	"strings"
	"testing"

	"github.com/DavidGodefroid/lea/internal/lockhttp"
)

// lockHTTPWorld adds what sudo lockhttp install leaves to lockHostWorld: a root-owned
// /usr/local/bin/lockhttp, /run/lockhttp and the lockhttp-clients group (gid 4300) with the user.
func lockHTTPWorld(t *testing.T) (*world, string) {
	t.Helper()
	root := lockHostWorld(t)
	// ca init --dir D makes a CA, as the real one does.
	ca := "#!/bin/sh\nif [ \"$1 $2 $3\" = \"ca init --dir\" ]; then mkdir -p \"$4\" && echo cert >\"$4/ca.pem\" && echo key >\"$4/ca-key.pem\"; fi\n"
	if err := os.WriteFile(root+"/usr/local/bin/lockhttp", []byte(ca), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root+"/run/lockhttp", 0o2710); err != nil {
		t.Fatal(err)
	}
	w := newWorld(t, repoRoot(t))
	g, _ := os.ReadFile(root + "/etc/group")
	user := w.launcher().userName()
	if err := os.WriteFile(root+"/etc/group", append(g, []byte("lockhttp-clients:x:4300:"+user+"\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	writeUserConf(t, w, "LEA_LOCKHTTP=1\n")
	return w, root
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

func TestLockHTTPProxyGetsTheGate(t *testing.T) {
	w, root := lockHTTPWorld(t)
	if err := w.launcher().ensureProxy(w.proj); err != nil {
		t.Fatal(err)
	}
	data := w.launcher().Cfg.DataDir()
	key, err := lockhttp.Key(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"[--label] [lea.lockhttp=1]",
		"[LEA_LOCKHTTP=1]",
		"[LEA_KEYPROXY_TOKEN=" + lockhttp.KeyproxyToken(key) + "]",
		"[type=bind,src=" + root + "/usr/local/bin/lockhttp,dst=/usr/local/bin/lockhttp,readonly]",
		"[type=bind,src=" + root + "/run/lockhttp,dst=/run/lockhttp]",
		"[type=bind,src=" + lockhttp.GateDir(data) + ",dst=/etc/lockhttp-gate,readonly]",
		"[type=bind,src=" + lockhttp.AuditDir(data) + ",dst=/var/log/lockhttp]",
		"[type=bind,src=" + lockhttp.StatusDir(data) + ",dst=/run/lockhttp-gate]",
		"[--group-add] [4300]", "[LEA_LOCKHTTP_GID=4300]",
	} {
		if !w.hasCall(want) {
			t.Errorf("proxy run lacks %s:\n%s", want, strings.Join(w.calls(), "\n"))
		}
	}
	// A running proxy without the gate is replaced; one with it is kept.
	w.env["FAKE_DOCKER_OUT_INSPECT__F"] = "true"
	w.env["FAKE_DOCKER_OUT_INSPECT___FORMAT"] = "@" + w.launcher().proxyRules()
	before := len(w.calls())
	if err := w.launcher().ensureProxy(w.proj); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(w.calls()[before:], "\n"), "docker [run]") {
		t.Fatal("a proxy without the gate was kept")
	}
	w.env["FAKE_DOCKER_OUT_INSPECT___FORMAT"] = "@" + w.launcher().proxyRules() + "^1"
	before = len(w.calls())
	if err := w.launcher().ensureProxy(w.proj); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(w.calls()[before:], "\n"), "docker [run]") {
		t.Fatal("a proxy with the gate was replaced")
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

func TestLockHTTPRefusesAnUnsetHost(t *testing.T) {
	for what, undo := range map[string]func(root string){
		"is absent":        func(r string) { os.Remove(r + "/usr/local/bin/lockhttp") },
		"/run/lockhttp":    func(r string) { os.Remove(r + "/run/lockhttp") },
		"lockhttp-clients": func(r string) { os.WriteFile(r+"/etc/group", []byte("root:x:0:\n"), 0o644) },
		"writable by root": func(r string) { os.Chmod(r+"/usr/local/bin/lockhttp", 0o777) },
	} {
		w, root := lockHTTPWorld(t)
		undo(root)
		err := w.launcher().ensureProxy(w.proj)
		if err == nil || !strings.Contains(err.Error(), what) || !strings.Contains(err.Error(), "LEA_LOCKHTTP=1") {
			t.Errorf("%s: err = %v", what, err)
		}
		if w.hasCall("docker [run]") {
			t.Errorf("%s: a proxy was started", what)
		}
	}
}

func TestLockHTTPRootlessPodmanKeepsGroups(t *testing.T) {
	w, _ := lockHTTPWorld(t)
	w.env[engineFormat] = "Podman Engine=5.8.9"
	if err := w.launcher().ensureProxy(w.proj); err != nil {
		t.Fatal(err)
	}
	if !w.hasCall("[--group-add] [keep-groups]") || w.hasCall("LEA_LOCKHTTP_GID") {
		t.Fatalf("rootless Podman:\n%s", strings.Join(w.calls(), "\n"))
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

func TestLockHTTPMITM(t *testing.T) {
	w, _ := lockHTTPWorld(t)
	writeUserConf(t, w, "LEA_LOCKHTTP=1\nLEA_LOCKHTTP_MITM=1\n")
	if err := w.launcher().ensureProxy(w.proj); err != nil {
		t.Fatal(err)
	}
	ca := lockhttp.CADir(w.launcher().Cfg.DataDir())
	stamp := "mitm-" + lockhttp.Hash("cert\n")[:12]
	for _, want := range []string{"[LEA_LOCKHTTP_MITM=1]", "[type=bind,src=" + ca + ",dst=/etc/lockhttp-ca,readonly]",
		"[--label] [lea.lockhttp=" + stamp + "]"} {
		if !w.hasCall(want) {
			t.Errorf("proxy run lacks %s:\n%s", want, strings.Join(w.calls(), "\n"))
		}
	}
	joined := strings.Join(lockArgs(t, w), "\n") + "\n"
	for _, want := range []string{"type=bind,src=" + ca + "/ca.pem,dst=/etc/lea/lockhttp-ca.pem,readonly",
		"NODE_EXTRA_CA_CERTS=/etc/lea/lockhttp-ca.pem", "REQUESTS_CA_BUNDLE=/etc/pki/tls/certs/ca-bundle.crt", "LEA_LOCKHTTP_MITM=1"} {
		if !strings.Contains(joined, "\n"+want+"\n") {
			t.Errorf("run args lack %s", want)
		}
	}
	if strings.Contains(joined, "ca-key") {
		t.Fatal("an agent container got the CA key")
	}
	// A new CA restarts the proxy.
	w.env["FAKE_DOCKER_OUT_INSPECT__F"] = "true"
	w.env["FAKE_DOCKER_OUT_INSPECT___FORMAT"] = "@" + w.launcher().proxyRules() + "^" + stamp
	before := len(w.calls())
	if err := w.launcher().ensureProxy(w.proj); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(w.calls()[before:], "\n"), "docker [run]") {
		t.Fatal("the proxy of the same CA was replaced")
	}
	os.WriteFile(ca+"/ca.pem", []byte("rotated\n"), 0o644)
	before = len(w.calls())
	if err := w.launcher().ensureProxy(w.proj); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(w.calls()[before:], "\n"), "docker [run]") {
		t.Fatal("a rotated CA kept the proxy")
	}
}

func TestLockHTTPMITMNeedsLockHTTP(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_LOCKHTTP_MITM=1\n")
	if _, err := LoadConfig(w.getenv, w.root, w.proj); err == nil || !strings.Contains(err.Error(), "LEA_LOCKHTTP_MITM=1 needs LEA_LOCKHTTP=1") {
		t.Fatalf("err = %v", err)
	}
}
