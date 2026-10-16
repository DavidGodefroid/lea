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

func TestInternalNetworkFallsBackWithoutIsolatedGatewayMode(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["FAKE_DOCKER_SEQ_NETWORK_INSPECT"] = "0 1" // egress exists, internal missing
	w.env["FAKE_DOCKER_SEQ_NETWORK_CREATE"] = "1 0"  // isolated refused, plain accepted
	l := w.launcher()
	if err := l.ensureNetworks("abc"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.errb.String(), "Docker refused gateway_mode_ipv4=isolated") {
		t.Fatalf("stderr %q", w.errb.String())
	}
	if !w.hasCall("docker [network] [create] [--label] [lea.prefix=lea] [--label] [lea.id=abc] [--internal] [lea-internal-abc]") {
		t.Fatalf("calls:\n%s", strings.Join(w.calls(), "\n"))
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
	w.env["FAKE_DOCKER_OUT_NETWORK_INSPECT"] = "true"
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
