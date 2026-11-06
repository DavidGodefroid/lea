package launcher

import (
	"errors"
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

// #105: gateway_mode_ipv4=isolated stored on an existing network proves nothing on an engine that
// predates it (it may hold the option and ignore it), nor on an engine lea cannot tell: the
// existing network is refused as a new one would be, unless LEA_ALLOW_GATEWAY=1.
func TestExistingIsolatedNetworkOnAnEngineWithoutIsolatedGatewayIsRefused(t *testing.T) {
	for _, c := range []struct{ engine, want string }{
		{"Engine=27.5.1", "Docker Engine 27.5.1 is older than 28.0"},
		{"Podman Engine=5.8.9", "Podman 5.8.9's bridge has no gateway_mode_ipv4=isolated"},
		{"", "lea cannot tell the container engine's version"},
		{"containerd=v2.1.4", "lea cannot tell the container engine's version"},
		{"Engine=", "lea cannot tell the container engine's version"},
	} {
		w := newWorld(t, repoRoot(t))
		w.env["FAKE_DOCKER_OUT_NETWORK_INSPECT"] = "true isolated|0123456789abcdef|<no value>"
		w.env[engineFormat] = c.engine
		err := w.launcher().ensureNetworks("abc")
		if err == nil || !strings.Contains(err.Error(), "lea-internal-abc has gateway_mode_ipv4=isolated but "+c.want) ||
			!strings.Contains(err.Error(), "LEA_ALLOW_GATEWAY=1") {
			t.Fatalf("%q: err = %v", c.engine, err)
		}
		w.env["LEA_ALLOW_GATEWAY"] = "1"
		if err := w.launcher().ensureNetworks("abc"); err != nil || w.hasCall("[network] [create] [--label] [lea.prefix=lea] [--label]") {
			t.Fatalf("%q: opt-in: %v", c.engine, err)
		}
		if !strings.Contains(w.errb.String(), c.want) || !strings.Contains(w.errb.String(), "LEA_ALLOW_GATEWAY=1") {
			t.Fatalf("%q: stderr %q", c.engine, w.errb.String())
		}
	}
}

// An engine lea cannot tell gets no new internal network either: lea does not let Docker's answer
// to the option decide (an engine that ignores it accepts it).
func TestInternalNetworkRefusesAnUnknownEngine(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["FAKE_DOCKER_SEQ_NETWORK_INSPECT"] = "0 1"
	w.env[engineFormat] = ""
	err := w.launcher().ensureNetworks("abc")
	if err == nil || !strings.Contains(err.Error(), "lea cannot tell the container engine's version") || !strings.Contains(err.Error(), "LEA_ALLOW_GATEWAY=1") {
		t.Fatalf("err = %v", err)
	}
	if w.hasCall("[network] [create] [--label] [lea.prefix=lea] [--label]") {
		t.Fatalf("a network is created:\n%s", strings.Join(w.calls(), "\n"))
	}
}

// bridges stands for the host's interfaces: name -> its IPv4 addresses; a name absent is an
// interface the host does not have (the bridge lives in another namespace).
func bridges(m map[string][]string, err error) func(string) ([]string, bool, error) {
	return func(name string) ([]string, bool, error) {
		a, ok := m[name]
		if ok && err != nil {
			return nil, true, err
		}
		return a, ok, nil
	}
}

// lea checks the isolation itself: a host address on the network's bridge is refused whatever
// gateway_mode_ipv4 says, on an existing network as on one lea just created.
func TestHostAddressOnTheBridgeIsRefused(t *testing.T) {
	const id = "0123456789abcdef0123"
	for _, c := range []struct {
		name, inspect string
		host          map[string][]string
		err           error
		want          string // "" accepted
	}{
		{"no address on br-<id>", "true isolated|" + id + "|<no value>", map[string][]string{"br-0123456789ab": nil}, nil, ""},
		{"bridge not visible", "true isolated|" + id + "|<no value>", map[string][]string{}, nil, ""},
		{"address on br-<id>", "true isolated|" + id + "|<no value>", map[string][]string{"br-0123456789ab": {"172.24.0.1/16"}},
			nil, "the host holds 172.24.0.1/16 on its bridge br-0123456789ab"},
		{"address on the named bridge", "true isolated|" + id + "|leabr0", map[string][]string{"leabr0": {"10.9.0.1/24"}, "br-0123456789ab": nil},
			nil, "the host holds 10.9.0.1/24 on its bridge leabr0"},
		{"unreadable addresses", "true isolated|" + id + "|<no value>", map[string][]string{"br-0123456789ab": nil},
			errors.New("netlink: permission denied"), "lea cannot read the host's addresses on its bridge br-0123456789ab (netlink: permission denied)"},
	} {
		for _, created := range []bool{false, true} {
			w := newWorld(t, repoRoot(t))
			w.env["FAKE_DOCKER_OUT_NETWORK_INSPECT"] = c.inspect
			if created {
				// egress exists, the internal network is missing, then inspected once created.
				w.env["FAKE_DOCKER_SEQ_NETWORK_INSPECT"] = "0 1 0"
			}
			l := w.launcher()
			l.bridgeAddrs = bridges(c.host, c.err)
			err := l.ensureNetworks("abc")
			if c.want == "" {
				if err != nil {
					t.Fatalf("%s (created %v): %v", c.name, created, err)
				}
				continue
			}
			if err == nil || !strings.Contains(err.Error(), "lea-internal-abc has gateway_mode_ipv4=isolated but "+c.want) ||
				!strings.Contains(err.Error(), "LEA_ALLOW_GATEWAY=1") {
				t.Fatalf("%s (created %v): err = %v", c.name, created, err)
			}
			if created != w.hasCall("[--internal] [-o] [com.docker.network.bridge.gateway_mode_ipv4=isolated] [lea-internal-abc]") {
				t.Fatalf("%s (created %v): calls:\n%s", c.name, created, strings.Join(w.calls(), "\n"))
			}
			w.env["LEA_ALLOW_GATEWAY"] = "1"
			w.env["FAKE_DOCKER_SEQ_NETWORK_INSPECT"] = ""
			l = w.launcher()
			l.bridgeAddrs = bridges(c.host, c.err)
			if err := l.ensureNetworks("abc"); err != nil || !strings.Contains(w.errb.String(), c.want) {
				t.Fatalf("%s (created %v): opt-in: %v, stderr %q", c.name, created, err, w.errb.String())
			}
		}
	}
}

func TestHostBridgeAddrsReadsTheHostInterfaces(t *testing.T) {
	if _, found, err := hostBridgeAddrs("lea-no-such-bridge0"); found || err != nil {
		t.Fatalf("a missing interface: found %v, %v", found, err)
	}
	addrs, found, err := hostBridgeAddrs("lo")
	if !found {
		t.Skip("no loopback interface named lo on this host")
	}
	if err != nil || !strings.Contains(strings.Join(addrs, " "), "127.0.0.1/8") {
		t.Fatalf("lo: %v %v", addrs, err)
	}
}

func TestParseNetwork(t *testing.T) {
	for in, want := range map[string][4]string{
		"true isolated|0123456789abcdef|<no value>": {"true", "isolated", "0123456789abcdef", "<no value>"},
		"true <no value>|0123456789abcdef|br0":      {"true", "<no value>", "0123456789abcdef", "br0"},
		"true |abc|":                                {"true", "", "abc", ""},
		"true isolated":                             {"true", "isolated", "", ""},
		"false":                                     {"false", "", "", ""},
	} {
		a, b, c, d := parseNetwork(in)
		if got := [4]string{a, b, c, d}; got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
