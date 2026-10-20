package providers

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func keyFile(t *testing.T, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(p, []byte("sk-test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func parse(t *testing.T, text string, o Options) ([]Provider, error) {
	t.Helper()
	if o.Internal == nil {
		o.Internal = Hosts{}
	}
	return Parse(strings.NewReader(text), o)
}

func TestParseReadsEndpoints(t *testing.T) {
	k := keyFile(t, 0o600)
	internal := ParseHosts(strings.NewReader("10.0.0.5 11434\n# comment\nollama.lan 11434,8000\n"))
	ps, err := parse(t, "# my endpoints\n\nollama http://10.0.0.5:11434/v1 model=qwen3:32b\n"+
		"lan http://ollama.lan:8000/v1 key="+k+"\nvllm https://vllm.example.com/v1 key="+k+" model=meta-llama/Llama-3.3-70B-Instruct\n",
		Options{Internal: internal, UID: os.Getuid()})
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 3 || ps[0].Name != "ollama" || ps[0].CLI != Codex || ps[0].Model != "qwen3:32b" || ps[0].Endpoint.KeyFile != "" ||
		ps[1].Endpoint.KeyFile != k || ps[2].Model != "meta-llama/Llama-3.3-70B-Instruct" || ps[2].Endpoint.Port() != 443 {
		t.Fatalf("%+v", ps)
	}
}

// The egress rules: no private, loopback, link-local or local upstream unless internal-hosts
// lists it with that port; no wildcard, user info, query or IPv6; a key only over https or to a
// listed host.
func TestParseRefusesUnsafeEndpoints(t *testing.T) {
	k := keyFile(t, 0o600)
	internal := ParseHosts(strings.NewReader("10.0.0.5 11434\n"))
	for _, line := range []string{
		"a http://127.0.0.1:11434/v1",
		"a http://localhost:11434/v1",
		"a http://lea-proxy:4400/v1",
		"a http://169.254.169.254/latest",
		"a http://192.168.1.10:11434/v1",
		"a http://10.0.0.5:8000/v1",         // listed, but not on that port
		"a http://10.0.0.5/v1",              // port 80 is not listed
		"a http://*.example.com/v1",         // wildcard
		"a https://user:pw@api.example.com", // user info
		"a https://api.example.com/v1?x=1",
		"a https://[::1]:8000/v1",
		"a ftp://api.example.com/v1",
		"a file:///etc/passwd",
		"a https://api.example.com/v1/../x",
		"a https://api.example.com/v%2f1",
		"a http://api.example.com/v1 key=" + k, // a key in clear text over the internet
		"a https://api.example.com:0/v1",
		"a https://api.example.com:99999/v1",
		"a https://api.example.com:080/v1",
	} {
		if _, err := parse(t, line+"\n", Options{Internal: internal, UID: -1}); err == nil {
			t.Errorf("accepted %q", line)
		}
	}
	if _, err := parse(t, "a http://10.0.0.5:11434/v1 key="+k+"\n", Options{Internal: internal, UID: -1}); err != nil {
		t.Errorf("a listed host may take a key over http: %v", err)
	}
}

func TestParseRefusesBadLines(t *testing.T) {
	for _, text := range []string{
		"claude https://a.example.com/v1\n", // reserved
		"codex https://a.example.com/v1\n",
		"kimi https://a.example.com/v1\n",
		"Bad https://a.example.com/v1\n",
		"a-very-long-provider-name https://a.example.com/v1\n",
		"a https://a.example.com/v1\na https://b.example.com/v1\n",
		"a\n",
		"a https://a.example.com/v1 model=-rf\n",
		"a https://a.example.com/v1 model=\n",
		"a https://a.example.com/v1 colour=blue\n",
		"a https://a.example.com/v1 model=x model=y\n",
		"a https://a.example.com/v1 key=relative/key\n",
		"a https://a.example.com/v1 x y z\n",
	} {
		if _, err := parse(t, text, Options{UID: -1}); err == nil {
			t.Errorf("accepted %q", text)
		}
	}
	var many strings.Builder
	for i := 0; i <= MaxEndpoints; i++ {
		many.WriteString("p" + strings.Repeat("x", i%10) + string(rune('a'+i)) + " https://a.example.com/v1\n")
	}
	if _, err := parse(t, many.String(), Options{UID: -1}); err == nil {
		t.Error("accepted more than MaxEndpoints")
	}
}

// A key file stays the user's own secret: never readable by others, never a symbolic link, never
// in the projects directory the agents read and write; the error never shows the key.
func TestCheckKeyFile(t *testing.T) {
	if err := CheckKeyFile(keyFile(t, 0o600), os.Getuid(), nil); err != nil {
		t.Fatal(err)
	}
	if err := CheckKeyFile(keyFile(t, 0o400), os.Getuid(), nil); err != nil {
		t.Fatal(err)
	}
	for _, m := range []os.FileMode{0o644, 0o640, 0o604, 0o660} {
		if err := CheckKeyFile(keyFile(t, m), os.Getuid(), nil); err == nil {
			t.Errorf("mode %o accepted", m)
		}
	}
	k := keyFile(t, 0o600)
	link := filepath.Join(t.TempDir(), "link")
	os.Symlink(k, link)
	if err := CheckKeyFile(link, os.Getuid(), nil); err == nil {
		t.Error("a symbolic link was accepted")
	}
	if err := CheckKeyFile(k, os.Getuid()+1, nil); err == nil {
		t.Error("another owner's file was accepted")
	}
	if err := CheckKeyFile(k, os.Getuid(), []string{filepath.Dir(k)}); err == nil {
		t.Error("a key in the projects directory was accepted")
	}
	if err := CheckKeyFile(filepath.Dir(k)+"/../"+filepath.Base(filepath.Dir(k))+"/key", os.Getuid(), nil); err == nil {
		t.Error("an unclean path was accepted")
	}
	empty := filepath.Join(t.TempDir(), "empty")
	os.WriteFile(empty, nil, 0o600)
	if err := CheckKeyFile(empty, os.Getuid(), nil); err == nil {
		t.Error("an empty key file was accepted")
	}
	big := filepath.Join(t.TempDir(), "big")
	os.WriteFile(big, []byte(strings.Repeat("k", MaxKeyBytes+1)), 0o600)
	if err := CheckKeyFile(big, os.Getuid(), nil); err == nil {
		t.Error("an oversized key file was accepted")
	}
	if err := CheckKeyFile("/nonexistent/sk-secret", os.Getuid(), nil); err == nil || strings.Contains(err.Error(), "sk-secret") {
		t.Errorf("err=%v", err)
	}
}

func TestReadKey(t *testing.T) {
	k := keyFile(t, 0o600)
	if got, err := ReadKey(k); err != nil || got != "sk-test" {
		t.Fatalf("%q %v", got, err)
	}
	bad := filepath.Join(t.TempDir(), "bad")
	os.WriteFile(bad, []byte("sk one\n"), 0o600)
	if _, err := ReadKey(bad); err == nil || strings.Contains(err.Error(), "sk one") {
		t.Fatalf("err=%v", err)
	}
}

func TestLoadWithoutAFile(t *testing.T) {
	r, err := Load(filepath.Join(t.TempDir(), "none"), Options{})
	if err != nil || !reflect.DeepEqual(r.Names(), []string{"claude", "codex"}) {
		t.Fatalf("%v %v", r, err)
	}
}

func TestResolve(t *testing.T) {
	r := Builtin()
	r.list = append(r.list, Provider{Name: "ollama-lan", CLI: Codex, Model: "qwen3", Endpoint: &Endpoint{}},
		Provider{Name: "bare", CLI: Codex, Endpoint: &Endpoint{}})
	for _, tc := range []struct {
		prov, model, defProv, defModel string
		want, wantModel                string
	}{
		{"", "", "", "", "claude", ""},
		{"", "", "codex", "gpt-5.1-codex", "codex", "gpt-5.1-codex"},
		{"", "opus", "codex", "gpt-5.1-codex", "codex", "opus"},
		{"claude", "", "codex", "gpt-5.1-codex", "claude", ""}, // LEA_MODEL is the default provider's
		{"ollama-lan", "", "", "", "ollama-lan", "qwen3"},
		{"ollama-lan", "llama3.3:70b", "", "", "ollama-lan", "llama3.3:70b"},
	} {
		p, m, err := r.Resolve(tc.prov, tc.model, tc.defProv, tc.defModel)
		if err != nil || p.Name != tc.want || m != tc.wantModel {
			t.Errorf("%+v: %s %s %v", tc, p.Name, m, err)
		}
	}
	for _, tc := range [][4]string{{"gemini", "", "", ""}, {"bare", "", "", ""}, {"", "--yolo", "", ""}, {"", "", "", "a b"},
		{strings.Repeat("x", 40), "", "", ""}, {"", "", "nope", ""}} {
		if _, _, err := r.Resolve(tc[0], tc[1], tc[2], tc[3]); err == nil {
			t.Errorf("accepted %q", tc)
		} else if strings.Contains(err.Error(), strings.Repeat("x", 40)) {
			t.Errorf("the error echoes an overlong name: %v", err)
		}
	}
}

func TestCheckModel(t *testing.T) {
	for _, m := range []string{"", "opus", "claude-opus-5-5", "opus[1m]", "gpt-5.1-codex", "qwen3:32b", "meta-llama/Llama-3.3-70B-Instruct", "a@b+c"} {
		if err := CheckModel(m); err != nil {
			t.Errorf("%q: %v", m, err)
		}
	}
	for _, m := range []string{"-m", "--yolo", "a b", `a"b`, "a\nb", "a=b", "a;b", "a$b", "a'b", "a\\b", strings.Repeat("a", MaxModelLen+1), "é"} {
		if err := CheckModel(m); err == nil {
			t.Errorf("accepted %q", m)
		}
	}
}

func TestArgvClaude(t *testing.T) {
	p, _ := Builtin().Get(Claude)
	got, err := p.Argv("dev-1", "/p", "opus", "", []string{"--debug"}, "go")
	if err != nil || !reflect.DeepEqual(got, []string{"--model", "opus", "--debug", "go"}) {
		t.Fatalf("%q %v", got, err)
	}
	const s = "0e5f7c2a-1b2c-4d5e-8f90-1234567890ab"
	got, _ = p.Argv("dev-1", "/p", "", s, nil, "go")
	if !reflect.DeepEqual(got, []string{"--resume", s}) {
		t.Fatalf("%q", got)
	}
	// A session that is not a UUID never reaches the command line: the prompt does.
	got, _ = p.Argv("dev-1", "/p", "", "--dangerously-skip-permissions", nil, "go")
	if !reflect.DeepEqual(got, []string{"go"}) {
		t.Fatalf("%q", got)
	}
}

func TestArgvCodex(t *testing.T) {
	p, _ := Builtin().Get(Codex)
	got, err := p.Argv("dev-1", "/home/u/projects", "gpt-5.1-codex", "", nil, "read your brief")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--sandbox", "workspace-write", "--ask-for-approval", "on-request",
		"-c", "check_for_update_on_startup=false",
		"-c", `mcp_servers.lea.command="/usr/local/bin/lea"`, "-c", `mcp_servers.lea.args=["mcp"]`,
		"-c", `mcp_servers.lea.env.LEA_AGENT="dev-1"`, "-c", `notify=["/usr/local/bin/lea","hook","codex","notify"]`,
		"-c", `projects."/home/u/projects".trust_level="untrusted"`,
		"--model", "gpt-5.1-codex", "read your brief"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	const s = "0e5f7c2a-1b2c-4d5e-8f90-1234567890ab"
	got, _ = p.Argv("dev-1", "/p", "", s, nil, "read your brief")
	if got[0] != "resume" || got[1] != s || got[len(got)-1] == "read your brief" {
		t.Fatalf("%q", got)
	}
	// A directory that cannot be a quoted TOML key is never trusted by lea.
	for _, wd := range []string{`/p/a"b`, `/p/a\b`, "/p/../q", "relative"} {
		got, _ = p.Argv("dev-1", wd, "", "", nil, "")
		for _, a := range got {
			if strings.HasPrefix(a, "projects.") {
				t.Errorf("%q trusted: %q", wd, a)
			}
		}
	}
	if _, err := p.Argv("Bad Name", "/p", "", "", nil, ""); err == nil {
		t.Error("an invalid agent name reached a -c option")
	}
	if _, err := p.Argv("dev-1", "/p", `x" -c y="z`, "", nil, ""); err == nil {
		t.Error("an unsafe model was accepted")
	}
}

func TestArgvEndpoint(t *testing.T) {
	p := Provider{Name: "ollama-lan", CLI: Codex, Endpoint: &Endpoint{}}
	got, err := p.Argv("dev-1", "/p", "qwen3", "", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	j := strings.Join(got, " ")
	for _, w := range []string{`model_provider="lea-ollama-lan"`, `model_providers.lea-ollama-lan.base_url="http://lea-proxy:4400/ollama-lan"`,
		`model_providers.lea-ollama-lan.wire_api="responses"`, "--model qwen3"} {
		if !strings.Contains(j, w) {
			t.Errorf("missing %s in %s", w, j)
		}
	}
	// Neither the upstream nor the key file ever reaches the agent's command line.
	if strings.Contains(j, "key") || strings.Contains(j, "env_key") {
		t.Errorf("a key reference in %s", j)
	}
}

// The forbidden directories are compared by their real paths: a key reached through a symbolic
// link to the projects directory, or a projects directory that is itself a link, is refused; so is
// a key with another hard link (it may sit in the projects directory).
func TestCheckKeyFileResolvesLinks(t *testing.T) {
	proj := t.TempDir()
	key := filepath.Join(proj, "k")
	os.WriteFile(key, []byte("sk\n"), 0o600)
	other := t.TempDir()
	viaLink := filepath.Join(other, "link")
	os.Symlink(proj, viaLink)
	if err := CheckKeyFile(filepath.Join(viaLink, "k"), os.Getuid(), []string{proj}); err == nil {
		t.Error("a key under a link to the projects directory was accepted")
	}
	projLink := filepath.Join(other, "proj-link")
	os.Symlink(proj, projLink)
	if err := CheckKeyFile(key, os.Getuid(), []string{projLink}); err == nil {
		t.Error("a key in a projects directory named through a link was accepted")
	}
	solo := filepath.Join(t.TempDir(), "solo")
	os.WriteFile(solo, []byte("sk\n"), 0o600)
	if err := CheckKeyFile(solo, os.Getuid(), []string{proj}); err != nil {
		t.Fatal(err)
	}
	os.Link(solo, filepath.Join(proj, "hard"))
	if err := CheckKeyFile(solo, os.Getuid(), []string{proj}); err == nil {
		t.Error("a key with a second hard link was accepted")
	}
}

// Parse keeps a key file's real path: the launcher mounts that one.
func TestParseKeepsTheKeysRealPath(t *testing.T) {
	k := keyFile(t, 0o600)
	link := filepath.Join(t.TempDir(), "dir")
	os.Symlink(filepath.Dir(k), link)
	ps, err := parse(t, "v https://v.example.com/v1 key="+filepath.Join(link, "key")+"\n", Options{UID: os.Getuid()})
	if err != nil || ps[0].Endpoint.KeyFile != k {
		t.Fatalf("%+v %v", ps, err)
	}
}

func TestUsageLimit(t *testing.T) {
	for _, c := range []struct {
		cli, kind string
		want      bool
	}{
		{Claude, "rate_limit", true},
		{Claude, "overloaded", false},
		{Claude, "authentication_failed", false},
		{Claude, "", false},
		{Claude, "Rate_limit", false},
		{Codex, "rate_limit", false},
		{"", "rate_limit", false},
	} {
		if got := UsageLimit(c.cli, c.kind); got != c.want {
			t.Errorf("UsageLimit(%q, %q) = %v, want %v", c.cli, c.kind, got, c.want)
		}
	}
}
