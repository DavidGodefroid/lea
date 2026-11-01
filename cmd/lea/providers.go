package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/DavidGodefroid/lea/internal/agents"
	"github.com/DavidGodefroid/lea/internal/daemon"
	"github.com/DavidGodefroid/lea/internal/launcher"
	"github.com/DavidGodefroid/lea/internal/providers"
)

// hostProviders is the daemon's registry: the providers file read again at each spawn, so that an
// edit needs no restart (the proxy's forwarder is restarted by the next lea).
func hostProviders(exe string, getenv func(string) string) func() (*providers.Registry, error) {
	return func() (*providers.Registry, error) {
		on, several := daemonLockTools(exe)
		return launcher.LoadProviders(on, several, getenv)
	}
}

func daemonProvidersStamp(exe string) string {
	on, several := daemonLockTools(exe)
	return launcher.DaemonProvidersStamp(on, several, os.Getenv)
}

// daemonLockTools is the lock tools lea wires for the configured projects directory and the
// instances of each tool lox up runs several locks of, from lea's config files and its up.json,
// read at each call (launcher.Config.ProjectsLockTools); nil when the configuration does not load.
func daemonLockTools(exe string) (map[string]bool, map[string][]string) {
	cfg, err := loadLeaConfig(exe)
	if err != nil {
		return nil, nil
	}
	return cfg.ProjectsLockTools()
}

// loadLeaConfig reads lea's config files as the launcher does, for the checkout the daemon runs
// from (<root>/build/lea).
func loadLeaConfig(exe string) (*launcher.Config, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return launcher.LoadConfig(os.Getenv, filepath.Dir(filepath.Dir(exe)), cwd)
}

// daemonLockWatch is the up.json of the configured projects directory (its physical path) the
// daemon watches for a lock added while lea runs: read with the launcher's trust checks and
// the tools on as a container started now gets them, without a lock whose socket volume docker
// does not label as lox up's (Config.UpLocksMounted), compared with the volumes
// each container of that projects directory mounts (docker inspect). ok is false without a
// projects directory or a configuration that loads.
func daemonLockWatch(exe string, docker agents.Docker) (w daemon.LockWatch, ok bool) {
	cfg, err := loadLeaConfig(exe)
	if err != nil {
		return w, false
	}
	proj := cfg.Get("LEA_PROJECTS_DIR")
	if proj == "" {
		return w, false
	}
	if r, err := filepath.EvalSymlinks(proj); err == nil {
		proj = r
	}
	return daemon.LockWatch{
		Dir: proj + "/.lockfamily",
		Read: func(logf func(string, ...any)) []daemon.UpLock {
			var out []daemon.UpLock
			for _, l := range cfg.UpLocksMounted(os.Environ(), proj, logf) {
				out = append(out, daemon.UpLock{Instance: l.Instance, Volume: l.Volume})
			}
			return out
		},
		Mounts: func(ctx context.Context, container string) ([]string, error) {
			p, vols, err := docker.Volumes(ctx, container)
			if err != nil {
				return nil, err
			}
			// A container of another projects directory has other locks: no hint from this one.
			if p != proj {
				return nil, fmt.Errorf("container %s: projects directory %q, not %q", container, p, proj)
			}
			return vols, nil
		},
	}, true
}
