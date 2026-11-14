package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/web"
)

// lea web's lea allow (#415) reads the pins as lea allow does: a new pin is shown first, and it
// records only the pins of the preview the user saw.
func TestWebHostAllowPreviewThenRecord(t *testing.T) {
	api, _, st := startDaemonWithStore(t)
	t.Setenv("LEA_DAEMON_SOCK", api)
	f := allowFixture{t, t.TempDir(), nil}
	f.origin("fixture", "git@github.com:o/fixture.git")
	f.origin("local", "/srv/git/local.git")
	h := &webHost{container: "lea-a", proj: f.proj}
	ctx := context.Background()

	ask := web.AllowAsk{Capabilities: []string{"pr", "release"}, Tag: "v0.15.*"}
	p, err := h.AllowPreview(ctx, ask)
	if err != nil {
		t.Fatal(err)
	}
	if p.Command != "lea allow pr release --tag 'v0.15.*'" || !p.Unseen || len(p.Repos) != 1 || !p.Repos[0].New ||
		p.Repos[0].Pin != "github.com/o/fixture" || len(p.Skipped) != 1 || !strings.HasPrefix(p.Skipped[0], "local") || len(p.Digest) != 16 {
		t.Fatalf("preview %+v", p)
	}
	if _, err := h.AllowRecord(ctx, ask, "0123456789abcdef"); err == nil || !strings.Contains(err.Error(), "pins changed") {
		t.Fatalf("another digest: %v", err)
	}
	res, err := h.AllowRecord(ctx, ask, p.Digest)
	if err != nil {
		t.Fatal(err)
	}
	var r struct{ Set struct{ ID int64 } }
	if json.Unmarshal(res, &r) != nil || r.Set.ID == 0 {
		t.Fatalf("recorded %s", res)
	}
	sets, err := st.GrantSets("lea-a", time.Now().Unix())
	if err != nil || len(sets) != 1 || strings.Join(sets[0].Capabilities, " ") != "pr release" || sets[0].Grants[0].Pin != "github.com/o/fixture" {
		t.Fatalf("sets %+v %v", sets, err)
	}
	// Seen now: the next preview has nothing new.
	if p, err = h.AllowPreview(ctx, web.AllowAsk{Capabilities: []string{"merge"}, Repos: []string{"fixture"}}); err != nil || p.Unseen ||
		p.Command != "lea allow merge --repo fixture" {
		t.Fatalf("second preview %+v %v", p, err)
	}
	// A capability of agents names no repository; an unknown one, a bad duration are refused.
	if p, err := h.AllowPreview(ctx, web.AllowAsk{Capabilities: []string{"stop-finished"}}); err != nil || len(p.Repos) != 0 || p.Unseen {
		t.Fatalf("stop-finished %+v %v", p, err)
	}
	for _, a := range []web.AllowAsk{{Capabilities: []string{"stop-finished"}, Repos: []string{"fixture"}},
		{Capabilities: []string{"teleport"}}, {Capabilities: []string{"merge"}, For: "30d"},
		{Capabilities: []string{"merge", "--yes", "0123456789abcdef"}}, {}} {
		if _, err := h.AllowPreview(ctx, a); err == nil {
			t.Fatalf("%+v accepted", a)
		}
	}
	// Without the projects directory, a forge capability says so.
	if _, err := (&webHost{container: "lea-a"}).AllowPreview(ctx, ask); err == nil || !strings.Contains(err.Error(), "projects directory") {
		t.Fatalf("no projects dir: %v", err)
	}
}
