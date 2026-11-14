package launcher

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The Go copies of the proxy's lists are the proxy's (#345): lea egress list and allow say what
// the proxy does.
func TestEgressListsMatchTheProxy(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "proxy", "render-squid-conf.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string][]string{"EGRESS_DEFAULT": egressDefault, "EGRESS_ALWAYS": egressAlways, "DOH_NAMES": egressDoH} {
		m := regexp.MustCompile(`(?s)\n` + name + `="([^"]*)"`).FindSubmatch(b)
		if m == nil {
			t.Fatalf("no %s in render-squid-conf.sh", name)
		}
		if want := strings.Fields(string(m[1])); !slices.Equal(got, want) {
			t.Errorf("%s: Go has %v, the proxy %v", name, got, want)
		}
	}
}

// A domain is checked as LEA_EGRESS_ALLOW checks its entries, and what the proxy refuses whatever
// the list says is refused up front.
func TestCheckEgressDomain(t *testing.T) {
	for in, want := range map[string]string{"deb.debian.org": "deb.debian.org", "Deb.Debian.ORG": "deb.debian.org", "a-b.example": "a-b.example"} {
		if got, err := checkEgressDomain(in); err != nil || got != want {
			t.Errorf("%q: %q, %v", in, got, err)
		}
	}
	for in, msg := range map[string]string{
		"https://deb.debian.org/debian": "not a domain name", "deb.debian.org:443": "not a domain name",
		"1.2.3.4": "not a domain name", "[2001:db8::1]": "not a domain name", "*.debian.org": "no wildcard", "*": "no wildcard",
		"default": "lea's default list", ".debian.org": "not a domain name", "a..b": "not a domain name", "a,b": "not a domain name",
		"a b": "not a domain name", "": "not a domain name", "dns.google": "DNS-over-HTTPS", "x.cloudflare-dns.com": "DNS-over-HTTPS",
		"doh.example.com": "DNS-over-HTTPS", "a.doh.example": "DNS-over-HTTPS",
	} {
		if _, err := checkEgressDomain(in); err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("%q: %v, want %q", in, err, msg)
		}
	}
}

// allow adds a domain once, keeps "default" (an empty list is lea's default one), never touches
// '*', and is held to the cap of LEA_EGRESS_ALLOW; deny removes only an entry of the list.
func TestEgressWithAndWithout(t *testing.T) {
	for _, tc := range []struct{ cur, d, want string }{
		{"", "deb.debian.org", "default,deb.debian.org"},
		{"default", "deb.debian.org", "default,deb.debian.org"},
		{"github.com", "deb.debian.org", "github.com,deb.debian.org"},
	} {
		if got, added, err := egressWith(tc.cur, tc.d); err != nil || !added || got != tc.want {
			t.Errorf("with(%q, %q) = %q %v %v, want %q", tc.cur, tc.d, got, added, err, tc.want)
		}
	}
	for _, tc := range []struct{ cur, d string }{
		{"", "api.github.com"}, {"github.com", "api.github.com"}, {"*", "deb.debian.org"}, {"github.com", "api.anthropic.com"},
	} {
		if got, added, err := egressWith(tc.cur, tc.d); err != nil || added || got != tc.cur {
			t.Errorf("with(%q, %q) = %q %v %v: should be already allowed", tc.cur, tc.d, got, added, err)
		}
	}
	full := ""
	for i := range maxEgressDomains {
		full += "," + string(rune('a'+i%26)) + strings.Repeat("x", i/26+1) + ".example"
	}
	full = strings.TrimPrefix(full, ",")
	if _, _, err := egressWith(full, "one-more.example"); err == nil || !strings.Contains(err.Error(), "at most 64") {
		t.Errorf("past the cap: %v", err)
	}
	for _, tc := range []struct{ cur, d, want string }{
		{"default,deb.debian.org", "deb.debian.org", "default"},
		{"github.com,deb.debian.org", "github.com", "deb.debian.org"},
	} {
		if got, err := egressWithout(tc.cur, tc.d); err != nil || got != tc.want {
			t.Errorf("without(%q, %q) = %q %v, want %q", tc.cur, tc.d, got, err, tc.want)
		}
	}
	for _, tc := range []struct{ cur, d, msg string }{
		{"", "github.com", "lea's default list allows it"},
		{"default", "api.anthropic.com", "always-allowed list (the model APIs) allows it"},
		{"github.com", "api.github.com", "LEA_EGRESS_ALLOW (github.com) allows it"},
		{"github.com", "deb.debian.org", "not in the egress allowlist"},
		{"deb.debian.org", "deb.debian.org", "only entry"},
		{"*", "deb.debian.org", "'*' allows any domain"},
	} {
		if _, err := egressWithout(tc.cur, tc.d); err == nil || !strings.Contains(err.Error(), tc.msg) {
			t.Errorf("without(%q, %q): %v, want %q", tc.cur, tc.d, err, tc.msg)
		}
	}
}

func userConf(t *testing.T, w *world) (string, os.FileMode) {
	t.Helper()
	p := filepath.Join(w.env["XDG_CONFIG_HOME"], "lea", "lea.conf")
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	return string(b), fi.Mode()
}

// lea egress allow writes LEA_EGRESS_ALLOW back to lea.conf (the other lines kept, private) and
// applies it to the running proxy with egress-apply.sh, without restarting it; deny takes it back.
func TestEgressAllowPersistsAndAppliesLive(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "# mine\nLEA_GIT_SSH='github.com:22'\n")
	w.env["FAKE_DOCKER_OUT_INSPECT__F"] = "true"
	if rc := w.launcher().Main([]string{"egress", "allow", "Deb.Debian.org"}); rc != 0 {
		t.Fatalf("rc %d: %s", rc, w.errb.String())
	}
	conf, mode := userConf(t, w)
	if conf != "# mine\nLEA_GIT_SSH='github.com:22'\nLEA_EGRESS_ALLOW='default,deb.debian.org'\n" || mode.Perm() != 0o600 {
		t.Fatalf("lea.conf %v:\n%s", mode, conf)
	}
	if !w.hasCall("docker [exec] [-u] [0] [-e] [LEA_EGRESS_ALLOW=default,deb.debian.org] [lea-proxy] [/usr/local/bin/egress-apply.sh]") {
		t.Fatalf("not applied live:\n%s", strings.Join(w.calls(), "\n"))
	}
	if w.hasCall("docker [run]") || w.hasCall("docker [rm]") || w.hasCall("[restart]") || w.hasCall("[stop]") {
		t.Fatalf("a container was restarted:\n%s", strings.Join(w.calls(), "\n"))
	}
	if !strings.Contains(w.errb.String(), "applied live to lea-proxy") {
		t.Fatalf("stderr: %s", w.errb.String())
	}
	// A second allow of a covered name changes nothing.
	os.WriteFile(w.log, nil, 0o644)
	if rc := w.launcher().Main([]string{"egress", "allow", "security.deb.debian.org"}); rc != 0 || w.hasCall("docker [exec]") {
		t.Fatalf("rc %d, calls %v", rc, w.calls())
	}
	if rc := w.launcher().Main([]string{"egress", "deny", "deb.debian.org"}); rc != 0 {
		t.Fatalf("deny rc %d: %s", rc, w.errb.String())
	}
	if conf, _ := userConf(t, w); !strings.Contains(conf, "LEA_EGRESS_ALLOW='default'\n") {
		t.Fatalf("lea.conf after deny:\n%s", conf)
	}
	if !w.hasCall("[LEA_EGRESS_ALLOW=default] [lea-proxy] [/usr/local/bin/egress-apply.sh]") {
		t.Fatalf("deny not applied live:\n%s", strings.Join(w.calls(), "\n"))
	}
}

// Without a running proxy, the list is saved for its next start; a proxy that cannot take it live
// fails the command with the way out, the list kept.
func TestEgressAllowWithoutALiveProxy(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["FAKE_DOCKER_OUT_INSPECT__F"] = "false"
	if rc := w.launcher().Main([]string{"egress", "allow", "deb.debian.org"}); rc != 0 || w.hasCall("docker [exec]") ||
		!strings.Contains(w.errb.String(), "not running: the next lea starts it with this list") {
		t.Fatalf("rc %d: %s %v", rc, w.errb.String(), w.calls())
	}
	w.env["FAKE_DOCKER_OUT_INSPECT__F"] = "true"
	w.env["FAKE_DOCKER_RC_EXEC__U"] = "1"
	if rc := w.launcher().Main([]string{"egress", "allow", "pkg.example"}); rc == 0 || !strings.Contains(w.errb.String(), "lea stop --all, then lea") {
		t.Fatalf("rc %d: %s", rc, w.errb.String())
	}
	if conf, _ := userConf(t, w); !strings.Contains(conf, "LEA_EGRESS_ALLOW='default,deb.debian.org,pkg.example'\n") {
		t.Fatalf("lea.conf:\n%s", conf)
	}
}

// Invalid input, '*' and a symbolic link as lea.conf leave the configuration alone.
func TestEgressAllowRefusals(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_EGRESS_ALLOW='*'\n")
	if rc := w.launcher().Main([]string{"egress", "allow", "deb.debian.org"}); rc != 0 || !strings.Contains(w.errb.String(), "already allows any domain") {
		t.Fatalf("rc %d: %s", rc, w.errb.String())
	}
	if conf, _ := userConf(t, w); conf != "LEA_EGRESS_ALLOW='*'\n" {
		t.Fatalf("'*' was rewritten:\n%s", conf)
	}
	for _, a := range [][]string{{"egress", "allow", "1.2.3.4"}, {"egress", "allow", "https://x.example/"}, {"egress", "allow", "*.example"},
		{"egress", "allow", "dns.google"}, {"egress", "allow"}, {"egress"}, {"egress", "open"}} {
		if rc := w.launcher().Main(a); rc == 0 {
			t.Errorf("%v: rc 0", a)
		}
	}
	if w.hasCall("docker [exec]") {
		t.Fatal("a refused allow reached the proxy")
	}
	w2 := newWorld(t, repoRoot(t))
	dir := filepath.Join(w2.env["XDG_CONFIG_HOME"], "lea")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(w2.dir, "elsewhere"), nil, 0o600)
	os.Symlink(filepath.Join(w2.dir, "elsewhere"), filepath.Join(dir, "lea.conf"))
	if rc := w2.launcher().Main([]string{"egress", "allow", "deb.debian.org"}); rc == 0 || !strings.Contains(w2.errb.String(), "symbolic link") {
		t.Fatalf("rc %d: %s", rc, w2.errb.String())
	}
	if b, _ := os.ReadFile(filepath.Join(w2.dir, "elsewhere")); len(b) != 0 {
		t.Fatalf("the link was followed: %s", b)
	}
}

// A running proxy whose labels differ in the allowlist alone takes the list live at the next lea
// (no recreation, the agents keep their connections), or keeps the one lea egress allow gave it;
// one that cannot take it is replaced as before.
func TestProxyTakesALiveAllowlistChange(t *testing.T) {
	for name, tc := range map[string]struct {
		src, applyRC   string
		apply, replace bool
	}{
		"already applied live": {src: "github.com,deb.debian.org"},
		"applied now":          {src: "github.com", apply: true},
		"cannot apply":         {src: "github.com", applyRC: "1", apply: true, replace: true},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t, repoRoot(t))
			writeUserConf(t, w, "LEA_EGRESS_ALLOW=github.com,deb.debian.org\n")
			w.env["FAKE_DOCKER_OUT_INSPECT__F"] = "true"
			rules := w.launcher().proxyRules(proxyConfine)
			w.env["FAKE_DOCKER_OUT_INSPECT___FORMAT"] = "|github.com@" + rules
			w.env["FAKE_DOCKER_OUT_CONTAINER_INSPECT"] = "github.com"
			w.env["FAKE_DOCKER_OUT_EXEC_LEA_PROXY"] = tc.src
			if tc.applyRC != "" {
				w.env["FAKE_DOCKER_RC_EXEC__U"] = tc.applyRC
			}
			if err := w.launcher().ensureProxy(w.proj); err != nil {
				t.Fatal(err)
			}
			if got := w.hasCall("[LEA_EGRESS_ALLOW=github.com,deb.debian.org] [lea-proxy] [/usr/local/bin/egress-apply.sh]"); got != tc.apply {
				t.Errorf("applied live: %v, want %v", got, tc.apply)
			}
			if got := w.hasCall("docker [run]"); got != tc.replace {
				t.Errorf("replaced: %v, want %v:\n%s", got, tc.replace, strings.Join(w.calls(), "\n"))
			}
		})
	}
}

// A change to or from '*' changes squid.conf itself: the proxy is replaced.
func TestProxyStarAllowlistIsReplaced(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_EGRESS_ALLOW='*'\n")
	w.env["FAKE_DOCKER_OUT_INSPECT__F"] = "true"
	rules := w.launcher().proxyRules(proxyConfine)
	w.env["FAKE_DOCKER_OUT_INSPECT___FORMAT"] = "|github.com@" + rules
	w.env["FAKE_DOCKER_OUT_CONTAINER_INSPECT"] = "github.com"
	if err := w.launcher().ensureProxy(w.proj); err != nil {
		t.Fatal(err)
	}
	if w.hasCall("egress-apply.sh") || !w.hasCall("docker [run]") {
		t.Fatalf("calls:\n%s", strings.Join(w.calls(), "\n"))
	}
}

// lea egress denied reads ERR_LEA_EGRESS refusals from the proxy's access log, by host, newest
// first; the other refusals (DoH, literals, private) cannot be allowed and are left out.
func TestParseEgressRefusals(t *testing.T) {
	log := strings.Join([]string{
		"1730000000.100 172.25.0.5 CONNECT deb.debian.org:443 403 - ERR_LEA_EGRESS",
		"1730000005.200 172.25.0.5 GET http://deb.debian.org/debian/dists/x 403 - ERR_LEA_EGRESS",
		"1730000003.000 172.25.0.9 CONNECT pkg.example:443 403 - ERR_LEA_EGRESS",
		"1730000004.000 172.25.0.5 CONNECT dns.google:443 403 - ERR_LEA_DOH",
		"1730000004.000 172.25.0.5 CONNECT 1.2.3.4:443 403 - ERR_LEA_LITERAL",
		"1730000004.000 172.25.0.5 CONNECT github.com:443 200 140.82.1.1 -",
		"lea-keyproxy: forge api.github.com \"GET\" \"/user\" 200",
		"garbage",
	}, "\n")
	rs := parseEgressRefusals(log)
	if len(rs) != 2 || rs[0].host != "deb.debian.org" || rs[0].count != 2 || rs[1].host != "pkg.example" ||
		rs[0].last.Unix() != 1730000005 || !slices.Equal(rs[1].clients, []string{"172.25.0.9"}) {
		t.Fatalf("%+v", rs)
	}
}

func TestEgressDenied(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["FAKE_DOCKER_OUT_INSPECT__F"] = "true"
	w.env["FAKE_DOCKER_OUT_LOGS___SINCE"] = "1730000000.100 172.25.0.5 CONNECT deb.debian.org:443 403 - ERR_LEA_EGRESS"
	w.env["FAKE_DOCKER_OUT_PS__Q"] = "abc"
	w.env["FAKE_DOCKER_OUT_INSPECT__F_AT_ABC"] = "/lea-0123 172.25.0.5"
	if rc := w.launcher().Main([]string{"egress", "denied", "--since", "2h"}); rc != 0 {
		t.Fatalf("rc %d: %s", rc, w.errb.String())
	}
	if got := w.out.String(); !strings.Contains(got, "deb.debian.org refused 1 time(s)") || !strings.Contains(got, "from lea-0123: lea egress allow deb.debian.org") {
		t.Fatalf("out: %q", got)
	}
	if !w.hasCall("docker [logs] [--since] [2h] [lea-proxy]") {
		t.Fatalf("calls: %v", w.calls())
	}
}

// lea web reads the refusals as data (#415): who asked, and what allows a host now.
func TestEgressRefused(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_EGRESS_ALLOW=default,deb.debian.org\n")
	w.env["FAKE_DOCKER_OUT_INSPECT__F"] = "true"
	w.env["FAKE_DOCKER_OUT_LOGS___SINCE"] = "1730000000.100 172.25.0.5 CONNECT deb.debian.org:443 403 - ERR_LEA_EGRESS\n" +
		"1730000001.100 172.25.0.5 CONNECT pkg.example:443 403 - ERR_LEA_EGRESS"
	w.env["FAKE_DOCKER_OUT_PS__Q"] = "abc"
	w.env["FAKE_DOCKER_OUT_INSPECT__F_AT_ABC"] = "/lea-0123 172.25.0.5"
	rs, err := w.launcher().EgressRefused("24h")
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 2 || rs[0].Host != "pkg.example" || rs[0].AllowedBy != "" || rs[0].Last != 1730000001 ||
		!slices.Equal(rs[0].From, []string{"lea-0123"}) || rs[1].Host != "deb.debian.org" || rs[1].AllowedBy != "LEA_EGRESS_ALLOW (deb.debian.org)" {
		t.Fatalf("%+v", rs)
	}
}

func TestEgressList(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_EGRESS_ALLOW=default,deb.debian.org\n")
	w.env["FAKE_DOCKER_OUT_INSPECT__F"] = "true"
	w.env["FAKE_DOCKER_OUT_EXEC_LEA_PROXY"] = "default"
	if rc := w.launcher().Main([]string{"egress", "list"}); rc != 0 {
		t.Fatalf("rc %d: %s", rc, w.errb.String())
	}
	out := w.out.String()
	for _, want := range []string{"github.com", "lea's default list", "deb.debian.org", "LEA_EGRESS_ALLOW", "anthropic.com", "always",
		"the running lea-proxy applies LEA_EGRESS_ALLOW='default'"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// No path from an agent reaches the live allowlist (#345): only the launcher's egress command
// runs the proxy's script, and the launcher stays on the host with LEA_CTL=1. (cmd/lea checks
// that the launcher refuses to run inside lea; internal/mcpserver that no tool names egress.)
func TestOnlyTheHostLauncherChangesTheAllowlist(t *testing.T) {
	root := repoRoot(t)
	var users []string
	for _, dir := range []string{"cmd", "internal", "image"} {
		filepath.WalkDir(filepath.Join(root, dir), func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || strings.HasSuffix(p, "_test.go") || strings.Contains(p, "testdata") {
				return nil
			}
			if b, err := os.ReadFile(p); err == nil && strings.Contains(string(b), "egress-apply.sh") {
				rel, _ := filepath.Rel(root, p)
				users = append(users, rel)
			}
			return nil
		})
	}
	// net.go only hashes the script into the proxy's rules digest (proxyRules).
	if !slices.Equal(users, []string{"internal/launcher/egress.go", "internal/launcher/net.go"}) {
		t.Fatalf("egress-apply.sh is named by %v, want the launcher's egress.go and net.go only", users)
	}
	if !ctlHostOnly["egress"] {
		t.Fatal("lea egress would run in lea-ctl with LEA_CTL=1")
	}
}
