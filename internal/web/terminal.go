package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/DavidGodefroid/lea/internal/client"
)

// The terminal view (#416): a read-only live view of an agent's pane. GET /api/terminal?key=K is
// a server-sent event stream of the pane's screen (the daemon's agent_capture: tmux
// capture-pane -e, lines with their SGR colours), sent again whenever it changes. Nothing ever
// goes the other way: typing into the pane is not offered.

const (
	// captureEvery is how often the stream reads the pane.
	captureEvery = 500 * time.Millisecond
	// maxTerminals caps the terminal streams open at once on one lea web.
	maxTerminals = 8
	// maxScreenLines and maxLineBytes cap what one screen carries to the browser.
	maxScreenLines = 500
	maxLineBytes   = 16 << 10
)

// Screen is one event of the terminal stream.
type Screen struct {
	Lines   []string `json:"lines"`
	Width   int      `json:"width"`
	Height  int      `json:"height"`
	CursorX int      `json:"cursor_x"`
	CursorY int      `json:"cursor_y"`
}

// terminals counts the open terminal streams.
type terminals struct {
	mu sync.Mutex
	n  int
}

func (t *terminals) take() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.n >= maxTerminals {
		return false
	}
	t.n++
	return true
}

func (t *terminals) give() { t.mu.Lock(); t.n--; t.mu.Unlock() }

// terminal serves GET /api/terminal?key=K for an agent of this lea: a "screen" event at once and
// at each change, a "terminal-error" event when the pane cannot be read (the stream goes on: the
// agent may get its terminal back).
func (s *Server) terminal(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if err := s.ours(key); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	if !s.terms.take() {
		writeError(w, http.StatusTooManyRequests, "too many terminals open: close one")
		return
	}
	defer s.terms.give()
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "retry: 2000\n\n")
	fl.Flush()
	ctx := r.Context()
	every := s.captureEvery
	if every == 0 {
		every = captureEvery
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	var last []byte
	lastErr := ""
	idle := 0
	for {
		sc, err := s.capture(ctx, key)
		if ctx.Err() != nil {
			return
		}
		switch {
		case err != nil:
			msg := "the lea daemon does not answer: is lea running?"
			var re *client.RemoteError
			if errors.As(err, &re) {
				msg = re.Msg
			}
			if msg != lastErr {
				b, _ := json.Marshal(map[string]string{"error": msg})
				fmt.Fprintf(w, "event: terminal-error\ndata: %s\n\n", b)
				fl.Flush()
				lastErr, last = msg, nil
			}
		default:
			b, _ := json.Marshal(sc)
			if string(b) != string(last) {
				fmt.Fprintf(w, "event: screen\ndata: %s\n\n", b)
				fl.Flush()
				last, lastErr, idle = b, "", 0
			} else if idle++; idle >= 30 {
				fmt.Fprint(w, ": ping\n\n")
				fl.Flush()
				idle = 0
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// capture reads the agent's screen, capped.
func (s *Server) capture(ctx context.Context, key string) (Screen, error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var sc Screen
	if err := s.call(cctx, "agent_capture", map[string]any{"key": key}, &sc); err != nil {
		return Screen{}, err
	}
	if len(sc.Lines) > maxScreenLines {
		sc.Lines = sc.Lines[len(sc.Lines)-maxScreenLines:]
	}
	for i, l := range sc.Lines {
		if len(l) > maxLineBytes {
			sc.Lines[i] = strings.ToValidUTF8(l[:maxLineBytes], "")
		}
	}
	if sc.Lines == nil {
		sc.Lines = []string{}
	}
	return sc, nil
}
