package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/DavidGodefroid/lea/internal/agents"
	"github.com/DavidGodefroid/lea/internal/bus"
	"github.com/DavidGodefroid/lea/internal/roles"
	"github.com/DavidGodefroid/lea/internal/store"
)

const (
	// maxPendingRequests is spec §5.2's "at most 2 pending requests per agent".
	maxPendingRequests = 2
	// requestTTL: a request nobody answered for an hour expires and frees its slot.
	requestTTL = 3600
	maxReason  = 2 << 10
	// listBrief cuts each brief in request_list, so the list always fits on the wire.
	listBrief = 2 << 10
	maxListed = 20
)

type requestParams struct {
	Role    string `json:"role"`
	Profile string `json:"profile"`
	Repo    string `json:"repo"`
	Branch  string `json:"branch"`
	Brief   string `json:"brief"`
	Reason  string `json:"reason"`
}

func canSpawn(role string) bool {
	r, err := roles.Get(role)
	return err == nil && r.CanSpawn
}

// requestSpawn turns an agent's wish for another agent into a spawn_request message to whoever
// decides (spec §5.2). The request is checked here; an acceptance is checked again in full.
func (d *Daemon) requestSpawn(o Origin, me string, raw json.RawMessage) (any, error) {
	caller, err := d.spawnedCaller(o, me, "agents_request_spawn")
	if err != nil {
		return nil, err
	}
	var p requestParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if canSpawn(caller.Role) {
		return nil, errors.New("agents_request_spawn: you are a lead: start the agent yourself with agents_spawn")
	}
	if err := checkLens(p.Role, p.Profile, p.Repo, p.Branch); err != nil {
		return nil, err
	}
	if p.Role == "" {
		p.Role = roles.Default
	}
	if r, err := roles.Get(p.Role); err != nil {
		return nil, err
	} else if r.CanSpawn {
		return nil, fmt.Errorf("agents_request_spawn: a %s cannot be requested: a lead's child never spawns", p.Role)
	}
	if p.Profile == "" {
		p.Profile = agents.DefaultProfile
	}
	if p.Profile != agents.DefaultProfile {
		return nil, fmt.Errorf("profile %q is not available yet: this version runs %s only", p.Profile, agents.DefaultProfile)
	}
	if err := agents.CheckRepo(p.Repo); err != nil {
		return nil, err
	}
	if err := agents.CheckBranch(p.Branch); err != nil {
		return nil, err
	}
	if err := checkBrief(p.Brief, true); err != nil {
		return nil, err
	}
	if len(p.Reason) > maxReason {
		return nil, fmt.Errorf("reason is %d bytes, limit %d", len(p.Reason), maxReason)
	}
	if err := bus.CheckText(p.Reason); err != nil {
		return nil, fmt.Errorf("reason %w", err)
	}
	if err := d.rates.allow("agents_request_spawn", me, d.now()); err != nil {
		return nil, err
	}
	lead := d.routeRequest(caller)
	r := store.SpawnRequest{Container: o.Container, Requester: me, Lead: lead, Role: p.Role, Profile: p.Profile,
		Repo: p.Repo, Branch: p.Branch, Brief: p.Brief, Reason: p.Reason}
	added, err := d.st.AddSpawnRequest(r, func(id int64) string { return requestBody(id, r, caller.Name) },
		maxPendingRequests, requestTTL, d.now())
	if err != nil {
		return nil, err
	}
	d.wake(lead)
	d.changed()
	return map[string]any{"request": added.ID, "sent_to": shortName(lead)}, nil
}

// routeRequest picks who decides (spec §5.2): the requester's parent when it is a running lead,
// else the oldest running lead of its container, else the user.
func (d *Daemon) routeRequest(caller store.Agent) string {
	if caller.Parent != "" {
		if p, err := d.st.AgentByKey(caller.Parent); err == nil && p.State != "done" && canSpawn(p.Role) {
			return p.Key
		}
	}
	as, err := d.st.SpawnedAgents(caller.Container)
	if err != nil {
		log.Printf("lea daemon: route a spawn request of %s: %v", caller.Key, err)
		return "user"
	}
	best, at := "", int64(0)
	for _, a := range as {
		if a.Key != caller.Key && a.State != "done" && canSpawn(a.Role) && (best == "" || a.Created < at) {
			best, at = a.Key, a.Created
		}
	}
	if best == "" {
		return "user"
	}
	return best
}

// requestBody is the spawn_request message: the request in plain words, then how to answer it.
// It is delivered wrapped as untrusted content from the requester, like any message.
func requestBody(id int64, r store.SpawnRequest, requester string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Spawn request #%d from %s: a %s agent (profile %s", id, requester, r.Role, r.Profile)
	if r.Repo != "" {
		fmt.Fprintf(&b, ", repo %s", r.Repo)
	}
	if r.Branch != "" {
		fmt.Fprintf(&b, ", branch %s", r.Branch)
	}
	b.WriteString(").\n")
	if r.Reason != "" {
		b.WriteString("Reason: " + r.Reason + "\n")
	}
	b.WriteString("Brief:\n" + r.Brief + "\n\n")
	if r.Lead == "user" {
		fmt.Fprintf(&b, "Accept with: lea request accept %d. Decline with: lea request decline %d [reason].", id, id)
	} else {
		fmt.Fprintf(&b, "Accept by calling agents_spawn with for=\"#%d\" (you may change the role, repo, branch and brief). Decline with msg_reply to this message, status fail, and your reason.", id)
	}
	return b.String()
}

// findRequest resolves agents_spawn's for: "#ID", or a requester's name (its oldest pending
// request). Only a pending request addressed to lead is found.
func (d *Daemon) findRequest(f, lead, container string) (store.SpawnRequest, error) {
	if err := d.sweepRequests(); err != nil {
		return store.SpawnRequest{}, err
	}
	if len(f) > maxForLen {
		return store.SpawnRequest{}, fmt.Errorf("for is %d bytes, limit %d", len(f), maxForLen)
	}
	if s, ok := strings.CutPrefix(f, "#"); ok {
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil || id < 1 {
			return store.SpawnRequest{}, fmt.Errorf("for: %q is not a request id", f)
		}
		r, err := d.st.SpawnRequestByID(id)
		if err != nil || r.Lead != lead || r.State != "pending" {
			return store.SpawnRequest{}, fmt.Errorf("no pending spawn request #%d for you", id)
		}
		return r, nil
	}
	if !validTarget(f) {
		return store.SpawnRequest{}, errors.New("for: #<request id> or the requester's name")
	}
	key, err := d.resolveSpawned(f, container)
	if err != nil {
		return store.SpawnRequest{}, err
	}
	r, err := d.st.PendingSpawnRequest(key, lead)
	if errors.Is(err, store.ErrNotFound) {
		return store.SpawnRequest{}, fmt.Errorf("no pending spawn request from %s for you", f)
	}
	return r, err
}

// sweepRequests expires requests past their TTL and withdraws those of ended requesters, so no
// lookup finds them pending (spec §5.2: an unanswered request frees its slot).
func (d *Daemon) sweepRequests() error {
	return d.st.SweepSpawnRequests(requestTTL, d.now())
}

// reopenRequest puts back a request whose spawn failed: it may be accepted again.
func (d *Daemon) reopenRequest(id int64) {
	if err := d.st.MoveSpawnRequest(id, "accepting", "pending", "", d.now()); err != nil {
		log.Printf("lea daemon: reopen spawn request #%d: %v", id, err)
	}
}

// acceptRequest closes an accepted request and opens the thread between the requester and the
// new agent: the brief (posted by the manager), the answer to the requester and the note to the new
// agent all reply to the request message, so they share its thread.
func (d *Daemon) acceptRequest(r store.SpawnRequest, by, role string, row agents.Row) {
	if err := d.st.MoveSpawnRequest(r.ID, "accepting", "accepted", row.Key, d.now()); err != nil {
		log.Printf("lea daemon: close spawn request #%d: %v", r.ID, err)
	}
	rn := shortName(r.Requester)
	d.post(by, r.Requester, "result", fmt.Sprintf("[status: ok] spawn request #%d accepted: %s started (role %s). Work with it through msg_send; this thread is shared with it.",
		r.ID, row.Name, role), r.Message)
	d.post(by, row.Key, "note", fmt.Sprintf("You were started for %s's spawn request #%d: work with %s through msg_send; this thread is shared with it.",
		rn, r.ID, rn), r.Message)
}

// declineRequest records a lead's fail answer to a spawn request: that brief is not sent again.
func (d *Daemon) declineRequest(msg int64, lead string) {
	r, err := d.st.SpawnRequestByMessage(msg)
	if err != nil || r.Lead != lead {
		return
	}
	if err := d.st.MoveSpawnRequest(r.ID, "pending", "declined", "", d.now()); err != nil && !errors.Is(err, store.ErrDecided) {
		log.Printf("lea daemon: decline spawn request #%d: %v", r.ID, err)
	}
}

// post stores a message lea composes on someone's behalf and wakes its recipient. Its text is
// lea's; agent text inside it was checked when it came in.
func (d *Daemon) post(from, to, kind, body string, replyTo int64) {
	if _, err := d.st.Send(store.Message{From: from, To: to, Kind: kind, Body: body, ReplyTo: replyTo, At: d.now()}); err != nil {
		log.Printf("lea daemon: post to %s: %v", to, err)
		return
	}
	d.wake(to)
	d.changed()
}

type requestView struct {
	ID        int64  `json:"id"`
	Container string `json:"container"`
	From      string `json:"from"`
	Role      string `json:"role"`
	Profile   string `json:"profile"`
	Repo      string `json:"repo"`
	Branch    string `json:"branch"`
	Brief     string `json:"brief"`
	Size      int    `json:"size"` // the whole brief's bytes, whatever Brief shows
	Reason    string `json:"reason"`
	At        int64  `json:"at"`
}

// requestList is the user's pending spawn requests (host only), briefs cut to fit the wire.
func (d *Daemon) requestList() ([]requestView, error) {
	if err := d.sweepRequests(); err != nil {
		return nil, err
	}
	rs, err := d.st.PendingSpawnRequests("user")
	if err != nil {
		return nil, err
	}
	out := []requestView{}
	for i, r := range rs {
		if i == maxListed {
			break
		}
		out = append(out, requestView{r.ID, r.Container, shortName(r.Requester), r.Role, r.Profile, r.Repo, r.Branch,
			cutBody(r.Brief, listBrief), len(r.Brief), r.Reason, r.At})
	}
	return out, nil
}

// requestDecide is the user's answer to a request addressed to the user (host only).
func (d *Daemon) requestDecide(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ID      int64  `json:"id"`
		Verdict string `json:"verdict"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if err := d.sweepRequests(); err != nil {
		return nil, err
	}
	r, err := d.st.SpawnRequestByID(p.ID)
	if err != nil || r.Lead != "user" {
		return nil, fmt.Errorf("no spawn request #%d for you", p.ID)
	}
	switch p.Verdict {
	case "accept":
		a := d.manager()
		if a == nil {
			return nil, errors.New("agents are not available in this daemon")
		}
		if err := d.st.MoveSpawnRequest(r.ID, "pending", "accepting", "", d.now()); err != nil {
			return nil, fmt.Errorf("spawn request #%d: %w", r.ID, err)
		}
		// The brief stays the requester's words: wrapped as untrusted content from that agent,
		// whatever the user saw of it when accepting.
		row, err := a.New(ctx, agents.NewRequest{Container: r.Container, Profile: r.Profile, Role: r.Role, Repo: r.Repo,
			Branch: r.Branch, Brief: r.Brief, BriefFrom: r.Requester, BriefReplyTo: r.Message})
		if err != nil {
			d.reopenRequest(r.ID)
			return nil, err
		}
		d.acceptRequest(r, "user", r.Role, row)
		return map[string]string{"name": row.Name, "key": row.Key}, nil
	case "decline":
		reason := p.Reason
		if reason == "" {
			reason = "declined by the user"
		}
		if len(reason) > maxReason || bus.CheckText(reason) != nil {
			return nil, errors.New("reason: at most 2 KiB of text without control characters")
		}
		if err := d.st.MoveSpawnRequest(r.ID, "pending", "declined", "", d.now()); err != nil {
			return nil, fmt.Errorf("spawn request #%d: %w", r.ID, err)
		}
		d.post("user", r.Requester, "result", "[status: fail] "+reason, r.Message)
		return struct{}{}, nil
	}
	return nil, errors.New("verdict must be accept or decline")
}
