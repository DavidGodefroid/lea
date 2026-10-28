package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/DavidGodefroid/lea/internal/lockhttp"
	"github.com/DavidGodefroid/lea/internal/store"
)

func TestLockHTTPBuildNamesOnlyRunningContainersOnTheirOwnNetwork(t *testing.T) {
	key := bytes.Repeat([]byte{9}, 32)
	nets := "lea-egress 10.88.0.2\nlea-internal-aaaa 10.89.1.2\nlea-internal-bbbb 10.89.2.2\nother-internal-cccc 10.89.3.2\nlea-internal-dddd 10.89.4.2\n"
	ps := "lea-aaaa\t/home/u/projects\nlea-dddd\t/home/u/other\n"
	agents := []store.Agent{
		{Container: "lea-aaaa", Name: "dev-1", State: "running", Role: "dev", Parent: "claude-1", Branch: "feat/x", TaskID: 7, SessionID: "s1"},
		{Container: "lea-aaaa", Name: "old", State: "done"},
		{Container: "lea-aaaa", Name: "parked", State: "stopped"},
		{Container: "lea-bbbb", Name: "dev-2", State: "running"},
	}
	s := lockHTTPBuild(key, "lea", nets, ps, agents)
	if len(s.Networks) != 2 || s.Networks[0] != (lockhttp.Network{LocalIP: "10.89.1.2", Container: "lea-aaaa", Project: "/home/u/projects"}) ||
		s.Networks[1].Container != "lea-dddd" {
		t.Fatalf("networks %+v", s.Networks)
	}
	var names []string
	for _, a := range s.Agents {
		names = append(names, a.Container+"/"+a.Agent)
		if a.Agent == "dev-1" && (a.TaskID != "7" || a.Parent != "claude-1" || a.SessionID != "s1" ||
			a.TokenSHA256 != lockhttp.Hash(lockhttp.Token(key, "lea-aaaa", "dev-1"))) {
			t.Errorf("dev-1: %+v", a)
		}
	}
	want := "lea-aaaa/dev-1 lea-aaaa/nested lea-aaaa/shell lea-dddd/nested lea-dddd/shell"
	if got := strings.Join(names, " "); got != want {
		t.Fatalf("agents %s, want %s", got, want)
	}
}
