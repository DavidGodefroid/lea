package launcher

import (
	"strings"
	"testing"
)

func TestFreeSubnets(t *testing.T) {
	if free, total := freeSubnets(dockerDefaultPools, nil); free != 31 || total != 31 {
		t.Fatalf("defaults: %d of %d", free, total)
	}
	used := parseSubnets("172.17.0.0/16 \n172.21.0.0/16 fd00::/64\n192.168.0.0/20 192.168.17.0/24 10.0.0.0/8")
	if free, total := freeSubnets(dockerDefaultPools, used); free != 27 || total != 31 {
		t.Fatalf("used: %d of %d", free, total)
	}
	pools := parsePools("10.10.0.0/16 24|10.20.0.0/16 24|")
	if len(pools) != 2 || pools[1] != (addressPool{"10.20.0.0/16", 24}) {
		t.Fatalf("pools %v", pools)
	}
	if free, total := freeSubnets(pools, parseSubnets("10.10.0.0/24 10.20.0.0/16")); free != 255 || total != 512 {
		t.Fatalf("configured: %d of %d", free, total)
	}
	if free, total := freeSubnets([]addressPool{{"10.0.0.0/8", 28}}, nil); free != maxPoolSubnets || total != maxPoolSubnets {
		t.Fatalf("capped: %d of %d", free, total)
	}
	if parsePools("") != nil {
		t.Fatal("no pools must be nil")
	}
}

// #397: lea doctor says how many address pools Docker has left, and what to do when none is.
func TestDoctorAddressPools(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["FAKE_DOCKER_OUT_NETWORK_LS"] = "n1\nn2"
	w.env["FAKE_DOCKER_OUT_NETWORK_INSPECT"] = "172.17.0.0/16 172.18.0.0/16"
	l := w.launcher()
	l.doctorAddressPools()
	if !strings.Contains(w.out.String(), "ok    Docker address pools: 29 of 31 free (Docker's default pools)") {
		t.Fatalf("stdout %q", w.out.String())
	}
	w = newWorld(t, repoRoot(t))
	w.env["FAKE_DOCKER_OUT_INFO___FORMAT"] = "10.10.0.0/24 24|"
	w.env["FAKE_DOCKER_OUT_NETWORK_LS"] = "n1"
	w.env["FAKE_DOCKER_OUT_NETWORK_INSPECT"] = "10.10.0.0/24"
	w.launcher().doctorAddressPools()
	if out := w.out.String(); !strings.Contains(out, "warn  Docker address pools: none free of 1 (daemon.json's default-address-pools)") ||
		!strings.Contains(out, "docker network prune") || strings.Contains(out, "LEA_ALLOW_GATEWAY") {
		t.Fatalf("stdout %q", out)
	}
	w = newWorld(t, repoRoot(t))
	w.env[engineFormat] = "Podman Engine=5.8.9"
	w.launcher().doctorAddressPools()
	if strings.Contains(w.out.String(), "address pools") {
		t.Fatalf("podman: %q", w.out.String())
	}
}
