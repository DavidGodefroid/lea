package main

import (
	"context"
	"log"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/DavidGodefroid/lea/internal/daemon"
	"github.com/DavidGodefroid/lea/internal/keyproxy"
	"github.com/DavidGodefroid/lea/internal/store"
)

// forgeTreeEvery is how often the daemon brings the forge forwarder's tree file up to date.
const forgeTreeEvery = 2 * time.Second

// forgeDir is the forge forwarder's key directory on the host (launcher's forgeDir), which the
// proxy mounts read-only.
func forgeDir(dataDir string) string { return filepath.Join(dataDir, "proxy", "forge") }

// forgeTree keeps the forge forwarder's tree file (keyproxy.TreeFile) in step with the proxy's
// networks and the towers of the store, until ctx ends (#110): the forwarder applies the push
// and merge rules of a tower's tree to the requests of those containers.
func forgeTree(ctx context.Context, st *store.Store, dataDir, prefix string) {
	t := time.NewTicker(forgeTreeEvery)
	defer t.Stop()
	for {
		if err := writeForgeTree(ctx, st, forgeDir(dataDir), prefix); err != nil {
			log.Printf("lea daemon: forge tree: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func writeForgeTree(ctx context.Context, st *store.Store, dir, prefix string) error {
	agents, err := st.Agents()
	if err != nil {
		return err
	}
	nets, err := exec.CommandContext(ctx, "docker", "inspect", "-f",
		`{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{$v.IPAddress}}{{"\n"}}{{end}}`, prefix+"-proxy").Output()
	if err != nil {
		// No proxy (yet): keep the last file; a new proxy's addresses come with the next pass.
		return nil
	}
	return keyproxy.WriteTree(dir, forgeTreeBuild(prefix, string(nets), daemon.TreeContainers(agents)))
}

// forgeTreeBuild is the tree file from the proxy's networks ("<network> <ip>" lines): a lea
// container's own internal network (<prefix>-internal-<id> for <prefix>-<id>) names it.
func forgeTreeBuild(prefix, nets string, tree map[string]bool) keyproxy.Tree {
	t := keyproxy.Tree{Addrs: map[string]keyproxy.TreeAddr{}}
	for _, l := range strings.Split(nets, "\n") {
		f := strings.Fields(l)
		if len(f) != 2 {
			continue
		}
		id, ok := strings.CutPrefix(f[0], prefix+"-internal-")
		if !ok || id == "" {
			continue
		}
		c := prefix + "-" + id
		t.Addrs[f[1]] = keyproxy.TreeAddr{Container: c, Tree: tree[c]}
	}
	return t
}
