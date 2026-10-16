// Package identity derives lea's socket paths and the calling agent's name from the environment.
package identity

import "path/filepath"

func HostDir(getenv func(string) string) string {
	base := getenv("XDG_DATA_HOME")
	if base == "" {
		base = filepath.Join(getenv("HOME"), ".local", "share")
	}
	return filepath.Join(base, "lea", "host")
}

func DaemonSock(getenv func(string) string) string {
	if s := getenv("LEA_DAEMON_SOCK"); s != "" {
		return s
	}
	return filepath.Join(HostDir(getenv), "daemon.sock")
}

func BusSock(getenv func(string) string) string {
	if s := getenv("LEA_BUS_SOCK"); s != "" {
		return s
	}
	return "/run/lea/bus/bus.sock"
}

// Agent is the calling agent's name: LEA_AGENT, set by lea for every agent it spawns, else "".
func Agent(getenv func(string) string) string {
	return getenv("LEA_AGENT")
}
