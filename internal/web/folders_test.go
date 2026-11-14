package web

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/deck"
)

// The browser and the deck share one layout file (#416): what one saves, the other shows.
func TestFoldersAreTheDecksLayoutFile(t *testing.T) {
	ts := newTestServer(t)
	path, _ := deck.LayoutPath(t.TempDir(), "lea-a")
	ts.s.SetLayoutPath(path)
	ts.s.foldersEvery = 20 * time.Millisecond
	// The deck saved a folder, and knows two agents.
	st := deck.FileStore{Path: path}
	if err := st.Save(deck.Layout{Format: 1, Known: []deck.Seen{{Key: "lea-a/dev-1", Created: 5}},
		Folders: []deck.Folder{{ID: "f1", Name: "auth", Agents: []string{"lea-a/dev-1"}}}}); err != nil {
		t.Fatal(err)
	}
	c := ts.login(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.url+"/api/folders", nil)
	req.AddCookie(c)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("folders: %v %v", resp, err)
	}
	defer resp.Body.Close()
	ch := readEvents(resp)
	ev := next(t, ch)
	var f Folders
	if ev.name != "folders" || json.Unmarshal([]byte(ev.data), &f) != nil || len(f.Folders) != 1 || f.Folders[0].Name != "auth" || len(f.Colours) != 8 {
		t.Fatalf("first event %+v", ev)
	}
	// The browser saves: the file holds it, the deck's Known stays.
	code, out := ts.post(t, "folders_set", map[string]any{"folders": []map[string]any{
		{"id": "f1", "name": "auth", "folded": true, "color": "teal", "agents": []string{}},
		{"id": "f2", "name": "web ui", "agents": []string{"lea-a/dev-1"}}}}, c, nil)
	if code != 200 {
		t.Fatalf("folders_set: %d %v", code, out)
	}
	l, err := st.Load()
	if err != nil || len(l.Folders) != 2 || !l.Folders[0].Folded || l.Folders[0].Color != "teal" || l.Folders[1].Agents[0] != "lea-a/dev-1" ||
		len(l.Known) != 1 || l.Format != 1 || l.Saved == 0 {
		t.Fatalf("saved %+v %v", l, err)
	}
	ev = next(t, ch)
	if ev.name != "folders" || !strings.Contains(ev.data, `"web ui"`) {
		t.Fatalf("after the browser's save %+v", ev)
	}
	// The deck saves: the browser hears of it.
	l.Folders = l.Folders[:1]
	st.Save(l)
	ev = next(t, ch)
	if ev.name != "folders" || strings.Contains(ev.data, `"web ui"`) {
		t.Fatalf("after the deck's save %+v", ev)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("layout file %v %v", fi, err)
	}
}

func TestFoldersSetRefusals(t *testing.T) {
	ts := newTestServer(t)
	c := ts.login(t)
	if code, out := ts.post(t, "folders_set", map[string]any{"folders": []any{}}, c, nil); code != 400 || !strings.Contains(out["error"].(string), "folders are off") {
		t.Fatalf("no layout path: %d %v", code, out)
	}
	path := filepath.Join(t.TempDir(), "deck", "lea-a.json")
	ts.s.SetLayoutPath(path)
	for name, body := range map[string]any{
		"another lea's agent": map[string]any{"folders": []map[string]any{{"id": "f1", "name": "x", "agents": []string{"lea-b/dev-1"}}}},
		"a bad id":            map[string]any{"folders": []map[string]any{{"id": "../x", "name": "x"}}},
		"an empty name":       map[string]any{"folders": []map[string]any{{"id": "f1", "name": "  "}}},
		"a control character": map[string]any{"folders": []map[string]any{{"id": "f1", "name": "a\x1b[2Jb"}}},
		"an unknown colour":   map[string]any{"folders": []map[string]any{{"id": "f1", "name": "x", "color": "red"}}},
		"an agent twice":      map[string]any{"folders": []map[string]any{{"id": "f1", "name": "x", "agents": []string{"lea-a/d"}}, {"id": "f2", "name": "y", "agents": []string{"lea-a/d"}}}},
		"an unknown field":    map[string]any{"folders": []any{}, "known": []any{}},
	} {
		if code, out := ts.post(t, "folders_set", body, c, nil); code != 400 {
			t.Fatalf("%s: %d %v", name, code, out)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("a refused layout was written")
	}
	if code, _ := ts.post(t, "folders_set", map[string]any{"folders": nil}, nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("without the cookie: %d", code)
	}
}
