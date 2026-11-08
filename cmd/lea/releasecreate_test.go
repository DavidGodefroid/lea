package main

import (
	"strings"
	"testing"

	"github.com/DavidGodefroid/lea/internal/store"
)

// TestReleaseCreateGrant (#353): the user grants release-create on one repository and a tag glob
// from the CLI; the agent's gh release create of a matching, already pushed tag is allowed by the
// hook (pinned); another tag, a missing --verify-tag or another release subcommand is not.
func TestReleaseCreateGrant(t *testing.T) {
	api, bus, st := startDaemonWithStore(t)
	if _, err := st.Register(store.NewAgent{Container: "lea-a", Name: "dev", Profile: "claude", Workdir: "/p", Role: "dev",
		Repo: "fixture", Branch: "feat/a"}, store.Caps{}, 1); err != nil {
		t.Fatal(err)
	}
	host := map[string]string{"LEA_DAEMON_SOCK": api}
	release := cmd("gh release create v0.15.0 --verify-tag --generate-notes")
	if d := preToolUseAs(t, bus, "dev", "Bash", release); d != "" {
		t.Fatalf("before any grant: %q", d)
	}
	rc, out, e := runEnv(t, host, "grant", "add", "release-create", "--container", "lea-a", "--repo", "fixture", "--github", "o/fixture",
		"--tag", "v0.15.*", "--expires", "2h")
	if rc != 0 || !strings.Contains(out, "release-create in lea-a repo fixture gh github.com/o/fixture tag v0.15.*") {
		t.Fatalf("grant add rc=%d out=%q err=%q", rc, out, e)
	}
	if d := preToolUseAs(t, bus, "dev", "Bash", release); d != "allow" {
		t.Fatalf("granted release: %q", d)
	}
	for _, c := range []string{"gh release create v0.16.0 --verify-tag", "gh release create v0.15.0 --generate-notes",
		"gh release delete v0.15.0 --yes", "gh release upload v0.15.0 dist/x"} {
		if d := preToolUseAs(t, bus, "dev", "Bash", cmd(c)); d == "allow" {
			t.Fatalf("%q allowed", c)
		}
	}
	if d := preToolUseIn(t, bus, "dev", "Bash", release, "/p/.lea/worktrees/fixture/dev/sub"); d == "allow" {
		t.Fatal("allowed from a subdirectory")
	}
	for _, args := range [][]string{
		{"grant", "add", "release-create", "--container", "lea-a", "--repo", "fixture"},
		{"grant", "add", "release-create", "--container", "lea-a", "--github", "o/fixture"},
		{"grant", "add", "release-create", "--container", "lea-a", "--repo", "fixture", "--github", "o/fixture", "--tag", "0.15.*"},
		{"grant", "add", "merge", "--container", "lea-a", "--repo", "fixture", "--github", "o/fixture", "--tag", "v1.*"},
	} {
		if rc, _, _ := runEnv(t, host, args...); rc != 1 {
			t.Errorf("%v: rc=%d", args, rc)
		}
	}
}
