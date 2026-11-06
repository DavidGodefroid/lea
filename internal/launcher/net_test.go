package launcher

import (
	"strings"
	"testing"
)

func TestInternalNetworkMustBeInternal(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["FAKE_DOCKER_OUT_NETWORK_INSPECT"] = "false"
	err := w.launcher().ensureNetworks("abc")
	if err == nil || !strings.Contains(err.Error(), "lea-internal-abc exists but is not an internal network") {
		t.Fatalf("err = %v", err)
	}
}

// #105: Docker refusing gateway_mode_ipv4=isolated stops lea before the plain network, and so
// before any container, is created.
func TestInternalNetworkFailsClosedWithoutIsolatedGatewayMode(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["FAKE_DOCKER_SEQ_NETWORK_INSPECT"] = "0 1" // egress exists, internal missing
	w.env["FAKE_DOCKER_SEQ_NETWORK_CREATE"] = "1 0"  // isolated refused, plain accepted
	err := w.launcher().ensureNetworks("abc")
	if err == nil || !strings.Contains(err.Error(), "Docker refused gateway_mode_ipv4=isolated") ||
		!strings.Contains(err.Error(), "Docker Engine 28.0 or later") || !strings.Contains(err.Error(), "LEA_ALLOW_GATEWAY=1") {
		t.Fatalf("err = %v", err)
	}
	if w.hasCall("docker [network] [create] [--label] [lea.prefix=lea] [--label] [lea.id=abc] [--internal] [lea-internal-abc]") {
		t.Fatalf("a plain internal network is created:\n%s", strings.Join(w.calls(), "\n"))
	}
}

func TestInternalNetworkFallsBackWithLeaAllowGateway(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["FAKE_DOCKER_SEQ_NETWORK_INSPECT"] = "0 1"
	w.env["FAKE_DOCKER_SEQ_NETWORK_CREATE"] = "1 0"
	w.env["LEA_ALLOW_GATEWAY"] = "1"
	if err := w.launcher().ensureNetworks("abc"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.errb.String(), "Docker refused gateway_mode_ipv4=isolated") || !strings.Contains(w.errb.String(), "LEA_ALLOW_GATEWAY=1") {
		t.Fatalf("stderr %q", w.errb.String())
	}
	if !w.hasCall("docker [network] [create] [--label] [lea.prefix=lea] [--label] [lea.id=abc] [--internal] [lea-internal-abc]") {
		t.Fatalf("calls:\n%s", strings.Join(w.calls(), "\n"))
	}
}

// An engine known to have no isolated gateway is refused without trying the option: a Docker
// Engine older than 28.0 may ignore it and create a network the host is reachable from.
func TestInternalNetworkRefusesAnEngineWithoutIsolatedGateway(t *testing.T) {
	for _, c := range []struct{ engine, want string }{
		{"Engine=27.5.1\nrunc=1.2.5", "Docker Engine 27.5.1 is older than 28.0"},
		{"Engine=26.1.4", "Docker Engine 26.1.4 is older than 28.0"},
		{"Podman Engine=5.8.9", "Podman 5.8.9's bridge has no gateway_mode_ipv4=isolated"},
	} {
		w := newWorld(t, repoRoot(t))
		w.env["FAKE_DOCKER_SEQ_NETWORK_INSPECT"] = "0 1"
		w.env[engineFormat] = c.engine
		err := w.launcher().ensureNetworks("abc")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: err = %v", c.engine, err)
		}
		if w.hasCall("[network] [create]") {
			t.Fatalf("%s: a network is created:\n%s", c.engine, strings.Join(w.calls(), "\n"))
		}
		w = newWorld(t, repoRoot(t))
		w.env["FAKE_DOCKER_SEQ_NETWORK_INSPECT"] = "0 1"
		w.env[engineFormat] = c.engine
		w.env["LEA_ALLOW_GATEWAY"] = "1"
		if err := w.launcher().ensureNetworks("abc"); err != nil {
			t.Fatalf("%s: opt-in: %v", c.engine, err)
		}
		if !w.hasCall("docker [network] [create] [--label] [lea.prefix=lea] [--label] [lea.id=abc] [--internal] [lea-internal-abc]") ||
			w.hasCall("[com.docker.network.bridge.gateway_mode_ipv4=isolated]") {
			t.Fatalf("%s: calls:\n%s", c.engine, strings.Join(w.calls(), "\n"))
		}
	}
	w := newWorld(t, repoRoot(t))
	w.env["FAKE_DOCKER_RC_NETWORK_INSPECT"] = "1"
	w.env[engineFormat] = "Engine=28.0.0"
	if err := w.launcher().ensureNetworks("abc"); err != nil || !w.hasCall("[com.docker.network.bridge.gateway_mode_ipv4=isolated] [lea-internal-abc]") {
		t.Fatalf("Docker 28.0: %v\n%s", err, strings.Join(w.calls(), "\n"))
	}
}

// An internal network left without an isolated gateway (by an older lea's fallback) is refused too.
func TestExistingInternalNetworkWithoutIsolatedGatewayIsRefused(t *testing.T) {
	for _, mode := range []string{"true nat", "true <no value>", "true "} {
		w := newWorld(t, repoRoot(t))
		w.env["FAKE_DOCKER_OUT_NETWORK_INSPECT"] = mode
		err := w.launcher().ensureNetworks("abc")
		if err == nil || !strings.Contains(err.Error(), "lea-internal-abc exists without gateway_mode_ipv4=isolated") {
			t.Fatalf("%q: err = %v", mode, err)
		}
		w.env["LEA_ALLOW_GATEWAY"] = "1"
		if err := w.launcher().ensureNetworks("abc"); err != nil || w.hasCall("[create] [--label] [lea.prefix=lea] [--label]") {
			t.Fatalf("%q: opt-in: %v", mode, err)
		}
		if !strings.Contains(w.errb.String(), "not isolated") {
			t.Fatalf("%q: stderr %q", mode, w.errb.String())
		}
	}
}

func TestProxyAttachRefusesAnUnreadableAddress(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["FAKE_DOCKER_OUT_CONTAINER_INSPECT"] = "172.18..5"
	if _, err := w.launcher().proxyAttach("abc"); err == nil || !strings.Contains(err.Error(), "could not read the address of lea-proxy on lea-internal-abc") {
		t.Fatalf("err = %v", err)
	}
	w.env["FAKE_DOCKER_OUT_CONTAINER_INSPECT"] = "172.18.0.5"
	if ip, err := w.launcher().proxyAttach("abc"); err != nil || ip != "172.18.0.5" {
		t.Fatalf("%q %v", ip, err)
	}
}

func TestReleaseNetworkIsBestEffort(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["FAKE_DOCKER_RC_NETWORK_DISCONNECT"] = "1"
	w.env["FAKE_DOCKER_RC_NETWORK_RM"] = "1"
	w.launcher().releaseNetwork("abc")
	if !w.hasCall("docker [network] [rm] [lea-internal-abc]") {
		t.Fatal("rm still runs after a failed disconnect")
	}
}

// Each projects dir gets its own internal network, with a Docker-allocated subnet; an existing
// internal network is left alone (net tests of the retired net.bats).
func TestEachProjectsDirGetsItsOwnInternalNetwork(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["FAKE_DOCKER_RC_NETWORK_INSPECT"] = "1"
	l := w.launcher()
	if err := l.ensureNetworks("abc"); err != nil {
		t.Fatal(err)
	}
	if err := l.ensureNetworks("def"); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(w.calls(), "\n")
	for _, want := range []string{"docker [network] [create] [--label] [lea.prefix=lea] [lea-egress]",
		"[--internal] [-o] [com.docker.network.bridge.gateway_mode_ipv4=isolated] [lea-internal-abc]", "[lea-internal-def]"} {
		if !strings.Contains(calls, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(calls, "--subnet") {
		t.Error("a subnet is chosen by lea")
	}
	w = newWorld(t, repoRoot(t))
	w.env["FAKE_DOCKER_OUT_NETWORK_INSPECT"] = "true isolated"
	if err := w.launcher().ensureNetworks("abc"); err != nil || w.hasCall("[create]") {
		t.Fatalf("an existing internal network is recreated: %v", err)
	}
}

func TestProxyAttachRefusesNoValue(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["FAKE_DOCKER_OUT_CONTAINER_INSPECT"] = "<no value>"
	if _, err := w.launcher().proxyAttach("abc"); err == nil || !strings.Contains(err.Error(), "address of lea-proxy on lea-internal-abc") {
		t.Fatalf("err = %v", err)
	}
}

func TestProxyAttachJoinsWithTheAlias(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["FAKE_DOCKER_OUTSEQ_CONTAINER_INSPECT"] = "|172.18.0.5"
	if ip, err := w.launcher().proxyAttach("abc"); err != nil || ip != "172.18.0.5" {
		t.Fatalf("%q %v", ip, err)
	}
	if !w.hasCall("docker [network] [connect] [--alias] [lea-proxy] [lea-internal-abc] [lea-proxy]") {
		t.Fatalf("calls:\n%s", strings.Join(w.calls(), "\n"))
	}
}
