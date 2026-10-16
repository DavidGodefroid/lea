package identity

import "testing"

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestPaths(t *testing.T) {
	e := env(map[string]string{"HOME": "/h"})
	if got := HostDir(e); got != "/h/.local/share/lea/host" {
		t.Fatal(got)
	}
	if got := DaemonSock(env(map[string]string{"HOME": "/h", "XDG_DATA_HOME": "/x"})); got != "/x/lea/host/daemon.sock" {
		t.Fatal(got)
	}
	if got := BusSock(e); got != "/run/lea/bus/bus.sock" {
		t.Fatal(got)
	}
	if got := BusSock(env(map[string]string{"LEA_BUS_SOCK": "/s"})); got != "/s" {
		t.Fatal(got)
	}
}

func TestAgent(t *testing.T) {
	if got := Agent(env(map[string]string{"LEA_AGENT": "dev"})); got != "dev" {
		t.Fatal(got)
	}
	if got := Agent(env(nil)); got != "" {
		t.Fatal(got)
	}
}
