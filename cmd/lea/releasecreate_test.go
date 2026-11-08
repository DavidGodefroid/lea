package main

import (
	"strings"
	"testing"

	"github.com/DavidGodefroid/lea/internal/store"
)

// TestReleaseCreateGrant (#353): the user allows release on one repository and a tag glob
// (lea allow release --tag); the agent's gh release create of a matching, already pushed tag is
// allowed by the hook (pinned); another tag, a missing --verify-tag or another release subcommand
// is not.
func TestReleaseCreateGrant(t *testing.T) {
	api, bus, st := startDaemonWithStore(t)
	if _, err := st.Register(store.NewAgent{Container: "lea-a", Name: "dev", Profile: "claude", Workdir: "/p", Role: "dev",
		Repo: "fixture", Branch: "feat/a"}, store.Caps{}, 1); err != nil {
		t.Fatal(err)
	}
	f := allowFixture{t, t.TempDir(), map[string]string{"LEA_DAEMON_SOCK": api}}
	f.origin("fixture", "https://github.com/o/fixture.git")
	release := cmd("gh release create v0.15.0 --verify-tag --generate-notes")
	if d := preToolUseAs(t, bus, "dev", "Bash", release); d != "" {
		t.Fatalf("before any grant: %q", d)
	}
	args := []string{"release", "--repo", "fixture", "--tag", "v0.15.*", "--for", "2h"}
	rc, out, e := f.allow(append(args, "--yes", f.digest(args...))...)
	if rc != 0 || !strings.Contains(out, "may publish GitHub releases of pushed tags on fixture (github.com/o/fixture), tags v0.15.*") {
		t.Fatalf("allow release rc=%d out=%q err=%q", rc, out, e)
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
		{"release", "--repo", "fixture", "--tag", "0.15.*"},
		{"merge", "--repo", "fixture", "--tag", "v1.*"},
	} {
		if rc, _, _ := f.allow(args...); rc != 1 {
			t.Errorf("%v: rc=%d", args, rc)
		}
	}
}
