package main

import (
	"os"

	"github.com/DavidGodefroid/lea/internal/launcher"
	"github.com/DavidGodefroid/lea/internal/providers"
)

// hostProviders is the daemon's registry: the providers file read again at each spawn, so that an
// edit needs no restart (the proxy's forwarder is restarted by the next lea).
func hostProviders(getenv func(string) string) func() (*providers.Registry, error) {
	return func() (*providers.Registry, error) { return launcher.LoadProviders(getenv) }
}

func daemonProvidersStamp() string { return launcher.DaemonProvidersStamp(os.Getenv) }
