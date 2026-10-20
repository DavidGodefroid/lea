package keyproxy

import (
	"bufio"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

// DefaultRouteIfaces are the interfaces of the IPv4 default route in a /proc/net/route table. In the
// proxy container that is the egress network's: the agents' networks are internal, without a
// gateway.
func DefaultRouteIfaces(r io.Reader) []string {
	var out []string
	sc := bufio.NewScanner(r)
	sc.Scan() // the header
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 8 && f[1] == "00000000" && f[7] == "00000000" {
			out = append(out, f[0])
		}
	}
	return out
}

// EgressAddrs returns a Refused function for the proxy container: it refuses a connection whose
// local address belongs to an interface of the default route, read again at most every 10 s (the
// container's networks change while it runs). When the default route cannot be read, every
// connection is refused: the forwarder fails closed.
func EgressAddrs(routes string) func(net.IP) bool {
	var mu sync.Mutex
	var at time.Time
	var addrs []net.IP
	ok := false
	load := func() {
		f, err := os.Open(routes)
		if err != nil {
			ok = false
			return
		}
		ifaces := DefaultRouteIfaces(f)
		f.Close()
		addrs, ok = nil, len(ifaces) > 0
		for _, n := range ifaces {
			ifc, err := net.InterfaceByName(n)
			if err != nil {
				ok = false
				return
			}
			as, err := ifc.Addrs()
			if err != nil {
				ok = false
				return
			}
			for _, a := range as {
				if ipn, isNet := a.(*net.IPNet); isNet {
					addrs = append(addrs, ipn.IP)
				}
			}
		}
	}
	return func(local net.IP) bool {
		mu.Lock()
		defer mu.Unlock()
		if time.Since(at) > 10*time.Second {
			load()
			at = time.Now()
		}
		if !ok {
			return true
		}
		for _, a := range addrs {
			if a.Equal(local) {
				return true
			}
		}
		return false
	}
}
