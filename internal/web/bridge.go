package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// callTimeout bounds one API call: starting an agent waits on docker and tmux.
const callTimeout = 60 * time.Second

// methods are the API's only methods, each mapped to the daemon's: params are decoded into the
// method's own fields and sent again, never passed through, and every agent they name must be
// one of this lea's. Anything else is refused before it reaches the socket.
var methods = map[string]func(s *Server, ctx context.Context, raw json.RawMessage) (any, error){
	"providers_list": func(s *Server, ctx context.Context, raw json.RawMessage) (any, error) {
		var out json.RawMessage
		return out, s.call(ctx, "providers_list", nil, &out)
	},
	"agent_new":       (*Server).agentNew,
	"agent_stop":      keyed("agent_stop", "key", nil),
	"agent_label":     keyed("agent_label", "key", []field{{"label", kindString}}),
	"agent_pause":     keyed("agent_pause", "key", []field{{"paused", kindBool}}),
	"agent_hold":      keyed("agent_hold", "agent", []field{{"held", kindBool}}),
	"agent_role":      keyed("agent_role", "agent", []field{{"role", kindString}}),
	"thread_list":     keyed("thread_list", "key", nil),
	"msg_send":        (*Server).msgSend,
	"approval_decide": (*Server).approvalDecide,
}

type kind int

const (
	kindString kind = iota
	kindBool
)

type field struct {
	name string
	kind kind
}

// method serves POST /api/<method>.
func (s *Server) method(w http.ResponseWriter, r *http.Request) {
	m, ok := methods[r.PathValue("method")]
	if !ok {
		writeError(w, http.StatusNotFound, "unknown method")
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/json" && !strings.HasPrefix(ct, "application/json;") {
		writeError(w, http.StatusUnsupportedMediaType, "send application/json")
		return
	}
	var raw json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&raw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), callTimeout)
	defer cancel()
	res, err := m(s, ctx, raw)
	if err != nil {
		var be badRequest
		if errors.As(err, &be) {
			writeError(w, http.StatusBadRequest, be.msg)
			return
		}
		daemonError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": res})
}

// badRequest is a request the bridge refuses itself, before any daemon call.
type badRequest struct{ msg string }

func (e badRequest) Error() string { return e.msg }

// ours checks that key names an agent of this lea: container/name.
func (s *Server) ours(key string) error {
	name, ok := strings.CutPrefix(key, s.container+"/")
	if !ok || name == "" || strings.Contains(name, "/") {
		return badRequest{"not an agent of this lea"}
	}
	return nil
}

// keyed is a method that names one agent of this lea in its keyField, with the given other
// fields, each of its own type.
func keyed(method, keyField string, fields []field) func(s *Server, ctx context.Context, raw json.RawMessage) (any, error) {
	return func(s *Server, ctx context.Context, raw json.RawMessage) (any, error) {
		var in map[string]json.RawMessage
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, badRequest{"invalid parameters"}
		}
		var key string
		if err := json.Unmarshal(in["key"], &key); err != nil {
			return nil, badRequest{"key must be a string"}
		}
		if err := s.ours(key); err != nil {
			return nil, err
		}
		out := map[string]any{keyField: key}
		for _, f := range fields {
			v, ok := in[f.name]
			if !ok {
				return nil, badRequest{f.name + " is missing"}
			}
			switch f.kind {
			case kindString:
				var x string
				if err := json.Unmarshal(v, &x); err != nil {
					return nil, badRequest{f.name + " must be a string"}
				}
				out[f.name] = x
			case kindBool:
				var x bool
				if err := json.Unmarshal(v, &x); err != nil {
					return nil, badRequest{f.name + " must be true or false"}
				}
				out[f.name] = x
			}
		}
		var res json.RawMessage
		return res, s.call(ctx, method, out, &res)
	}
}

// agentNew starts an agent or a control tower in this lea, labelled when a label is given.
func (s *Server) agentNew(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Profile, Name, Role, Repo, Branch, Brief, Label, Model string
		Tower                                                  bool
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, badRequest{"invalid parameters"}
	}
	var row struct {
		Key   string `json:"key"`
		Name  string `json:"name"`
		Label string `json:"label"`
	}
	params := map[string]any{"container": s.container, "profile": p.Profile, "name": p.Name, "role": p.Role,
		"repo": p.Repo, "branch": p.Branch, "brief": p.Brief, "model": p.Model, "tower": p.Tower}
	if err := s.call(ctx, "agent_new", params, &row); err != nil {
		return nil, err
	}
	if p.Label != "" && !p.Tower {
		if err := s.call(ctx, "agent_label", map[string]any{"key": row.Key, "label": p.Label}, nil); err != nil {
			return nil, err
		}
		row.Label = p.Label
	}
	return row, nil
}

// msgSend writes to an agent of this lea as the user: a note, or a request (the tower's chat).
func (s *Server) msgSend(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct{ To, Body, Kind string }
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, badRequest{"invalid parameters"}
	}
	if err := s.ours(p.To); err != nil {
		return nil, err
	}
	if p.Kind != "note" && p.Kind != "request" {
		return nil, badRequest{"kind must be note or request"}
	}
	var res json.RawMessage
	return res, s.call(ctx, "msg_send", map[string]any{"to": p.To, "body": p.Body, "kind": p.Kind}, &res)
}

// approvalDecide answers a permission request of one of this lea's agents.
func (s *Server) approvalDecide(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ID      int64
		Verdict string
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, badRequest{"invalid parameters"}
	}
	if p.Verdict != "allow" && p.Verdict != "deny" && p.Verdict != "always" {
		return nil, badRequest{"verdict must be allow, deny or always"}
	}
	var pend []struct {
		ID    int64  `json:"id"`
		Agent string `json:"agent"`
	}
	if err := s.call(ctx, "approval_list", nil, &pend); err != nil {
		return nil, err
	}
	found := false
	for _, a := range pend {
		found = found || (a.ID == p.ID && s.ours(a.Agent) == nil)
	}
	if !found {
		return nil, badRequest{"no such pending permission in this lea"}
	}
	return struct{}{}, s.call(ctx, "approval_decide", map[string]any{"id": p.ID, "verdict": p.Verdict}, nil)
}
