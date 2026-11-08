package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
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
