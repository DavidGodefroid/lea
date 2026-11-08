package deck

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/agents"
	"github.com/DavidGodefroid/lea/internal/daemon"
	"github.com/DavidGodefroid/lea/internal/roles"
	"github.com/DavidGodefroid/lea/internal/store"
)

// #340: agent names are per container, so a new agent can get the key of a done agent another
// lead started. It joins its own lead's folder, never the folder the previous agent of that key
// was in.

// twoLeads is a deck whose folder A holds lead l1 (or no folder, when l1Filed is false) and
// folder B holds lead l2 and its done child dev-1, on store st.
func twoLeads(t *testing.T, st LayoutStore, l1Filed bool) (Model, []Agent) {
	t.Helper()
	l1, l2 := launched(ag("l1", "idle", 1), 100), launched(ag("l2", "idle", 1), 100)
	old := spawned("dev-1", "lea-a/l2", 500)
	old.State = "done"
	agents := []Agent{l1, l2, old}
	m := sized(newModel(&fakeAPI{}).WithLayout(st), 34, 30)
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 7, Agents: agents}})
	if m.folderOf("lea-a/dev-1") != 1 {
		t.Fatalf("old dev-1 not in B: %+v", m.layout)
	}
	if !l1Filed {
		m = m.moveOut("lea-a/l1")
	}
	return m, agents
}

func twoFolders() Layout {
	return Layout{Saved: 9_000, Format: layoutKnown, Known: []Seen{{Key: "lea-a/dev-1", Created: 500}}, Folders: []Folder{
		{ID: "f1", Name: "A", Agents: []string{"lea-a/l1"}},
		{ID: "f2", Name: "B", Agents: []string{"lea-a/l2", "lea-a/dev-1"}}}}
}

// The new dev-1, started by l1, leaves B for A, saved.
func TestARenewedKeyJoinsItsOwnLeadsFolder(t *testing.T) {
	st := &memStore{l: twoFolders()}
	m, agents := twoLeads(t, st, true)
	saves := st.saves
	agents[2] = spawned("dev-1", "lea-a/l1", 9_500)
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Agents: agents}})
	if got := order(m); got != "[A] l1 dev-1 [B] l2" {
		t.Fatalf("order %q", got)
	}
	if st.saves != saves+1 || len(st.l.Folders[0].Agents) != 2 || st.l.Folders[0].Agents[1] != "lea-a/dev-1" ||
		len(st.l.Folders[1].Agents) != 1 {
		t.Fatalf("saved %+v", st.l)
	}
}

// With l1 at the root, the new dev-1 is at the root, saved, not in B.
func TestARenewedKeyOfALeadAtTheRootLeavesTheOldFolder(t *testing.T) {
	st := &memStore{l: twoFolders()}
	m, agents := twoLeads(t, st, false)
	saves := st.saves
	agents[2] = spawned("dev-1", "lea-a/l1", 9_500)
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Agents: agents}})
	if got := order(m); got != "[A] [B] l2 l1 dev-1" {
		t.Fatalf("order %q", got)
	}
	if st.saves != saves+1 || len(st.l.Folders[1].Agents) != 1 {
		t.Fatalf("saved %+v", st.l)
	}
}

// A renewed key the user started (no lead), or one whose previous agent had no lead, leaves the
// old folder too: Known lists the agents without a lead as well.
func TestARenewedKeyWithoutALeadLeavesTheOldFolder(t *testing.T) {
	st := &memStore{l: twoFolders()}
	m, agents := twoLeads(t, st, true)
	agents[2] = launched(ag("dev-1", "working", 1), 9_500)
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Agents: agents}})
	if got := order(m); got != "[A] l1 [B] l2 dev-1" {
		t.Fatalf("user's dev-1: order %q", got)
	}
	// The lead l2 itself ends, and its name goes to a child of l1.
	st = &memStore{l: twoFolders()}
	m, agents = twoLeads(t, st, true)
	agents[1].State = "done"
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Agents: agents}})
	agents[1] = spawned("l2", "lea-a/l1", 9_500)
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 9, Agents: agents}})
	if got := order(m); got != "[A] l1 l2 [B] dev-1" {
		t.Fatalf("lead's name reused: order %q", got)
	}
}

// The same incarnation, moved by the user, stays where the user put it, on later snapshots and
// after a deck restart.
func TestAMovedRenewedKeyStaysPut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lea-a.json")
	st := FileStore{Path: path}
	if err := st.Save(twoFolders()); err != nil {
		t.Fatal(err)
	}
	m, agents := twoLeads(t, st, true)
	agents[2] = spawned("dev-1", "lea-a/l1", 9_500)
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Agents: agents}})
	m = m.moveAgent("lea-a/dev-1", 1) // back to B, by hand
	for v := int64(9); v < 11; v++ {
		m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: v, Agents: agents}})
	}
	if got := order(m); got != "[A] l1 [B] l2 dev-1" {
		t.Fatalf("order %q", got)
	}
	later := func() time.Time { return time.Unix(20_000, 0) }
	r := sized(New(&fakeAPI{}, "lea-a", false, later).WithLayout(st), 34, 30)
	r, _ = step(t, r, snapshotMsg{snap: Snapshot{Version: 12, Agents: agents}})
	if got := order(r); got != "[A] l1 [B] l2 dev-1" {
		t.Fatalf("after restart: order %q", got)
	}
}

// A key renewed in the second of a save is still re-filed (#296's race).
func TestARenewedKeyInTheSaveSecondIsRefiled(t *testing.T) {
	st := &memStore{l: twoFolders()}
	m, agents := twoLeads(t, st, true)
	m = m.toggleFold(folderKey("f2")) // saved at clock
	if st.l.Saved != clock.Unix() {
		t.Fatalf("saved at %d", st.l.Saved)
	}
	agents[2] = spawned("dev-1", "lea-a/l1", clock.Unix())
	m, _ = step(t, m, snapshotMsg{snap: Snapshot{Version: 8, Agents: agents}})
	if m.folderOf("lea-a/dev-1") != 0 {
		t.Fatalf("not re-filed: %+v", m.layout)
	}
}

// End to end on a real store and daemon: two leads in two folders; l2's child ends; l1's child
// gets its name; the deck shows it in l1's folder. The same holds when the done row was
// forgotten first (a name only comes back once its row is gone).
func TestE2ERespawnedNameFollowsItsLead(t *testing.T) {
	for _, forget := range []bool{false, true} {
		dir := t.TempDir()
		st, err := store.Open(filepath.Join(dir, "lea.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		now := int64(1_000)
		mgr := agents.NewManager(agents.Config{Store: st, Now: func() int64 { return now }})
		d := daemon.New(st, filepath.Join(dir, "run"), func() int64 { return now })
		d.SetAgents(mgr)
		sock := filepath.Join(dir, "d.sock")
		l, err := daemon.Listen(sock)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		go d.Serve(ctx, l, daemon.Origin{})
		c := Client{Sock: sock}

		reg := func(name, parent, role string) {
			t.Helper()
			if _, err := st.Register(store.NewAgent{Container: "lea-e340", Name: name, Profile: "claude", Workdir: "/p", Role: role, Parent: parent}, store.Caps{}, now); err != nil {
				t.Fatal(err)
			}
		}
		var version int64
		layout := FileStore{Path: filepath.Join(dir, "deck", "lea-e340.json")}
		if err := layout.Save(Layout{Saved: 500, Format: layoutKnown, Folders: []Folder{
			{ID: "f1", Name: "Lock Family", Agents: []string{"lea-e340/claude-1"}},
			{ID: "f2", Name: "C9", Agents: []string{"lea-e340/claude-2"}}}}); err != nil {
			t.Fatal(err)
		}
		m := sized(New(&fakeAPI{}, "lea-e340", false, func() time.Time { return time.Unix(now, 0) }).WithLayout(layout), 34, 30)
		refresh := func() {
			t.Helper()
			mgr.Changed()
			snap, err := c.Watch(ctx, "lea-e340", version)
			if err != nil {
				t.Fatal(err)
			}
			version = snap.Version
			m, _ = step(t, m, snapshotMsg{snap: snap})
		}
		reg("claude-1", "", roles.Lead)
		reg("claude-2", "", roles.Lead)
		reg("dev-2", "lea-e340/claude-2", roles.Dev)
		refresh()
		if m.folderOf("lea-e340/dev-2") != 1 {
			t.Fatalf("forget=%v: claude-2's dev-2 not in C9: %+v", forget, m.layout)
		}
		if err := st.SetState("lea-e340/dev-2", "done", "", now); err != nil {
			t.Fatal(err)
		}
		refresh()
		if forget {
			if err := st.ForgetAgent("lea-e340/dev-2"); err != nil {
				t.Fatal(err)
			}
		}
		now++
		reg("dev-2", "lea-e340/claude-1", roles.Dev)
		refresh()
		if got := order(m); got != "[Lock Family] claude-1 dev-2 [C9] claude-2" {
			t.Fatalf("forget=%v: order %q", forget, got)
		}
		saved, err := layout.Load()
		if err != nil || len(saved.Folders[0].Agents) != 2 || saved.Folders[0].Agents[1] != "lea-e340/dev-2" || len(saved.Folders[1].Agents) != 1 {
			t.Fatalf("forget=%v: saved %+v %v", forget, saved, err)
		}
	}
}
