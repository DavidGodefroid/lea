package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"

	"github.com/DavidGodefroid/lea/internal/store"
)

func TestOriginGitHub(t *testing.T) {
	for url, want := range map[string]string{
		"https://github.com/o/api.git":       "o/api",
		"https://github.com/o/api":           "o/api",
		"https://github.com/o/api/":          "o/api",
		"git@github.com:o/web.git":           "o/web",
		"ssh://git@github.com/o/web.git":     "o/web",
		"ssh://git@github.com:22/o/web":      "",
		"https://user@github.com/O-1/x.y_z":  "O-1/x.y_z",
		"https://gitlab.com/o/x.git":         "",
		"https://github.com.evil.io/o/x.git": "",
		"https://evil.io/github.com/o/x.git": "",
		"https://github.com/o/x/y.git":       "",
		"https://github.com/o/..":            "",
		"file:///srv/x.git":                  "",
		"/srv/x.git":                         "",
		"":                                   "",
		"https://github.com/o/x.git?a=b":     "",
		"git@github.com:o/x.git\nurl = x":    "",
		"http://github.com/o/x.git":          "",
	} {
		if got := originGitHub(url); got != want {
			t.Errorf("%q: %q, want %q", url, got, want)
		}
	}
}

func TestOriginPin(t *testing.T) {
	hosts := map[string]bool{"gitlab.com": true, "git.corp.example": true}
	for url, want := range map[string]string{
		"https://github.com/o/api.git":                "github.com/o/api",
		"https://gitlab.com/g/x.git":                  "gitlab:gitlab.com/g/x",
		"git@gitlab.com:g/sub/x.git":                  "gitlab:gitlab.com/g/sub/x",
		"ssh://git@git.corp.example/team/sub/app.git": "gitlab:git.corp.example/team/sub/app",
		"https://me@git.corp.example/team/app":        "gitlab:git.corp.example/team/app",
		"https://GIT.CORP.EXAMPLE/team/app.git":       "gitlab:git.corp.example/team/app",
		"https://other.example/team/app.git":          "",
		"https://gitlab.com/x.git":                    "",
		"https://gitlab.com/g/../x.git":               "",
		"https://gitlab.com:8443/g/x.git":             "",
		"https://gitlab.com.evil.io/g/x.git":          "",
		"http://gitlab.com/g/x.git":                   "",
		"https://gitlab.com/g/x.git?ref=y":            "",
		"git@git.corp.example:team/app.git\nurl = x":  "",
	} {
		if got := originPin(url, hosts); got != want {
			t.Errorf("%q: %q, want %q", url, got, want)
		}
	}
}

func TestRepoOrigin(t *testing.T) {
	dir := t.TempDir()
	write := func(repo, config string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, repo, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, repo, ".git", "config"), []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a", "[core]\n\tbare = false\n[remote \"upstream\"]\n\turl = https://github.com/u/a.git\n[remote \"origin\"]\n\turl = https://github.com/o/a.git\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n")
	write("b", "[remote \"origin\"]\n  URL=git@github.com:o/b.git ; a comment\n")
	write("c", "[remote \"origin\"]\n\turl = https://github.com/o/c.git\n\turl = https://github.com/o/d.git\n")
	write("d", "[remote \"upstream\"]\n\turl = https://github.com/o/d.git\n")
	write("e", "[remote \"origin\"]\n\turl = \"https://github.com/o/e.git\"\n")
	// Only a regular file is read: not a symlink (to a file of the agent's choosing), a FIFO or a
	// device, and no more than 1 MiB of it.
	write("f", "")
	os.Remove(filepath.Join(dir, "f", ".git", "config"))
	if err := os.Symlink(filepath.Join(dir, "a", ".git", "config"), filepath.Join(dir, "f", ".git", "config")); err != nil {
		t.Fatal(err)
	}
	write("g", "")
	os.Remove(filepath.Join(dir, "g", ".git", "config"))
	if err := syscall.Mkfifo(filepath.Join(dir, "g", ".git", "config"), 0o600); err != nil {
		t.Fatal(err)
	}
	write("h", "[remote \"origin\"]\n\turl = https://github.com/o/h.git\n"+strings.Repeat("#\n", 1<<19))
	for repo, want := range map[string]string{"a": "https://github.com/o/a.git", "b": "git@github.com:o/b.git", "c": "", "d": "",
		"e": "https://github.com/o/e.git", "missing": "", "f": "", "g": "", "h": ""} {
		if got := repoOrigin(filepath.Join(dir, repo)); got != want {
			t.Errorf("%s: %q, want %q", repo, got, want)
		}
	}
}

// TestDeliveryCommand is issue #92's one-time setup: lea delivery on in a projects directory
// covers each repository whose origin is on github.com, with no expiry; status lists them; off
// revokes them.
func TestDeliveryCommand(t *testing.T) {
	api, bus, st := startDaemonWithStore(t)
	if _, err := st.Register(store.NewAgent{Container: "lea-a", Name: "dev", Profile: "claude", Workdir: "/p", Role: "dev",
		Repo: "fixture", Branch: "feat/a"}, store.Caps{}, 1); err != nil {
		t.Fatal(err)
	}
	proj := t.TempDir()
	origin := func(repo, url string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(proj, repo, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(proj, repo, ".git", "config"), []byte("[remote \"origin\"]\n\turl = "+url+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	origin("fixture", "git@github.com:o/fixture.git")
	origin("web", "https://github.com/o/web.git")
	origin("gl", "https://gitlab.com/o/gl.git")
	origin("corp", "git@git.corp.example:team/sub/corp.git")
	origin(".lea", "https://github.com/o/hidden.git")
	if err := os.MkdirAll(filepath.Join(proj, "plain"), 0o755); err != nil {
		t.Fatal(err)
	}
	host := map[string]string{"LEA_DAEMON_SOCK": api}
	if rc, out, _ := runEnv(t, host, "delivery", "--container", "lea-a", "--projects", proj, "status"); rc != 0 || !strings.Contains(out, "delivery is off") {
		t.Fatalf("status before: rc=%d %q", rc, out)
	}
	push := cmd("git push -u origin feat/a")
	if d := preToolUseAs(t, bus, "dev", "Bash", push); d != "" {
		t.Fatalf("before delivery: %q", d)
	}
	// The first on only lists the pins and their digest: the user confirms exactly those with
	// --yes DIGEST. A pin that changed in between (an agent rewrote an origin, added a
	// repository) makes the digest stale: nothing is recorded.
	confirm := func(flags ...string) string {
		t.Helper()
		rc, _, e := runEnv(t, host, append([]string{"delivery", "--container", "lea-a", "--projects", proj, "on"}, flags...)...)
		m := regexp.MustCompile(`lea delivery on --yes ([0-9a-f]{16})\b`).FindStringSubmatch(e)
		if rc != 1 || m == nil {
			t.Fatalf("on without --yes: rc=%d err=%q", rc, e)
		}
		return m[1]
	}
	if rc, _, e := runEnv(t, host, "delivery", "--container", "lea-a", "--projects", proj, "on"); rc != 1 || !strings.Contains(e, "fixture: new pin github.com/o/fixture") {
		t.Fatalf("first on: rc=%d err=%q", rc, e)
	}
	digest := confirm()
	if d := preToolUseAs(t, bus, "dev", "Bash", push); d != "" {
		t.Fatalf("after an unconfirmed on: %q", d)
	}
	origin("web", "https://github.com/swap/web.git")
	if rc, _, e := runEnv(t, host, "delivery", "--container", "lea-a", "--projects", proj, "on", "--yes", digest); rc != 1 ||
		!strings.Contains(e, "the pins changed since they were listed") {
		t.Fatalf("stale digest: rc=%d err=%q", rc, e)
	}
	if d := preToolUseAs(t, bus, "dev", "Bash", push); d != "" {
		t.Fatalf("after a stale digest: %q", d)
	}
	origin("web", "https://github.com/o/web.git")
	if rc, _, _ := runEnv(t, host, "delivery", "--container", "lea-a", "--projects", proj, "on", "--yes", "0000000000000000"); rc != 1 {
		t.Fatal("a made-up digest")
	}
	rc, out, e := runEnv(t, host, "delivery", "--container", "lea-a", "--projects", proj, "on", "--yes", confirm("--gitlab", "git.corp.example"), "--gitlab", "git.corp.example")
	if rc != 0 || !strings.Contains(out, "fixture  github.com/o/fixture") || !strings.Contains(out, "web  github.com/o/web") ||
		!strings.Contains(out, "gl  gitlab:gitlab.com/o/gl") || !strings.Contains(out, "corp  gitlab:git.corp.example/team/sub/corp") || strings.Contains(out, "hidden") || !strings.Contains(out, "plain  skipped: no origin") {
		t.Fatalf("on: rc=%d out=%q err=%q", rc, out, e)
	}
	for _, c := range []string{"git push -u origin feat/a", "gh pr create --head feat/a --title t --body b", "gh pr merge 3 --squash"} {
		if d := preToolUseAs(t, bus, "dev", "Bash", cmd(c)); d != "allow" {
			t.Fatalf("%s with delivery: %q", c, d)
		}
	}
	if d := preToolUseAs(t, bus, "dev", "Bash", cmd("git push origin main")); d != "deny" {
		t.Fatalf("push main with delivery: %q", d)
	}
	if rc, out, _ := runEnv(t, host, "delivery", "--container", "lea-a", "--projects", proj, "status"); rc != 0 ||
		!strings.Contains(out, "delivery is on: corp (gitlab:git.corp.example/team/sub/corp), fixture (github.com/o/fixture), gl (gitlab:gitlab.com/o/gl), web (github.com/o/web)") {
		t.Fatalf("status on: rc=%d %q", rc, out)
	}
	if rc, out, _ := runEnv(t, host, "grant", "ls"); rc != 0 || !strings.Contains(out, "standing") || !strings.Contains(out, "pr-create in lea-a repo web") {
		t.Fatalf("grant ls: rc=%d %q", rc, out)
	}
	// An origin that changed since the last on (an agent can write .git/config) is not pinned
	// without --yes: the user checks it first.
	origin("web", "https://github.com/evil/web.git")
	if rc, _, e := runEnv(t, host, "delivery", "--container", "lea-a", "--projects", proj, "on"); rc != 1 ||
		!strings.Contains(e, "web: origin changed from github.com/o/web to github.com/evil/web") {
		t.Fatalf("changed origin: rc=%d err=%q", rc, e)
	}
	if rc, out, _ := runEnv(t, host, "delivery", "--container", "lea-a", "--projects", proj, "status"); rc != 0 || !strings.Contains(out, "web (github.com/o/web)") {
		t.Fatalf("status after a refused on: rc=%d %q", rc, out)
	}
	if rc, out, e := runEnv(t, host, "delivery", "--container", "lea-a", "--projects", proj, "on", "--yes", confirm()); rc != 0 || !strings.Contains(out, "web  github.com/evil/web") {
		t.Fatalf("on --yes: rc=%d out=%q err=%q", rc, out, e)
	}
	if rc, out, _ := runEnv(t, host, "delivery", "--container", "lea-a", "--projects", proj, "off"); rc != 0 || !strings.Contains(out, "delivery is off") {
		t.Fatalf("off: rc=%d %q", rc, out)
	}
	// Off forgets nothing: an origin changed while delivery was off still needs --yes, and an
	// unchanged set applies with a plain on.
	origin("web", "https://github.com/evil2/web.git")
	if rc, _, e := runEnv(t, host, "delivery", "--container", "lea-a", "--projects", proj, "on"); rc != 1 ||
		!strings.Contains(e, "web: origin changed from github.com/evil/web to github.com/evil2/web") {
		t.Fatalf("changed while off: rc=%d err=%q", rc, e)
	}
	origin("web", "https://github.com/evil/web.git")
	if rc, out, e := runEnv(t, host, "delivery", "--container", "lea-a", "--projects", proj, "on"); rc != 0 || !strings.Contains(out, "delivery is on") ||
		!strings.Contains(out, "corp  gitlab:git.corp.example/team/sub/corp") {
		t.Fatalf("same pins after off: rc=%d out=%q err=%q", rc, out, e)
	}
	if rc, _, _ := runEnv(t, host, "delivery", "--container", "lea-a", "--projects", proj, "off"); rc != 0 {
		t.Fatal("off again")
	}
	if d := preToolUseAs(t, bus, "dev", "Bash", push); d != "" {
		t.Fatalf("after off: %q", d)
	}
	for _, args := range [][]string{{"delivery", "--container", "lea-a", "--projects", proj}, {"delivery", "--container", "lea-a", "--projects", proj, "maybe"},
		{"delivery", "--container", "lea-a", "--projects", proj, "off", "--yes"}, {"delivery", "--container", "lea-a", "--projects", proj, "on", "-y"},
		{"delivery", "--container", "lea-a", "--projects", proj, "on", "--yes"}, {"delivery", "--container", "lea-a", "--projects", proj, "on", "--yes", "xyz"},
		{"delivery", "--container", "lea-a", "--projects", proj, "on", "--yes", "0123456789abcdef", "--yes", "0123456789abcdef"},
		{"delivery", "--container", "lea-a", "--projects", proj, "on", "--gitlab"}, {"delivery", "--container", "lea-a", "--projects", proj, "on", "--gitlab", "bad host"},
		{"delivery", "--container", "lea-a", "--projects", proj, "status", "--gitlab", "x.example"},
		{"delivery", "--container", "lea-a", "on"}, {"delivery", "--container", "lea-a", "--projects", "relative", "on"}} {
		if rc, _, _ := runEnv(t, host, args...); rc != 2 {
			t.Errorf("%v: rc=%d", args, rc)
		}
	}
}
