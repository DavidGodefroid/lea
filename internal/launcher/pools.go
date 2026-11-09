package launcher

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Docker's address pools (#397): each network Docker creates without --subnet takes one subnet of
// its default-address-pools. Once all are taken, every network create fails, lea's included.

// addressPool is one of Docker's default-address-pools: base split into subnets of /size.
type addressPool struct {
	base string
	size int
}

// dockerDefaultPools are Docker Engine's own pools, used when daemon.json sets no
// default-address-pools: 31 networks.
var dockerDefaultPools = []addressPool{
	{"172.17.0.0/16", 16}, {"172.18.0.0/16", 16}, {"172.19.0.0/16", 16},
	{"172.20.0.0/14", 16}, {"172.24.0.0/14", 16}, {"172.28.0.0/14", 16},
	{"192.168.0.0/16", 20},
}

// poolsFormat is what docker info prints of the configured pools, read by parsePools.
const poolsFormat = `{{range .DefaultAddressPools}}{{.Base}} {{.Size}}|{{end}}`

// parsePools reads poolsFormat's output; nil when daemon.json sets none.
func parsePools(out string) []addressPool {
	var pools []addressPool
	for _, f := range strings.Split(out, "|") {
		base, size, ok := strings.Cut(strings.TrimSpace(f), " ")
		if !ok {
			continue
		}
		if n, err := strconv.Atoi(size); err == nil {
			pools = append(pools, addressPool{base, n})
		}
	}
	return pools
}

// maxPoolSubnets caps the subnets counted in one pool (a 10.0.0.0/8 split in /24 is 65536).
const maxPoolSubnets = 1 << 16

// freeSubnets counts the subnets of pools and those of them no used subnet overlaps.
func freeSubnets(pools []addressPool, used []*net.IPNet) (free, total int) {
	for _, p := range pools {
		_, base, err := net.ParseCIDR(p.base)
		if err != nil || base.IP.To4() == nil {
			continue
		}
		ones, _ := base.Mask.Size()
		if p.size < ones || p.size > 32 || p.size-ones > 16 {
			continue
		}
		n := 1 << (p.size - ones)
		if n > maxPoolSubnets {
			n = maxPoolSubnets
		}
		start := ipv4Int(base.IP)
		mask := net.CIDRMask(p.size, 32)
		for i := 0; i < n; i++ {
			sub := &net.IPNet{IP: intIPv4(start + uint32(i)<<(32-p.size)), Mask: mask}
			total++
			if !overlapsAny(sub, used) {
				free++
			}
		}
	}
	return free, total
}

func overlapsAny(sub *net.IPNet, used []*net.IPNet) bool {
	for _, u := range used {
		if sub.Contains(u.IP) || u.Contains(sub.IP) {
			return true
		}
	}
	return false
}

func ipv4Int(ip net.IP) uint32 {
	v := ip.To4()
	return uint32(v[0])<<24 | uint32(v[1])<<16 | uint32(v[2])<<8 | uint32(v[3])
}

func intIPv4(v uint32) net.IP { return net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v)) }

// parseSubnets reads the IPv4 subnets in out, separated by spaces or newlines.
func parseSubnets(out string) []*net.IPNet {
	var subs []*net.IPNet
	for _, f := range strings.Fields(out) {
		if _, n, err := net.ParseCIDR(f); err == nil && n.IP.To4() != nil {
			subs = append(subs, n)
		}
	}
	return subs
}

// doctorAddressPools is lea doctor's line on Docker's free address pools: how many more networks
// Docker can create, lea's included. Podman, or an engine lea cannot tell, gets no line.
func (l *Launcher) doctorAddressPools() {
	if engine, _ := l.hostEngine(); engine != "docker" {
		return
	}
	out, err := l.output(false, "docker", "info", "--format", poolsFormat)
	if err != nil {
		return
	}
	pools, from := parsePools(out), "daemon.json's default-address-pools"
	if pools == nil {
		pools, from = dockerDefaultPools, "Docker's default pools"
	}
	var used []*net.IPNet
	if ids, _ := l.output(false, "docker", "network", "ls", "-q"); strings.TrimSpace(ids) != "" {
		args := append([]string{"network", "inspect", "-f", `{{range .IPAM.Config}}{{.Subnet}} {{end}}`}, strings.Fields(ids)...)
		subs, _ := l.output(false, "docker", args...)
		used = parseSubnets(subs)
	}
	free, total := freeSubnets(pools, used)
	if free == 0 {
		fmt.Fprintf(l.Stdout, "warn  Docker address pools: none free of %d (%s): lea cannot create a network; %s\n", total, from,
			"remove unused networks (docker network prune) or add default-address-pools in /etc/docker/daemon.json")
		return
	}
	fmt.Fprintf(l.Stdout, "ok    Docker address pools: %d of %d free (%s)\n", free, total, from)
}
