package daemon

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	km "github.com/DavidGodefroid/lockkit/managed"

	"github.com/DavidGodefroid/lea/internal/grants"
	"github.com/DavidGodefroid/lea/internal/managed"
	"github.com/DavidGodefroid/lea/internal/store"
)

// setManaged makes e's daemon read the managed file body (version 1 added).
func setManaged(t *testing.T, e env, body string) *managed.Policy {
	t.Helper()
	f, err := km.Parse([]byte("version = 1\norganisation = \"ICTools\"\n" + body))
	if err != nil {
		t.Fatal(err)
	}
	p, err := managed.FromFile(f)
	if err != nil {
		t.Fatal(err)
	}
	e.d.SetManaged(func() (*managed.Policy, error) { return p, nil })
	return p
}

func allowSetRaw(t *testing.T, e env, p map[string]any) (any, error) {
	t.Helper()
	p["container"] = "lea-a"
	if _, ok := p["capabilities"]; !ok {
		p["capabilities"] = []string{"push"}
		p["repos"] = []map[string]string{{"repo": "fixture", "pin": "o/fixture"}}
	}
	raw, _ := json.Marshal(p)
	return e.d.allowSet(raw)
}

// standing_grants = false refuses lea allow without --for; grant_ttl_max refuses a --for beyond
// it; a set within them records the managed file's fingerprint.
func TestManagedAllowBounds(t *testing.T) {
	e := start(t)
	m := setManaged(t, e, "[lea]\nstanding_grants = false\ngrant_ttl_max = \"7d\"\n")
	if _, err := allowSetRaw(t, e, map[string]any{}); err == nil ||
		!strings.Contains(err.Error(), "blocked by company policy: lea.standing_grants = false") {
		t.Fatalf("standing: %v", err)
	}
	if _, err := allowSetRaw(t, e, map[string]any{"ttl_s": 30 * 24 * 3600}); err == nil ||
		!strings.Contains(err.Error(), "blocked by company policy: lea.grant_ttl_max = 7d (30d asked)") {
		t.Fatalf("30d: %v", err)
	}
	v, err := allowSetRaw(t, e, map[string]any{"ttl_s": 7 * 24 * 3600})
	if err != nil {
		t.Fatal(err)
	}
	s := v.(map[string]any)["set"].(grants.Set)
	sets, _ := e.d.st.GrantSets("lea-a", e.clock.Load())
	if len(sets) != 1 || sets[0].ID != s.ID || sets[0].Managed != m.Fingerprint() || m.Fingerprint() == "" {
		t.Fatalf("sets %+v, want the fingerprint %s", sets, m.Fingerprint())
	}
	// grant_ttl_max alone refuses a standing set too: it would outlive the bound.
	setManaged(t, e, "[lea]\ngrant_ttl_max = \"8h\"\n")
	if _, err := allowSetRaw(t, e, map[string]any{}); err == nil || !strings.Contains(err.Error(), "lea.grant_ttl_max = 8h") {
		t.Fatalf("standing under grant_ttl_max: %v", err)
	}
	// Without a [lea] rule, a standing set is recorded as before.
	setManaged(t, e, "")
	if _, err := allowSetRaw(t, e, map[string]any{}); err != nil {
		t.Fatal(err)
	}
}

// A lead's request is clamped to grant_ttl_max; the user's --expires beyond it is refused.
func TestManagedGrantRequestTTL(t *testing.T) {
	e := start(t)
	setManaged(t, e, "[lea]\ngrant_ttl_max = \"1h\"\n")
	lead, err := e.d.st.Register(store.NewAgent{Container: "lea-a", Name: "lead", Profile: "claude", Workdir: "/p", Role: "lead",
		Repo: "fixture", Branch: "feat/lead"}, store.Caps{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"action": "merge", "repo": "fixture", "github": "o/fixture"})
	v, err := e.d.grantsRequest(e.ctx, Origin{Container: "lea-a"}, lead.Key, raw)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := strconv.ParseInt(strings.TrimPrefix(v.(map[string]any)["request"].(string), "g"), 10, 64)
	if r, _ := e.d.st.GrantRequestByID(id); r.TTLS != 3600 {
		t.Fatalf("the default 8h is not clamped: %+v", r)
	}
	decide := func(p map[string]any) (any, error) {
		p["id"], p["verdict"] = id, "approve"
		raw, _ := json.Marshal(p)
		return e.d.grantRequestDecide(raw)
	}
	if _, err := decide(map[string]any{"ttl_s": 30 * 24 * 3600}); err == nil ||
		!strings.Contains(err.Error(), "blocked by company policy: lea.grant_ttl_max = 1h (30d asked)") {
		t.Fatalf("--expires 30d: %v", err)
	}
	g, err := decide(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if gr := g.(grants.Grant); gr.Expires != e.clock.Load()+3600 {
		t.Fatalf("grant %+v", gr)
	}
}

// Grants recorded before the bounds stop applying beyond them: a standing one under
// standing_grants = false, a long one past grant_ttl_max from its creation. A file that no longer
// loads ends every grant (fail closed); lea allow shows why.
func TestManagedBoundsRecordedGrants(t *testing.T) {
	e := start(t)
	if _, err := allowSetRaw(t, e, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if _, err := allowSetRaw(t, e, map[string]any{"capabilities": []string{"stop-finished"}, "ttl_s": 30 * 24 * 3600}); err != nil {
		t.Fatal(err)
	}
	actions := func() string {
		gs, err := e.d.activeGrants("lea-a", e.clock.Load())
		if err != nil {
			return "error: " + err.Error()
		}
		var out []string
		for _, g := range gs {
			out = append(out, g.Action)
		}
		return strings.Join(out, " ")
	}
	// push is recorded as its commit-identity row.
	if got := actions(); got != "commit-identity stop-finished" {
		t.Fatalf("no managed file: %q", got)
	}
	setManaged(t, e, "[lea]\nstanding_grants = false\n")
	if got := actions(); got != "stop-finished" {
		t.Fatalf("standing_grants = false: %q", got)
	}
	setManaged(t, e, "[lea]\ngrant_ttl_max = \"1h\"\n")
	if got := actions(); got != "commit-identity stop-finished" {
		t.Fatalf("within the hour: %q", got)
	}
	e.clock.Add(3601)
	// The standing set too: grant_ttl_max bounds every grant from its creation.
	if got := actions(); got != "" {
		t.Fatalf("past grant_ttl_max: %q", got)
	}
	raw, _ := json.Marshal(map[string]any{"container": "lea-a"})
	v, err := e.d.allowList(raw)
	if err != nil {
		t.Fatal(err)
	}
	if mv := v.(map[string]any)["managed"].(map[string]any); mv["max_ttl_s"] != int64(3600) || !strings.HasPrefix(mv["describe"].(string), "Managed by ICTools") {
		t.Fatalf("allow_list managed %+v", mv)
	}
	e.d.SetManaged(func() (*managed.Policy, error) { return nil, errors.New("managed: bad file") })
	if got := actions(); !strings.Contains(got, "no grant applies") {
		t.Fatalf("bad file: %q", got)
	}
	if _, err := allowSetRaw(t, e, map[string]any{"ttl_s": 3600}); err == nil {
		t.Fatal("a grant recorded while the managed file does not load")
	}
	if bound := e.d.boundGrants([]grants.Grant{{Action: "push"}}); len(bound) != 0 {
		t.Fatalf("boundGrants with a bad file: %v", bound)
	}
}

func TestBound(t *testing.T) {
	gs := []grants.Grant{{ID: 1, Created: 100, Expires: grants.Standing}, {ID: 2, Created: 100, Expires: 200}, {ID: 3, Created: 100, Expires: 100 + 7200}}
	if got := grants.Bound(gs, 0, false); len(got) != 3 || got[0].Expires != grants.Standing {
		t.Fatalf("no bound: %+v", got)
	}
	got := grants.Bound(gs, 3600, true)
	if len(got) != 2 || got[0].ID != 2 || got[0].Expires != 200 || got[1].ID != 3 || got[1].Expires != 3700 {
		t.Fatalf("bound: %+v", got)
	}
	if got := grants.Bound(gs, 3600, false); got[0].Expires != 3700 {
		t.Fatalf("a standing grant under grant_ttl_max: %+v", got)
	}
}
