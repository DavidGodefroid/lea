package main

import (
	"os"
	"path/filepath"
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
	// The first on only lists the pins: the user confirms them with --yes.
	if rc, _, e := runEnv(t, host, "delivery", "--container", "lea-a", "--projects", proj, "on"); rc != 1 ||
		!strings.Contains(e, "fixture: new pin github.com/o/fixture") || !strings.Contains(e, "lea delivery on --yes") {
		t.Fatalf("first on: rc=%d err=%q", rc, e)
	}
	if d := preToolUseAs(t, bus, "dev", "Bash", push); d != "" {
		t.Fatalf("after an unconfirmed on: %q", d)
	}
	rc, out, e := runEnv(t, host, "delivery", "--container", "lea-a", "--projects", proj, "on", "--yes")
	if rc != 0 || !strings.Contains(out, "fixture  github.com/o/fixture") || !strings.Contains(out, "web  github.com/o/web") ||
		!strings.Contains(out, "gl  skipped: origin is not on github.com") || strings.Contains(out, "hidden") || !strings.Contains(out, "plain  skipped: no origin") {
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
		!strings.Contains(out, "delivery is on: fixture (github.com/o/fixture), web (github.com/o/web)") {
		t.Fatalf("status on: rc=%d %q", rc, out)
	}
	if rc, out, _ := runEnv(t, host, "grant", "ls"); rc != 0 || !strings.Contains(out, "standing") || !strings.Contains(out, "pr-create in lea-a repo web") {
		t.Fatalf("grant ls: rc=%d %q", rc, out)
	}
	// An origin that changed since the last on (an agent can write .git/config) is not pinned
	// without --yes: the user checks it first.
	origin("web", "https://github.com/evil/web.git")
	if rc, _, e := runEnv(t, host, "delivery", "--container", "lea-a", "--projects", proj, "on"); rc != 1 ||
		!strings.Contains(e, "web: origin changed from github.com/o/web to github.com/evil/web") || !strings.Contains(e, "lea delivery on --yes") {
		t.Fatalf("changed origin: rc=%d err=%q", rc, e)
	}
	if rc, out, _ := runEnv(t, host, "delivery", "--container", "lea-a", "--projects", proj, "status"); rc != 0 || !strings.Contains(out, "web (github.com/o/web)") {
		t.Fatalf("status after a refused on: rc=%d %q", rc, out)
	}
	if rc, out, e := runEnv(t, host, "delivery", "--container", "lea-a", "--projects", proj, "on", "--yes"); rc != 0 || !strings.Contains(out, "web  github.com/evil/web") {
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
	if rc, out, e := runEnv(t, host, "delivery", "--container", "lea-a", "--projects", proj, "on"); rc != 0 || !strings.Contains(out, "delivery is on") {
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
		{"delivery", "--container", "lea-a", "on"}, {"delivery", "--container", "lea-a", "--projects", "relative", "on"}} {
		if rc, _, _ := runEnv(t, host, args...); rc != 2 {
			t.Errorf("%v: rc=%d", args, rc)
		}
	}
}
