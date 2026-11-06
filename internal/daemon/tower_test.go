package daemon

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/agents"
	"github.com/DavidGodefroid/lea/internal/grants"
	"github.com/DavidGodefroid/lea/internal/roles"
	"github.com/DavidGodefroid/lea/internal/store"
)

// treeAgents is a manager that registers the agents it starts in the store, with the caps the
// real manager passes, sends the brief as the first message, and ends the agents it stops: the
// agent tree as the daemon sees it, without tmux or Docker.
type treeAgents struct {
	*fakeAgents
	st        *store.Store
	maxAgents int
	n         int
}

func newTreeAgents(e env, maxAgents int) *treeAgents {
	return &treeAgents{fakeAgents: &fakeAgents{}, st: e.d.st, maxAgents: maxAgents}
}

func (f *treeAgents) New(ctx context.Context, r agents.NewRequest) (agents.Row, error) {
	f.mu.Lock()
	f.n++
	name := r.Name
	if name == "" {
		name = fmt.Sprintf("%s-%d", r.NamePrefix, f.n)
	}
	f.req = r
	f.mu.Unlock()
	if r.Role == "" {
		r.Role = roles.Default
	}
	caps := store.Caps{MaxAgents: f.maxAgents}
	if r.Parent != "" {
		caps.MaxChildren = roles.MaxChildren
	}
	a, err := f.st.Register(store.NewAgent{Container: r.Container, Name: name, Profile: "claude", Workdir: "/p", Role: r.Role,
		Parent: r.Parent, Repo: r.Repo, Branch: r.Branch}, caps, 1)
	if err != nil {
		return agents.Row{}, err
	}
	if _, err := f.st.CreateTask(r.Container, a.Key, "task", 1); err != nil {
		return agents.Row{}, err
	}
	if r.Brief != "" {
		if _, err := f.st.Send(store.Message{From: r.BriefFrom, To: a.Key, Kind: "request", Body: r.Brief, At: 1}); err != nil {
			return agents.Row{}, err
		}
	}
	f.rec("new " + a.Key)
	return agents.Row{Key: a.Key, Name: a.Name, Role: a.Role, Profile: "claude", State: "unknown"}, nil
}

func (f *treeAgents) Stop(ctx context.Context, k string) error {
	f.rec("stop " + k)
	return f.st.SetState(k, "done", "", 2)
}

func spawnAs(t *testing.T, e env, agent string, p map[string]any) (string, error) {
	t.Helper()
	var r struct{ Name string }
	err := call(t, e.bus, agent, "agents_spawn", p, &r)
	return r.Name, err
}

// The acceptance path of #110, end to end through the daemon's API: the user starts a tower;
// the tower starts a lead; the lead starts a dev; the dev cannot start agents nor write to the
// tower; the dev completes its issue and reports to its lead; the lead stops it and reports to
// the tower.
func TestTowerLeadDevEndToEnd(t *testing.T) {
	e := start(t)
	fa := newTreeAgents(e, 30)
	e.d.SetAgents(fa)
	var tw agents.Row
	if err := call(t, e.api, "", "agent_new", map[string]any{"container": "lea-a", "name": "tw", "role": "tower"}, &tw); err != nil {
		t.Fatal(err)
	}
	if err := call(t, e.api, "", "agent_new", map[string]any{"container": "lea-a", "name": "tw2", "role": "tower"}, nil); err == nil ||
		!strings.Contains(err.Error(), "one tower per container") {
		t.Fatalf("second tower: %v", err)
	}
	if _, err := spawnAs(t, e, "tw", map[string]any{"role": "dev", "brief": "x"}); err == nil || !strings.Contains(err.Error(), "only lead agents") {
		t.Fatalf("tower started a dev: %v", err)
	}
	lead, err := spawnAs(t, e, "tw", map[string]any{"role": "lead", "repo": "shop", "brief": "you own shop: fix issue #7"})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []string{"lead", "tower"} {
		if _, err := spawnAs(t, e, lead, map[string]any{"role": r, "brief": "x"}); err == nil {
			t.Fatalf("the lead started a %s", r)
		}
	}
	dev, err := spawnAs(t, e, lead, map[string]any{"role": "dev", "repo": "shop", "branch": "fix/7", "brief": "fix issue #7, reply with a result"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := spawnAs(t, e, dev, map[string]any{"brief": "help"}); err == nil || !strings.Contains(err.Error(), "a dev cannot start agents") {
		t.Fatalf("dev spawn: %v", err)
	}
	if err := call(t, e.bus, dev, "msg_send", map[string]any{"to": "tw", "body": "hi", "kind": "note"}, nil); err == nil ||
		!strings.Contains(err.Error(), "write to your lead "+lead) {
		t.Fatalf("dev wrote to the tower: %v", err)
	}
	if err := call(t, e.bus, "tw", "msg_send", map[string]any{"to": dev, "body": "hi", "kind": "note"}, nil); err == nil ||
		!strings.Contains(err.Error(), "write to its lead "+lead) {
		t.Fatalf("tower wrote to a dev: %v", err)
	}
	// The tower sees its lead with the team rolled up, not the dev.
	var as []agentView
	if err := call(t, e.bus, "tw", "agents_list", nil, &as); err != nil {
		t.Fatal(err)
	}
	seen := map[string]agentView{}
	for _, a := range as {
		seen[a.Name] = a
	}
	if _, ok := seen[dev]; ok {
		t.Fatalf("the tower sees the dev: %+v", as)
	}
	if l := seen[lead]; l.Team == nil || l.Team.Children != 1 || l.Team.States["unknown"] != 1 {
		t.Fatalf("lead row %+v team %+v", l, l.Team)
	}
	// The lead still sees its dev.
	var ls []agentView
	if err := call(t, e.bus, lead, "agents_list", nil, &ls); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range ls {
		found = found || a.Name == dev && a.Parent == lead
	}
	if !found {
		t.Fatalf("the lead does not see its dev: %+v", as)
	}

	// The dev completes the issue and answers its brief.
	brief := inboxOf(t, e, dev)
	if len(brief) != 1 || !strings.Contains(brief[0].Text, "issue #7") {
		t.Fatalf("dev inbox=%+v", brief)
	}
	if err := call(t, e.bus, dev, "msg_reply", map[string]any{"id": brief[0].ID, "status": "ok", "body": "PR #12 merged, CI green"}, nil); err != nil {
		t.Fatal(err)
	}
	got := inboxOf(t, e, lead)
	if len(got) != 2 || !strings.Contains(got[1].Text, "PR #12 merged") || got[1].Kind != "result" {
		t.Fatalf("lead inbox=%+v", got)
	}
	// The lead stops its dev and reports to the tower.
	if err := call(t, e.bus, lead, "agents_stop", map[string]any{"name": dev}, nil); err != nil {
		t.Fatal(err)
	}
	if a, _ := e.d.st.AgentByKey("lea-a/" + dev); a.State != "done" {
		t.Fatalf("dev state %s", a.State)
	}
	if err := call(t, e.bus, lead, "msg_send", map[string]any{"to": "tw", "body": "issue #7 done: PR #12", "kind": "result"}, nil); err != nil {
		t.Fatal(err)
	}
	got = inboxOf(t, e, "tw")
	if len(got) != 1 || got[0].Kind != "result" || !strings.Contains(got[0].Text, "issue #7 done") {
		t.Fatalf("tower inbox=%+v", got)
	}
	// The dev reported before it was stopped: no health signal reaches the lead.
	e.d.checkHealth()
	if ms := inboxOf(t, e, lead); len(ms) != 0 {
		t.Fatalf("lead got %+v", ms)
	}
	if err := call(t, e.bus, "tw", "agents_stop", map[string]any{"name": lead}, nil); err != nil {
		t.Fatalf("the tower stops its lead once its team is gone: %v", err)
	}
}

func TestAgentsStopCascade(t *testing.T) {
	e := start(t)
	fa := newTreeAgents(e, 0)
	e.d.SetAgents(fa)
	tw := spawned(t, e, "tw", "tower", "")
	lead := spawned(t, e, "lead", "lead", tw)
	spawned(t, e, "d1", "dev", lead)
	spawned(t, e, "d2", "reviewer", lead)
	if err := call(t, e.bus, "tw", "agents_stop", map[string]any{"name": "lead"}, nil); err == nil ||
		!strings.Contains(err.Error(), "runs 2 agents (d1, d2): pass cascade: true") {
		t.Fatalf("without cascade: %v", err)
	}
	if len(fa.calls) != 0 {
		t.Fatalf("calls=%q", fa.calls)
	}
	// Only the lead's parent stops it.
	spawned(t, e, "other", "lead", "")
	if err := call(t, e.bus, "other", "agents_stop", map[string]any{"name": "lead", "cascade": true}, nil); err == nil ||
		!strings.Contains(err.Error(), "not one of your children") {
		t.Fatalf("another lead: %v", err)
	}
	var r map[string]any
	if err := call(t, e.bus, "tw", "agents_stop", map[string]any{"name": "lead", "cascade": true}, &r); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fa.calls, []string{"stop lea-a/d1", "stop lea-a/d2", "stop lea-a/lead"}) {
		t.Fatalf("calls=%q", fa.calls)
	}
	if r["stopped"] != "lead" || fmt.Sprint(r["children"]) != "[d1 d2]" {
		t.Fatalf("result=%v", r)
	}
	// None of them is signalled as dead: their parent stopped them.
	e.d.checkHealth()
	if ms := inboxOf(t, e, "tw"); len(ms) != 0 {
		t.Fatalf("tower got %+v", ms)
	}
}

func TestAStopFinishedGrantNeverCoversACascade(t *testing.T) {
	e := start(t)
	tw := spawned(t, e, "tw", "tower", "")
	lead := spawned(t, e, "lead", "lead", tw)
	e.d.st.SetState(lead, "idle", "", 2)
	o := Origin{Container: "lea-a"}
	if !e.d.finishedChild(o, tw, []byte(`{"name":"lead"}`)) {
		t.Fatal("an idle child is finished")
	}
	if e.d.finishedChild(o, tw, []byte(`{"name":"lead","cascade":true}`)) {
		t.Fatal("a cascade counted as finished")
	}
}

func TestMaxAgentsIsEnforcedAcrossTheTree(t *testing.T) {
	e := start(t)
	e.d.SetAgents(newTreeAgents(e, 4))
	tw := spawned(t, e, "tw", "tower", "")
	spawned(t, e, "l1", "lead", tw)
	if _, err := spawnAs(t, e, "tw", map[string]any{"role": "lead", "brief": "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := spawnAs(t, e, "l1", map[string]any{"role": "dev", "brief": "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := spawnAs(t, e, "l1", map[string]any{"role": "dev", "brief": "x"}); err == nil || !strings.Contains(err.Error(), "LEA_MAX_AGENTS 4") {
		t.Fatalf("fifth agent: %v", err)
	}
}

// health signals go to the child's own parent, once per episode.
func TestHealthSignalsTheParentOnce(t *testing.T) {
	e := start(t)
	e.d.SetHealth(70, 10*time.Minute)
	tw := spawned(t, e, "tw", "tower", "")
	lead := spawned(t, e, "lead", "lead", tw)
	dev := spawned(t, e, "dev1", "dev", lead)
	e.d.st.CreateTask("lea-a", dev, "fix", 1)
	e.d.st.SetState(dev, "working", "", e.clock.Load())
	notes := func(agent string) []string {
		t.Helper()
		var out []string
		for _, m := range inboxOf(t, e, agent) {
			out = append(out, m.Text)
		}
		return out
	}
	e.d.st.SetUsage(dev, store.Usage{CtxPct: 75, CostUSD: 1})
	e.d.checkHealth()
	e.d.checkHealth()
	if ms := notes("lead"); len(ms) != 1 || !strings.Contains(ms[0], "dev1 has used 75%") {
		t.Fatalf("lead notes=%q", ms)
	}
	if ms := notes("tw"); len(ms) != 0 {
		t.Fatalf("the tower got %q", ms)
	}
	// Back under the threshold (a fresh context), then over again: one more note.
	e.d.st.SetUsage(dev, store.Usage{CtxPct: 20, CostUSD: 1})
	e.d.checkHealth()
	e.d.st.SetUsage(dev, store.Usage{CtxPct: 90, CostUSD: 1})
	e.d.checkHealth()
	if ms := notes("lead"); len(ms) != 1 || !strings.Contains(ms[0], "90%") {
		t.Fatalf("lead notes=%q", ms)
	}
	// Usage limit.
	e.d.st.SetLimit(dev, -1, 1)
	e.d.checkHealth()
	e.d.checkHealth()
	if ms := notes("lead"); len(ms) != 1 || !strings.Contains(ms[0], "usage limit") || !strings.Contains(ms[0], "waits for the user") {
		t.Fatalf("lead notes=%q", ms)
	}
	e.d.st.SetLimit(dev, 0, 0)
	// Idle with its task unfinished, past LEA_IDLE_WARN.
	e.d.st.SetState(dev, "idle", "", e.clock.Load())
	e.clock.Add(9 * 60)
	e.d.checkHealth()
	if ms := notes("lead"); len(ms) != 0 {
		t.Fatalf("too early: %q", ms)
	}
	e.clock.Add(2 * 60)
	e.d.checkHealth()
	e.d.checkHealth()
	if ms := notes("lead"); len(ms) != 1 || !strings.Contains(ms[0], "idle for 11m") {
		t.Fatalf("lead notes=%q", ms)
	}
	// Ended without a result.
	e.d.st.SetState(dev, "done", "", e.clock.Load())
	e.d.checkHealth()
	e.d.checkHealth()
	if ms := notes("lead"); len(ms) != 1 || !strings.Contains(ms[0], "ended without sending you a result") {
		t.Fatalf("lead notes=%q", ms)
	}
	// The lead itself is watched by the tower.
	e.d.st.SetUsage(lead, store.Usage{CtxPct: 80, CostUSD: 1})
	e.d.checkHealth()
	if ms := notes("tw"); len(ms) != 1 || !strings.Contains(ms[0], "lead has used 80%") {
		t.Fatalf("tower notes=%q", ms)
	}
	// Off.
	e.d.SetHealth(0, 0)
	d2 := spawned(t, e, "dev2", "dev", lead)
	e.d.st.SetUsage(d2, store.Usage{CtxPct: 99, CostUSD: 1})
	e.d.checkHealth()
	if ms := notes("lead"); len(ms) != 0 {
		t.Fatalf("off: %q", ms)
	}
}

// RunHealth arms its timer through the daemon's seam (#278) and looks again each time it fires.
func TestRunHealthUsesTheTimerSeam(t *testing.T) {
	e := start(t)
	var mu sync.Mutex
	var fire []func()
	e.d.afterFunc = func(d time.Duration, f func()) *time.Timer {
		mu.Lock()
		defer mu.Unlock()
		if d != healthEvery {
			t.Errorf("delay %s", d)
		}
		fire = append(fire, f)
		return time.NewTimer(time.Hour)
	}
	lead := spawned(t, e, "lead", "lead", "")
	dev := spawned(t, e, "dev1", "dev", lead)
	e.d.st.SetUsage(dev, store.Usage{CtxPct: 95, CostUSD: 1})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.d.RunHealth(ctx)
	mu.Lock()
	f := fire[0]
	mu.Unlock()
	f()
	if ms := inboxOf(t, e, "lead"); len(ms) != 1 {
		t.Fatalf("lead inbox=%+v", ms)
	}
	mu.Lock()
	n := len(fire)
	mu.Unlock()
	if n != 2 {
		t.Fatalf("the watch did not re-arm: %d timers", n)
	}
}

func TestLeadWindow(t *testing.T) {
	e := start(t)
	tw := spawned(t, e, "tw", "tower", "")
	lead := spawned(t, e, "lead", "lead", tw)
	spawned(t, e, "dev1", "dev", lead)
	spawned(t, e, "solo", "lead", "")
	for _, tc := range []struct{ agent, lead, window, want string }{
		{"lead", "dev1", "mon-sun 21:30-23:59 Europe/Brussels", "only a tower"},
		{"tw", "solo", "mon-sun 21:30-23:59 Europe/Brussels", "not one of your leads"},
		{"tw", "dev1", "mon-sun 21:30-23:59 Europe/Brussels", "not one of your leads"},
		{"tw", "lead", "every evening", "window:"},
		{"tw", "a\x1bb", "mon 10:00-11:00 UTC", "invalid lead name"},
	} {
		if err := call(t, e.bus, tc.agent, "lead_window", map[string]any{"lead": tc.lead, "window": tc.window}, nil); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s sets %s: %v, want %q", tc.agent, tc.lead, err, tc.want)
		}
	}
	var r map[string]any
	if err := call(t, e.bus, "tw", "lead_window", map[string]any{"lead": "lead", "window": "mon-sun 21:30-23:59 Europe/Brussels"}, &r); err != nil {
		t.Fatal(err)
	}
	if a, _ := e.d.st.AgentByKey(lead); a.Window != "mon-sun 21:30-23:59 Europe/Brussels" {
		t.Fatalf("window=%q", a.Window)
	}
	if ms := inboxOf(t, e, "lead"); len(ms) != 1 || !strings.Contains(ms[0].Text, "deploy window to mon-sun 21:30-23:59") {
		t.Fatalf("lead inbox=%+v", ms)
	}
	var as []agentView
	call(t, e.bus, "tw", "agents_list", nil, &as)
	for _, a := range as {
		if a.Name == "lead" && a.Window != "mon-sun 21:30-23:59 Europe/Brussels" {
			t.Fatalf("list window=%q", a.Window)
		}
	}
	if err := call(t, e.bus, "tw", "lead_window", map[string]any{"lead": "lead", "window": ""}, nil); err != nil {
		t.Fatal(err)
	}
	if a, _ := e.d.st.AgentByKey(lead); a.Window != "" {
		t.Fatalf("cleared window=%q", a.Window)
	}
}

func TestReviewApprove(t *testing.T) {
	e := start(t)
	lead := spawned(t, e, "lead", "lead", "")
	rev, _ := e.d.st.Register(store.NewAgent{Container: "lea-a", Name: "rev", Profile: "claude", Workdir: "/p", Role: "reviewer", Parent: lead, Repo: "shop"}, store.Caps{}, 1)
	spawned(t, e, "dev1", "dev", lead)
	sha := strings.Repeat("ab", 20)
	for _, tc := range []struct {
		agent string
		p     map[string]any
		want  string
	}{
		{"dev1", map[string]any{"pr": 7, "sha": sha}, "only a reviewer"},
		{"rev", map[string]any{"pr": 0, "sha": sha}, "pr is"},
		{"rev", map[string]any{"pr": 7, "sha": "abc"}, "sha is"},
		{"rev", map[string]any{"pr": 7, "sha": strings.ToUpper(sha)}, "sha is"},
		{"rev", map[string]any{"pr": 7, "sha": sha, "repo": "other"}, "you review shop"},
	} {
		if err := call(t, e.bus, tc.agent, "review_approve", tc.p, nil); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%v: %v, want %q", tc.p, err, tc.want)
		}
	}
	if err := call(t, e.bus, "rev", "review_approve", map[string]any{"pr": 7, "sha": sha}, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.d.st.PRApprovals("lea-a", "shop", 7); !reflect.DeepEqual(got, []string{sha}) {
		t.Fatalf("approvals=%q (%s)", got, rev.Key)
	}
}

// grantCheck wraps a granted merge of an agent of a tower's tree in the production policy, with
// its lead's window and the reviewer approvals; outside a tower's tree the merge is as before.
func TestGrantCheckAppliesTheMergePolicyInATowersTree(t *testing.T) {
	e := start(t)
	tw := spawned(t, e, "tw", "tower", "")
	lead := spawned(t, e, "lead", "lead", tw)
	dev, err := e.d.st.Register(store.NewAgent{Container: "lea-a", Name: "dev1", Profile: "claude", Workdir: "/p", Role: "dev", Parent: lead,
		Repo: "shop", Branch: "fix/7"}, store.Caps{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	solo, _ := e.d.st.Register(store.NewAgent{Container: "lea-a", Name: "solo", Profile: "claude", Workdir: "/p", Role: "dev",
		Repo: "shop", Branch: "fix/8"}, store.Caps{}, 1)
	if _, err := e.d.st.AddGrant(grants.Grant{Container: "lea-a", Action: grants.Merge, Repo: "shop", GitHub: "github.com/someone/shop",
		Created: 1, Expires: grants.Standing}); err != nil {
		t.Fatal(err)
	}
	check := func(agent, key, branchCwd, cmd string) map[string]any {
		t.Helper()
		var r map[string]any
		in := map[string]any{"tool": "Bash", "input": map[string]string{"command": cmd},
			"cwd": "/p/.lea/worktrees/shop/" + branchCwd, "env": map[string]string{"HOME": "/home/u"}}
		if err := call(t, e.bus, agent, "grant_check", in, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := check("dev1", dev.Key, "dev1", "gh pr merge 7 --squash")
	cmd, _ := r["command"].(string)
	if r["verdict"] != "allow" || !strings.Contains(cmd, "lea_isprod") || !strings.Contains(cmd, "has no deploy window") ||
		!strings.Contains(cmd, "no reviewer agent approved PR 7") {
		t.Fatalf("no window: %v", r)
	}
	// Inside the lead's window, with an approval.
	e.d.st.SetDeployWindow(lead, "mon-sun 00:00-23:59 UTC")
	sha := strings.Repeat("cd", 20)
	e.d.st.AddPRApproval("lea-a", "shop", 7, sha, "lea-a/rev", 1)
	r = check("dev1", dev.Key, "dev1", "gh pr merge 7 --squash")
	cmd, _ = r["command"].(string)
	if r["verdict"] != "allow" || strings.Contains(cmd, "deploy window") || !strings.Contains(cmd, sha) {
		t.Fatalf("open window: %v", r)
	}
	// A merge the policy cannot check is denied.
	r = check("dev1", dev.Key, "dev1", "gh pr merge fix/7")
	if r["verdict"] != "deny" || !strings.Contains(fmt.Sprint(r["reason"]), "gh pr merge N") {
		t.Fatalf("branch-named merge: %v", r)
	}
	// The pinned call may report the irreversible migration it refused, once.
	if err := call(t, e.bus, "dev1", "merge_blocked", map[string]any{"pr": "7", "lines": "+DROP TABLE orders;"}, nil); err != nil {
		t.Fatal(err)
	}
	ms := inboxOf(t, e, "tw")
	if len(ms) != 1 || ms[0].Kind != "request" || !strings.Contains(ms[0].Text, "irreversible migration") ||
		!strings.Contains(ms[0].Text, "DROP TABLE orders") || !strings.Contains(ms[0].Text, "Backup") || !strings.Contains(ms[0].Text, "PR #7 of lead") {
		t.Fatalf("tower inbox=%+v", ms)
	}
	if err := call(t, e.bus, "dev1", "merge_blocked", map[string]any{"pr": "7", "lines": "x"}, nil); err == nil {
		t.Fatal("a second report passed")
	}
	if err := call(t, e.bus, "dev1", "merge_blocked", map[string]any{"pr": "9", "lines": "x"}, nil); err == nil {
		t.Fatal("a report of a merge never let through passed")
	}
	// Outside a tower's tree: the merge as before.
	r = check("solo", solo.Key, "solo", "gh pr merge 8")
	cmd, _ = r["command"].(string)
	if r["verdict"] != "allow" || strings.Contains(cmd, "lea_isprod") {
		t.Fatalf("solo: %v", r)
	}
}

// A grant scoped to the tower's lineage reaches its leads' children (#110, A6): intended, the
// tower's grant covers its whole tree.
func TestGrantLineageReachesTheTowersGrandchildren(t *testing.T) {
	e := start(t)
	tw := spawned(t, e, "tw", "tower", "")
	lead := spawned(t, e, "lead", "lead", tw)
	dev := spawned(t, e, "dev1", "dev", lead)
	c, err := e.d.grantCaller(Origin{Container: "lea-a"}, dev, "")
	if err != nil || !reflect.DeepEqual(c.Lineage, []string{dev, lead, tw}) {
		t.Fatalf("lineage=%q err=%v", c.Lineage, err)
	}
	if _, err := e.d.st.AddGrant(grants.Grant{Container: "lea-a", Action: grants.CloseIssue, Agent: tw, Repo: "shop", GitHub: "github.com/someone/shop",
		Created: 1, Expires: grants.Standing}); err != nil {
		t.Fatal(err)
	}
	var gs []map[string]any
	if err := call(t, e.bus, "dev1", "grants_list", nil, &gs); err != nil || len(gs) != 1 {
		t.Fatalf("grants=%v err=%v", gs, err)
	}
}

func TestTowerOwnersOnItsOwnRow(t *testing.T) {
	e := start(t)
	e.d.SetTowerOwners([]string{"me", "my-org"})
	spawned(t, e, "tw", "tower", "")
	spawned(t, e, "lead", "lead", "")
	var as []agentView
	if err := call(t, e.bus, "tw", "agents_list", nil, &as); err != nil {
		t.Fatal(err)
	}
	for _, a := range as {
		if a.Name == "tw" && !reflect.DeepEqual(a.Owners, []string{"me", "my-org"}) || a.Name != "tw" && a.Owners != nil {
			t.Fatalf("%s owners=%q", a.Name, a.Owners)
		}
	}
	var bs []agentView
	if err := call(t, e.bus, "lead", "agents_list", nil, &bs); err != nil {
		t.Fatal(err)
	}
	for _, a := range bs {
		if a.Owners != nil {
			t.Fatalf("a lead sees owners: %+v", a)
		}
	}
}

// The status line of an agent of a tower's tree shows its lead's deploy window (#110).
func TestStatusReportNamesTheDeployWindow(t *testing.T) {
	e := start(t)
	tw := spawned(t, e, "tw", "tower", "")
	lead := spawned(t, e, "lead", "lead", tw)
	spawned(t, e, "dev1", "dev", lead)
	spawned(t, e, "solo", "dev", "")
	report := func(agent string) map[string]any {
		t.Helper()
		var r map[string]any
		if err := call(t, e.bus, agent, "status_report", map[string]any{"ctx_pct": 5, "cost_usd": 0.1}, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := report("dev1"); r["deploy"] != "no window closed" {
		t.Fatalf("no window: %v", r)
	}
	e.d.st.SetDeployWindow(lead, "mon-sun 00:00-23:59 UTC")
	if r := report("dev1"); r["deploy"] != "00:00-23:59 open" {
		t.Fatalf("open: %v", r)
	}
	if r := report("lead"); r["deploy"] != "00:00-23:59 open" {
		t.Fatalf("the lead: %v", r)
	}
	if r := report("solo"); r["deploy"] != nil {
		t.Fatalf("outside a tower's tree: %v", r)
	}
}

// In a tower's tree a merge no grant pins is denied, whatever its form: it never reaches the deck,
// where an "always" or a user's Bash(gh pr merge:*) rule would skip the production policy (#110).
// Outside a tower's tree such a call takes the normal path, as before.
func TestGrantCheckDeniesAnUngrantedMergeInATowersTree(t *testing.T) {
	e := start(t)
	tw := spawned(t, e, "tw", "tower", "")
	lead := spawned(t, e, "lead", "lead", tw)
	_, err := e.d.st.Register(store.NewAgent{Container: "lea-a", Name: "dev1", Profile: "claude", Workdir: "/p", Role: "dev", Parent: lead,
		Repo: "shop", Branch: "fix/7"}, store.Caps{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.st.Register(store.NewAgent{Container: "lea-a", Name: "solo", Profile: "claude", Workdir: "/p", Role: "dev",
		Repo: "shop", Branch: "fix/8"}, store.Caps{}, 1); err != nil {
		t.Fatal(err)
	}
	check := func(agent, cwd, tool string, input any) map[string]any {
		t.Helper()
		var r map[string]any
		in := map[string]any{"tool": tool, "input": input, "cwd": "/p/.lea/worktrees/shop/" + cwd, "env": map[string]string{"HOME": "/home/u"}}
		if err := call(t, e.bus, agent, "grant_check", in, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	forms := []string{"gh pr merge 7 --squash", "gh api -X PUT repos/someone/shop/pulls/7/merge",
		"gh api graphql -f query=mutation-mergePullRequest", "gh api graphql -F query=@merge.graphql",
		"curl -X PUT https://api.github.com/repos/someone/shop/pulls/7/merge",
		// Percent-escaped and expanded merge paths (the forwarder decodes and lower-cases the path).
		"gh api -XPUT repos/someone/shop/pull%73/7/merge", "gh api -XPUT repos/someone/shop/pulls/7/${M}",
		// GraphQL calls lea cannot read literally, options from a file, other merge endpoints.
		"curl -X POST https://api.github.com/graphql -d @q.json", "gh api graphql --input -", "curl -K req.cfg",
		`python3 -c 'import requests; requests.post("https://api.github.com/graphql", json=q)'`,
		"curl -X POST https://api.bitbucket.org/2.0/repositories/o/r/pullrequests/7/merge"}
	// No merge grant at all: every form is denied in the tree, for the dev and the lead alike.
	for _, cmd := range forms {
		for _, who := range []struct{ name, cwd string }{{"dev1", "dev1"}, {"lead", "lead"}} {
			r := check(who.name, who.cwd, "Bash", map[string]string{"command": cmd})
			if r["verdict"] != "deny" || !strings.Contains(fmt.Sprint(r["reason"]), "matches no grant") {
				t.Fatalf("%s %q: %v", who.name, cmd, r)
			}
		}
		if r := check("solo", "solo", "Bash", map[string]string{"command": cmd}); r["verdict"] != "" {
			t.Fatalf("solo %q: %v", cmd, r)
		}
	}
	if r := check("dev1", "dev1", "mcp__github__merge_pull_request", map[string]any{"pullNumber": 7}); r["verdict"] != "deny" {
		t.Fatalf("MCP merge: %v", r)
	}
	// A call that is no merge keeps the normal path.
	if r := check("dev1", "dev1", "Bash", map[string]string{"command": "gh pr view 7 --json mergeable"}); r["verdict"] != "" {
		t.Fatalf("pr view: %v", r)
	}
	// With the tree's merge grant, gh pr merge N is pinned under the policy; the API forms stay denied.
	if _, err := e.d.st.AddGrant(grants.Grant{Container: "lea-a", Action: grants.Merge, Repo: "shop", GitHub: "github.com/someone/shop",
		Created: 1, Expires: grants.Standing}); err != nil {
		t.Fatal(err)
	}
	if r := check("dev1", "dev1", "Bash", map[string]string{"command": forms[0]}); r["verdict"] != "allow" || !strings.Contains(fmt.Sprint(r["command"]), "lea_isprod") {
		t.Fatalf("granted merge: %v", r)
	}
	for _, cmd := range forms[1:] {
		if r := check("dev1", "dev1", "Bash", map[string]string{"command": cmd}); r["verdict"] != "deny" {
			t.Fatalf("granted tree, %q: %v", cmd, r)
		}
	}
}

// In a tower's tree a forge write that rewrites a file on a branch or moves one (contents API, git
// refs) is denied, granted tree or not: it would rewrite the trusted .lea/production or move a
// production branch past the policy (#110). Outside a tower's tree it takes the normal path.
func TestGrantCheckDeniesBranchWritesInATowersTree(t *testing.T) {
	e := start(t)
	tw := spawned(t, e, "tw", "tower", "")
	lead := spawned(t, e, "lead", "lead", tw)
	if _, err := e.d.st.Register(store.NewAgent{Container: "lea-a", Name: "dev1", Profile: "claude", Workdir: "/p", Role: "dev", Parent: lead,
		Repo: "shop", Branch: "fix/7"}, store.Caps{}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.st.Register(store.NewAgent{Container: "lea-a", Name: "solo", Profile: "claude", Workdir: "/p", Role: "dev",
		Repo: "shop", Branch: "fix/8"}, store.Caps{}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.st.AddGrant(grants.Grant{Container: "lea-a", Action: grants.Merge, Repo: "shop", GitHub: "github.com/someone/shop",
		Created: 1, Expires: grants.Standing}); err != nil {
		t.Fatal(err)
	}
	check := func(agent, tool string, input any) map[string]any {
		t.Helper()
		var r map[string]any
		in := map[string]any{"tool": tool, "input": input, "cwd": "/p/.lea/worktrees/shop/" + agent, "env": map[string]string{"HOME": "/home/u"}}
		if err := call(t, e.bus, agent, "grant_check", in, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	for _, cmd := range []string{
		"gh api -X PUT repos/someone/shop/contents/.lea/production -f branch=main -f message=x -f content=eA== -f sha=abc",
		"gh api -X PATCH repos/someone/shop/git/refs/heads/main -f sha=X -F force=true",
		"gh api -X DELETE repos/someone/shop/git/refs/heads/release", "gh api repos/someone/shop/git/refs -f ref=refs/heads/main -f sha=X",
	} {
		for _, who := range []string{"dev1", "lead"} {
			if r := check(who, "Bash", map[string]string{"command": cmd}); r["verdict"] != "deny" || !strings.Contains(fmt.Sprint(r["reason"]), "tower's tree") {
				t.Fatalf("%s %q: %v", who, cmd, r)
			}
		}
		if r := check("solo", "Bash", map[string]string{"command": cmd}); r["verdict"] != "" {
			t.Fatalf("solo %q: %v", cmd, r)
		}
	}
	if r := check("dev1", "mcp__github__create_or_update_file", map[string]any{"path": ".lea/production"}); r["verdict"] != "deny" {
		t.Fatalf("MCP file write: %v", r)
	}
	// Reads keep the normal path.
	if r := check("dev1", "Bash", map[string]string{"command": "gh api repos/someone/shop/contents/.lea/production"}); r["verdict"] != "" {
		t.Fatalf("contents read: %v", r)
	}
}

// In a tower's tree a push never reaches a production branch (#110, re-review B1): a push no
// grant pins is denied whatever its form (its own branch may be production: release, a default
// branch other than main), and a granted push is pinned behind the production-branch check.
// Outside a tower's tree a push takes the normal path, as before.
func TestGrantCheckGuardsPushesInATowersTree(t *testing.T) {
	e := start(t)
	tw := spawned(t, e, "tw", "tower", "")
	lead := spawned(t, e, "lead", "lead", tw)
	for _, a := range []store.NewAgent{
		{Name: "dev1", Role: "dev", Parent: lead, Branch: "release"},
		{Name: "dev2", Role: "dev", Parent: lead, Branch: "develop"},
		{Name: "solo", Role: "dev", Branch: "staging"},
	} {
		a.Container, a.Profile, a.Workdir, a.Repo = "lea-a", "claude", "/p", "shop"
		if _, err := e.d.st.Register(a, store.Caps{}, 1); err != nil {
			t.Fatal(err)
		}
	}
	check := func(agent, cmd string) map[string]any {
		t.Helper()
		var r map[string]any
		in := map[string]any{"tool": "Bash", "input": map[string]string{"command": cmd}, "cwd": "/p/.lea/worktrees/shop/" + agent,
			"env": map[string]string{"HOME": "/home/u"}}
		if err := call(t, e.bus, agent, "grant_check", in, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	raw := func(b string) []string {
		return []string{"git push origin " + b, "git push -u origin " + b, "git push origin refs/heads/" + b, "git push origin HEAD:refs/heads/" + b,
			"git push origin " + b + ":refs/heads/" + b, "cd /p/.lea/worktrees/shop && git push origin " + b, "git add -A && git commit -m x && git push origin " + b}
	}
	denied := []string{"git push origin +release", "git push --all origin", "git push --mirror origin", "git push origin HEAD:release", "git push -f origin release", "echo $(git push origin release)"}
	// No push grant: every push of the tree is denied, for the dev and the lead alike.
	for _, who := range []struct{ name, branch string }{{"dev1", "release"}, {"dev2", "develop"}} {
		for _, cmd := range raw(who.branch) {
			if r := check(who.name, cmd); r["verdict"] != "deny" || !strings.Contains(fmt.Sprint(r["reason"]), "tower's tree") {
				t.Fatalf("%s %q: %v", who.name, cmd, r)
			}
		}
	}
	for _, cmd := range denied {
		if r := check("dev1", cmd); r["verdict"] != "deny" {
			t.Fatalf("%q: %v", cmd, r)
		}
	}
	if r := check("lead", "git push origin main"); r["verdict"] != "deny" {
		t.Fatalf("lead: %v", r)
	}
	// What only names a push is not one.
	for _, cmd := range []string{"echo git push origin release", "git status", `gh pr create --title x --body "then git push origin release"`} {
		if r := check("dev1", cmd); r["verdict"] == "deny" && strings.Contains(fmt.Sprint(r["reason"]), "git push runs only") {
			t.Fatalf("%q: %v", cmd, r)
		}
	}
	// Outside a tower's tree: never the tree's deny.
	if r := check("solo", "git push origin staging"); strings.Contains(fmt.Sprint(r["reason"]), "tower's tree") {
		t.Fatalf("solo: %v", r)
	}
	// The tree's push grant (commit-identity, part of the delivery grants): pinned behind the check.
	if _, err := e.d.st.AddGrant(grants.Grant{Container: "lea-a", Action: grants.CommitIdentity, Repo: "shop", GitHub: "github.com/someone/shop",
		Created: 1, Expires: grants.Standing}); err != nil {
		t.Fatal(err)
	}
	for _, who := range []struct{ name, branch string }{{"dev1", "release"}, {"dev2", "develop"}} {
		r := check(who.name, "git push origin "+who.branch)
		cmd := fmt.Sprint(r["command"])
		if r["verdict"] != "allow" || !strings.Contains(cmd, "lea_dst=") || !strings.Contains(cmd, who.branch) || !strings.Contains(cmd, ".lea/production") {
			t.Fatalf("granted %s: %v", who.name, r)
		}
		// A refspec the grant does not pin stays denied.
		for _, c := range raw(who.branch)[2:5] {
			if r := check(who.name, c); r["verdict"] != "deny" {
				t.Fatalf("granted tree, %q: %v", c, r)
			}
		}
	}
	if r := check("solo", "git push origin staging"); r["verdict"] != "allow" || strings.Contains(fmt.Sprint(r["command"]), "lea_dst") {
		t.Fatalf("solo, granted: %v", r)
	}
}

// An agent of a tower's tree is never started on a production branch (#110): main, master and
// HEAD are refused by the daemon, and the manager is told the agent joins a tree (it reads the
// repository's default branch and .lea/production).
func TestSpawnRefusesAProductionBranchInATowersTree(t *testing.T) {
	e := start(t)
	ta := newTreeAgents(e, 0)
	e.d.SetAgents(ta)
	tw := spawned(t, e, "tw", "tower", "")
	spawned(t, e, "l1", "lead", tw)
	spawned(t, e, "solo-lead", "lead", "")
	for _, who := range []string{"tw", "l1"} {
		role := "dev"
		if who == "tw" {
			role = "lead"
		}
		for _, b := range []string{"main", "master", "HEAD", "refs/heads/main"} {
			if _, err := spawnAs(t, e, who, map[string]any{"role": role, "brief": "x", "branch": b}); err == nil || !strings.Contains(err.Error(), "production branch") {
				t.Fatalf("%s branch %s: %v", who, b, err)
			}
		}
		if _, err := spawnAs(t, e, who, map[string]any{"role": role, "brief": "x", "branch": "fix/7"}); err != nil {
			t.Fatal(err)
		}
		if !ta.req.Tree {
			t.Fatalf("%s: the manager was not told the agent joins a tree", who)
		}
	}
	if _, err := spawnAs(t, e, "solo-lead", map[string]any{"role": "dev", "brief": "x", "branch": "fix/9"}); err != nil || ta.req.Tree {
		t.Fatalf("outside a tree: tree=%v err=%v", ta.req.Tree, err)
	}
}
