package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/DavidGodefroid/lea/internal/bus"
	"github.com/DavidGodefroid/lea/internal/store"
)

// Grant requests (#342): the user approves a merge or a release tag in a lead's chat, and lea had
// no way from those words to a grant (a message relayed to a child is an agent's text, never a
// grant). A lead files grants_request: it queues a request, checked as grant_add checks a grant,
// and records nothing. The user decides on the host (lea approve g<N>, lea grant approve <N>):
// grant_request_decide is host-only (dispatch), and only it records the grant, scoped to the
// lead's subtree (its key as the grant's agent), so the lead's children are covered and no one
// else is.

const (
	// maxPendingGrantRequests caps a lead's open grant requests: one per repository and action
	// of a team's work.
	maxPendingGrantRequests = 20
	// grantRequestTTL: a request the user did not answer in a day expires, and the lead is told.
	grantRequestTTL = 24 * 3600
)

// grantsRequest is a lead's request for a grant covering itself and its children.
func (d *Daemon) grantsRequest(ctx context.Context, o Origin, me string, raw json.RawMessage) (any, error) {
	caller, err := d.spawnedCaller(o, me, "grants_request")
	if err != nil {
		return nil, err
	}
	if !canSpawn(caller.Role) {
		return nil, errors.New("grants_request: leads only: ask your lead (msg_send) to file it for you")
	}
	var p struct {
		grantParams
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	// The lea is the caller's, the subtree the caller's own: nothing the request says.
	p.Container, p.Agent = o.Container, ""
	ttl, err := checkGrant(&p.grantParams)
	if err != nil {
		return nil, err
	}
	if len(p.Reason) > maxReason {
		return nil, fmt.Errorf("reason is %d bytes, limit %d", len(p.Reason), maxReason)
	}
	if err := bus.CheckText(p.Reason); err != nil {
		return nil, fmt.Errorf("reason %w", err)
	}
	d.sweepGrantRequests()
	r, err := d.st.AddGrantRequest(store.GrantRequest{Container: o.Container, Requester: me, Action: p.Action, Repo: p.Repo,
		Branch: p.Branch, GitHub: p.GitHub, TTLS: int64(ttl / time.Second), Reason: p.Reason}, maxPendingGrantRequests, d.now())
	if errors.Is(err, store.ErrExists) {
		return map[string]any{"request": fmt.Sprintf("g%d", r.ID), "state": "pending",
			"note": "the same request already waits for the user"}, nil
	}
	if err != nil {
		return nil, err
	}
	d.changed()
	text := fmt.Sprintf("%s asks for a %s grant: lea approve g%d or lea deny g%d (lea requests)", shortName(me), describeRequest(r), r.ID, r.ID)
	d.post(me, "user", "request", text, 0)
	d.attention(ctx, o.Container, me, text)
	return map[string]any{"request": fmt.Sprintf("g%d", r.ID), "state": "pending",
		"note": fmt.Sprintf("the user decides on the host (lea approve g%d); you get a message when it is decided. Until then no grant exists: tell the user, in one line, what to approve.", r.ID)}, nil
}

// describeRequest is a request as the user reads it.
func describeRequest(r store.GrantRequest) string {
	s := r.Action + " on repo " + r.Repo + " (" + r.GitHub + ")"
	if r.Branch != "" && r.Branch != "*" {
		s += " branch " + r.Branch
	}
	return s + " for " + shortName(r.Requester) + " and its children, " + (time.Duration(r.TTLS) * time.Second).String()
}

// sweepGrantRequests expires the requests nobody answered and tells their requesters.
func (d *Daemon) sweepGrantRequests() {
	rs, err := d.st.SweepGrantRequests(grantRequestTTL, d.now())
	if err != nil {
		log.Printf("lea daemon: sweep grant requests: %v", err)
		return
	}
	for _, r := range rs {
		d.post("user", r.Requester, "result", fmt.Sprintf("[status: fail] grant request g%d expired: the user did not answer it (%s)",
			r.ID, describeRequest(r)), 0)
	}
}

type grantRequestView struct {
	ID        int64  `json:"id"`
	Container string `json:"container"`
	From      string `json:"from"`
	Action    string `json:"action"`
	Repo      string `json:"repo"`
	Branch    string `json:"branch"`
	GitHub    string `json:"github"`
	TTLS      int64  `json:"ttl_s"`
	Reason    string `json:"reason"`
	At        int64  `json:"at"`
}

// grantRequestList is the pending grant requests (host only).
func (d *Daemon) grantRequestList() ([]grantRequestView, error) {
	d.sweepGrantRequests()
	rs, err := d.st.PendingGrantRequests(maxGrantList)
	if err != nil {
		return nil, err
	}
	out := []grantRequestView{}
	for _, r := range rs {
		out = append(out, grantRequestView{r.ID, r.Container, shortName(r.Requester), r.Action, r.Repo, r.Branch, r.GitHub, r.TTLS, r.Reason, r.At})
	}
	return out, nil
}

// grantRequestDecide is the user's answer to a grant request (host only): approve records the
// grant (for the requester's subtree; ttl_s, when given, replaces the life the lead asked for),
// deny records nothing. Either way the requester is told.
func (d *Daemon) grantRequestDecide(raw json.RawMessage) (any, error) {
	var p struct {
		ID      int64  `json:"id"`
		Verdict string `json:"verdict"`
		Reason  string `json:"reason"`
		TTLS    int64  `json:"ttl_s"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	d.sweepGrantRequests()
	r, err := d.st.GrantRequestByID(p.ID)
	if err != nil {
		return nil, fmt.Errorf("no grant request g%d", p.ID)
	}
	if r.State != "pending" {
		return nil, fmt.Errorf("grant request g%d is %s", r.ID, r.State)
	}
	switch p.Verdict {
	case "approve":
		gp := grantParams{Container: r.Container, Action: r.Action, Repo: r.Repo, Branch: r.Branch, GitHub: r.GitHub, TTLS: r.TTLS}
		if p.TTLS != 0 {
			gp.TTLS = p.TTLS
		}
		// Checked again: the rules may be stricter than when the lead asked.
		ttl, err := checkGrant(&gp)
		if err != nil {
			return nil, err
		}
		a, err := d.st.AgentByKey(r.Requester)
		if err != nil || a.State == "done" {
			return nil, fmt.Errorf("grant request g%d: its lead %s is gone", r.ID, shortName(r.Requester))
		}
		gp.Agent = r.Requester
		g, err := d.recordGrant(gp, ttl)
		if err != nil {
			return nil, err
		}
		if err := d.st.DecideGrantRequest(r.ID, "approved", g.ID, d.now()); err != nil {
			// Decided meanwhile (another lea approve): the first answer stands.
			if rerr := d.st.RevokeGrant(g.ID, d.now()); rerr != nil {
				log.Printf("lea daemon: revoke grant #%d of a decided request: %v", g.ID, rerr)
			}
			d.changed()
			return nil, fmt.Errorf("grant request g%d: %w", r.ID, err)
		}
		r.TTLS = int64(ttl / time.Second)
		d.post("user", r.Requester, "result", fmt.Sprintf("[status: ok] grant request g%d approved by the user: grant #%d, %s."+
			" It covers you and your children from now (grants_list); tell them to run the covered command again.", r.ID, g.ID, describeRequest(r)), 0)
		return g, nil
	case "deny":
		reason := p.Reason
		if reason == "" {
			reason = "denied by the user"
		}
		if len(reason) > maxReason || bus.CheckText(reason) != nil {
			return nil, errors.New("reason: at most 2 KiB of text without control characters")
		}
		if err := d.st.DecideGrantRequest(r.ID, "denied", 0, d.now()); err != nil {
			return nil, fmt.Errorf("grant request g%d: %w", r.ID, err)
		}
		d.post("user", r.Requester, "result", fmt.Sprintf("[status: fail] grant request g%d denied (%s): %s", r.ID, describeRequest(r), reason), 0)
		return struct{}{}, nil
	}
	return nil, errors.New("verdict must be approve or deny")
}
