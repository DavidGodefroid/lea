package launcher

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sshKey is a fresh ed25519 public key in known_hosts form: "ssh-ed25519 <base64>".
func sshKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var b []byte
	for _, s := range [][]byte{[]byte("ssh-ed25519"), pub} {
		b = binary.BigEndian.AppendUint32(b, uint32(len(s)))
		b = append(b, s...)
	}
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(b)
}

func (w *world) confDir() string  { return w.dir + "/config/lea" }
func (w *world) userConf() string { return w.confDir() + "/lea.conf" }

func (w *world) writeConf(name, s string) string {
	w.t.Helper()
	p := w.confDir() + "/" + name
	if err := os.MkdirAll(w.confDir(), 0o700); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		w.t.Fatal(err)
	}
	return p
}

func (w *world) read(p string) string {
	w.t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		w.t.Fatal(err)
	}
	return string(b)
}

// config runs `lea config ARGS` with stdin and returns the status, stdout and stderr.
func (w *world) config(stdin string, args ...string) (int, string, string) {
	w.t.Helper()
	l := w.launcher()
	l.Stdin = strings.NewReader(stdin)
	w.out.Reset()
	w.errb.Reset()
	code := l.Main(append([]string{"config"}, args...))
	return code, w.out.String(), w.errb.String()
}

func (w *world) shareFile(s string) string {
	w.t.Helper()
	p := w.dir + "/team.json"
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		w.t.Fatal(err)
	}
	return p
}

// snapshot is every file under the configuration directory, by path, to check nothing changed.
func (w *world) snapshot() map[string]string {
	w.t.Helper()
	m := map[string]string{}
	filepath.Walk(w.dir+"/config", func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			b, _ := os.ReadFile(p)
			m[p] = string(b)
		}
		return nil
	})
	return m
}

func sameSnapshot(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func decodeShare(t *testing.T, s string) shareFile {
	t.Helper()
	var f shareFile
	if err := json.Unmarshal([]byte(s), &f); err != nil {
		t.Fatalf("%v in %q", err, s)
	}
	return f
}

// Export: only allow-listed keys the user config sets, the two files by content, never a path,
// LEA_PROJECTS_DIR or a personal key; unknown keys and path values are skipped with a warning.
func TestConfigExportAllowList(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	k1 := sshKey(t)
	kh := w.writeConf("git_known_hosts", "# pinned\ngithub.com "+k1+" a comment\n@cert-authority * "+sshKey(t)+"\n")
	ih := w.writeConf("hosts", "# ours\ngit.corp.example 443,8443\n10.0.0.10 443 # db\n")
	w.writeConf("lea.conf", "LEA_PROJECTS_DIR='"+w.proj+"'\nLEA_GIT_SSH='github.com:22'\nLEA_LOCKSQL=0\nLEA_APPROVALS=pane\n"+
		"LEA_SOUND=0\nLEA_FOO=1\nLEA_LOCKSQL_REF='"+w.home+"/locksql'\nLEA_IMAGE=ghcr.io/corp/lea:1.2\n"+
		"LEA_GIT_KNOWN_HOSTS='"+kh+"'\nLEA_INTERNAL_HOSTS='"+ih+"'\n")
	code, out, errs := w.config("", "export")
	if code != 0 {
		t.Fatalf("export: %d %s", code, errs)
	}
	f := decodeShare(t, out)
	want := map[string]string{"LEA_GIT_SSH": "github.com:22", "LEA_LOCKSQL": "0", "LEA_APPROVALS": "pane", "LEA_IMAGE": "ghcr.io/corp/lea:1.2"}
	if f.Format != 1 || len(f.Keys) != len(want) {
		t.Fatalf("export = %+v", f)
	}
	for k, v := range want {
		if f.Keys[k] != v {
			t.Errorf("%s = %q, want %q", k, f.Keys[k], v)
		}
	}
	if f.Files["git_known_hosts"] != "github.com "+k1+"\n" {
		t.Errorf("git_known_hosts = %q", f.Files["git_known_hosts"])
	}
	if f.Files["internal-hosts"] != "git.corp.example 443,8443\n10.0.0.10 443\n" {
		t.Errorf("internal-hosts = %q", f.Files["internal-hosts"])
	}
	if strings.Contains(out, w.dir) {
		t.Errorf("a path was exported: %s", out)
	}
	for _, m := range []string{"LEA_FOO", "LEA_LOCKSQL_REF", "@cert-authority"} {
		if !strings.Contains(errs, m) {
			t.Errorf("no warning about %s in %q", m, errs)
		}
	}
	_, out, _ = w.config("", "export", "--include-personal")
	if decodeShare(t, out).Keys["LEA_SOUND"] != "0" {
		t.Errorf("--include-personal: %s", out)
	}
}

func TestConfigExportOutputFile(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.writeConf("lea.conf", "LEA_LOCKSQL=0\n")
	p := w.dir + "/out/team.json"
	if code, _, errs := w.config("", "export", "--output", p); code != 0 {
		t.Fatal(errs)
	}
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", fi, err)
	}
	if decodeShare(t, w.read(p)).Keys["LEA_LOCKSQL"] != "0" {
		t.Fatal(w.read(p))
	}
	os.Symlink(w.dir+"/elsewhere", w.dir+"/link.json")
	if code, _, errs := w.config("", "export", "--output", w.dir+"/link.json"); code == 0 || !strings.Contains(errs, "symbolic link") {
		t.Fatalf("symlinked output: %d %s", code, errs)
	}
}

// Anything outside the format, an invalid value or an injection attempt is refused with a clear
// message, and nothing is written.
func TestConfigImportRefuses(t *testing.T) {
	key := sshKey(t)
	cases := map[string]struct{ in, msg string }{
		"newer format":     {`{"lea_config_format":2,"keys":{}}`, "upgrade lea"},
		"no format":        {`{"keys":{}}`, "format"},
		"unknown field":    {`{"lea_config_format":1,"keys":{},"run":"x"}`, "unknown field"},
		"two documents":    {`{"lea_config_format":1,"keys":{}} {}`, "one JSON object"},
		"not shared key":   {`{"lea_config_format":1,"keys":{"LEA_PREFIX":"x"}}`, "LEA_PREFIX is not a shared key"},
		"projects dir":     {`{"lea_config_format":1,"keys":{"LEA_PROJECTS_DIR":"/tmp"}}`, "LEA_PROJECTS_DIR is not a shared key"},
		"approvals":        {`{"lea_config_format":1,"keys":{"LEA_APPROVALS":"both"}}`, "LEA_APPROVALS must be deck or pane"},
		"timeout":          {`{"lea_config_format":1,"keys":{"LEA_APPROVAL_TIMEOUT":"601"}}`, "LEA_APPROVAL_TIMEOUT"},
		"git ssh shell":    {`{"lea_config_format":1,"keys":{"LEA_GIT_SSH":"x; rm -rf ~"}}`, "LEA_GIT_SSH"},
		"subshell":         {`{"lea_config_format":1,"keys":{"LEA_K8S_NAMESPACE":"$(id)"}}`, "LEA_K8S_NAMESPACE"},
		"backquotes":       {"{\"lea_config_format\":1,\"keys\":{\"LEA_NESTED_NETWORKS\":\"`id`\"}}", "LEA_NESTED_NETWORKS"},
		"quote breakout":   {`{"lea_config_format":1,"keys":{"LEA_OTEL_UPSTREAM":"x'; id; '"}}`, "LEA_OTEL_UPSTREAM"},
		"new line":         {`{"lea_config_format":1,"keys":{"LEA_LOCKSQL":"1\nLEA_PREFIX=x"}}`, "one line"},
		"image option":     {`{"lea_config_format":1,"keys":{"LEA_IMAGE":"-v/:/host"}}`, "LEA_IMAGE"},
		"plain http":       {`{"lea_config_format":1,"keys":{"LEA_RELEASES_URL":"http://evil.example"}}`, "https"},
		"no releases url":  {`{"lea_config_format":1,"keys":{"LEA_RELEASES_URL":""}}`, "LEA_RELEASES_URL"},
		"locksql path":     {`{"lea_config_format":1,"keys":{"LEA_LOCKSQL_REF":"/home/x/locksql"}}`, "LEA_LOCKSQL_REF"},
		"unknown file":     {`{"lea_config_format":1,"keys":{},"files":{"lea.conf":"x"}}`, "lea.conf is not a shared file"},
		"ca marker":        {`{"lea_config_format":1,"keys":{},"files":{"git_known_hosts":"@cert-authority * ` + key + `\n"}}`, "git_known_hosts line 1"},
		"wildcard host":    {`{"lea_config_format":1,"keys":{},"files":{"git_known_hosts":"*.corp ` + key + `\n"}}`, "git_known_hosts line 1"},
		"bad key":          {`{"lea_config_format":1,"keys":{},"files":{"git_known_hosts":"h ssh-ed25519 AAAA\n"}}`, "git_known_hosts line 1"},
		"bad port":         {`{"lea_config_format":1,"keys":{},"files":{"internal-hosts":"ok.example 443\nh.example 70000\n"}}`, "internal-hosts line 2"},
		"bad internal ip":  {`{"lea_config_format":1,"keys":{},"files":{"internal-hosts":"10.0.0.300 443\n"}}`, "internal-hosts line 1"},
		"internal shell":   {`{"lea_config_format":1,"keys":{},"files":{"internal-hosts":"$(id) 443\n"}}`, "internal-hosts line 1"},
		"not json":         {`LEA_GIT_SSH=x`, "JSON"},
		"egress wildcard":  {`{"lea_config_format":1,"keys":{"LEA_EGRESS_ALLOW":"*.github.com"}}`, "LEA_EGRESS_ALLOW"},
		"releases plain http": {`{"lea_config_format":1,"keys":{"LEA_RELEASES_URL":"http://releases"}}`, "https"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t, repoRoot(t))
			w.writeConf("lea.conf", "LEA_LOCKSQL=1\n")
			before := w.snapshot()
			code, _, errs := w.config("", "import", w.shareFile(c.in), "--yes", "--allow-egress", "--allow-host-keys", "--allow-code")
			if code == 0 || !strings.Contains(errs, c.msg) {
				t.Fatalf("status %d, stderr %q, want %q", code, errs, c.msg)
			}
			if !sameSnapshot(before, w.snapshot()) {
				t.Fatal("a refused import wrote a file")
			}
		})
	}
}

// Merge keeps the local-only keys, backs the user config up, writes it private, and the
// launcher and the shell both read the imported values.
func TestConfigImportMerge(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.writeConf("lea.conf", "LEA_PROJECTS_DIR='"+w.proj+"'\nLEA_SOUND=0\nLEA_APPROVALS=deck\n")
	delete(w.env, "LEA_PROJECTS_DIR")
	in := w.shareFile(`{"lea_config_format":1,"lea_version":"v1.0.0","keys":{"LEA_APPROVALS":"pane","LEA_LOCKSQL":"0","LEA_APPROVAL_TIMEOUT":"120"}}`)
	code, out, errs := w.config("", "import", in, "--yes")
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	if !strings.Contains(out, "LEA_APPROVALS: deck -> pane") || !strings.Contains(out, "LEA_LOCKSQL: (unset) -> 0") {
		t.Errorf("no diff in %q", out)
	}
	if fi, err := os.Stat(w.userConf()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", fi, err)
	}
	if !strings.Contains(w.read(w.userConf()+".bak"), "LEA_APPROVALS=deck") {
		t.Error("no backup of the previous user config")
	}
	cfg, err := LoadConfig(w.getenv, w.root, w.proj)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"LEA_APPROVALS": "pane", "LEA_LOCKSQL": "0", "LEA_APPROVAL_TIMEOUT": "120",
		"LEA_PROJECTS_DIR": w.proj, "LEA_SOUND": "0"} {
		if cfg.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, cfg.Get(k), v)
		}
	}
	so, se, sc := shellEval(t, w.root, w.environ(), w.proj,
		`lea_load_config; printf '%s|%s|%s|%s' "$LEA_APPROVALS" "$LEA_LOCKSQL" "$LEA_APPROVAL_TIMEOUT" "$LEA_PROJECTS_DIR"`)
	if sc != 0 || so != "pane|0|120|"+w.proj {
		t.Errorf("the shell reads %q (%s)", so, se)
	}
	if !strings.Contains(errs, "lea stop --all") {
		t.Errorf("no note on when approvals apply: %q", errs)
	}
}

// Keys and lines that widen what the agents reach, pin host keys or choose the code lea runs
// need their own flag, --yes is not enough; --dry-run shows them and writes nothing.
func TestConfigImportSensitiveNeedsAFlag(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.writeConf("lea.conf", "LEA_LOCKSQL=1\n")
	key := sshKey(t)
	in := w.shareFile(`{"lea_config_format":1,"keys":{"LEA_GIT_SSH":"git.corp.example:22","LEA_IMAGE":"ghcr.io/corp/lea:1"},` +
		`"files":{"git_known_hosts":"git.corp.example ` + key + `\n","internal-hosts":"git.corp.example 443\n"}}`)
	before := w.snapshot()
	code, out, errs := w.config("", "import", in, "--dry-run")
	if code != 0 {
		t.Fatalf("dry run: %d %s", code, errs)
	}
	for _, m := range []string{"LEA_GIT_SSH: (unset) -> git.corp.example:22  [egress]", "LEA_IMAGE: (unset) -> ghcr.io/corp/lea:1  [code]",
		"+ git.corp.example " + key + "  [host key]", "+ git.corp.example 443  [egress]", "--allow-egress", "--allow-host-keys", "--allow-code"} {
		if !strings.Contains(out, m) {
			t.Errorf("dry run does not show %q:\n%s", m, out)
		}
	}
	if !sameSnapshot(before, w.snapshot()) {
		t.Fatal("--dry-run wrote a file")
	}
	code, _, errs = w.config("", "import", in, "--yes")
	if code == 0 || !strings.Contains(errs, "--allow-egress") || !strings.Contains(errs, "--allow-host-keys") || !strings.Contains(errs, "--allow-code") {
		t.Fatalf("--yes alone: %d %q", code, errs)
	}
	code, _, errs = w.config("", "import", in, "--yes", "--allow-egress", "--allow-code")
	if code == 0 || strings.Contains(errs, "--allow-egress") || !strings.Contains(errs, "--allow-host-keys") {
		t.Fatalf("without --allow-host-keys: %d %q", code, errs)
	}
	if !sameSnapshot(before, w.snapshot()) {
		t.Fatal("a refused import wrote a file")
	}
	if code, _, errs = w.config("", "import", in, "--yes", "--allow-egress", "--allow-code", "--allow-host-keys"); code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	for _, f := range []string{"git_known_hosts", "internal-hosts"} {
		if fi, err := os.Lstat(w.confDir() + "/" + f); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v", f, fi, err)
		}
	}
	cfg, err := LoadConfig(w.getenv, w.root, w.proj)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Get("LEA_INTERNAL_HOSTS") != w.confDir()+"/internal-hosts" || cfg.Get("LEA_GIT_KNOWN_HOSTS") != w.confDir()+"/git_known_hosts" {
		t.Errorf("internal hosts %q, known hosts %q", cfg.Get("LEA_INTERNAL_HOSTS"), cfg.Get("LEA_GIT_KNOWN_HOSTS"))
	}
	if !strings.Contains(w.read(w.confDir()+"/internal-hosts"), "git.corp.example 443\n") {
		t.Error(w.read(w.confDir() + "/internal-hosts"))
	}
	if !strings.Contains(errs, "lea init") {
		t.Errorf("no note to rerun lea init: %q", errs)
	}
}

// A host whose pinned key differs is a conflict, refused in merge mode even with
// --allow-host-keys; otherwise the files are a de-duplicated union.
func TestConfigImportKnownHostsConflictAndUnion(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	k1, k2, k3 := sshKey(t), sshKey(t), sshKey(t)
	w.writeConf("lea.conf", "")
	w.writeConf("git_known_hosts", "# mine\ngithub.com "+k1+"\n")
	flags := []string{"--yes", "--allow-host-keys"}
	in := w.shareFile(`{"lea_config_format":1,"keys":{},"files":{"git_known_hosts":"github.com ` + k2 + `\n"}}`)
	before := w.snapshot()
	code, _, errs := w.config("", append([]string{"import", in}, flags...)...)
	if code == 0 || !strings.Contains(errs, "github.com") || !strings.Contains(errs, "--replace") {
		t.Fatalf("conflict: %d %q", code, errs)
	}
	if !sameSnapshot(before, w.snapshot()) {
		t.Fatal("a conflict wrote a file")
	}
	in = w.shareFile(`{"lea_config_format":1,"keys":{},"files":{"git_known_hosts":"github.com ` + k1 + `\ngitlab.com ` + k3 + `\n"}}`)
	if code, _, errs = w.config("", append([]string{"import", in}, flags...)...); code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	got := w.read(w.confDir() + "/git_known_hosts")
	if got != "# mine\ngithub.com "+k1+"\ngitlab.com "+k3+"\n" {
		t.Errorf("union = %q", got)
	}
	if !strings.Contains(w.read(w.confDir()+"/git_known_hosts.bak"), "# mine") {
		t.Error("no backup of git_known_hosts")
	}
	in = w.shareFile(`{"lea_config_format":1,"keys":{},"files":{"git_known_hosts":"github.com ` + k2 + `\n"}}`)
	if code, _, errs = w.config("", append([]string{"import", in, "--replace"}, flags...)...); code != 0 {
		t.Fatalf("--replace: %d %s", code, errs)
	}
	if got := w.read(w.confDir() + "/git_known_hosts"); got != "github.com "+k2+"\n" {
		t.Errorf("replaced = %q", got)
	}
}

// --replace replaces the shared part only: shared keys the file does not set go, the local-only
// ones stay.
func TestConfigImportReplace(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.writeConf("lea.conf", "LEA_PROJECTS_DIR='"+w.proj+"'\nLEA_LOCKSQL=0\nexport LEA_APPROVALS=pane\nLEA_SOUND=0\n")
	in := w.shareFile(`{"lea_config_format":1,"keys":{"LEA_APPROVAL_TIMEOUT":"90"}}`)
	// Removing LEA_LOCKSQL=0 wires locksql again (the default): that needs --allow-lock.
	if code, _, errs := w.config("", "import", in, "--replace", "--yes"); code == 0 || !strings.Contains(errs, "--allow-lock") {
		t.Fatalf("without --allow-lock: %d %s", code, errs)
	}
	if code, _, errs := w.config("", "import", in, "--replace", "--yes", "--allow-lock"); code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	got := w.read(w.userConf())
	for _, gone := range []string{"LEA_LOCKSQL", "LEA_APPROVALS"} {
		if strings.Contains(got, gone+"=") {
			t.Errorf("%s kept: %q", gone, got)
		}
	}
	for _, kept := range []string{"LEA_PROJECTS_DIR='" + w.proj + "'", "LEA_SOUND=0", "LEA_APPROVAL_TIMEOUT='90'"} {
		if !strings.Contains(got, kept) {
			t.Errorf("%s missing: %q", kept, got)
		}
	}
}

// Without --yes, a non-terminal stdin is refused; a symlinked target is refused; an import that
// changes nothing writes nothing.
func TestConfigImportConfirmationAndTargets(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.writeConf("lea.conf", "LEA_LOCKSQL=1\n")
	in := w.shareFile(`{"lea_config_format":1,"keys":{"LEA_LOCKSQL":"0"}}`)
	before := w.snapshot()
	if code, _, errs := w.config("y\n", "import", in); code == 0 || !strings.Contains(errs, "--yes") {
		t.Fatalf("no terminal: %d %q", code, errs)
	}
	if !sameSnapshot(before, w.snapshot()) {
		t.Fatal("an unconfirmed import wrote a file")
	}
	same := w.shareFile(`{"lea_config_format":1,"keys":{"LEA_LOCKSQL":"1"}}`)
	if code, out, errs := w.config("", "import", same, "--yes"); code != 0 || !strings.Contains(out, "nothing to change") {
		t.Fatalf("no-op: %d %q %q", code, out, errs)
	}
	if !sameSnapshot(before, w.snapshot()) {
		t.Fatal("a no-op import wrote a file")
	}
	os.Rename(w.userConf(), w.dir+"/real.conf")
	os.Symlink(w.dir+"/real.conf", w.userConf())
	if code, _, errs := w.config("", "import", in, "--yes"); code == 0 || !strings.Contains(errs, "symbolic link") {
		t.Fatalf("symlinked lea.conf: %d %q", code, errs)
	}
	if w.read(w.dir+"/real.conf") != "LEA_LOCKSQL=1\n" {
		t.Fatal("wrote through the symlink")
	}
}

// A protected key set in the environment beats the file: the import says so.
func TestConfigImportWarnsAboutTheEnvironment(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["LEA_APPROVALS"] = "pane"
	in := w.shareFile(`{"lea_config_format":1,"keys":{"LEA_APPROVALS":"deck","LEA_LOCKSQL":"0"}}`)
	code, _, errs := w.config("", "import", in, "--yes")
	if code != 0 || !strings.Contains(errs, "LEA_APPROVALS is set in the environment") {
		t.Fatalf("%d %q", code, errs)
	}
}

// End to end: A exports, B (its own projects dir) imports; B then resolves A's shared keys, keeps
// its projects dir, and a second import changes nothing.
func TestConfigExportImportRoundTrip(t *testing.T) {
	a := newWorld(t, repoRoot(t))
	key := sshKey(t)
	a.writeConf("git_known_hosts", "git.corp.example "+key+"\n")
	ih := a.writeConf("internal-hosts", "git.corp.example 443\n")
	a.writeConf("lea.conf", "LEA_PROJECTS_DIR='"+a.proj+"'\nLEA_GIT_SSH='git.corp.example:22'\nLEA_APPROVALS=pane\n"+
		"LEA_LOCKSQL=0\nLEA_EGRESS_ALLOW='github.com,proxy.golang.org'\nLEA_K8S_NAMESPACE=team\nLEA_INTERNAL_HOSTS='"+ih+"'\n")
	code, team, errs := a.config("", "export")
	if code != 0 {
		t.Fatalf("export: %s", errs)
	}
	b := newWorld(t, repoRoot(t))
	b.writeConf("lea.conf", "LEA_PROJECTS_DIR='"+b.proj+"'\n")
	in := b.shareFile(team)
	all := []string{"import", in, "--yes", "--allow-egress", "--allow-host-keys", "--allow-code"}
	if code, _, errs := b.config("", all...); code != 0 {
		t.Fatalf("import: %s", errs)
	}
	ca, err := LoadConfig(a.getenv, a.root, a.proj)
	if err != nil {
		t.Fatal(err)
	}
	cb, err := LoadConfig(b.getenv, b.root, b.proj)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"LEA_GIT_SSH", "LEA_APPROVALS", "LEA_LOCKSQL", "LEA_EGRESS_ALLOW", "LEA_K8S_NAMESPACE"} {
		if ca.Get(k) != cb.Get(k) {
			t.Errorf("%s: A %q, B %q", k, ca.Get(k), cb.Get(k))
		}
	}
	if b.read(cb.Get("LEA_GIT_KNOWN_HOSTS")) != "git.corp.example "+key+"\n" || b.read(cb.Get("LEA_INTERNAL_HOSTS")) != "git.corp.example 443\n" {
		t.Error("the files did not come over")
	}
	if !strings.Contains(b.read(b.userConf()), "LEA_PROJECTS_DIR='"+b.proj+"'") {
		t.Error("B's projects dir changed")
	}
	before := b.snapshot()
	if code, out, errs := b.config("", all...); code != 0 || !strings.Contains(out, "nothing to change") {
		t.Fatalf("second import: %d %q %q", code, out, errs)
	}
	if !sameSnapshot(before, b.snapshot()) {
		t.Fatal("the second import wrote a file")
	}
}

func TestConfigUsage(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	for _, args := range [][]string{{}, {"nope"}, {"export", "--nope"}, {"import"}, {"import", "a", "b"}} {
		if code, _, errs := w.config("", args...); code != 2 || !strings.Contains(errs, "lea config export") {
			t.Errorf("%v: %d %q", args, code, errs)
		}
	}
}

// A new host may come with several key types; a host already pinned here gets no other key in a
// merge, whatever its type.
func TestConfigImportKnownHostsKeyTypes(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	k1, k2, k3 := sshKey(t), sshKey(t), sshKey(t)
	w.writeConf("git_known_hosts", "github.com "+k1+"\n")
	rsa := "ssh-rsa " + base64.StdEncoding.EncodeToString(append([]byte{0, 0, 0, 7}, "ssh-rsa"...))
	in := w.shareFile(`{"lea_config_format":1,"keys":{},"files":{"git_known_hosts":"gitlab.com ` + k2 + `\ngitlab.com ` + rsa + `\n"}}`)
	if code, _, errs := w.config("", "import", in, "--yes", "--allow-host-keys"); code != 0 {
		t.Fatalf("two key types of a new host: %d %s", code, errs)
	}
	before := w.snapshot()
	in = w.shareFile(`{"lea_config_format":1,"keys":{},"files":{"git_known_hosts":"github.com,gitlab.com ` + k3 + `\n"}}`)
	if code, _, errs := w.config("", "import", in, "--yes", "--allow-host-keys"); code == 0 || !strings.Contains(errs, "github.com, gitlab.com") {
		t.Fatalf("another key for pinned hosts: %d %q", code, errs)
	}
	if !sameSnapshot(before, w.snapshot()) {
		t.Fatal("a conflict wrote a file")
	}
}

// The key of older lea (LEA_GITLAB_SSH) goes when LEA_GIT_SSH is imported: it would set the
// endpoints otherwise. It is exported as LEA_GIT_SSH.
func TestConfigShareLegacyGitSSH(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.writeConf("lea.conf", "LEA_GITLAB_SSH='gitlab.com:22'\n")
	_, out, _ := w.config("", "export")
	if decodeShare(t, out).Keys["LEA_GIT_SSH"] != "gitlab.com:22" {
		t.Errorf("export: %s", out)
	}
	in := w.shareFile(`{"lea_config_format":1,"keys":{"LEA_GIT_SSH":""}}`)
	if code, _, errs := w.config("", "import", in, "--yes", "--allow-egress"); code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	if got := w.read(w.userConf()); strings.Contains(got, "LEA_GITLAB_SSH") || !strings.Contains(got, "LEA_GIT_SSH=''") {
		t.Errorf("lea.conf = %q", got)
	}
}

// From stdin, there is no terminal left to confirm on: --yes is needed. A key name never reaches
// the terminal as a control sequence.
func TestConfigImportStdinAndNames(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	if code, _, errs := w.config(`{"lea_config_format":1,"keys":{"LEA_LOCKSQL":"0"}}`, "import", "-"); code == 0 || !strings.Contains(errs, "--yes") {
		t.Fatalf("stdin without --yes: %d %q", code, errs)
	}
	if code, _, errs := w.config(`{"lea_config_format":1,"keys":{"LEA_LOCKSQL":"0"}}`, "import", "-", "--yes"); code != 0 {
		t.Fatalf("stdin: %d %q", code, errs)
	}
	code, _, errs := w.config(`{"lea_config_format":1,"keys":{"\u001b]0;x\u0007":"1"}}`, "import", "-", "--yes")
	if code == 0 || strings.ContainsAny(errs, "\x1b\x07") || !strings.Contains(errs, "is not a shared key") {
		t.Fatalf("escape in a key name: %d %q", code, errs)
	}
}

// --replace on internal-hosts shows the lines it drops.
func TestConfigImportReplaceShowsRemovedLines(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	ih := w.writeConf("internal-hosts", "old.example 443\nkept.example 443\n")
	w.writeConf("lea.conf", "LEA_INTERNAL_HOSTS='"+ih+"'\n")
	in := w.shareFile(`{"lea_config_format":1,"keys":{},"files":{"internal-hosts":"kept.example 443\nnew.example 8443\n"}}`)
	code, out, errs := w.config("", "import", in, "--replace", "--yes", "--allow-egress")
	if code != 0 || !strings.Contains(out, "- old.example 443") || !strings.Contains(out, "+ new.example 8443  [egress]") || strings.Contains(out, "kept.example") {
		t.Fatalf("%d %q %q", code, out, errs)
	}
	if got := w.read(ih); got != "kept.example 443\nnew.example 8443\n" {
		t.Errorf("internal-hosts = %q", got)
	}
}

// A hashed host name cannot be checked against the local pins: it is not shared. A host pinned
// here under a hashed name still conflicts with another key for it.
func TestConfigImportHashedHosts(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	k1, k2 := sshKey(t), sshKey(t)
	salt := []byte("0123456789abcdefghij")
	m := hmac.New(sha1.New, salt)
	m.Write([]byte("github.com"))
	hashed := "|1|" + base64.StdEncoding.EncodeToString(salt) + "|" + base64.StdEncoding.EncodeToString(m.Sum(nil))
	w.writeConf("git_known_hosts", hashed+" "+k1+"\n")
	before := w.snapshot()
	in := w.shareFile(`{"lea_config_format":1,"keys":{},"files":{"git_known_hosts":"` + hashed + ` ` + k2 + `\n"}}`)
	if code, _, errs := w.config("", "import", in, "--yes", "--allow-host-keys"); code == 0 || !strings.Contains(errs, "hashed host names") {
		t.Fatalf("hashed import: %d %q", code, errs)
	}
	in = w.shareFile(`{"lea_config_format":1,"keys":{},"files":{"git_known_hosts":"GitHub.com ` + k2 + `\n"}}`)
	if code, _, errs := w.config("", "import", in, "--yes", "--allow-host-keys"); code == 0 || !strings.Contains(errs, "github.com already pinned") {
		t.Fatalf("plain import over a hashed pin: %d %q", code, errs)
	}
	if !sameSnapshot(before, w.snapshot()) {
		t.Fatal("a refused import wrote a file")
	}
	_, out, errs := w.config("", "export")
	if _, ok := decodeShare(t, out).Files["git_known_hosts"]; ok || !strings.Contains(errs, "hashed host names") {
		t.Errorf("export of a hashed line: %s %q", out, errs)
	}
}

// Turning a locksql console on opens a lock tool to the agents: --yes is not enough. Turning it
// off needs nothing more.
func TestConfigImportLocksqlNeedsAllowLock(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.writeConf("lea.conf", "LEA_LOCKSQL=0\n")
	in := w.shareFile(`{"lea_config_format":1,"keys":{"LEA_LOCKSQL":"1"}}`)
	before := w.snapshot()
	if code, out, _ := w.config("", "import", in, "--dry-run"); code != 0 || !strings.Contains(out, "LEA_LOCKSQL: 0 -> 1  [lock tool]") || !strings.Contains(out, "--allow-lock") {
		t.Fatalf("dry run: %d %q", code, out)
	}
	if code, _, errs := w.config("", "import", in, "--yes", "--allow-egress", "--allow-code", "--allow-host-keys"); code == 0 || !strings.Contains(errs, "--allow-lock") {
		t.Fatalf("without --allow-lock: %d %q", code, errs)
	}
	if !sameSnapshot(before, w.snapshot()) {
		t.Fatal("a refused import wrote a file")
	}
	if code, _, errs := w.config("", "import", in, "--yes", "--allow-lock"); code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	in = w.shareFile(`{"lea_config_format":1,"keys":{"LEA_LOCKSQL":"0"}}`)
	if code, _, errs := w.config("", "import", in, "--yes"); code != 0 {
		t.Fatalf("turning it off: %d %s", code, errs)
	}
	// The lock tools of later lea (LEA_LOCKMQ, LEA_LOCKFILE) are not shared at all.
	in = w.shareFile(`{"lea_config_format":1,"keys":{"LEA_LOCKMQ":"1"}}`)
	if code, _, errs := w.config("", "import", in, "--yes", "--allow-lock"); code == 0 || !strings.Contains(errs, "LEA_LOCKMQ is not a shared key") {
		t.Fatalf("LEA_LOCKMQ: %d %q", code, errs)
	}
}

// An organisation's files, in use at their own paths, stay there when the import adds nothing to
// them; an import that adds lines copies them under ~/.config/lea with the additions.
func TestConfigImportKeepsTheOrganisationPaths(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	key := sshKey(t)
	org := w.dir + "/org"
	os.MkdirAll(org, 0o755)
	os.WriteFile(org+"/hosts", []byte("a.example 443\n"), 0o644)
	os.WriteFile(org+"/known_hosts", []byte("git.corp.example "+key+"\n"), 0o644)
	w.writeConf("lea.conf", "LEA_INTERNAL_HOSTS='"+org+"/hosts'\nLEA_GIT_KNOWN_HOSTS='"+org+"/known_hosts'\n")
	all := []string{"--yes", "--allow-egress", "--allow-host-keys"}
	in := w.shareFile(`{"lea_config_format":1,"keys":{},"files":{"internal-hosts":"a.example 443\n","git_known_hosts":"git.corp.example ` + key + `\n"}}`)
	before := w.snapshot()
	if code, out, errs := w.config("", append([]string{"import", in}, all...)...); code != 0 || !strings.Contains(out, "nothing to change") {
		t.Fatalf("nothing added: %d %q %q", code, out, errs)
	}
	if !sameSnapshot(before, w.snapshot()) {
		t.Fatal("an import that adds nothing wrote a file")
	}
	in = w.shareFile(`{"lea_config_format":1,"keys":{},"files":{"internal-hosts":"b.example 443\n","git_known_hosts":"git.corp.example ` + key + `\n"}}`)
	if code, _, errs := w.config("", append([]string{"import", in}, all...)...); code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	cfg, err := LoadConfig(w.getenv, w.root, w.proj)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Get("LEA_INTERNAL_HOSTS") != w.confDir()+"/internal-hosts" || w.read(cfg.Get("LEA_INTERNAL_HOSTS")) != "a.example 443\nb.example 443\n" {
		t.Errorf("internal hosts %q", cfg.Get("LEA_INTERNAL_HOSTS"))
	}
	if cfg.Get("LEA_GIT_KNOWN_HOSTS") != org+"/known_hosts" {
		t.Errorf("known hosts moved to %q although nothing was added", cfg.Get("LEA_GIT_KNOWN_HOSTS"))
	}
}
