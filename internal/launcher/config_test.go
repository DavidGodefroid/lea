package launcher

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testHome = "/home/u"

func TestParseValue(t *testing.T) {
	ok := map[string]string{
		``:                     ``,
		`lea:dev`:              `lea:dev`,
		`''`:                   ``,
		`""`:                   ``,
		`'a b'`:                `a b`,
		`'/tmp/it'\''s'`:       `/tmp/it's`,
		`"git.example.com:22"`: `git.example.com:22`,
		`a=b`:                  `a=b`,
		`x # trailing comment`: `x`,
		`'$HOME' `:             `$HOME`,
		`caf\é`:                `café`,
		`$HOME/x`:              testHome + `/x`,
		`${HOME}/x`:            testHome + `/x`,
		`"$HOME/x y"`:          testHome + `/x y`,
		`"${HOME}"`:            testHome,
		`~`:                    testHome,
		`~/x`:                  testHome + `/x`,
		`'~/x'`:                `~/x`,
		`$HOME`:                testHome,
	}
	for in, want := range ok {
		got, err := parseValue(in, testHome)
		if err != nil || got != want {
			t.Errorf("parseValue(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"`id`", `"a\b"`, `a b`, `'open`, `"open`, `a;b`, `$(id)`, `"$(id)"`, `$X/x`, `${X:-y}`,
		`"$USER"`, `$HOMEX`, `${HOME`, `$`, `a~b`, `~user/x`, `x\`, "a\rb", `a|b`, `a&b`, `a(b`, `$((1))`} {
		if v, err := parseValue(in, testHome); err == nil {
			t.Errorf("parseValue(%q) = %q, want an error", in, v)
		}
	}
}

func TestConfigRefusesShellSyntax(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	for _, line := range []string{"if true; then X=1; fi", "LEA_IMAGE=${X:-y}", "lea_x() { :; }", ". ./other.conf", "LEA_IMAGE=a b",
		"LEA_IMAGE=$(id)", "LEA_IMAGE=`id`", "LEA_IMAGE=a;b"} {
		writeUserConf(t, w, "LEA_PREFIX=lea\n"+line+"\n")
		_, err := LoadConfig(w.getenv, w.root, w.proj)
		if err == nil || !strings.Contains(err.Error(), "line 2") || !strings.Contains(err.Error(), "rewrite the line as KEY='value'") ||
			strings.Contains(err.Error(), "LEA_LAUNCHER") || strings.Contains(err.Error(), "never runs") {
			t.Errorf("%q: err = %v", line, err)
		}
	}
}

func TestConfigExpandsHome(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	delete(w.env, "LEA_PROJECTS_DIR")
	writeUserConf(t, w, "LEA_PROJECTS_DIR=~/p\nLEA_IMAGE=\"$HOME/i\"\nLEA_PROXY_IMAGE=${HOME}/x\n")
	c, err := LoadConfig(w.getenv, w.root, w.proj)
	if err != nil {
		t.Fatal(err)
	}
	if c.Get("LEA_PROJECTS_DIR") != w.home+"/p" || c.Get("LEA_IMAGE") != w.home+"/i" || c.Get("LEA_PROXY_IMAGE") != w.home+"/x" {
		t.Fatalf("got %q %q %q", c.Get("LEA_PROJECTS_DIR"), c.Get("LEA_IMAGE"), c.Get("LEA_PROXY_IMAGE"))
	}
}

func TestConfigValidatesLikeTheShell(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	cases := map[string]string{
		"LEA_APPROVALS=popup\n":      "LEA_APPROVALS must be deck or pane, not 'popup'",
		"LEA_APPROVAL_TIMEOUT=601\n": "LEA_APPROVAL_TIMEOUT must be a number of seconds from 1 to 600 without leading zeros, not '601'",
		"LEA_APPROVAL_TIMEOUT=060\n": "not '060'",
		"LEA_AUTOUPDATE=yes\n":       "LEA_AUTOUPDATE must be 1 (check for a new release every day) or 0, not 'yes'",
		"LEA_AUTOUPDATE_SKIP=v1.2\n": "LEA_AUTOUPDATE_SKIP must be empty or a release version (vX.Y.Z, set by lea rollback), not 'v1.2'",
	}
	for conf, want := range cases {
		writeUserConf(t, w, conf)
		if _, err := LoadConfig(w.getenv, w.root, w.proj); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", conf, err, want)
		}
	}
}

func TestConfigEnvironmentAndFiles(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_APPROVALS=pane\nLEA_PREFIX=mine\nLEA_CLIPBOARD=0\n")
	w.env["LEA_APPROVALS"] = "deck"
	w.env["LEA_PREFIX"] = "env"
	w.env["LEA_CLIPBOARD"] = "1"
	c, err := LoadConfig(w.getenv, w.root, w.proj)
	if err != nil {
		t.Fatal(err)
	}
	if c.Get("LEA_APPROVALS") != "deck" || c.Get("LEA_PREFIX") != "mine" {
		t.Fatalf("APPROVALS=%q PREFIX=%q", c.Get("LEA_APPROVALS"), c.Get("LEA_PREFIX"))
	}
	env := strings.Join(c.Environ([]string{"LEA_CLIPBOARD=1", "LEA_PREFIX=env", "OTHER=x"}), "\n")
	for _, want := range []string{"LEA_CLIPBOARD=0", "LEA_PREFIX=mine", "OTHER=x", "LEA_APPROVALS=deck"} {
		if !strings.Contains(env+"\n", want+"\n") {
			t.Errorf("Environ lacks %s:\n%s", want, env)
		}
	}
	if strings.Contains(env, "LEA_PREFIX=env") {
		t.Errorf("the file's LEA_PREFIX must replace the environment's:\n%s", env)
	}
}

func TestExplicitMissingConfigIsAnError(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["LEA_CONFIG"] = w.dir + "/nope.conf"
	if _, err := LoadConfig(w.getenv, w.root, w.proj); err == nil || !strings.Contains(err.Error(), "nope.conf") {
		t.Fatalf("err = %v", err)
	}
}

func writeUserConf(t *testing.T, w *world, s string) {
	t.Helper()
	p := filepath.Join(w.env["XDG_CONFIG_HOME"], "lea", "lea.conf")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

// LEA_GIT_SSH lists git SSH endpoints; the keys of lea before any forge could be used
// (LEA_GITLAB_SSH, LEA_GITLAB_KNOWN_HOSTS) are read when the new ones are unset.
func TestGitSSHEndpoints(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	conf := filepath.Join(w.env["XDG_CONFIG_HOME"], "lea")
	for file, want := range map[string]string{
		"": "",
		"LEA_GIT_SSH='github.com:22  gitlab.com:22'\n": "github.com:22 gitlab.com:22",
		"LEA_GITLAB_SSH=gitlab.example.org:22\n":       "gitlab.example.org:22",
		"LEA_GITLAB_SSH=a:22\nLEA_GIT_SSH=b:2222\n":    "b:2222",
	} {
		writeUserConf(t, w, file)
		c, err := LoadConfig(w.getenv, w.root, w.proj)
		if err != nil || c.GitSSH() != want {
			t.Errorf("%q: %v, want %q", file, err, want)
		}
	}
	for _, bad := range []string{"bad host:22", "h:0", "h:65536", "h:", ":22", "h:022", "h_x:22", "a:22 a:23"} {
		writeUserConf(t, w, "LEA_GIT_SSH='"+bad+"'\n")
		if _, err := LoadConfig(w.getenv, w.root, w.proj); err == nil || !strings.Contains(err.Error(), "LEA_GIT_SSH") {
			t.Errorf("%q accepted: %v", bad, err)
		}
	}
	writeUserConf(t, w, "")
	c, _ := LoadConfig(w.getenv, w.root, w.proj)
	if c.Get("LEA_GIT_KNOWN_HOSTS") != conf+"/git_known_hosts" {
		t.Errorf("known hosts %q", c.Get("LEA_GIT_KNOWN_HOSTS"))
	}
	os.MkdirAll(conf, 0o700)
	os.WriteFile(conf+"/gitlab_known_hosts", nil, 0o600)
	if c, _ := LoadConfig(w.getenv, w.root, w.proj); c.Get("LEA_GIT_KNOWN_HOSTS") != conf+"/gitlab_known_hosts" {
		t.Errorf("the old known hosts file is not used: %q", c.Get("LEA_GIT_KNOWN_HOSTS"))
	}
	for _, v := range []string{"yes", "2"} {
		writeUserConf(t, w, "LEA_LOCKSQL="+v+"\n")
		if _, err := LoadConfig(w.getenv, w.root, w.proj); err == nil || !strings.Contains(err.Error(), "LEA_LOCKSQL") {
			t.Errorf("LEA_LOCKSQL=%s accepted: %v", v, err)
		}
	}
}

// The sound keys of the user configuration reach the daemon, which reads them at start.
func TestSoundKeysReachTheDaemon(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_SOUND_DONE=0\nLEA_SOUND_ATTENTION=/a.oga\n")
	w.env["LEA_SOUND"] = "0"
	c, err := LoadConfig(w.getenv, w.root, w.proj)
	if err != nil {
		t.Fatal(err)
	}
	env := strings.Join(c.Environ(nil), "\n")
	for _, want := range []string{"LEA_SOUND=0", "LEA_SOUND_DONE=0", "LEA_SOUND_ATTENTION=/a.oga"} {
		if !strings.Contains(env+"\n", want+"\n") {
			t.Errorf("Environ lacks %s:\n%s", want, env)
		}
	}
}

// LEA_EGRESS_ALLOW is off by default; set, it is normalized to lower-case comma-separated domain
// names, validated as lea_load_config does.
func TestEgressAllow(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	for file, want := range map[string]string{
		"": "",
		"LEA_EGRESS_ALLOW='GitHub.com, api.anthropic.com  proxy.golang.org,'\n": "github.com,api.anthropic.com,proxy.golang.org",
	} {
		writeUserConf(t, w, file)
		c, err := LoadConfig(w.getenv, w.root, w.proj)
		if err != nil || c.Get("LEA_EGRESS_ALLOW") != want {
			t.Errorf("%q: %v, got %q, want %q", file, err, c.Get("LEA_EGRESS_ALLOW"), want)
			continue
		}
		if env := strings.Join(c.Environ(nil), "\n") + "\n"; !strings.Contains(env, "\nLEA_EGRESS_ALLOW="+want+"\n") {
			t.Errorf("Environ lacks LEA_EGRESS_ALLOW=%s", want)
		}
	}
	for _, bad := range []string{"git/hub.com", ".github.com", "github.com.", "a..b", "10.0.0.1", "*.github.com", "h_x.com"} {
		writeUserConf(t, w, "LEA_EGRESS_ALLOW='"+bad+"'\n")
		want := "LEA_EGRESS_ALLOW must list domain names separated by commas, got '" + bad + "'"
		if _, err := LoadConfig(w.getenv, w.root, w.proj); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
	v := ""
	for n := 1; n <= 65; n++ {
		v += fmt.Sprintf(",d%d.example", n)
	}
	writeUserConf(t, w, "LEA_EGRESS_ALLOW='"+v+"'\n")
	if _, err := LoadConfig(w.getenv, w.root, w.proj); err == nil || !strings.Contains(err.Error(), "LEA_EGRESS_ALLOW lists 65 domains, at most 64") {
		t.Errorf("err = %v", err)
	}
}
