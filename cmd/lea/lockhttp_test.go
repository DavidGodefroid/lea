package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DavidGodefroid/lea/internal/lockhttp"
	"github.com/DavidGodefroid/lea/internal/store"
)

func TestLockHTTPBuildNamesOnlyRunningContainersOnTheirOwnNetwork(t *testing.T) {
	key := bytes.Repeat([]byte{9}, 32)
	nets := "lea-egress 10.88.0.2\nlea-internal-aaaa 10.89.1.2\nlea-internal-bbbb 10.89.2.2\nother-internal-cccc 10.89.3.2\nlea-internal-dddd 10.89.4.2\n"
	ps := lockHTTPRunning("lea-aaaa\t/home/u/projects\nlea-dddd\t/home/u/other\n")
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

func TestLockHTTPCredentialsHashTheLoginsOfRunningContainers(t *testing.T) {
	data := t.TempDir()
	login := func(id, tok string) {
		d := filepath.Join(data, "state", "claude", id)
		os.MkdirAll(d, 0o700)
		os.WriteFile(filepath.Join(d, ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"`+tok+`","refreshToken":"r"}}`), 0o600)
	}
	login("aaaa", "sk-ant-oat-1")
	login("cccc", "sk-ant-oat-gone")
	c := lockHTTPCredentials(data, "lea", map[string]string{"lea-aaaa": "/p", "lea-bbbb": "/q", "other-x": ""}, nil)
	if c.Version != 1 || len(c.Containers) != 2 || len(c.Keyproxy) != 0 {
		t.Fatalf("%+v", c)
	}
	if got := c.Containers["lea-aaaa"]; len(got) != 1 || got[0] != lockhttp.Hash("sk-ant-oat-1") {
		t.Fatalf("lea-aaaa: %q", got)
	}
	if got, ok := c.Containers["lea-bbbb"]; !ok || len(got) != 0 {
		t.Fatalf("a container without a login: %q %v", got, ok)
	}
	b, _ := json.Marshal(c)
	if strings.Contains(string(b), "sk-ant") {
		t.Fatal("credentials.json holds a token")
	}
}

func TestReadLockHTTPStatus(t *testing.T) {
	p := filepath.Join(t.TempDir(), "status.json")
	if s := readLockHTTPStatus(p); s.Console != "" || s.Version != 1 {
		t.Fatalf("missing: %+v", s)
	}
	os.WriteFile(p, []byte(`{"version":1,"console":"up","pending":2,"pending_by_container":{"lea-a":2},"policy_fp":"sha256:x"}`), 0o644)
	if s := readLockHTTPStatus(p); s.Console != "up" || s.PendingByContainer["lea-a"] != 2 {
		t.Fatalf("%+v", s)
	}
}
