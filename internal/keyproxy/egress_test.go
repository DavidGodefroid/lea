package keyproxy

import (
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const routeTable = `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
eth1	000019AC	00000000	0001	0	0	0	0000FFFF	0	0	0
eth0	00000000	010012AC	0003	0	0	0	00000000	0	0	0
eth0	000012AC	00000000	0001	0	0	0	0000FFFF	0	0	0
`

func TestDefaultRouteIfaces(t *testing.T) {
	if got := DefaultRouteIfaces(strings.NewReader(routeTable)); !reflect.DeepEqual(got, []string{"eth0"}) {
		t.Fatalf("%q", got)
	}
	if got := DefaultRouteIfaces(strings.NewReader("Iface\tDestination\n")); got != nil {
		t.Fatalf("%q", got)
	}
}

// A connection on the default route's interface (the egress network: the host, other containers)
// is refused; one on any other interface (an agents' internal network) passes. Without a readable
// routing table, every connection is refused.
func TestEgressAddrs(t *testing.T) {
	dir := t.TempDir()
	routes := filepath.Join(dir, "route")
	os.WriteFile(routes, []byte("Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\n"+
		"lo\t00000000\t00000000\t0003\t0\t0\t0\t00000000\n"), 0o600)
	refused := EgressAddrs(routes)
	if !refused(net.ParseIP("127.0.0.1")) {
		t.Error("an address of the default route's interface passed")
	}
	if refused(net.ParseIP("10.99.0.2")) {
		t.Error("an address of another interface was refused")
	}
	if !EgressAddrs(filepath.Join(dir, "none"))(net.ParseIP("10.99.0.2")) {
		t.Error("no routing table: a connection passed")
	}
}

func TestForwarderRefusesTheEgressNetwork(t *testing.T) {
	up := newEcho(t)
	base, _ := url.Parse(up.URL + "/v1")
	via, _ := url.Parse("http://127.0.0.1:9")
	f, _ := New(Config{Endpoints: []Endpoint{{Name: "x", URL: base}}, Via: via, Log: log.New(io.Discard, "", 0),
		Refused: func(ip net.IP) bool { return ip.IsLoopback() }})
	fw := httptest.NewServer(f)
	defer fw.Close()
	if code, _, _ := raw(t, fw.URL, "GET", "/x/models", nil); code != http.StatusForbidden {
		t.Errorf("a connection on a refused address: %d", code)
	}
	if in, _ := up.request(); in != nil {
		t.Error("the upstream was reached")
	}
}

// Matrix parameters (';') never pass: some gateways read "..;" as "..".
func TestCleanRawPathRefusesSemicolons(t *testing.T) {
	for _, p := range []string{"/n/..;/admin", "/n/a;b", "/n/v1;x=1/models", "/n/%3b", "/n/%3B/x", "/n/.%3b/x", "/n/a,b", "/n/a=b"} {
		if cleanRawPath(p) {
			t.Errorf("%q accepted", p)
		}
	}
	for _, p := range []string{"/n/responses", "/n/models/qwen3:32b", "/n/models/meta-llama%2BLlama", "/n/chat/completions", "/n/v1/"} {
		if !cleanRawPath(p) {
			t.Errorf("%q refused", p)
		}
	}
}
