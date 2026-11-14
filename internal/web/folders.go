package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/DavidGodefroid/lea/internal/deck"
)

// Folders (#416): lea web reads and writes the deck's own layout file (deck.LayoutPath: the
// host's data dir, which no container mounts), so the browser and the deck show the same folders.
// GET /api/folders is a server-sent event stream of the folders, sent again whenever the file
// changes (the deck saved, or another browser); POST /api/folders_set replaces them. The deck
// reads the file again too (deck.WithLayoutWatch).

// foldersEvery is how often the folder stream reads the layout file.
const foldersEvery = time.Second

func init() {
	methods["folders_set"] = (*Server).foldersSet
}

// SetLayoutPath names the deck's layout file of this lea ("" : folders are off).
func (s *Server) SetLayoutPath(p string) { s.layoutPath = p }

// Folders is one event of the folder stream: the folders, and the colours a folder may take.
type Folders struct {
	Folders []deck.Folder `json:"folders"`
	Colours []string      `json:"colours"`
}

func (s *Server) layoutStore() (deck.FileStore, error) {
	if s.layoutPath == "" {
		return deck.FileStore{}, badRequest{"folders are off: lea has no host data dir"}
	}
	return deck.FileStore{Path: s.layoutPath}, nil
}

// folders serves GET /api/folders.
func (s *Server) folders(w http.ResponseWriter, r *http.Request) {
	st, err := s.layoutStore()
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
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
	fmt.Fprint(w, "retry: 2000\n\n")
	fl.Flush()
	every := s.foldersEvery
	if every == 0 {
		every = foldersEvery
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	var last string
	idle := 0
	for {
		l, err := st.Load()
		var b []byte
		name := "folders"
		if err != nil {
			name = "folders-error"
			b, _ = json.Marshal(map[string]string{"error": "folders not loaded: " + err.Error()})
		} else {
			b, _ = json.Marshal(Folders{Folders: s.ownFolders(l.Folders), Colours: deck.FolderColourNames()})
		}
		if cur := name + string(b); cur != last {
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b)
			fl.Flush()
			last, idle = cur, 0
		} else if idle++; idle >= 25 {
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
			idle = 0
		}
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
		}
	}
}

// ownFolders keeps only this lea's agents in each folder (the file is this lea's; a key of
// another is never shown), never nil.
func (s *Server) ownFolders(fs []deck.Folder) []deck.Folder {
	out := make([]deck.Folder, 0, len(fs))
	for _, f := range fs {
		agents := []string{}
		for _, k := range f.Agents {
			if s.ours(k) == nil {
				agents = append(agents, k)
			}
		}
		f.Agents = agents
		out = append(out, f)
	}
	return out
}

// foldersSet replaces the folders: {"folders": [{id, name, folded, color, agents}]}. The rest of
// the layout (which agents it has seen, for the deck's filing of new agents) stays; the deck's
// own checks refuse a layout it would never write.
func (s *Server) foldersSet(ctx context.Context, raw json.RawMessage) (any, error) {
	st, err := s.layoutStore()
	if err != nil {
		return nil, err
	}
	var p struct {
		Folders []deck.Folder `json:"folders"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, badRequest{"invalid parameters: " + err.Error()}
	}
	for _, f := range p.Folders {
		for _, k := range f.Agents {
			if err := s.ours(k); err != nil {
				return nil, err
			}
		}
	}
	l, err := st.Load()
	if err != nil {
		return nil, badRequest{"folders not loaded: " + err.Error()}
	}
	if p.Folders == nil {
		p.Folders = []deck.Folder{}
	}
	l.Folders = p.Folders
	l.Saved = time.Now().Unix()
	if err := st.Save(l); err != nil {
		return nil, badRequest{"folders not saved: " + err.Error()}
	}
	return Folders{Folders: s.ownFolders(l.Folders), Colours: deck.FolderColourNames()}, nil
}
