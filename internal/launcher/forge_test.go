package launcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const ghHosts = `github.com:
    users:
        octo:
            oauth_token: gho_userToken0123456789
    git_protocol: https
    oauth_token: gho_entryToken0123456789
    user: octo
ghe.example.com:
    oauth_token: gho_enterprise
    user: octo
`

func TestStripGHToken(t *testing.T) {
	out, tok, n := stripGHToken(ghHosts)
	if tok != "gho_entryToken0123456789" || n != 2 {
		t.Errorf("token %q, %d lines", tok, n)
	}
	want := "github.com:\n    users:\n        octo:\n    git_protocol: https\n    user: octo\nghe.example.com:\n    oauth_token: gho_enterprise\n    user: octo\n"
	if out != want {
		t.Errorf("hosts.yml:\n%s", out)
	}
	// Only a user's token: it is the one.
	if _, tok, n := stripGHToken("github.com:\n    users:\n        a:\n            oauth_token: \"gho_u\"\n"); tok != "gho_u" || n != 1 {
		t.Errorf("user only: %q %d", tok, n)
	}
	if out, _, n := stripGHToken("ghe.example.com:\n    oauth_token: x\n"); n != 0 || out != "ghe.example.com:\n    oauth_token: x\n" {
		t.Errorf("another host touched: %q", out)
	}
}

func TestStripStoreToken(t *testing.T) {
	in := "https://octo:ghp_store%2Ftoken@github.com\nhttps://me:glpat-x@gitlab.com\nhttps://x:y@GitHub.com:443/\n"
	out, tok, n := stripStoreToken(in)
	if tok != "ghp_store/token" || n != 2 || out != "https://me:glpat-x@gitlab.com\n" {
		t.Errorf("store: %q %q %d", out, tok, n)
	}
}

func TestMigrateGitHubTokenMovesItToTheProxy(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	st := w.data + "/lea/state"
	for d, f := range map[string]string{"gh/hosts.yml": ghHosts, "git/credentials": "https://octo:ghp_fromstore@github.com\n"} {
		if err := os.MkdirAll(filepath.Dir(st+"/"+d), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(st+"/"+d, []byte(f), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	l := w.launcher()
	what, err := l.migrateGitHubToken()
	if err != nil || what != "gh's config and git's credential store" {
		t.Fatalf("%q %v", what, err)
	}
	b, err := os.ReadFile(w.data + "/lea/proxy/forge/github.com")
	if err != nil || string(b) != "gho_entryToken0123456789\n" {
		t.Fatalf("token file: %q %v", b, err)
	}
	if fi, _ := os.Stat(w.data + "/lea/proxy/forge/github.com"); fi.Mode().Perm() != 0o600 {
		t.Errorf("token mode %v", fi.Mode())
	}
	if fi, _ := os.Stat(w.data + "/lea/proxy/forge"); fi.Mode().Perm() != 0o700 {
		t.Errorf("forge dir mode %v", fi.Mode())
	}
	for _, f := range []string{"gh/hosts.yml", "git/credentials"} {
		b, _ := os.ReadFile(st + "/" + f)
		if strings.Contains(string(b), "Token0123456789") || strings.Contains(string(b), "ghp_") {
			t.Errorf("%s still holds a GitHub token: %q", f, b)
		}
	}
	// gh's config gets a version: gh would otherwise migrate hosts.yml, and refuse without a token.
	if b, _ := os.ReadFile(st + "/gh/config.yml"); string(b) != "version: \"1\"\n" {
		t.Errorf("config.yml %q", b)
	}
	// Nothing left: nothing moved, the proxy's token stays.
	if what, err := l.migrateGitHubToken(); err != nil || what != "" {
		t.Errorf("second run: %q %v", what, err)
	}
	if b, _ := os.ReadFile(w.data + "/lea/proxy/forge/github.com"); string(b) != "gho_entryToken0123456789\n" {
		t.Errorf("second run changed the token: %q", b)
	}
}

func TestGitHubLoginLogoutStatus(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	l := w.launcher()
	if err := l.github([]string{"status"}); err != nil || !strings.Contains(w.out.String(), "github: no token") {
		t.Fatalf("status: %v %q", err, w.out.String())
	}
	l.Stdin = strings.NewReader("")
	if err := l.github([]string{"login"}); err == nil {
		t.Error("an empty login was accepted")
	}
	l.Stdin = strings.NewReader("has space\n")
	if err := l.github([]string{"login"}); err == nil {
		t.Error("a token with a space was accepted")
	}
	// A login in lea (gh's config) is moved too, but the token given wins.
	if err := os.MkdirAll(w.data+"/lea/state/gh", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(w.data+"/lea/state/gh/hosts.yml", []byte(ghHosts), 0o600); err != nil {
		t.Fatal(err)
	}
	l.Stdin = strings.NewReader("github_pat_given\n")
	if err := l.github([]string{"login"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(w.data + "/lea/proxy/forge/github.com"); string(b) != "github_pat_given\n" {
		t.Errorf("token %q", b)
	}
	if b, _ := os.ReadFile(w.data + "/lea/state/gh/hosts.yml"); strings.Contains(string(b), "gho_entry") {
		t.Errorf("gh's config kept its token")
	}
	if strings.Contains(w.errb.String(), "github_pat_given") || strings.Contains(w.out.String(), "github_pat_given") {
		t.Error("the token was printed")
	}
	w.out.Reset()
	if err := l.github([]string{"status"}); err != nil || !strings.Contains(w.out.String(), "a token is in the proxy") {
		t.Errorf("status after login: %q", w.out.String())
	}
	if err := l.github([]string{"logout"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(w.data + "/lea/proxy/forge/github.com"); !os.IsNotExist(err) {
		t.Errorf("logout left the token: %v", err)
	}
	if err := l.github([]string{"nope"}); err == nil {
		t.Error("unknown verb accepted")
	}
}
