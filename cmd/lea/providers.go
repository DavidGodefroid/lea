package main

import (
	"os"
	"path/filepath"

	"github.com/DavidGodefroid/lea/internal/launcher"
	"github.com/DavidGodefroid/lea/internal/providers"
)

// hostProviders is the daemon's registry: the providers file read again at each spawn, so that an
// edit needs no restart (the proxy's forwarder is restarted by the next lea).
func hostProviders(exe string, getenv func(string) string) func() (*providers.Registry, error) {
	return func() (*providers.Registry, error) { return launcher.LoadProviders(daemonToolsOn(exe), getenv) }
}

func daemonProvidersStamp(exe string) string {
	return launcher.DaemonProvidersStamp(daemonToolsOn(exe), os.Getenv)
}

// daemonToolsOn is the lock tools lea wires for the configured projects directory, from lea's
// config files and its up.json, read at each call (launcher.Config.ProjectsToolsOn); nil when the
// configuration does not load.
func daemonToolsOn(exe string) map[string]bool {
	cfg, err := loadLeaConfig(exe)
	if err != nil {
		return nil
	}
	return cfg.ProjectsToolsOn()
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
