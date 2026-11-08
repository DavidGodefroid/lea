package deck

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// #296: an agent a lead in a folder spawns joins that folder exactly once, whatever the timing
// of the layout's saves (Saved is in seconds, and a tie used to keep the agent at the root).

// towerDeck is a deck whose folder web holds the lead, on store st, at the clock's second
// (10_000), the second its children are created in.
func towerDeck(t *testing.T, st LayoutStore) (Model, Agent) {
	t.Helper()
	lead := launched(ag("lead", "idle", 1), 100)
	m := sized(newModel(&fakeAPI{}).WithLayout(st), 34, 30)
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 7, Agents: []Agent{lead}}})
	return m, lead
}

// Two children created in the same second, delivered in two snapshots, both join the folder:
// the save that files the first does not hide the second.
func TestSameSecondChildrenBothFollowTheirLead(t *testing.T) {
	st := &memStore{l: Layout{Saved: 9_000, Folders: []Folder{{ID: "f1", Name: "web", Agents: []string{"lea-a/lead"}}}}}
	m, lead := towerDeck(t, st)
	a, b := spawned("a", "lea-a/lead", clock.Unix()), spawned("b", "lea-a/lead", clock.Unix())
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Agents: []Agent{lead, a}}})
	if st.l.Saved != clock.Unix() {
		t.Fatalf("filing a saved at %d, want the same second %d", st.l.Saved, clock.Unix())
	}
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 9, Agents: []Agent{lead, a, b}}})
	if got := order(m); got != "[web] lead a b" {
		t.Fatalf("order %q", got)
	}
	if fa := st.l.Folders[0].Agents; len(fa) != 3 || fa[1] != "lea-a/a" || fa[2] != "lea-a/b" {
		t.Fatalf("saved %+v", st.l)
	}
}

// A fold, a rename or a move saved between a child's creation and its first snapshot does not
// keep it out of its lead's folder.
func TestASaveBeforeAChildsFirstSnapshotDoesNotKeepItOut(t *testing.T) {
	for name, act := range map[string]func(Model) Model{
		"fold":   func(m Model) Model { return m.toggleFold(folderKey("f1")) },
		"rename": func(m Model) Model { return m.renameFolder(folderKey("f1"), "shop") },
		"move":   func(m Model) Model { return m.moveAgent("lea-a/other", 1) },
		"colour": func(m Model) Model { return m.setColour(folderKey("f1"), "teal") },
	} {
		st := &memStore{l: Layout{Saved: 9_000, Folders: []Folder{
			{ID: "f1", Name: "web", Agents: []string{"lea-a/lead"}}, {ID: "f2", Name: "misc"}}}}
		lead, other := launched(ag("lead", "idle", 1), 100), launched(ag("other", "idle", 1), 100)
		m := sized(newModel(&fakeAPI{}).WithLayout(st), 34, 30)
		m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 7, Agents: []Agent{lead, other}}})
		saves := st.saves
		m = act(m) // in the second the child is created, after it
		if st.saves != saves+1 || st.l.Saved < clock.Unix() {
			t.Fatalf("%s: not saved now (%d saves, saved %d)", name, st.saves, st.l.Saved)
		}
		kid := spawned("kid", "lea-a/lead", clock.Unix())
		m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Agents: []Agent{lead, other, kid}}})
		if m.folderOf("lea-a/kid") != 0 {
			t.Fatalf("%s: kid not filed, layout %+v", name, m.layout)
		}
		if fa := st.l.Folders[0].Agents; len(fa) != 2 || fa[1] != "lea-a/kid" {
			t.Fatalf("%s: saved %+v", name, st.l)
		}
	}
}

// A child the user moves out stays out on later snapshots, and after the deck restarts on the
// same layout file.
func TestAMovedOutChildStaysOutAcrossARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deck", "lea-a.json")
	st := FileStore{Path: path}
	if err := st.Save(Layout{Saved: 9_000, Folders: []Folder{{ID: "f1", Name: "web", Agents: []string{"lea-a/lead"}}}}); err != nil {
		t.Fatal(err)
	}
	m, lead := towerDeck(t, st)
	kid := spawned("kid", "lea-a/lead", clock.Unix())
	agents := []Agent{lead, kid}
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Agents: agents}})
	if m.folderOf("lea-a/kid") != 0 {
		t.Fatalf("kid not filed: %+v", m.layout)
	}
	m.selected = "lea-a/kid"
	m = typed(t, m, "u")
	for v := int64(9); v < 11; v++ {
		m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: v, Agents: agents}})
	}
	if m.folderOf("lea-a/kid") >= 0 {
		t.Fatalf("moved back: %+v", m.layout)
	}
	// A new deck, later, on the same file.
	later := func() time.Time { return time.Unix(20_000, 0) }
	r := sized(New(&fakeAPI{}, "lea-a", false, later).WithLayout(st), 34, 30)
	if r.status != "" {
		t.Fatalf("status %q", r.status)
	}
	r, _ = step(t, r, snapshotMsg{snap: Snapshot{Version: 12, Agents: agents}})
	if got := order(r); got != "[web] lead kid" {
		t.Fatalf("after restart: order %q, layout %+v", got, r.layout)
	}
	l, err := st.Load()
	if err != nil || l.Format != layoutKnown || len(l.Folders[0].Agents) != 1 {
		t.Fatalf("file %+v %v", l, err)
	}
}

// A child spawned while no deck is attached is filed on the next attach, even when it was
// created in the second of the layout's last save.
func TestAChildSpawnedWhileDetachedInTheSaveSecondIsFiled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lea-a.json")
	st := FileStore{Path: path}
	if err := st.Save(Layout{Saved: 9_000, Folders: []Folder{{ID: "f1", Name: "web", Agents: []string{"lea-a/lead"}}}}); err != nil {
		t.Fatal(err)
	}
	m, lead := towerDeck(t, st)
	m = m.toggleFold(folderKey("f1")) // saved at clock, the deck then detaches
	_ = m
	kid := spawned("kid", "lea-a/lead", clock.Unix())
	r := sized(newModel(&fakeAPI{}).WithLayout(st), 34, 30)
	r, _ = step(t, r, snapshotMsg{snap: Snapshot{Version: 9, Agents: []Agent{lead, kid}}})
	if r.folderOf("lea-a/kid") != 0 {
		t.Fatalf("kid not filed: %+v", r.layout)
	}
}

// A layout written before #296 keeps every agent where it is on upgrade: the agents it lists
// that started no later than its last save are known, whether in a folder or at the root; only
// an agent started after it is filed (one spawned while detached, as before).
func TestALayoutBeforeKnownKeepsItsAgentsInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lea-a.json")
	old := `{"folders":[{"id":"f1","name":"web","agents":["lea-a/lead","lea-a/in"]}],"saved":9000}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	st := FileStore{Path: path}
	lead := launched(ag("lead", "idle", 1), 100)
	in, out := spawned("in", "lea-a/lead", 200), spawned("out", "lea-a/lead", 9_000)
	newer := spawned("newer", "lea-a/lead", 9_001)
	m := sized(newModel(&fakeAPI{}).WithLayout(st), 34, 30)
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 7, Agents: []Agent{lead, in, out, newer}}})
	if got := order(m); got != "[web] lead in newer out" {
		t.Fatalf("order %q", got)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var l Layout
	if err := json.Unmarshal(b, &l); err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, k := range l.Known {
		known[k.Key] = true
	}
	if l.Format != layoutKnown || !known["lea-a/in"] || !known["lea-a/out"] || !known["lea-a/newer"] {
		t.Fatalf("saved %+v", l)
	}
	// A layout that never recorded a save (before #241) files nothing on its first attach.
	st0 := &memStore{l: Layout{Folders: []Folder{{ID: "f1", Name: "web", Agents: []string{"lea-a/lead"}}}}}
	m0 := withFolders(t, st0, lead, newer)
	if got := order(m0); got != "[web] lead newer" || st0.saves != 0 {
		t.Fatalf("before #241: order %q, %d saves", got, st0.saves)
	}
}

// Known forgets the agents no longer listed on a save, keeps at most maxKnown keys, and a
// layout with a bad Known or Format is refused.
func TestKnownIsPrunedBoundedAndChecked(t *testing.T) {
	st := &memStore{l: Layout{Saved: 9_000, Format: layoutKnown, Known: []Seen{{Key: "lea-a/gone", Created: 100}},
		Folders: []Folder{{ID: "f1", Name: "web", Agents: []string{"lea-a/lead"}}}}}
	m, lead := towerDeck(t, st)
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Agents: []Agent{lead, spawned("kid", "lea-a/lead", 9_500)}}})
	if len(st.l.Known) != 2 || st.l.Known[0] != (Seen{Key: "lea-a/lead", Created: 100}) ||
		st.l.Known[1] != (Seen{Key: "lea-a/kid", Created: 9_500}) {
		t.Fatalf("known %v", st.l.Known)
	}
	// A child with a key reused from a gone agent's is new again.
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 9, Agents: []Agent{lead}}})
	m = m.toggleFold(folderKey("f1"))
	if len(st.l.Known) != 1 || st.l.Known[0].Key != "lea-a/lead" {
		t.Fatalf("known after the kid went %v", st.l.Known)
	}

	many := make([]Agent, 0, maxKnown+5)
	many = append(many, lead)
	for i := 0; i < maxKnown+4; i++ {
		many = append(many, spawned("k"+itoa(i), "lea-a/nobody", 9_500))
	}
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 10, Agents: many}})
	if len(m.layout.Known) != maxKnown || m.layout.Known[maxKnown-1].Key != "lea-a/k"+itoa(maxKnown+3) {
		t.Fatalf("known %d keys", len(m.layout.Known))
	}
	if err := m.layout.check(); err != nil {
		t.Fatal(err)
	}

	for _, l := range []Layout{
		{Format: 2},
		{Format: layoutKnown, Known: []Seen{{Key: "lea-a/x"}, {Key: "lea-a/x", Created: 1}}},
		{Format: layoutKnown, Known: []Seen{{Key: "not a key"}}},
		{Format: layoutKnown, Known: []Seen{{Key: "lea-a/x", Created: -1}}},
		{Format: layoutKnown, Known: make([]Seen, maxKnown+1)},
	} {
		if err := l.check(); err == nil {
			t.Fatalf("accepted %+v", l.Format)
		}
	}
}

// A done agent whose key store.Register reuses for a new agent (a later Created) is new to the
// layout again: moved out of its folder, then respawned, it joins its new lead's folder, whether
// that is the same lead or another lead in another folder. The same incarnation is never moved
// back, and the respawn is filed once, across a restart too.
func TestARespawnedDoneKeyFollowsItsNewLead(t *testing.T) {
	for name, tc := range map[string]struct {
		lead2  string
		folder int
		order  string
	}{
		"same lead":    {"lea-a/lead", 0, "[web] lead kid [misc] lead2"},
		"another lead": {"lea-a/lead2", 1, "[web] lead [misc] lead2 kid"},
	} {
		path := filepath.Join(t.TempDir(), "lea-a.json")
		st := FileStore{Path: path}
		if err := st.Save(Layout{Saved: 9_000, Folders: []Folder{
			{ID: "f1", Name: "web", Agents: []string{"lea-a/lead"}},
			{ID: "f2", Name: "misc", Agents: []string{"lea-a/lead2"}}}}); err != nil {
			t.Fatal(err)
		}
		lead, lead2 := launched(ag("lead", "idle", 1), 100), launched(ag("lead2", "idle", 1), 100)
		m := sized(newModel(&fakeAPI{}).WithLayout(st), 34, 30)
		m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 7, Agents: []Agent{lead, lead2}}})
		kid := spawned("kid", "lea-a/lead", clock.Unix())
		m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Agents: []Agent{lead, lead2, kid}}})
		if m.folderOf("lea-a/kid") != 0 {
			t.Fatalf("%s: kid not filed: %+v", name, m.layout)
		}
		m.selected = "lea-a/kid"
		m = typed(t, m, "u")
		if m.folderOf("lea-a/kid") >= 0 {
			t.Fatalf("%s: kid not moved out: %+v", name, m.layout)
		}
		done := kid
		done.State = "done"
		m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 9, Agents: []Agent{lead, lead2, done}}})
		if m.folderOf("lea-a/kid") >= 0 {
			t.Fatalf("%s: done kid moved back: %+v", name, m.layout)
		}
		again := spawned("kid", tc.lead2, 10_500)
		agents := []Agent{lead, lead2, again}
		m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 10, Agents: agents}})
		if got := m.folderOf("lea-a/kid"); got != tc.folder {
			t.Fatalf("%s: respawned kid in folder %d, want %d: %+v", name, got, tc.folder, m.layout)
		}
		if got := order(m); got != tc.order {
			t.Fatalf("%s: order %q", name, got)
		}
		l, err := st.Load()
		if err != nil || len(l.Known) != 3 || l.Known[2] != (Seen{Key: "lea-a/kid", Created: 10_500}) {
			t.Fatalf("%s: saved known %+v %v", name, l.Known, err)
		}
		// Moved out again, the respawn stays out, after a restart too.
		m.selected = "lea-a/kid"
		m = typed(t, m, "u")
		m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 11, Agents: agents}})
		r := sized(newModel(&fakeAPI{}).WithLayout(st), 34, 30)
		r, _ = step(t, r, snapshotMsg{snap: Snapshot{Version: 12, Agents: agents}})
		if m.folderOf("lea-a/kid") >= 0 || r.folderOf("lea-a/kid") >= 0 {
			t.Fatalf("%s: respawn moved back: %+v / %+v", name, m.layout, r.layout)
		}
	}
}

// A full Known of the longest agent keys still loads back from the layout file.
func TestAFullKnownOfLongKeysRoundTrips(t *testing.T) {
	st := FileStore{Path: filepath.Join(t.TempDir(), "lea-a.json")}
	l := Layout{Saved: 9_000, Format: layoutKnown}
	for i := 0; i < maxKnown; i++ {
		n := itoa(i)
		l.Known = append(l.Known, Seen{Key: strings.Repeat("c", 64) + "/" + strings.Repeat("n", 64-len(n)) + n, Created: 1 << 40})
	}
	if err := l.check(); err != nil {
		t.Fatal(err)
	}
	if err := st.Save(l); err != nil {
		t.Fatal(err)
	}
	got, err := st.Load()
	if err != nil || len(got.Known) != maxKnown {
		t.Fatalf("load: %d known, %v", len(got.Known), err)
	}
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}
