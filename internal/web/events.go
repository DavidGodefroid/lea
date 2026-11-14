package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// watchWait is how long the daemon holds one agents_watch (seconds); a stream with no change
// meanwhile gets a comment line, which keeps the connection open.
const watchWait = 25

func sleepRetry(ctx context.Context) {
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
	}
}

// State is one event of the stream: the lea's agents as agents_watch reports them, their tasks
// (board_list: status, next, blocker), the permissions they wait on and their questions to the
// user, all of this lea's only.
type State struct {
	Version   int64             `json:"version"`
	Container string            `json:"container"`
	Snapshot  json.RawMessage   `json:"snapshot"`
	Tasks     []json.RawMessage `json:"tasks"`
	Approvals []json.RawMessage `json:"approvals"`
	Questions []json.RawMessage `json:"questions"`
}

// events serves GET /api/events, a server-sent event stream: a "state" event at once and at
// every change of the lea (the daemon's agents_watch long-poll), a "lea-error" event while the
// daemon does not answer. While it is open the browser is attached as the lea's deck
// (deck_web_attach): permission prompts wait for it.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "retry: 1000\n\n")
	fl.Flush()
	ctx := r.Context()
	go s.attach(ctx)
	since := int64(-1) // never a version: the first watch answers at once
	for ctx.Err() == nil {
		st, err := s.state(ctx, since)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			b, _ := json.Marshal(map[string]string{"error": "the lea daemon does not answer: is lea running?"})
			fmt.Fprintf(w, "event: lea-error\ndata: %s\n\n", b)
			fl.Flush()
			since = -1
			s.retry(ctx)
			continue
		}
		if st == nil { // the watch timed out: nothing changed
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
			continue
		}
		since = st.Version
		b, err := json.Marshal(st)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "id: %d\nevent: state\ndata: %s\n\n", st.Version, b)
		fl.Flush()
	}
}

// attach holds the browser attached as the lea's deck until ctx ends, again after a daemon
// restart.
func (s *Server) attach(ctx context.Context) {
	for ctx.Err() == nil {
		s.call(ctx, "deck_web_attach", map[string]any{"container": s.container}, nil)
		s.retry(ctx)
	}
}

// state waits for a change after version since and gathers the lea's state; nil when the watch
// timed out with no change.
func (s *Server) state(ctx context.Context, since int64) (*State, error) {
	var snap json.RawMessage
	wctx, cancel := context.WithTimeout(ctx, (watchWait+10)*time.Second)
	defer cancel()
	if err := s.call(wctx, "agents_watch", map[string]any{"container": s.container, "since": since, "timeout_s": watchWait}, &snap); err != nil {
		return nil, err
	}
	var v struct {
		Version int64 `json:"version"`
	}
	if err := json.Unmarshal(snap, &v); err != nil {
		return nil, err
	}
	if v.Version == since {
		return nil, nil
	}
	cctx, cancel2 := context.WithTimeout(ctx, 10*time.Second)
	defer cancel2()
	var board struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := s.call(cctx, "board_list", map[string]any{"container": s.container}, &board); err != nil {
		return nil, err
	}
	var approvals, questions []json.RawMessage
	if err := s.call(cctx, "approval_list", nil, &approvals); err != nil {
		return nil, err
	}
	if err := s.call(cctx, "question_list", nil, &questions); err != nil {
		return nil, err
	}
	return &State{Version: v.Version, Container: s.container, Snapshot: snap, Tasks: nonNil(board.Items),
		Approvals: s.mine(approvals), Questions: s.mine(questions)}, nil
}

// mine keeps the entries whose agent is one of this lea's.
func (s *Server) mine(list []json.RawMessage) []json.RawMessage {
	out := []json.RawMessage{}
	for _, e := range list {
		var a struct {
			Agent string `json:"agent"`
		}
		if json.Unmarshal(e, &a) == nil && strings.HasPrefix(a.Agent, s.container+"/") {
			out = append(out, e)
		}
	}
	return out
}

func nonNil(l []json.RawMessage) []json.RawMessage {
	if l == nil {
		return []json.RawMessage{}
	}
	return l
}
