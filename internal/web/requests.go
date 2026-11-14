package web

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/DavidGodefroid/lea/internal/grants"
)

// Requests, allowances, egress and the tower's questions (#415): the decisions the user takes on
// the host, from the browser. Each method names nothing of another lea: a request, a grant
// request or a question is answered only when the daemon lists it for one of this lea's agents,
// and the allowances are always this lea's (the container is the server's, never the page's).

// Host is what lea web does on the host beside the daemon: the launcher's part of lea allow (the
// pins of the projects directory's repositories, read from their origins at that moment) and lea
// egress. A server without one says so on those methods.
type Host interface {
	// AllowPreview reads the pins an allowance would record and the digest that names them.
	AllowPreview(ctx context.Context, a AllowAsk) (AllowPreview, error)
	// AllowRecord records a, if its pins still read as digest (what the user was shown).
	AllowRecord(ctx context.Context, a AllowAsk, digest string) (json.RawMessage, error)
	// EgressRefused is lea egress denied: the hosts the proxy refused lately.
	EgressRefused(ctx context.Context) (json.RawMessage, error)
	// EgressChange is lea egress allow|deny HOST, applied live; it returns what lea reports.
	EgressChange(ctx context.Context, allow bool, host string) (string, error)
}

// AllowAsk is an allowance the page asks for: lea allow CAPABILITY... [--repo R]... [--branch
// GLOB] [--agent NAME] [--tag GLOB] [--for DURATION].
type AllowAsk struct {
	Capabilities []string `json:"capabilities"`
	Repos        []string `json:"repos"`
	Branch       string   `json:"branch"`
	Agent        string   `json:"agent"`
	Tag          string   `json:"tag"`
	For          string   `json:"for"`
}

// AllowPreview is what AllowAsk would record: its lea allow command, each repository's pin
// (New: never recorded; Was: the pin recorded before, when it changed), the repositories left
// out and why, and the digest that records exactly these pins.
type AllowPreview struct {
	Command string      `json:"command"`
	Repos   []AllowRepo `json:"repos"`
	Skipped []string    `json:"skipped"`
	Digest  string      `json:"digest"`
	// Unseen: a pin the user has not confirmed yet (agents can rewrite .git/config).
	Unseen bool `json:"unseen"`
}

// AllowRepo is one repository of an AllowPreview.
type AllowRepo struct {
	Repo string `json:"repo"`
	Pin  string `json:"pin"`
	New  bool   `json:"new,omitempty"`
	Was  string `json:"was,omitempty"`
}

// SetHost gives the server the host's part of lea allow and lea egress.
func (s *Server) SetHost(h Host) { s.host = h }

func init() {
	for name, m := range map[string]func(s *Server, ctx context.Context, raw json.RawMessage) (any, error){
		"request_decide":       (*Server).requestDecide,
		"grant_request_decide": (*Server).grantRequestDecide,
		"question_answer":      (*Server).questionAnswer,
		"allow_list":           (*Server).allowList,
		"allow_off":            (*Server).allowOff,
		"allow_preview":        (*Server).allowPreview,
		"allow_add":            (*Server).allowAdd,
		"egress_refused":       (*Server).egressRefused,
		"egress_allow":         egressChange(true),
		"egress_deny":          egressChange(false),
	} {
		methods[name] = m
	}
}

// pendingIn checks that the daemon's list method holds id for this lea (an entry whose container
// is the server's).
func (s *Server) pendingIn(ctx context.Context, list string, id int64) error {
	var rs []struct {
		ID        int64  `json:"id"`
		Container string `json:"container"`
	}
	if err := s.call(ctx, list, nil, &rs); err != nil {
		return err
	}
	for _, r := range rs {
		if r.ID == id && r.Container == s.container {
			return nil
		}
	}
	return badRequest{"no such pending request in this lea"}
}

// decision is a request's answer from the page: its id, a verdict among its method's, a reason.
type decision struct {
	ID      int64
	Verdict string
	Reason  string
}

func decode(raw json.RawMessage, v any) error {
	if err := json.Unmarshal(raw, v); err != nil {
		return badRequest{"invalid parameters"}
	}
	return nil
}

// requestDecide accepts or declines a spawn request (sN) of this lea addressed to the user, as
// lea approve sN / lea deny sN do.
func (s *Server) requestDecide(ctx context.Context, raw json.RawMessage) (any, error) {
	var p decision
	if err := decode(raw, &p); err != nil {
		return nil, err
	}
	if p.Verdict != "accept" && p.Verdict != "decline" {
		return nil, badRequest{"verdict must be accept or decline"}
	}
	if err := s.pendingIn(ctx, "request_list", p.ID); err != nil {
		return nil, err
	}
	var res json.RawMessage
	return res, s.call(ctx, "request_decide", map[string]any{"id": p.ID, "verdict": p.Verdict, "reason": p.Reason}, &res)
}

// grantRequestDecide approves or denies a lead's grant request (gN) of this lea, as lea approve
// gN / lea deny gN do.
func (s *Server) grantRequestDecide(ctx context.Context, raw json.RawMessage) (any, error) {
	var p decision
	if err := decode(raw, &p); err != nil {
		return nil, err
	}
	if p.Verdict != "approve" && p.Verdict != "deny" {
		return nil, badRequest{"verdict must be approve or deny"}
	}
	if err := s.pendingIn(ctx, "grant_request_list", p.ID); err != nil {
		return nil, err
	}
	var res json.RawMessage
	return res, s.call(ctx, "grant_request_decide", map[string]any{"id": p.ID, "verdict": p.Verdict, "reason": p.Reason}, &res)
}

// questionAnswer answers the open question of one of this lea's towers.
func (s *Server) questionAnswer(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ID   int64
		Text string
	}
	if err := decode(raw, &p); err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.Text) == "" {
		return nil, badRequest{"write the answer"}
	}
	var qs []struct {
		ID    int64  `json:"id"`
		Agent string `json:"agent"`
		State string `json:"state"`
	}
	if err := s.call(ctx, "question_list", nil, &qs); err != nil {
		return nil, err
	}
	found := false
	for _, q := range qs {
		found = found || (q.ID == p.ID && q.State == "open" && s.ours(q.Agent) == nil)
	}
	if !found {
		return nil, badRequest{"no such open question in this lea"}
	}
	var res json.RawMessage
	return res, s.call(ctx, "question_answer", map[string]any{"id": p.ID, "text": p.Text}, &res)
}

// capabilityView is a capability as the allowances screen offers it.
type capabilityView struct {
	Name  string `json:"name"`
	Does  string `json:"does"`
	Forge bool   `json:"forge"`
}

// allowList is lea allow for this lea: its allowances and recent uses, and the capabilities and
// presets one can add, from grants.Capabilities.
func (s *Server) allowList(ctx context.Context, raw json.RawMessage) (any, error) {
	var res json.RawMessage
	if err := s.call(ctx, "allow_list", map[string]any{"container": s.container}, &res); err != nil {
		return nil, err
	}
	caps := make([]capabilityView, 0, len(grants.Capabilities))
	for _, c := range grants.Capabilities {
		caps = append(caps, capabilityView{c.Name, c.Does, c.Forge()})
	}
	return map[string]any{"allowances": res, "capabilities": caps, "presets": grants.Presets}, nil
}

// allowOff is lea allow off N|CAPABILITY|deliver for this lea.
func (s *Server) allowOff(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Set        int64
		Capability string
	}
	if err := decode(raw, &p); err != nil {
		return nil, err
	}
	if (p.Set == 0) == (p.Capability == "") {
		return nil, badRequest{"name one allowance (set) or one capability"}
	}
	var res json.RawMessage
	return res, s.call(ctx, "allow_off", map[string]any{"container": s.container, "set": p.Set, "capability": p.Capability}, &res)
}

var errNoHost = badRequest{"lea web runs without the launcher here: run lea web from your projects directory"}

func (s *Server) allowAsk(raw json.RawMessage) (AllowAsk, error) {
	var a AllowAsk
	if err := decode(raw, &a); err != nil {
		return a, err
	}
	if s.host == nil {
		return a, errNoHost
	}
	if _, err := grants.Expand(a.Capabilities); err != nil {
		return a, badRequest{err.Error()}
	}
	return a, nil
}

// allowPreview is what an allowance would record, before the user confirms its pins.
func (s *Server) allowPreview(ctx context.Context, raw json.RawMessage) (any, error) {
	a, err := s.allowAsk(raw)
	if err != nil {
		return nil, err
	}
	return hostResult(s.host.AllowPreview(ctx, a))
}

// allowAdd records an allowance whose pins the user was shown (its preview's digest).
func (s *Server) allowAdd(ctx context.Context, raw json.RawMessage) (any, error) {
	a, err := s.allowAsk(raw)
	if err != nil {
		return nil, err
	}
	var p struct{ Digest string }
	if err := decode(raw, &p); err != nil {
		return nil, err
	}
	if p.Digest == "" {
		return nil, badRequest{"preview the allowance first: its pins must be the ones you saw"}
	}
	return hostResult(s.host.AllowRecord(ctx, a, p.Digest))
}

func (s *Server) egressRefused(ctx context.Context, raw json.RawMessage) (any, error) {
	if s.host == nil {
		return nil, errNoHost
	}
	return hostResult(s.host.EgressRefused(ctx))
}

func egressChange(allow bool) func(s *Server, ctx context.Context, raw json.RawMessage) (any, error) {
	return func(s *Server, ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct{ Host string }
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		if s.host == nil {
			return nil, errNoHost
		}
		if p.Host == "" || len(p.Host) > 253 {
			return nil, badRequest{"host: a domain name"}
		}
		said, err := s.host.EgressChange(ctx, allow, p.Host)
		return hostResult(map[string]string{"said": said}, err)
	}
}

// hostResult answers a host call: its refusal is a bad request with its own text (lea allow's
// or lea egress's message, as the terminal would show it).
func hostResult[T any](v T, err error) (any, error) {
	if err != nil {
		return nil, badRequest{err.Error()}
	}
	return v, nil
}

// grantRequestView is a pending grant request of this lea with the capability it maps to.
type grantRequestView struct {
	ID         int64  `json:"id"`
	From       string `json:"from"`
	Action     string `json:"action"`
	Capability string `json:"capability,omitempty"`
	Does       string `json:"does,omitempty"`
	Repo       string `json:"repo"`
	Branch     string `json:"branch"`
	GitHub     string `json:"github"`
	Tag        string `json:"tag,omitempty"`
	TTLS       int64  `json:"ttl_s"`
	Reason     string `json:"reason"`
	At         int64  `json:"at"`
}

// requests gathers this lea's spawn requests to the user and grant requests, the grant requests
// with their grouped advice (grants.Advise).
func (s *Server) requests(ctx context.Context) ([]json.RawMessage, []grantRequestView, *grants.Advice, error) {
	var spawn []json.RawMessage
	if err := s.call(ctx, "request_list", nil, &spawn); err != nil {
		return nil, nil, nil, err
	}
	var all []struct {
		grantRequestView
		Container string `json:"container"`
	}
	if err := s.call(ctx, "grant_request_list", nil, &all); err != nil {
		return nil, nil, nil, err
	}
	gs := []grantRequestView{}
	var ws []grants.Want
	for _, g := range all {
		if g.Container != s.container {
			continue
		}
		v := g.grantRequestView
		if c, ok := grants.CapabilityOf(v.Action); ok {
			v.Capability, v.Does = c.Name, c.Does
		}
		gs = append(gs, v)
		ws = append(ws, grants.Want{ID: v.ID, Action: v.Action, Repo: v.Repo, Tag: v.Tag})
	}
	var advice *grants.Advice
	if len(ws) > 0 {
		a := grants.Advise(ws)
		advice = &a
	}
	return s.ofThisLea(spawn), gs, advice, nil
}

// ofThisLea keeps the entries whose container is this lea's.
func (s *Server) ofThisLea(list []json.RawMessage) []json.RawMessage {
	out := []json.RawMessage{}
	for _, e := range list {
		var r struct {
			Container string `json:"container"`
		}
		if json.Unmarshal(e, &r) == nil && r.Container == s.container {
			out = append(out, e)
		}
	}
	return out
}
