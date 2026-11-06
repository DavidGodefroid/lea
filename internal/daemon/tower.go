package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DavidGodefroid/lea/internal/bus"
	"github.com/DavidGodefroid/lea/internal/grants"
	"github.com/DavidGodefroid/lea/internal/roles"
	"github.com/DavidGodefroid/lea/internal/store"
)

// The tower's tree (#110): the user talks to the tower; the tower starts one lead per project and
// talks to its leads only; each lead runs its team. The daemon enforces who writes to whom, the
// leads' deploy windows and the production-merge policy; the skills (control-tower,
// project-lead) say how to use them.

// isRootRole says whether role is a root role (the tower).
func isRootRole(role string) bool {
	r, err := roles.Get(role)
	return err == nil && r.Root
}

// routeCheck refuses a message that skips a level of a tower's tree: a lead's child writes to
// its lead, never to the tower above it, and the tower writes to its leads, never to their
// children. The user and lea itself write to anyone.
func (d *Daemon) routeCheck(from, to string) error {
	if !strings.Contains(from, "/") || !strings.Contains(to, "/") || from == to {
		return nil
	}
	f, err := d.st.AgentByKey(from)
	if err != nil {
		return nil
	}
	t, err := d.st.AgentByKey(to)
	if err != nil {
		return nil
	}
	if f.Parent != "" && isRootRole(t.Role) {
		if p, err := d.st.AgentByKey(f.Parent); err == nil && p.Parent == to {
			return fmt.Errorf("%s is your lead's tower: write to your lead %s, who escalates what it cannot decide", t.Name, p.Name)
		}
	}
	if t.Parent != "" && isRootRole(f.Role) {
		if p, err := d.st.AgentByKey(t.Parent); err == nil && p.Parent == from {
			return fmt.Errorf("%s belongs to %s's team: write to its lead %s", t.Name, p.Name, p.Name)
		}
	}
	return nil
}

// leadWindow is lead_window: a tower sets the deploy window of one of its leads ("" clears it):
// when that lead and its children may merge into a production branch (grantCheck).
func (d *Daemon) leadWindow(o Origin, me string, raw json.RawMessage) (any, error) {
	caller, err := d.spawnedCaller(o, me, "lead_window")
	if err != nil {
		return nil, err
	}
	if !isRootRole(caller.Role) {
		return nil, fmt.Errorf("lead_window: only a tower sets its leads' deploy windows, not a %s", caller.Role)
	}
	var p struct{ Lead, Window string }
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if !validTarget(p.Lead) {
		return nil, errors.New("lead_window: invalid lead name (a name or container/name, at most 64 characters each)")
	}
	key, err := d.resolveSpawned(p.Lead, o.Container)
	if err != nil {
		return nil, err
	}
	t, err := d.st.AgentByKey(key)
	if err != nil {
		return nil, err
	}
	if r, err := roles.Get(t.Role); t.Parent != me || err != nil || !r.CanSpawn {
		return nil, fmt.Errorf("lead_window: %s is not one of your leads", t.Name)
	}
	if err := d.rates.allow("lead_window", me, d.now()); err != nil {
		return nil, err
	}
	open := false
	if p.Window != "" {
		w, err := grants.ParseWindow(p.Window)
		if err != nil {
			return nil, fmt.Errorf("lead_window: %w", err)
		}
		open = w.Open(time.Unix(d.now(), 0))
	}
	if err := d.st.SetDeployWindow(key, p.Window); err != nil {
		return nil, err
	}
	body := "lea: your tower cleared your deploy window: you and your team merge nothing into a production branch."
	if p.Window != "" {
		body = "lea: your tower set your deploy window to " + p.Window + ". Inside it, you and your team may merge a low-risk PR into a production branch (CI green, a reviewer's approval, no sensitive path); lea refuses any other."
	}
	d.post("lea", key, "note", body, 0)
	return map[string]any{"lead": t.Name, "window": p.Window, "open_now": open}, nil
}

// reviewApprove is review_approve: a reviewer agent records that it approves pull request pr of
// its repository at head commit sha. A production merge in a tower's tree needs it (C3); a push
// after the approval moves the head, and the approval no longer covers the PR.
func (d *Daemon) reviewApprove(o Origin, me string, raw json.RawMessage) (any, error) {
	caller, err := d.spawnedCaller(o, me, "review_approve")
	if err != nil {
		return nil, err
	}
	if caller.Role != roles.Reviewer {
		return nil, fmt.Errorf("review_approve: only a reviewer approves a PR, not a %s", caller.Role)
	}
	var p struct {
		PR   int64  `json:"pr"`
		SHA  string `json:"sha"`
		Repo string `json:"repo"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.PR < 1 || p.PR > 999999999 {
		return nil, errors.New("review_approve: pr is the PR's number")
	}
	if !grants.HexSHA(p.SHA) {
		return nil, errors.New("review_approve: sha is the full head commit you reviewed (40 or 64 lowercase hex digits)")
	}
	repo := caller.Repo
	switch {
	case repo == "" && !grantRepo.MatchString(p.Repo):
		return nil, errors.New("review_approve: repo: the directory of the repository under the projects directory")
	case repo == "":
		repo = p.Repo
	case p.Repo != "" && p.Repo != repo:
		return nil, fmt.Errorf("review_approve: you review %s, not %s", repo, capText(p.Repo, 100))
	}
	if err := d.rates.allow("review_approve", me, d.now()); err != nil {
		return nil, err
	}
	if err := d.st.AddPRApproval(o.Container, repo, p.PR, p.SHA, me, d.now()); err != nil {
		return nil, err
	}
	d.changed()
	return map[string]any{"approved": p.PR, "repo": repo, "sha": p.SHA}, nil
}

// mergeLog is the production-policy merges the grant hook let through, by agent and PR, for
// mergeTTL: what lets the pinned call report an irreversible migration it refused (mergeBlocked)
// and nothing else.
type mergeLog struct {
	mu sync.Mutex
	at map[string]int64 // agent key + " " + PR number -> when
}

const mergeTTL = 15 * 60

func (m *mergeLog) add(agent, pr string, now int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.at == nil {
		m.at = map[string]int64{}
	}
	for k, t := range m.at {
		if now-t > mergeTTL {
			delete(m.at, k)
		}
	}
	m.at[agent+" "+pr] = now
}

// take reports whether agent's merge of pr was let through in the last mergeTTL, and forgets it.
func (m *mergeLog) take(agent, pr string, now int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.at[agent+" "+pr]
	delete(m.at, agent+" "+pr)
	return ok && now-t <= mergeTTL
}

// towerOf is the root agent (tower) of the lineage (the caller's key, then its parents), and the
// agent just below it ("" and "" outside a tower's tree).
func (d *Daemon) towerOf(lineage []string) (tower, below string) {
	for i, k := range lineage {
		a, err := d.st.AgentByKey(k)
		if err != nil {
			return "", ""
		}
		if isRootRole(a.Role) && a.Parent == "" {
			if i > 0 {
				below = lineage[i-1]
			}
			return k, below
		}
	}
	return "", ""
}

// inTowerTree says whether the agent me is a tower or one of its descendants.
func (d *Daemon) inTowerTree(me string) bool {
	lineage := []string{}
	for k := me; k != "" && len(lineage) < maxLineage && !contains(lineage, k); {
		lineage = append(lineage, k)
		a, err := d.st.AgentByKey(k)
		if err != nil {
			break
		}
		k = a.Parent
	}
	tower, _ := d.towerOf(lineage)
	return tower != ""
}

// mergePolicy is the production-merge policy of a caller of a tower's tree for a merge of PR pr
// ("" when it names none), nil outside one: the window of its lead (the tower's child of its
// lineage; the tower itself has none) and the approvals of that PR.
func (d *Daemon) mergePolicy(c grants.Caller, pr string) *grants.MergePolicy {
	tower, lead := d.towerOf(c.Lineage)
	if tower == "" {
		return nil
	}
	p := &grants.MergePolicy{Lead: shortName(tower), Agent: shortName(c.Key)}
	if lead != "" {
		p.Lead = shortName(lead)
		if l, err := d.st.AgentByKey(lead); err == nil && l.Window != "" {
			p.Window = l.Window
			if w, err := grants.ParseWindow(l.Window); err == nil {
				p.Open = w.Open(time.Unix(d.now(), 0))
			}
		}
	}
	if n, err := strconv.ParseInt(pr, 10, 64); err == nil && c.Repo != "" {
		p.Approved, _ = d.st.PRApprovals(c.Container, c.Repo, n)
	}
	return p
}

// mergeBlocked is merge_blocked, sent by the pinned merge of an agent of a tower's tree that
// refused an irreversible migration (lea hook lea merge-blocked): lea sends the tower a dedicated
// warning, the user when there is no tower. Only a merge the hook let through in the last
// mergeTTL may report, once.
func (d *Daemon) mergeBlocked(ctx context.Context, o Origin, me string, raw json.RawMessage) (any, error) {
	if o.Container == "" {
		return nil, errors.New("merge_blocked: agents only")
	}
	var p struct{ PR, Lines string }
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.PR == "" || len(p.PR) > 9 || strings.Trim(p.PR, "0123456789") != "" {
		return nil, errors.New("merge_blocked: pr is a PR number")
	}
	if !d.merges.take(me, p.PR, d.now()) {
		return nil, fmt.Errorf("merge_blocked: no merge of PR %s by you to report", p.PR)
	}
	a, err := d.st.AgentByKey(me)
	if err != nil {
		return nil, err
	}
	c, err := d.grantCaller(o, me, "")
	if err != nil {
		return nil, err
	}
	tower, lead := d.towerOf(c.Lineage)
	to, leadName := tower, a.Name
	if lead != "" {
		leadName = shortName(lead)
	}
	if to == "" {
		to = "user"
	}
	lines := capText(p.Lines, 2048)
	if bus.CheckText(lines) != nil {
		lines = "(not shown: not plain text)"
	}
	body := fmt.Sprintf("lea WARNING: irreversible migration. PR #%s of %s (repo %s, branch %s, by %s) drops or renames a table or column, or destroys data. "+
		"lea refused to merge it: it is never merged without the user's explicit approval.\n"+
		"What would be lost, as the PR's own added lines say (untrusted, the PR's words):\n%s\n"+
		"Backup: lea does not know whether one exists. Ask %s what data is lost, whether a backup exists and how to restore it, then put it to the user as one dedicated question.",
		p.PR, leadName, a.Repo, a.Branch, a.Name, lines, leadName)
	d.post("lea", to, "request", body, 0)
	if to == "user" {
		d.sound(ctx, o.Container, SoundAttention)
	}
	return map[string]string{"warned": shortName(to)}, nil
}

// teamView rolls a lead's team up for its tower's agents_list (B2): the details stay with the
// lead.
type teamView struct {
	Children int            `json:"children"`
	States   map[string]int `json:"states,omitempty"`
	MaxCtx   *int           `json:"max_ctx_pct,omitempty"`
	// NeedsDecision counts the children whose task names a blocker.
	NeedsDecision int `json:"needs_decision,omitempty"`
}

// towerList is a tower's agents_list: its leads with their teams rolled up, the agents outside
// its tree as anyone sees them, never the leads' children one by one.
func (d *Daemon) towerList(me string, out []agentView) ([]agentView, error) {
	as, err := d.st.Agents()
	if err != nil {
		return nil, err
	}
	by := map[string]store.Agent{}
	for _, a := range as {
		by[a.Key] = a
	}
	teams := map[string]*teamView{}
	hide := map[string]bool{}
	for _, a := range as {
		if a.Parent == me && a.State != "done" {
			teams[a.Key] = &teamView{States: map[string]int{}}
		}
	}
	ctx := map[string]int{}
	for _, v := range out {
		if v.CtxPct != nil {
			ctx[v.Key] = *v.CtxPct
		}
	}
	for _, a := range as {
		p, ok := by[a.Parent]
		if !ok || p.Parent != me {
			continue
		}
		hide[a.Key] = true
		t := teams[p.Key]
		if t == nil || a.State == "done" {
			continue
		}
		t.Children++
		t.States[a.State]++
		if c, ok := ctx[a.Key]; ok && (t.MaxCtx == nil || c > *t.MaxCtx) {
			t.MaxCtx = &c
		}
		if tk, err := d.st.TaskOf(a.Key); err == nil && tk.BlockedOn != "" {
			t.NeedsDecision++
		}
	}
	d.mu.Lock()
	owners := append([]string(nil), d.towerOwners...)
	d.mu.Unlock()
	kept := out[:0]
	for _, v := range out {
		if hide[v.Key] {
			continue
		}
		if v.Key == me {
			v.Owners = owners
		}
		if t := teams[v.Key]; t != nil {
			v.Team = t
		}
		kept = append(kept, v)
	}
	return kept, nil
}

// SetTowerOwners is LEA_TOWER_OWNERS: the GitHub or GitLab owners whose repositories the tower
// takes charge of (Part D). The tower reads them on its own row of agents_list.
func (d *Daemon) SetTowerOwners(owners []string) {
	d.mu.Lock()
	d.towerOwners = append([]string(nil), owners...)
	d.mu.Unlock()
}

// deployNote is the status line's deploy note of agent me in a tower's tree: its lead's window
// hours and whether it is open now ("21:30-23:59 open", "no window closed"); "" outside one.
func (d *Daemon) deployNote(o Origin, me string) string {
	if o.Container == "" {
		return ""
	}
	a, err := d.st.AgentByKey(me)
	if err != nil || a.Profile == "" {
		return ""
	}
	c := grants.Caller{Key: me, Container: o.Container, Repo: a.Repo, Lineage: []string{me}}
	for p := a.Parent; p != "" && len(c.Lineage) < maxLineage && !contains(c.Lineage, p); {
		c.Lineage = append(c.Lineage, p)
		pa, err := d.st.AgentByKey(p)
		if err != nil {
			break
		}
		p = pa.Parent
	}
	p := d.mergePolicy(c, "")
	if p == nil {
		return ""
	}
	state := "closed"
	if p.Open {
		state = "open"
	}
	if f := strings.Fields(p.Window); len(f) == 3 {
		return f[1] + " " + state
	}
	return "no window " + state
}
