package main

import "testing"

// The tree file names a lea container by its own internal network's address of the proxy's.
func TestForgeTreeBuild(t *testing.T) {
	nets := "lea-egress 10.0.0.2\nlea-internal-a 172.18.0.5\nlea-internal-b 172.19.0.5\nother 1.2.3.4\nbad\n"
	tr := forgeTreeBuild("lea", nets, map[string]bool{"lea-a": true})
	if len(tr.Addrs) != 2 || tr.Addrs["172.18.0.5"].Container != "lea-a" || !tr.Addrs["172.18.0.5"].Tree ||
		tr.Addrs["172.19.0.5"].Container != "lea-b" || tr.Addrs["172.19.0.5"].Tree {
		t.Fatalf("%+v", tr)
	}
}
