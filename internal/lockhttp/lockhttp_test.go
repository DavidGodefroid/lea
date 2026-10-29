package lockhttp

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeyIsCreatedOnceAndPrivate(t *testing.T) {
	d := t.TempDir()
	k1, err := Key(d)
	if err != nil || len(k1) != 32 {
		t.Fatalf("Key: %v, %d bytes", err, len(k1))
	}
	fi, err := os.Stat(keyPath(d))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("token.key mode %v, %v", fi.Mode(), err)
	}
	k2, err := Key(d)
	if err != nil || !bytes.Equal(k1, k2) {
		t.Fatalf("second Key differs: %v", err)
	}
}

func TestKeyOfAnotherSizeIsAnError(t *testing.T) {
	d := t.TempDir()
	os.MkdirAll(Dir(d), 0o700)
	os.WriteFile(keyPath(d), []byte("short"), 0o600)
	if _, err := Key(d); err == nil || !strings.Contains(err.Error(), "want 32") {
		t.Fatalf("got %v", err)
	}
}

func TestTokensDifferPerContainerAndAgent(t *testing.T) {
	k := bytes.Repeat([]byte{1}, 32)
	seen := map[string]bool{}
	for _, tok := range []string{Token(k, "lea-a", "dev-1"), Token(k, "lea-b", "dev-1"), Token(k, "lea-a", "dev-2"),
		Token(k, "lea-a", ShellAgent), KeyproxyToken(k)} {
		if len(tok) != 32 || seen[tok] {
			t.Fatalf("token %q repeated or not 32 hex digits", tok)
		}
		seen[tok] = true
	}
	if Token(k, "lea-a", "dev-1") != Token(append([]byte{}, k...), "lea-a", "dev-1") {
		t.Fatal("Token is not deterministic")
	}
}

func TestBuildNamesContainersAndHashesTokens(t *testing.T) {
	k := bytes.Repeat([]byte{7}, 32)
	s := Build(k, []Network{{LocalIP: "10.89.3.2", Container: "lea-b", Project: "/p/b"}, {LocalIP: "10.89.1.2", Container: "lea-a"}},
		[]AgentSource{
			{Container: "lea-a", Agent: "dev-5", Role: "dev", Parent: "claude-1", Branch: "feat/x", TaskID: "12"},
			{Container: "lea-gone", Agent: "dev-9"},
			{Container: "lea-a", Agent: ShellAgent, TokenSHA256: "forged"},
		})
	if s.Version != 1 || s.Networks[0].LocalIP != "10.89.1.2" {
		t.Fatalf("version or order: %+v", s)
	}
	if s.Keyproxy.TokenSHA256 != Hash(KeyproxyToken(k)) {
		t.Fatal("keyproxy hash")
	}
	got := map[string]AgentSource{}
	for _, a := range s.Agents {
		got[a.Container+"/"+a.Agent] = a
	}
	want := []string{"lea-a/dev-5", "lea-a/nested", "lea-a/shell", "lea-b/nested", "lea-b/shell"}
	if len(s.Agents) != len(want) {
		t.Fatalf("agents %+v", s.Agents)
	}
	for i, w := range want {
		if s.Agents[i].Container+"/"+s.Agents[i].Agent != w {
			t.Fatalf("agent %d is %s/%s, want %s", i, s.Agents[i].Container, s.Agents[i].Agent, w)
		}
	}
	if a := got["lea-a/dev-5"]; a.TokenSHA256 != Hash(Token(k, "lea-a", "dev-5")) || a.Role != "dev" || a.TaskID != "12" {
		t.Fatalf("dev-5: %+v", a)
	}
	if got["lea-a/shell"].TokenSHA256 != Hash(Token(k, "lea-a", ShellAgent)) {
		t.Fatal("a store row named shell must not override the shell token")
	}
	b, _ := json.Marshal(s)
	if strings.Contains(string(b), Token(k, "lea-a", "dev-5")) {
		t.Fatal("sources.json holds a token")
	}
}

func TestWriteJSONIsAtomicAndSkipsIdentical(t *testing.T) {
	p := filepath.Join(t.TempDir(), "gate", "sources.json")
	s := Build(bytes.Repeat([]byte{3}, 32), []Network{{LocalIP: "10.0.0.2", Container: "lea-a"}}, nil)
	changed, err := WriteJSON(p, s)
	if err != nil || !changed {
		t.Fatalf("first write: %v %v", changed, err)
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if changed, err = WriteJSON(p, s); err != nil || changed {
		t.Fatalf("identical write: %v %v", changed, err)
	}
	var back Sources
	b, _ := os.ReadFile(p)
	if err := json.Unmarshal(b, &back); err != nil || back.Version != 1 || len(back.Agents) != 2 {
		t.Fatalf("read back %v %+v", err, back)
	}
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(p), ".tmp-*"))
	if len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

func TestSourcerFollowsTheFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sources.json")
	s := NewSourcer(p)
	if _, ok := s.Source(net.ParseIP("10.0.0.2")); ok {
		t.Fatal("no file names a source")
	}
	k := bytes.Repeat([]byte{1}, 32)
	WriteJSON(p, Build(k, []Network{{LocalIP: "10.0.0.2", Container: "lea-a"}}, nil))
	if c, ok := s.Source(net.ParseIP("10.0.0.2")); !ok || c != "lea-a" {
		t.Fatalf("got %q %v", c, ok)
	}
	WriteJSON(p, Build(k, []Network{{LocalIP: "10.0.0.3", Container: "lea-bb"}}, nil))
	if _, ok := s.Source(net.ParseIP("10.0.0.2")); ok {
		t.Fatal("a removed network still names its container")
	}
	if c, _ := s.Source(net.ParseIP("10.0.0.3")); c != "lea-bb" {
		t.Fatalf("new network: %q", c)
	}
	os.WriteFile(p, []byte(`{"version":2,"networks":[{"local_ip":"10.0.0.3","container":"x"}]}`), 0o644)
	if _, ok := s.Source(net.ParseIP("10.0.0.3")); ok {
		t.Fatal("another contract version is not read")
	}
}

func TestDropNetworks(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sources.json")
	if err := DropNetworks(p, func(Network) bool { return true }); err != nil {
		t.Fatalf("missing file: %v", err)
	}
	k := bytes.Repeat([]byte{1}, 32)
	WriteJSON(p, Build(k, []Network{{LocalIP: "10.0.0.2", Container: "lea-a"}, {LocalIP: "10.0.1.2", Container: "lea-b"}}, nil))
	if err := DropNetworks(p, func(n Network) bool { return n.Container == "lea-a" }); err != nil {
		t.Fatal(err)
	}
	s := NewSourcer(p)
	if _, ok := s.Source(net.ParseIP("10.0.0.2")); ok {
		t.Fatal("dropped network still names lea-a")
	}
	if c, _ := s.Source(net.ParseIP("10.0.1.2")); c != "lea-b" {
		t.Fatalf("kept network: %q", c)
	}
}

// A lockhttp directory made before lea's key (wider than 0700) is made private with it.
func TestKeyMakesItsDirectoryPrivate(t *testing.T) {
	d := t.TempDir()
	if err := os.MkdirAll(Dir(d), 0o755); err != nil {
		t.Fatal(err)
	}
	os.Chmod(Dir(d), 0o755)
	if _, err := Key(d); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(Dir(d)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("lockhttp dir mode %v, %v", fi.Mode(), err)
	}
}
