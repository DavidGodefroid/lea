package locks

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func repoSet(t *testing.T) *Set {
	t.Helper()
	s, err := LoadSet(filepath.Join("..", "..", ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The shipped set is complete: every Lock of the Registry and lockhttp, by digest, signed at its
// own tag, with a client archive for each architecture of lea's image.
func TestShippedSetIsComplete(t *testing.T) {
	s := repoSet(t)
	for _, name := range append(Names(), "lockhttp") {
		p, ok := s.Pins[name]
		if !ok {
			t.Fatalf("no pin for %s", name)
		}
		if p.Identity != Identity(name, p.Version) || Digest(p.Image) == "" || p.Protocol != 1 {
			t.Errorf("%s: %+v", name, p)
		}
		if _, inImage := Get(name); inImage && (len(p.SHA256["amd64"]) != 64 || len(p.SHA256["arm64"]) != 64) {
			t.Errorf("%s: archives %v", name, p.SHA256)
		}
	}
	if !strings.HasPrefix(s.Summary(), "lockkit-v") || !Version.MatchString(s.Lox) {
		t.Errorf("summary %q, lox %q", s.Summary(), s.Lox)
	}
}

func TestRegistryWiring(t *testing.T) {
	want := map[string][2]string{"locksql": {"LEA_LOCKSQL", "10000"}, "lockmq": {"LEA_LOCKMQ", "10010"}, "lockfile": {"LEA_LOCKFILE", "10030"}}
	if strings.Join(Names(), " ") != "locksql lockmq lockfile" {
		t.Fatalf("order %v", Names())
	}
	for n, w := range want {
		l, ok := Get(n)
		if !ok || l.Switch != w[0] || l.GID != w[1] || l.Client() != "/usr/local/bin/"+n || l.SocketDir() != "/run/"+n || !l.Hub {
			t.Errorf("%s: %+v", n, l)
		}
		if l.MCPServer("") != n || l.MCPServer("orders") != n+"-orders" {
			t.Errorf("%s: MCP %s %s", n, l.MCPServer(""), l.MCPServer("orders"))
		}
	}
	if l, _ := Get("locksql"); l.ProfileDir != ".locksql" {
		t.Errorf("locksql profile dir %q", l.ProfileDir)
	}
	if _, ok := Get("lockhttp"); ok {
		t.Error("lockhttp has no client in lea's image")
	}
}

func setText(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A set that mixes releases, names a digest of another repository, or misses a pin is refused.
func TestParseSetRefuses(t *testing.T) {
	good := setText(t)
	for name, edit := range map[string][2]string{
		"identity of another tag": {"refs/tags/v0.17.0\n", "refs/tags/v0.16.0\n"},
		"image of another Lock":   {"LOCKS_LOCKMQ_IMAGE=ghcr.io/davidgodefroid/lockmq@", "LOCKS_LOCKMQ_IMAGE=ghcr.io/davidgodefroid/locksql@"},
		"image by tag":            {"LOCKS_LOCKFILE_IMAGE=ghcr.io/davidgodefroid/lockfile@sha256:", "LOCKS_LOCKFILE_IMAGE=ghcr.io/davidgodefroid/lockfile:0.8.0#"},
		"no archive":              {"LOCKS_LOCKMQ_SHA256_ARM64=", "LOCKS_LOCKMQ_SHA256_ARM6="},
		"unknown key":             {"LOCKS_SET=", "LOCKS_EXTRA=1\nLOCKS_SET="},
		"twice":                   {"LOCKS_SET=", "LOCKS_LOX_VERSION=v0.9.0\nLOCKS_SET="},
		"quoted":                  {"LOCKS_LOCKSQL_PROTOCOL=1", "LOCKS_LOCKSQL_PROTOCOL='1'"},
		"no protocol":             {"LOCKS_LOCKFILE_PROTOCOL=1", "LOCKS_LOCKFILE_PROTOCOL=x"},
		"lockhttp archive":        {"LOCKS_LOCKHTTP_PROTOCOL=1", "LOCKS_LOCKHTTP_PROTOCOL=1\nLOCKS_LOCKHTTP_SHA256_AMD64=" + strings.Repeat("a", 64)},
	} {
		bad := strings.Replace(good, edit[0], edit[1], 1)
		if bad == good {
			t.Fatalf("%s: the edit changes nothing", name)
		}
		if _, err := ParseSet(strings.NewReader(bad)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func client(t *testing.T, name string) Client {
	p := repoSet(t).Pins[name]
	return Client{Pin: p, Version: p.Version}
}

func TestCheckVerdicts(t *testing.T) {
	sql := client(t, "locksql")
	v := sql.Pin.Version
	other := "ghcr.io/davidgodefroid/locksql@sha256:" + strings.Repeat("b", 64)
	for name, c := range map[string]struct {
		cl     Client
		o      Observed
		status Status
		want   []string
	}{
		"the set's image": {sql, Observed{Type: "locksql", Instance: "crm", Image: sql.Pin.Image}, OK,
			[]string{"runs locksql " + v + ", the release of lea's locksql client " + v + " (protocol 1)"}},
		"the set's vault image": {sql, Observed{Type: "locksql", Instance: "crm", Image: sql.Pin.Variants["vault"]}, OK,
			[]string{"(vault image)"}},
		"another image, same release by its handshake": {sql, Observed{Type: "locksql", Instance: "crm", Image: other,
			Hello: &Hello{ProtocolMajor: 1, ConsoleVersion: strings.TrimPrefix(v, "v")}}, OK, []string{"(its console's handshake)"}},
		"an older lock": {sql, Observed{Type: "locksql", Instance: "crm", Image: other, Hello: &Hello{ProtocolMajor: 1, ConsoleVersion: "0.11.0"}}, Warn,
			[]string{"older than lea's locksql client", "brew upgrade lox, then lox add locksql crm --version " + v}},
		"a newer lock": {sql, Observed{Type: "locksql", Instance: "crm", Image: other, Hello: &Hello{ProtocolMajor: 1, ConsoleVersion: "9.0.0"}}, Warn,
			[]string{"newer than lea's locksql client", "lea update"}},
		// The incompatible pair: the console speaks another protocol major.
		"incompatible: a newer protocol": {sql, Observed{Type: "locksql", Instance: "crm", Image: other, Hello: &Hello{ProtocolMajor: 2, ConsoleVersion: "9.0.0"}}, Fail,
			[]string{"incompatible: it runs locksql v9.0.0 (its console's handshake), protocol 2", "speaks protocol 1", "every call of lea's agents is refused", "lea update"}},
		"incompatible: an older protocol": {sql, Observed{Type: "locksql", Instance: "crm", Hello: &Hello{ProtocolMajor: 3, ConsoleVersion: "0.0.1"}}, Fail,
			[]string{"incompatible", "brew upgrade lox, then lox add locksql crm --version " + v}},
		"unknown": {sql, Observed{Type: "locksql", Instance: "crm", Image: other, HelloErr: "no console answers"}, Warn,
			[]string{"cannot tell", "no console answers", "lox add locksql crm --version " + v}},
		"no image": {sql, Observed{Type: "locksql", Instance: "crm"}, Warn, []string{"pins no image"}},
		"a dev override": {Client{Pin: sql.Pin, Version: "v0.16.0"}, Observed{Type: "locksql", Instance: "crm", Image: sql.Pin.Image}, Warn,
			[]string{"newer than lea's locksql client v0.16.0", "--version v0.16.0"}},
		"a dev checkout": {Client{Pin: sql.Pin}, Observed{Type: "locksql", Instance: "crm", Image: sql.Pin.Image}, Warn,
			[]string{"a dev checkout", "remove LEA_LOCKSQL_REF"}},
	} {
		got := Check(c.cl, c.o)
		if got.Status != c.status || got.Subject != "lock crm (locksql)" {
			t.Errorf("%s: %+v", name, got)
		}
		for _, w := range c.want {
			if !strings.Contains(got.Detail, w) {
				t.Errorf("%s: %q lacks %q", name, got.Detail, w)
			}
		}
	}
	// Another Lock's image of the same digest is not this Lock's.
	if _, ok := sql.Pin.ImageOf(strings.Replace(sql.Pin.Image, "locksql@", "lockmq@", 1)); ok {
		t.Error("another repository's image passed")
	}
}

// fakeConsole serves one hello answer per connection on a socket in dir.
func fakeConsole(t *testing.T, dir, name, answer string) {
	t.Helper()
	ln, err := net.Listen("unix", filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				line, _ := bufio.NewReader(c).ReadString('\n')
				var req struct {
					Method string         `json:"method"`
					Params map[string]any `json:"params"`
				}
				if json.Unmarshal([]byte(line), &req) != nil || req.Method != "hello" || req.Params["protocol_major"] != float64(1) {
					c.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"bad"}}` + "\n"))
					return
				}
				c.Write([]byte(answer + "\n"))
			}()
		}
	}()
}

func TestAskHello(t *testing.T) {
	dir, err := os.MkdirTemp("", "lh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	fakeConsole(t, dir, "a-uat.sock", `{"jsonrpc":"2.0","id":1,"result":{"protocol_major":1,"console_version":"0.17.0","profile":"uat"}}`)
	fakeConsole(t, dir, "b-prod.sock", `{"jsonrpc":"2.0","id":1,"error":{"code":-32001,"message":"protocol mismatch: use the same locksql version"}}`)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644)
	old := peerUID
	t.Cleanup(func() { peerUID = old })
	peerUID = func(net.Conn) (int, error) { return 10001, nil }
	rs, err := AskHello(dir, nil, 10001, 2*time.Second)
	if err != nil || len(rs) != 2 {
		t.Fatalf("%v %+v", err, rs)
	}
	if rs[0].Hello == nil || rs[0].Hello.ProtocolMajor != 1 || rs[0].Hello.ConsoleVersion != "0.17.0" || rs[0].Hello.Profile != "uat" {
		t.Errorf("a: %+v", rs[0])
	}
	if rs[1].Hello != nil || !strings.Contains(rs[1].Err, "refused the handshake: protocol mismatch") {
		t.Errorf("b: %+v", rs[1])
	}
	// A socket another uid serves is never asked.
	rs, _ = AskHello(dir, nil, 10011, 2*time.Second)
	if rs[0].Hello != nil || !strings.Contains(rs[0].Err, "not by the console account (uid 10011)") {
		t.Errorf("other uid: %+v", rs[0])
	}
	if _, err := AskHello(t.TempDir(), nil, 10001, time.Second); err == nil || !strings.Contains(err.Error(), "no console socket") {
		t.Errorf("empty dir: %v", err)
	}
}

// The socket names are lockkit's: the projects directory's hash (or "user"), then the profile.
func TestSocketNames(t *testing.T) {
	ns := SocketNames("/run/locksql", "/home/u/projects", "dev")
	if len(ns) != 2 || !regexp.MustCompile(`^[0-9a-f]{8}-dev\.sock$`).MatchString(ns[0]) || ns[1] != "user-dev.sock" ||
		ns[0] != SocketNames("/run/locksql", "/home/u/projects/", "dev")[0] {
		t.Errorf("%q", ns)
	}
	long := strings.Repeat("p", 64)
	if n := SocketNames("/run/locksql/"+strings.Repeat("i", 40), "/x", long)[0]; !strings.Contains(n, "-~") || len(n) != 8+2+16+5 {
		t.Errorf("long: %q", n)
	}
}

// Named sockets are asked without listing the directory (a 2710 one cannot be listed); a name
// that is not a socket's is refused.
func TestAskHelloNamed(t *testing.T) {
	dir, err := os.MkdirTemp("", "lh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	fakeConsole(t, dir, "a-uat.sock", `{"jsonrpc":"2.0","id":1,"result":{"protocol_major":1,"console_version":"v0.8.0","profile":"uat"}}`)
	old := peerUID
	t.Cleanup(func() { peerUID = old })
	peerUID = func(net.Conn) (int, error) { return 10031, nil }
	rs, err := AskHello(dir, []string{"a-uat.sock", "b-x.sock"}, 10031, time.Second)
	if err != nil || len(rs) != 2 || rs[0].Hello == nil || rs[0].Hello.ConsoleVersion != "v0.8.0" || rs[1].Hello != nil || !strings.Contains(rs[1].Err, "no console answers") {
		t.Fatalf("%v %+v", err, rs)
	}
	for _, bad := range []string{"../x.sock", "a/b.sock", "x"} {
		if _, err := AskHello(dir, []string{bad}, 10031, time.Second); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
