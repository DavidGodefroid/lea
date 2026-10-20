package store

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "lea.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestAgents(t *testing.T) {
	s := open(t)
	k, err := s.UpsertAgent("lea-abc", "hook-only", 10)
	if err != nil || k != "lea-abc/hook-only" {
		t.Fatalf("key=%q err=%v", k, err)
	}
	if err := s.SetState(k, "working", "sess-1", 11); err != nil {
		t.Fatal(err)
	}
	if err := s.SetState(k, "idle", "", 12); err != nil {
		t.Fatal(err)
	}
	as, err := s.Agents()
	if err != nil || len(as) != 1 {
		t.Fatalf("agents=%v err=%v", as, err)
	}
	if as[0].State != "idle" || as[0].SessionID != "sess-1" || as[0].Updated != 12 {
		t.Fatalf("agent=%+v", as[0])
	}
}

func TestThreadsAndUnread(t *testing.T) {
	s := open(t)
	m1, err := s.Send(Message{From: "a", To: "b", Kind: "request", Body: "hi", At: 1})
	if err != nil || m1.Thread != m1.ID {
		t.Fatalf("m1=%+v err=%v", m1, err)
	}
	m2, err := s.Send(Message{From: "b", To: "a", Kind: "result", Body: "ok", ReplyTo: m1.ID, Hops: 1, At: 2})
	if err != nil || m2.Thread != m1.ID || m2.Hops != 1 {
		t.Fatalf("m2=%+v err=%v", m2, err)
	}
	un, _ := s.Unread("a", 8)
	if len(un) != 1 || un[0].ID != m2.ID {
		t.Fatalf("unread=%v", un)
	}
	if err := s.MarkRead([]int64{m2.ID}, 3); err != nil {
		t.Fatal(err)
	}
	if un, _ := s.Unread("a", 8); len(un) != 0 {
		t.Fatalf("still unread: %v", un)
	}
	if n, _ := s.SentSince("a", 0); n != 1 {
		t.Fatalf("sent=%d", n)
	}
	if _, err := s.Get(999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
}

func TestApprovals(t *testing.T) {
	s := open(t)
	id, err := s.AddApproval("c/a", "Bash", `{"command":"make test"}`, false, 1)
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := s.PendingApprovals(); len(p) != 1 {
		t.Fatalf("pending=%v", p)
	}
	if err := s.SetVerdict(id, "allow"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetVerdict(id, "deny"); !errors.Is(err, ErrDecided) {
		t.Fatalf("err=%v", err)
	}
	if err := s.SetVerdict(42, "deny"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
	if p, _ := s.PendingApprovals(); len(p) != 0 {
		t.Fatalf("pending=%v", p)
	}
}

func TestSendUnknownParentStoresNothing(t *testing.T) {
	s := open(t)
	if _, err := s.Send(Message{From: "a", To: "b", Kind: "result", ReplyTo: 77, At: 1}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
	if un, _ := s.Unread("b", 8); len(un) != 0 {
		t.Fatalf("stored: %v", un)
	}
}

func TestRegisterAgentAndLookups(t *testing.T) {
	s := open(t)
	a, err := s.RegisterAgent("lea-a", "claude-1", "claude", "/p/shop", 10)
	if err != nil {
		t.Fatal(err)
	}
	want := Agent{Key: "lea-a/claude-1", Container: "lea-a", Name: "claude-1", State: "unknown",
		Updated: 10, Profile: "claude", Workdir: "/p/shop", Created: 10, Role: "dev"}
	if !reflect.DeepEqual(a, want) {
		t.Fatalf("registered %+v, want %+v", a, want)
	}
	got, err := s.AgentByKey("lea-a/claude-1")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("by key %+v err=%v", got, err)
	}
	if _, err := s.AgentByKey("lea-a/nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing agent: err=%v", err)
	}
	// A hook event of the same agent lands on the same row.
	if k, err := s.UpsertAgent("lea-a", "claude-1", 11); err != nil || k != a.Key {
		t.Fatalf("upsert key=%q err=%v", k, err)
	}
	if err := s.SetState(a.Key, "working", "sess", 12); err != nil {
		t.Fatal(err)
	}
	got, _ = s.AgentByKey(a.Key)
	if got.State != "working" || got.Profile != "claude" {
		t.Fatalf("after hook %+v", got)
	}
}

func TestRegisterAgentRefusesALiveNameAndReusesAnEndedOne(t *testing.T) {
	s := open(t)
	if _, err := s.RegisterAgent("lea-a", "claude-1", "claude", "/p", 10); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterAgent("lea-a", "claude-1", "claude", "/p", 11); !errors.Is(err, ErrExists) {
		t.Fatalf("second register: err=%v", err)
	}
	if err := s.SetState("lea-a/claude-1", "done", "sess-old", 12); err != nil {
		t.Fatal(err)
	}
	a, err := s.RegisterAgent("lea-a", "claude-1", "claude", "/p/other", 13)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.AgentByKey(a.Key)
	if got.State != "unknown" || got.SessionID != "" || got.Workdir != "/p/other" || got.Created != 13 {
		t.Fatalf("reused row %+v", got)
	}
}

func TestSpawnedAgentsAndForget(t *testing.T) {
	s := open(t)
	s.RegisterAgent("lea-a", "zed", "claude", "/p", 1)
	s.RegisterAgent("lea-a", "amy", "claude", "/p", 2)
	s.RegisterAgent("lea-b", "bob", "claude", "/q", 3)
	s.UpsertAgent("lea-a", "hook-only", 4) // registered by a hook only: not spawned
	as, err := s.SpawnedAgents("lea-a")
	if err != nil || len(as) != 2 || as[0].Name != "amy" || as[1].Name != "zed" {
		t.Fatalf("spawned=%+v err=%v", as, err)
	}
	if err := s.ForgetAgent("lea-a/amy"); err != nil {
		t.Fatal(err)
	}
	as, _ = s.SpawnedAgents("lea-a")
	if len(as) != 1 || as[0].Name != "zed" {
		t.Fatalf("after forget %+v", as)
	}
}

func TestMigrateAddsAgentColumnsToAPhase0Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lea.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE agents(
	  key TEXT PRIMARY KEY, container TEXT NOT NULL, name TEXT NOT NULL,
	  session_id TEXT NOT NULL DEFAULT '', state TEXT NOT NULL DEFAULT 'unknown', updated INTEGER NOT NULL);
	INSERT INTO agents(key, container, name, state, updated) VALUES('lea-a/hook-only', 'lea-a', 'hook-only', 'idle', 5);`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	for i := 0; i < 2; i++ { // the migration is idempotent
		s, err := Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		as, err := s.Agents()
		var old Agent
		for _, a := range as { // rows are ordered by key: the phase 0 row is not always first
			if a.Key == "lea-a/hook-only" {
				old = a
			}
		}
		if err != nil || old.State != "idle" || old.Profile != "" {
			t.Fatalf("open %d: agents=%+v err=%v", i, as, err)
		}
		if i == 0 {
			if _, err := s.RegisterAgent("lea-a", "claude-1", "claude", "/p", 9); err != nil {
				t.Fatal(err)
			}
		}
		s.Close()
	}
}

func TestLastShownPerContainer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lea.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if k, err := s.LastShown("lea-a"); err != nil || k != "" {
		t.Fatalf("k=%q err=%v", k, err)
	}
	s.SetLastShown("lea-a", "lea-a/x")
	s.SetLastShown("lea-a", "lea-a/y")
	s.SetLastShown("lea-b", "lea-b/z")
	s.Close()
	s, _ = Open(path) // survives a daemon restart
	defer s.Close()
	if k, _ := s.LastShown("lea-a"); k != "lea-a/y" {
		t.Fatalf("lea-a: %q", k)
	}
	if k, _ := s.LastShown("lea-b"); k != "lea-b/z" {
		t.Fatalf("lea-b: %q", k)
	}
}

func TestForgetAgentMarksItsUnreadMessagesRead(t *testing.T) {
	s := open(t)
	s.RegisterAgent("lea-a", "claude-1", "claude", "/p", 1)
	if _, err := s.Send(Message{From: "lea-a/x", To: "lea-a/claude-1", Kind: "note", Body: "old", At: 2}); err != nil {
		t.Fatal(err)
	}
	if err := s.ForgetAgent("lea-a/claude-1"); err != nil {
		t.Fatal(err)
	}
	if ms, err := s.Unread("lea-a/claude-1", 10); err != nil || len(ms) != 0 {
		t.Fatalf("a reused name inherits %+v (err %v)", ms, err)
	}
}

func TestApprovalExpiryAndAlways(t *testing.T) {
	s := open(t)
	id, err := s.AddApproval("c/a", "Bash", `{"command":"make test"}`, true, 1)
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := s.PendingApprovals(); len(p) != 1 || !p[0].Always {
		t.Fatalf("pending=%+v", p)
	}
	if err := s.ExpireApproval(id); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.PendingApprovals(); len(p) != 0 {
		t.Fatalf("an expired approval is still pending: %+v", p)
	}
	if err := s.SetVerdict(id, "allow"); !errors.Is(err, ErrDecided) {
		t.Fatalf("a verdict after expiry: err=%v", err)
	}
	if a, err := s.ApprovalByID(id); err != nil || a.Verdict != "expired" || !a.Always || a.Tool != "Bash" || a.Agent != "c/a" {
		t.Fatalf("approval=%+v err=%v", a, err)
	}
	id2, _ := s.AddApproval("c/a", "Write", `{}`, false, 2)
	if err := s.SetVerdict(id2, "deny"); err != nil {
		t.Fatal(err)
	}
	if err := s.ExpireApproval(id2); !errors.Is(err, ErrDecided) {
		t.Fatalf("expiry after a verdict: err=%v", err)
	}
	if a, _ := s.ApprovalByID(id2); a.Verdict != "deny" || a.Always {
		t.Fatalf("approval=%+v", a)
	}
	if err := s.ExpireApproval(999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expire 999: err=%v", err)
	}
	if _, err := s.ApprovalByID(999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("approval 999: err=%v", err)
	}
}

func TestUsage(t *testing.T) {
	s := open(t)
	a, err := s.RegisterAgent("lea-a", "claude-1", "claude", "/p", 1)
	if err != nil {
		t.Fatal(err)
	}
	if u, err := s.Usages("lea-a"); err != nil || u[a.Key] != (Usage{CtxPct: -1, CostUSD: -1}) {
		t.Fatalf("usages=%+v err=%v", u, err)
	}
	for i, tc := range []struct {
		u       Usage
		changed bool
	}{
		{Usage{42, 1.2}, true}, {Usage{42, 1.2}, false}, {Usage{43, 1.2}, true}, {Usage{43, 1.25}, true},
	} {
		if ch, err := s.SetUsage(a.Key, tc.u); err != nil || ch != tc.changed {
			t.Fatalf("step %d: changed=%v err=%v", i, ch, err)
		}
	}
	if u, _ := s.Usages("lea-a"); u[a.Key] != (Usage{43, 1.25}) {
		t.Fatalf("usages=%+v", u)
	}
	if u, _ := s.Usages("lea-b"); len(u) != 0 {
		t.Fatalf("other container: %+v", u)
	}
	if ch, err := s.SetUsage("lea-a/nobody", Usage{1, 1}); ch || err != nil {
		t.Fatalf("unknown agent: changed=%v err=%v", ch, err)
	}
	// A reused name starts with an unknown usage.
	s.SetState(a.Key, "done", "", 2)
	if _, err := s.RegisterAgent("lea-a", "claude-1", "claude", "/p", 3); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.Usages("lea-a"); u[a.Key] != (Usage{-1, -1}) {
		t.Fatalf("reused row usage=%+v", u)
	}
}

func TestUnreadCountsThreadAndMarkUnread(t *testing.T) {
	s := open(t)
	send := func(from, to, body string) Message {
		t.Helper()
		m, err := s.Send(Message{From: from, To: to, Kind: "note", Body: body, At: 1})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	m1 := send("user", "c/a", "one")
	m2 := send("c/b", "c/a", "two")
	m3 := send("c/a", "user", "three")
	send("c/b", "user", "four")
	send("c/b", "c/c", "five") // not a's
	counts, err := s.UnreadCounts()
	if err != nil || !reflect.DeepEqual(counts, map[string]int{"c/a": 2, "user": 2, "c/c": 1}) {
		t.Fatalf("counts=%v err=%v", counts, err)
	}
	if n, err := s.UnreadCount("c/a"); err != nil || n != 2 {
		t.Fatalf("count=%d err=%v", n, err)
	}
	if by, err := s.UnreadBySender("user"); err != nil || !reflect.DeepEqual(by, map[string]int{"c/a": 1, "c/b": 1}) {
		t.Fatalf("by sender=%v err=%v", by, err)
	}
	ids := func(ms []Message) []int64 {
		var out []int64
		for _, m := range ms {
			out = append(out, m.ID)
		}
		return out
	}
	if th, err := s.Thread("c/a", 10); err != nil || !reflect.DeepEqual(ids(th), []int64{m1.ID, m2.ID, m3.ID}) {
		t.Fatalf("thread=%v err=%v", ids(th), err)
	}
	if th, _ := s.Thread("c/a", 2); !reflect.DeepEqual(ids(th), []int64{m2.ID, m3.ID}) {
		t.Fatalf("last two, oldest first: %v", ids(th))
	}
	if err := s.MarkRead([]int64{m1.ID, m2.ID}, 5); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.UnreadCount("c/a"); n != 0 {
		t.Fatalf("after read: %d", n)
	}
	if err := s.MarkUnread([]int64{m1.ID}); err != nil {
		t.Fatal(err)
	}
	if un, _ := s.Unread("c/a", 8); len(un) != 1 || un[0].ID != m1.ID || un[0].ReadAt != 0 {
		t.Fatalf("after put back: %+v", un)
	}
	if err := s.MarkRead(nil, 6); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkUnread(nil); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateAddsSlice2Columns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lea.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE agents(
	  key TEXT PRIMARY KEY, container TEXT NOT NULL, name TEXT NOT NULL,
	  session_id TEXT NOT NULL DEFAULT '', state TEXT NOT NULL DEFAULT 'unknown', updated INTEGER NOT NULL);
	CREATE TABLE approvals(
	  id INTEGER PRIMARY KEY AUTOINCREMENT, agent TEXT NOT NULL, tool TEXT NOT NULL,
	  input TEXT NOT NULL, verdict TEXT NOT NULL DEFAULT '', at INTEGER NOT NULL);
	INSERT INTO agents(key, container, name, state, updated) VALUES('lea-a/w1', 'lea-a', 'w1', 'idle', 5);
	INSERT INTO approvals(agent, tool, input, at) VALUES('lea-a/w1', 'Bash', '{}', 5);`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	for i := 0; i < 2; i++ { // the migration is idempotent
		s, err := Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		if p, err := s.PendingApprovals(); err != nil || len(p) != 1 || p[0].Always {
			t.Fatalf("open %d: pending=%+v err=%v", i, p, err)
		}
		if u, err := s.Usages("lea-a"); err != nil || u["lea-a/w1"] != (Usage{-1, -1}) {
			t.Fatalf("open %d: usages=%+v err=%v", i, u, err)
		}
		s.Close()
	}
}

func TestForgetAgentAlsoMarksItsMessagesToTheUserRead(t *testing.T) {
	s := open(t)
	s.RegisterAgent("lea-a", "claude-1", "claude", "/p", 1)
	if _, err := s.Send(Message{From: "lea-a/claude-1", To: "user", Kind: "note", Body: "hi", At: 2}); err != nil {
		t.Fatal(err)
	}
	if err := s.ForgetAgent("lea-a/claude-1"); err != nil {
		t.Fatal(err)
	}
	if n, err := s.UnreadCount("user"); err != nil || n != 0 {
		t.Fatalf("user unread=%d err=%v", n, err)
	}
}

func TestForgetAgentExpiresItsPendingApprovals(t *testing.T) {
	s := open(t)
	s.RegisterAgent("lea-a", "claude-1", "claude", "/p", 1)
	s.AddApproval("lea-a/claude-1", "Bash", "{}", false, 1)
	s.AddApproval("lea-a/other", "Bash", "{}", false, 1)
	if err := s.ForgetAgent("lea-a/claude-1"); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.PendingApprovals(); len(p) != 1 || p[0].Agent != "lea-a/other" {
		t.Fatalf("pending=%+v", p)
	}
}

func TestExpirePendingApprovals(t *testing.T) {
	s := open(t)
	s.AddApproval("c/a", "Bash", "{}", false, 1)
	if err := s.ExpirePendingApprovals(); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.PendingApprovals(); len(p) != 0 {
		t.Fatalf("pending=%+v", p)
	}
}

func reg(t *testing.T, s *Store, name, role, parent string, c Caps) (Agent, error) {
	t.Helper()
	return s.Register(NewAgent{Container: "lea-a", Name: name, Profile: "claude", Workdir: "/p", Role: role, Parent: parent}, c, 1)
}

func TestRegisterRecordsRoleAndTree(t *testing.T) {
	s := open(t)
	lead, err := reg(t, s, "boss", "lead", "", Caps{})
	if err != nil {
		t.Fatal(err)
	}
	kid, err := s.Register(NewAgent{Container: "lea-a", Name: "dev-1", Profile: "claude", Workdir: "/p/shop",
		Role: "dev", Parent: lead.Key, Repo: "shop", Branch: "feat/x"}, Caps{MaxChildren: 6}, 2)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.AgentByKey(kid.Key)
	if err != nil || !reflect.DeepEqual(got, kid) || got.Role != "dev" || got.Parent != lead.Key || got.Repo != "shop" || got.Branch != "feat/x" || got.TaskID != 0 {
		t.Fatalf("kid=%+v stored=%+v err=%v", kid, got, err)
	}
	if ks, err := s.Children(lead.Key); err != nil || len(ks) != 1 || ks[0].Name != "dev-1" {
		t.Fatalf("children=%+v err=%v", ks, err)
	}
	if _, err := reg(t, s, "x", "", "", Caps{}); err == nil {
		t.Fatal("an agent without a role was registered")
	}
}

func TestRegisterCapsChildrenOnly(t *testing.T) {
	s := open(t)
	// A project runs as many agents as it starts: only a lead's children are capped.
	for i := 0; i < 30; i++ {
		role := "dev"
		if i == 1 {
			role = "lead" // a1 gets children below
		}
		if _, err := reg(t, s, fmt.Sprintf("a%d", i), role, "", Caps{}); err != nil {
			t.Fatalf("agent %d: %v", i, err)
		}
	}
	if _, err := reg(t, s, "k1", "dev", "lea-a/a1", Caps{MaxChildren: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := reg(t, s, "k2", "dev", "lea-a/a1", Caps{MaxChildren: 1}); !errors.Is(err, ErrLimit) || !strings.Contains(err.Error(), "a1 already runs 1 agents (max_children 1)") {
		t.Fatalf("second child: err=%v", err)
	}
	if a, _ := s.AgentByKey("lea-a/k2"); a.Key != "" {
		t.Fatal("a refused agent left a row")
	}
	s.SetState("lea-a/k1", "done", "", 4)
	if _, err := reg(t, s, "k2", "dev", "lea-a/a1", Caps{MaxChildren: 1}); err != nil {
		t.Fatalf("after the child ended: %v", err)
	}
}

func TestRegisterCapsHoldUnderConcurrentSpawns(t *testing.T) {
	s := open(t)
	reg(t, s, "boss", "lead", "", Caps{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, limited := 0, 0
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.Register(NewAgent{Container: "lea-a", Name: fmt.Sprintf("c%d", i), Profile: "claude", Workdir: "/p",
				Role: "dev", Parent: "lea-a/boss"}, Caps{MaxChildren: 3}, 1)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrLimit):
				limited++
			default:
				t.Errorf("register c%d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if ok != 3 || limited != 9 {
		t.Fatalf("ok=%d limited=%d, want 3 and 9", ok, limited)
	}
}

func TestSetRoleKeepsTheTreeTwoLevels(t *testing.T) {
	s := open(t)
	lead, _ := reg(t, s, "boss", "lead", "", Caps{})
	kid, _ := reg(t, s, "kid", "dev", lead.Key, Caps{})
	if err := s.SetRole(kid.Key, "lead", true); !errors.Is(err, ErrTree) || !strings.Contains(err.Error(), "kid was started by boss") {
		t.Fatalf("child promoted: err=%v", err)
	}
	if err := s.SetRole(lead.Key, "dev", false); !errors.Is(err, ErrTree) || !strings.Contains(err.Error(), "boss runs 1 agents") {
		t.Fatalf("lead with children demoted: err=%v", err)
	}
	if err := s.SetRole(kid.Key, "reviewer", false); err != nil {
		t.Fatal(err)
	}
	s.SetState(kid.Key, "done", "", 2)
	if err := s.SetRole(lead.Key, "dev", false); err != nil {
		t.Fatalf("demote after the child ended: %v", err)
	}
	if a, _ := s.AgentByKey(lead.Key); a.Role != "dev" {
		t.Fatalf("role=%q", a.Role)
	}
	if a, _ := s.AgentByKey(kid.Key); a.Role != "reviewer" {
		t.Fatalf("kid role=%q", a.Role)
	}
	if err := s.SetRole("lea-a/nope", "dev", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown agent: err=%v", err)
	}
	k, _ := s.UpsertAgent("lea-a", "hook-only", 3)
	if err := s.SetRole(k, "lead", true); err == nil || !strings.Contains(err.Error(), "not started by lea") {
		t.Fatalf("hook-only agent: err=%v", err)
	}
}

func TestForgetAgentOrphansItsChildren(t *testing.T) {
	s := open(t)
	lead, _ := reg(t, s, "boss", "lead", "", Caps{})
	kid, _ := reg(t, s, "kid", "dev", lead.Key, Caps{})
	if err := s.ForgetAgent(lead.Key); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.AgentByKey(kid.Key); a.Parent != "" {
		t.Fatalf("parent=%q after the lead was forgotten", a.Parent)
	}
}

func TestReusedNameDoesNotInheritChildren(t *testing.T) {
	s := open(t)
	lead, _ := reg(t, s, "boss", "lead", "", Caps{})
	reg(t, s, "kid", "dev", lead.Key, Caps{})
	s.SetState(lead.Key, "done", "", 2)
	again, err := reg(t, s, "boss", "lead", "", Caps{})
	if err != nil {
		t.Fatal(err)
	}
	if ks, _ := s.Children(again.Key); len(ks) != 0 {
		t.Fatalf("the new boss inherited %+v", ks)
	}
}

func TestRegisterRefusesABadParent(t *testing.T) {
	s := open(t)
	if _, err := reg(t, s, "kid", "dev", "lea-a/ghost", Caps{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing parent: err=%v", err)
	}
	lead, _ := reg(t, s, "boss", "lead", "", Caps{})
	kid, _ := reg(t, s, "kid", "dev", lead.Key, Caps{})
	if _, err := reg(t, s, "grandkid", "dev", kid.Key, Caps{}); !errors.Is(err, ErrTree) {
		t.Fatalf("third level: err=%v", err)
	}
	if _, err := reg(t, s, "me", "lead", "lea-a/me", Caps{}); !errors.Is(err, ErrTree) {
		t.Fatalf("own parent: err=%v", err)
	}
	for _, n := range []string{"grandkid", "me"} {
		if a, _ := s.AgentByKey("lea-a/" + n); a.Key != "" {
			t.Fatalf("refused %s left a row", n)
		}
	}
}

func TestTasksAreCreatedLinkedAndEnded(t *testing.T) {
	s := open(t)
	a, _ := s.RegisterAgent("lea-a", "dev-1", "claude", "/p", 1)
	id, err := s.CreateTask("lea-a", a.Key, "fix the cart", 2)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.AgentByKey(a.Key); got.TaskID != id {
		t.Fatalf("task_id=%d, want %d", got.TaskID, id)
	}
	tk, err := s.TaskOf(a.Key)
	if err != nil || tk.ID != id || tk.Title != "fix the cart" || tk.Container != "lea-a" || tk.Agent != a.Key || tk.Created != 2 || tk.Ended != 0 {
		t.Fatalf("task=%+v err=%v", tk, err)
	}
	if _, err := s.CreateTask("lea-a", "lea-a/nobody", "", 3); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown agent: err=%v", err)
	}
	id2, _ := s.CreateTask("lea-a", a.Key, "next", 4)
	if tk, _ := s.TaskOf(a.Key); tk.ID != id2 {
		t.Fatalf("open task=%+v, want #%d", tk, id2)
	}
	if ts, _ := s.Tasks("lea-a", 10); len(ts) != 2 || ts[0].ID != id2 || ts[1].ID != id || ts[1].Ended != 4 {
		t.Fatalf("tasks=%+v", ts)
	}
	if err := s.DeleteTask(id2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TaskOf(a.Key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted task still open: err=%v", err)
	}
	if got, _ := s.AgentByKey(a.Key); got.TaskID != 0 {
		t.Fatalf("task_id=%d after delete", got.TaskID)
	}
	if ts, _ := s.Tasks("", 0); len(ts) != 0 {
		t.Fatalf("open tasks=%+v", ts)
	}
}

func TestForgetEndsTheTask(t *testing.T) {
	s := open(t)
	a, _ := s.RegisterAgent("lea-a", "dev-1", "claude", "/p", 1)
	id, _ := s.CreateTask("lea-a", a.Key, "t", 2)
	if err := s.ForgetAgent(a.Key); err != nil {
		t.Fatal(err)
	}
	var ended int64
	if err := s.db.QueryRow(`SELECT ended FROM tasks WHERE id=?`, id).Scan(&ended); err != nil || ended == 0 {
		t.Fatalf("ended=%d err=%v", ended, err)
	}
	if _, err := s.RegisterAgent("lea-a", "dev-1", "claude", "/p", 3); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TaskOf(a.Key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a new agent of the same name has a task: err=%v", err)
	}
}

func TestRegisterChecksTheParentsRoleInTheTransaction(t *testing.T) {
	s := open(t)
	dev, err := s.Register(NewAgent{Container: "lea-a", Name: "dev1", Profile: "claude", Workdir: "/p", Role: "dev"}, Caps{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Register(NewAgent{Container: "lea-a", Name: "kid", Profile: "claude", Workdir: "/p", Role: "dev", Parent: dev.Key}, Caps{}, 2); err == nil ||
		!strings.Contains(err.Error(), "cannot start agents") {
		t.Fatalf("a dev got a child: err=%v", err)
	}
}

func TestSpawnRequests(t *testing.T) {
	s := open(t)
	body := func(id int64) string { return fmt.Sprintf("request #%d", id) }
	req := SpawnRequest{Container: "lea-a", Requester: "lea-a/kid", Lead: "lea-a/boss", Role: "reviewer", Profile: "claude",
		Brief: "review the cart", Reason: "eyes"}
	r1, err := s.AddSpawnRequest(req, body, 2, 3600, 10)
	if err != nil || r1.ID == 0 || r1.State != "pending" || r1.Message == 0 || r1.At != 10 {
		t.Fatalf("r1=%+v err=%v", r1, err)
	}
	m, _ := s.Get(r1.Message)
	if m.Kind != "spawn_request" || m.From != "lea-a/kid" || m.To != "lea-a/boss" || m.Body != fmt.Sprintf("request #%d", r1.ID) {
		t.Fatalf("message=%+v", m)
	}
	with := func(brief string) SpawnRequest { r := req; r.Brief = brief; return r }
	r2, err := s.AddSpawnRequest(with("other"), body, 2, 3600, 11)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddSpawnRequest(with("third"), body, 2, 3600, 12); !errors.Is(err, ErrLimit) || !strings.Contains(err.Error(), "at most 2") {
		t.Fatalf("third pending: err=%v", err)
	}
	if p, _ := s.PendingSpawnRequest("lea-a/kid", "lea-a/boss"); p.ID != r1.ID {
		t.Fatalf("oldest pending=%+v", p)
	}
	if ps, _ := s.PendingSpawnRequests("lea-a/boss"); len(ps) != 2 || ps[0].ID != r1.ID {
		t.Fatalf("pending=%+v", ps)
	}
	if err := s.MoveSpawnRequest(r1.ID, "pending", "declined", "", 13); err != nil {
		t.Fatal(err)
	}
	if err := s.MoveSpawnRequest(r1.ID, "pending", "accepted", "lea-a/x", 14); !errors.Is(err, ErrDecided) {
		t.Fatalf("move a declined request: err=%v", err)
	}
	if err := s.MoveSpawnRequest(999, "pending", "declined", "", 14); !errors.Is(err, ErrNotFound) {
		t.Fatalf("move #999: err=%v", err)
	}
	if _, err := s.AddSpawnRequest(with("  review the cart\n"), body, 2, 3600, 15); !errors.Is(err, ErrDeclined) {
		t.Fatalf("declined brief sent again: err=%v", err)
	}
	r3, err := s.AddSpawnRequest(with("third"), body, 2, 3600, 16)
	if err != nil {
		t.Fatalf("after a decline frees a slot: %v", err)
	}
	if _, err := s.AddSpawnRequest(with("fourth"), body, 2, 3600, 17); !errors.Is(err, ErrLimit) {
		t.Fatalf("two pending again: err=%v", err)
	}
	if _, err := s.AddSpawnRequest(with("fourth"), body, 2, 3600, 11+3600); err != nil {
		t.Fatalf("after r2 expired: %v", err)
	}
	if g, _ := s.SpawnRequestByID(r2.ID); g.State != "expired" {
		t.Fatalf("r2=%+v", g)
	}
	if g, err := s.SpawnRequestByMessage(r1.Message); err != nil || g.ID != r1.ID || g.State != "declined" || g.Decided != 13 {
		t.Fatalf("by message=%+v err=%v", g, err)
	}
	if err := s.MoveSpawnRequest(r3.ID, "pending", "accepting", "", 20); err != nil {
		t.Fatal(err)
	}
	if err := s.MoveSpawnRequest(r3.ID, "accepting", "accepted", "lea-a/rev", 21); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.SpawnRequestByID(r3.ID); g.State != "accepted" || g.Agent != "lea-a/rev" {
		t.Fatalf("r3=%+v", g)
	}
}

func TestForgettingAgentsClosesTheirRequests(t *testing.T) {
	s := open(t)
	kid, _ := s.RegisterAgent("lea-a", "kid", "claude", "/p", 1)
	boss, _ := s.RegisterAgent("lea-a", "boss", "claude", "/p", 1)
	body := func(int64) string { return "r" }
	r1, _ := s.AddSpawnRequest(SpawnRequest{Container: "lea-a", Requester: kid.Key, Lead: boss.Key, Role: "dev", Profile: "claude", Brief: "a"}, body, 2, 3600, 2)
	if err := s.ForgetAgent(kid.Key); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.SpawnRequestByID(r1.ID); g.State != "withdrawn" {
		t.Fatalf("requester gone: %+v", g)
	}
	other, _ := s.RegisterAgent("lea-a", "other", "claude", "/p", 3)
	r2, _ := s.AddSpawnRequest(SpawnRequest{Container: "lea-a", Requester: other.Key, Lead: boss.Key, Role: "dev", Profile: "claude", Brief: "b"}, body, 2, 3600, 4)
	if err := s.ForgetAgent(boss.Key); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.SpawnRequestByID(r2.ID); g.State != "expired" {
		t.Fatalf("lead gone: %+v", g)
	}
	if _, err := s.AddSpawnRequest(SpawnRequest{Container: "lea-a", Requester: other.Key, Lead: "user", Role: "dev", Profile: "claude", Brief: "b"}, body, 2, 3600, 5); err != nil {
		t.Fatalf("an expired brief may be sent again: %v", err)
	}
}

func TestAStuckAcceptingRequestExpires(t *testing.T) {
	s := open(t)
	body := func(int64) string { return "r" }
	req := SpawnRequest{Container: "lea-a", Requester: "lea-a/kid", Lead: "lea-a/boss", Role: "dev", Profile: "claude"}
	with := func(b string) SpawnRequest { r := req; r.Brief = b; return r }
	r1, _ := s.AddSpawnRequest(with("a"), body, 1, 3600, 10)
	if err := s.MoveSpawnRequest(r1.ID, "pending", "accepting", "", 100); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddSpawnRequest(with("b"), body, 1, 3600, 100+3599); !errors.Is(err, ErrLimit) {
		t.Fatalf("accepting counts as open: err=%v", err)
	}
	if _, err := s.AddSpawnRequest(with("b"), body, 1, 3600, 100+3600); err != nil {
		t.Fatalf("a spawn that never finished frees its slot: %v", err)
	}
	if g, _ := s.SpawnRequestByID(r1.ID); g.State != "expired" {
		t.Fatalf("r1=%+v", g)
	}
}

func TestSweepSpawnRequests(t *testing.T) {
	s := open(t)
	kid, _ := s.RegisterAgent("lea-a", "kid", "claude", "/p", 1)
	ended, _ := s.RegisterAgent("lea-a", "ended", "claude", "/p", 1)
	body := func(int64) string { return "r" }
	add := func(requester, brief string, at int64) SpawnRequest {
		r, err := s.AddSpawnRequest(SpawnRequest{Container: "lea-a", Requester: requester, Lead: "user", Role: "dev", Profile: "claude", Brief: brief}, body, 2, 100, at)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	old, fresh, gone := add(kid.Key, "old", 10), add(kid.Key, "fresh", 60), add(ended.Key, "e", 60)
	if err := s.SetState(ended.Key, "done", "", 61); err != nil {
		t.Fatal(err)
	}
	if err := s.SweepSpawnRequests(100, 110); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		r    SpawnRequest
		want string
	}{{old, "expired"}, {fresh, "pending"}, {gone, "withdrawn"}} {
		if g, _ := s.SpawnRequestByID(c.r.ID); g.State != c.want {
			t.Fatalf("%s: %+v, want %s", c.r.Brief, g, c.want)
		}
	}
}

func TestUpdateTaskCreatesReportsChangesAndStatuses(t *testing.T) {
	s := open(t)
	a, _ := s.RegisterAgent("lea-a", "dev-1", "claude", "/p", 1)
	if ch, err := s.UpdateTask(a.Key, "working", "tests", "", 2); err != nil || !ch {
		t.Fatalf("first update: changed=%v err=%v", ch, err)
	}
	if tk, _ := s.TaskOf(a.Key); tk.StatusText != "working" || tk.Next != "tests" || tk.Updated != 2 {
		t.Fatalf("task=%+v", tk)
	}
	if ch, _ := s.UpdateTask(a.Key, "working", "tests", "", 3); ch {
		t.Fatal("the same report changed something")
	}
	if ch, _ := s.UpdateTask(a.Key, "blocked", "tests", "review", 4); !ch {
		t.Fatal("a new report changed nothing")
	}
	if st, _ := s.TaskStatuses("lea-a"); st[a.Key] != (TaskState{Status: "blocked", Next: true, Blocked: true}) {
		t.Fatalf("statuses=%v", st)
	}
	// A task with only a next step still says the agent waits; an empty one says nothing.
	b, _ := s.RegisterAgent("lea-a", "dev-2", "claude", "/p", 1)
	s.UpdateTask(b.Key, "", "CI", "", 5)
	c, _ := s.RegisterAgent("lea-a", "dev-3", "claude", "/p", 1)
	s.UpdateTask(c.Key, "done", "", "", 5)
	st, _ := s.TaskStatuses("lea-a")
	if st[b.Key] != (TaskState{Next: true}) || st[c.Key] != (TaskState{Status: "done"}) {
		t.Fatalf("statuses=%v", st)
	}
	if _, err := s.UpdateTask("lea-a/nobody", "x", "", "", 5); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown agent: err=%v", err)
	}
}

func TestLeases(t *testing.T) {
	s := open(t)
	a, b := "lea-a/a", "lea-a/b"
	l, err := s.AcquireLease("lea-a", a, "src/*.go", 60, 100, 16)
	if err != nil || l.ID == 0 || l.Expires != 160 {
		t.Fatalf("lease=%+v err=%v", l, err)
	}
	if _, err := s.AcquireLease("lea-a", b, "src/main.go", 60, 101, 16); !errors.Is(err, ErrHeld) || !strings.Contains(err.Error(), `a holds "src/*.go" for 59 s more`) {
		t.Fatalf("overlap: err=%v", err)
	}
	if _, err := s.AcquireLease("lea-b", b, "src/main.go", 60, 101, 16); err != nil {
		t.Fatalf("another container: %v", err)
	}
	again, err := s.AcquireLease("lea-a", a, "src/*.go", 600, 110, 16)
	if err != nil || again.ID != l.ID || again.Expires != 710 {
		t.Fatalf("extend=%+v err=%v", again, err)
	}
	if _, err := s.AcquireLease("lea-a", a, "docs/*", 60, 111, 1); !errors.Is(err, ErrLimit) {
		t.Fatalf("over the per-agent cap: err=%v", err)
	}
	if err := s.ReleaseLease(l.ID, b); !errors.Is(err, ErrNotFound) {
		t.Fatalf("release another agent's lease: err=%v", err)
	}
	if err := s.ReleaseLease(l.ID, a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcquireLease("lea-a", b, "src/main.go", 60, 112, 16); err != nil {
		t.Fatalf("after release: %v", err)
	}
	if ls, _ := s.Leases("lea-a", 112); len(ls) != 1 || ls[0].Agent != b {
		t.Fatalf("leases=%+v", ls)
	}
	if _, err := s.AcquireLease("lea-a", a, "src/main.go", 60, 173, 16); err != nil {
		t.Fatalf("after b's lease expired: %v", err)
	}
}

func TestGlobsOverlap(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"src/*.go", "src/main.go", true}, {"src/main.go", "src/*.go", true}, {"src/*", "src/*.go", true},
		{"a/b", "a/b", true}, {"src/*.go", "docs/x.md", false}, {"src/*", "src/a/b.go", false},
	} {
		if got := GlobsOverlap(tc.a, tc.b); got != tc.want {
			t.Fatalf("%q ~ %q = %v", tc.a, tc.b, got)
		}
	}
}

func TestForgetDropsLeases(t *testing.T) {
	s := open(t)
	a, _ := s.RegisterAgent("lea-a", "boss", "claude", "/p", 1)
	if _, err := s.AcquireLease("lea-a", a.Key, "src/*", 60, 1, 16); err != nil {
		t.Fatal(err)
	}
	if err := s.ForgetAgent(a.Key); err != nil {
		t.Fatal(err)
	}
	if ls, _ := s.Leases("", 1); len(ls) != 0 {
		t.Fatalf("leases left: %+v", ls)
	}
}

func TestGlobsOverlapIsBounded(t *testing.T) {
	long := strings.Repeat("*a", 100)
	if GlobsOverlap(long, strings.Repeat("a", 200)) || GlobsOverlap(strings.Repeat("a", 300), strings.Repeat("a", 301)) {
		t.Fatal("an oversized pattern must only match itself")
	}
	if !GlobsOverlap(long, long) {
		t.Fatal("equal patterns overlap")
	}
}

func TestLeaseListIsAgentsOwn(t *testing.T) {
	s := open(t)
	l, err := s.AcquireLease("lea-a", "lea-a/a", "x", 10, 1, 16)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseLease(l.ID, "lea-a/a"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseLease(l.ID, "lea-a/a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second release: %v", err)
	}
}

func TestLabelIsSetClearedAndResetOnReuse(t *testing.T) {
	s := open(t)
	if _, err := s.RegisterAgent("lea-a", "claude-1", "claude", "/p", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLabel("lea-a/claude-1", "auth refactor"); err != nil {
		t.Fatal(err)
	}
	if a, err := s.AgentByKey("lea-a/claude-1"); err != nil || a.Label != "auth refactor" || a.Name != "claude-1" {
		t.Fatalf("agent=%+v err=%v", a, err)
	}
	if as, _ := s.SpawnedAgents("lea-a"); len(as) != 1 || as[0].Label != "auth refactor" {
		t.Fatalf("spawned=%+v", as)
	}
	if err := s.SetLabel("lea-a/claude-1", ""); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.AgentByKey("lea-a/claude-1"); a.Label != "" {
		t.Fatalf("label not cleared: %+v", a)
	}
	if err := s.SetLabel("lea-a/nobody", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown agent: %v", err)
	}
	// A new agent reusing the name of an ended one never inherits its label.
	s.SetLabel("lea-a/claude-1", "old")
	s.SetState("lea-a/claude-1", "done", "", 2)
	if _, err := s.RegisterAgent("lea-a", "claude-1", "claude", "/p", 3); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.AgentByKey("lea-a/claude-1"); a.Label != "" {
		t.Fatalf("label inherited: %+v", a)
	}
}

func TestMigrateAddsTheLabelColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lea.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE agents(
	  key TEXT PRIMARY KEY, container TEXT NOT NULL, name TEXT NOT NULL,
	  session_id TEXT NOT NULL DEFAULT '', state TEXT NOT NULL DEFAULT 'unknown', updated INTEGER NOT NULL);
	INSERT INTO agents(key, container, name, state, updated) VALUES('lea-a/w1', 'lea-a', 'w1', 'idle', 5);`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	for i := 0; i < 2; i++ {
		s, err := Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		if a, err := s.AgentByKey("lea-a/w1"); err != nil || a.Label != "" {
			t.Fatalf("open %d: agent=%+v err=%v", i, a, err)
		}
		s.Close()
	}
}

func TestLaunchIsRecordedAndResetOnReuse(t *testing.T) {
	s := open(t)
	a, err := s.RegisterAgent("lea-a", "dev-1", "claude", "/p", 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetLaunch(a.Key, []string{"--model", "x y"}, "You are the lea agent dev-1."); err != nil {
		t.Fatal(err)
	}
	got, err := s.AgentByKey(a.Key)
	if err != nil || !reflect.DeepEqual(got.Args, []string{"--model", "x y"}) || got.Prompt != "You are the lea agent dev-1." {
		t.Fatalf("agent=%+v err=%v", got, err)
	}
	if err := s.SetState(a.Key, "done", "", 11); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterAgent("lea-a", "dev-1", "claude", "/p", 12); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.AgentByKey(a.Key); got.Args != nil || got.Prompt != "" {
		t.Fatalf("a reused name kept the launch of the ended agent: %+v", got)
	}
	if err := s.SetLaunch("lea-a/none", nil, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown agent: err=%v", err)
	}
}

func TestPausedIsSetAndClearedWhenTheProcessGoes(t *testing.T) {
	s := open(t)
	if _, err := s.RegisterAgent("lea-a", "claude-1", "claude", "/p", 1); err != nil {
		t.Fatal(err)
	}
	s.SetState("lea-a/claude-1", "working", "", 2)
	if err := s.SetPaused("lea-a/claude-1", true); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.AgentByKey("lea-a/claude-1"); !a.Paused || a.State != "working" {
		t.Fatalf("agent=%+v", a)
	}
	if as, _ := s.SpawnedAgents("lea-a"); len(as) != 1 || !as[0].Paused {
		t.Fatalf("spawned=%+v", as)
	}
	// Its own hooks keep reporting: the pause stays.
	s.SetState("lea-a/claude-1", "idle", "", 3)
	if a, _ := s.AgentByKey("lea-a/claude-1"); !a.Paused {
		t.Fatalf("a hook cleared the pause: %+v", a)
	}
	if err := s.SetPaused("lea-a/claude-1", false); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.AgentByKey("lea-a/claude-1"); a.Paused {
		t.Fatalf("not resumed: %+v", a)
	}
	if err := s.SetPaused("lea-a/nobody", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown agent: %v", err)
	}
	// A process that ends, stops with its container or starts again runs unpaused.
	for _, st := range []string{"done", "stopped", "unknown"} {
		s.SetPaused("lea-a/claude-1", true)
		s.SetState("lea-a/claude-1", st, "", 4)
		if a, _ := s.AgentByKey("lea-a/claude-1"); a.Paused {
			t.Fatalf("still paused once %s: %+v", st, a)
		}
	}
	// A new agent reusing the name of an ended one is never paused.
	s.SetState("lea-a/claude-1", "done", "", 4)
	s.SetPaused("lea-a/claude-1", true)
	if _, err := s.RegisterAgent("lea-a", "claude-1", "claude", "/p", 5); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.AgentByKey("lea-a/claude-1"); a.Paused {
		t.Fatalf("pause inherited: %+v", a)
	}
}

// An agent's model and a spawn request's model are kept (the columns an older lea.db lacks are
// added by migrate).
func TestModelIsStored(t *testing.T) {
	s := open(t)
	a, err := s.Register(NewAgent{Container: "lea-a", Name: "cx", Profile: "codex", Model: "gpt-5.1-codex", Workdir: "/p", Role: "dev"}, Caps{}, 1)
	if err != nil || a.Model != "gpt-5.1-codex" {
		t.Fatalf("%+v %v", a, err)
	}
	if got, err := s.AgentByKey(a.Key); err != nil || got.Model != "gpt-5.1-codex" || got.Profile != "codex" {
		t.Fatalf("%+v %v", got, err)
	}
	r, err := s.AddSpawnRequest(SpawnRequest{Container: "lea-a", Requester: "lea-a/kid", Lead: "user", Role: "dev", Profile: "lan",
		Model: "qwen3", Brief: "b"}, func(int64) string { return "r" }, 2, 3600, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.SpawnRequestByID(r.ID); err != nil || got.Model != "qwen3" || got.Profile != "lan" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestReplyFromTakesTheAddresseesFirstReply(t *testing.T) {
	s := open(t)
	q, _ := s.Send(Message{From: "c/a", To: "c/b", Kind: "question", Body: "q", At: 1})
	if _, err := s.ReplyFrom(q.ID, "c/b", "c/a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
	s.Send(Message{From: "c/x", To: "c/a", Kind: "note", Body: "not b", ReplyTo: q.ID, At: 2})
	s.Send(Message{From: "c/b", To: "c/x", Kind: "result", Body: "not to a", ReplyTo: q.ID, At: 2})
	r1, _ := s.Send(Message{From: "c/b", To: "c/a", Kind: "result", Body: "one", ReplyTo: q.ID, At: 3})
	s.Send(Message{From: "c/b", To: "c/a", Kind: "result", Body: "two", ReplyTo: q.ID, At: 4})
	if m, err := s.ReplyFrom(q.ID, "c/b", "c/a"); err != nil || m.ID != r1.ID || m.Body != "one" {
		t.Fatalf("m=%+v err=%v", m, err)
	}
}

func TestSubagentCounts(t *testing.T) {
	s := open(t)
	a, err := s.RegisterAgent("lea-a", "claude-1", "claude", "/p", 1)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.RegisterAgent("lea-b", "claude-1", "claude", "/p", 1)
	for i, tc := range []struct {
		n       int
		changed bool
	}{{2, true}, {2, false}, {0, true}} {
		if ch, err := s.SetSubagents(a.Key, tc.n); err != nil || ch != tc.changed {
			t.Fatalf("step %d: changed=%v err=%v", i, ch, err)
		}
	}
	s.SetSubagents(a.Key, 3)
	s.SetSubagents(b.Key, 1)
	if n, _ := s.Subagents("lea-a"); len(n) != 1 || n[a.Key] != 3 {
		t.Fatalf("lea-a: %v", n)
	}
	if n, _ := s.Subagents(""); len(n) != 2 {
		t.Fatalf("all: %v", n)
	}
	if ch, err := s.SetSubagents("lea-a/nobody", 1); ch || err != nil {
		t.Fatalf("unknown agent: changed=%v err=%v", ch, err)
	}
	if err := s.ClearSubagents(); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Subagents(""); len(n) != 0 {
		t.Fatalf("after clear: %v", n)
	}
	// A reused name starts with none.
	s.SetSubagents(a.Key, 2)
	s.SetState(a.Key, "done", "", 2)
	if _, err := s.RegisterAgent("lea-a", "claude-1", "claude", "/p", 3); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Subagents("lea-a"); len(n) != 0 {
		t.Fatalf("reused: %v", n)
	}
}

// A usage limit is recorded, survives the agent's own state changes and a container restart
// (stopped, unknown: the resumed session takes it over), and goes with the process (done) or a
// new agent reusing the name.
func TestLimitIsSetAndClearedWhenTheProcessGoes(t *testing.T) {
	s := open(t)
	if _, err := s.RegisterAgent("lea-a", "claude-1", "claude", "/p", 1); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.AgentByKey("lea-a/claude-1"); a.LimitUntil != 0 || a.LimitTries != 0 {
		t.Fatalf("new agent limited: %+v", a)
	}
	if err := s.SetLimit("lea-a/claude-1", 900, 2); err != nil {
		t.Fatal(err)
	}
	s.SetState("lea-a/claude-1", "idle", "", 3)
	if a, _ := s.AgentByKey("lea-a/claude-1"); a.LimitUntil != 900 || a.LimitTries != 2 {
		t.Fatalf("limit=%+v", a)
	}
	if as, _ := s.SpawnedAgents("lea-a"); len(as) != 1 || as[0].LimitUntil != 900 {
		t.Fatalf("spawned=%+v", as)
	}
	if err := s.SetLimit("lea-a/nobody", 1, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown agent: %v", err)
	}
	for _, st := range []string{"stopped", "unknown"} {
		s.SetLimit("lea-a/claude-1", 900, 2)
		s.SetState("lea-a/claude-1", st, "", 4)
		if a, _ := s.AgentByKey("lea-a/claude-1"); a.LimitUntil != 900 || a.LimitTries != 2 {
			t.Fatalf("limit lost once %s: %+v", st, a)
		}
	}
	// A paused agent's pending resume is given up when it stops or restarts: the pause it loses
	// meant "do not resume me".
	for _, st := range []string{"stopped", "unknown"} {
		s.SetState("lea-a/claude-1", "idle", "", 4)
		s.SetLimit("lea-a/claude-1", 900, 2)
		if err := s.SetPaused("lea-a/claude-1", true); err != nil {
			t.Fatal(err)
		}
		s.SetState("lea-a/claude-1", st, "", 4)
		if a, _ := s.AgentByKey("lea-a/claude-1"); a.Paused || a.LimitUntil != -1 || a.LimitTries != 2 {
			t.Fatalf("paused then %s: %+v", st, a)
		}
	}
	s.SetLimit("lea-a/claude-1", -1, 6)
	s.SetState("lea-a/claude-1", "done", "", 4)
	if a, _ := s.AgentByKey("lea-a/claude-1"); a.LimitUntil != 0 || a.LimitTries != 0 {
		t.Fatalf("still limited once done: %+v", a)
	}
	s.SetState("lea-a/claude-1", "done", "", 4)
	s.SetLimit("lea-a/claude-1", 900, 3)
	if _, err := s.RegisterAgent("lea-a", "claude-1", "claude", "/p", 5); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.AgentByKey("lea-a/claude-1"); a.LimitUntil != 0 || a.LimitTries != 0 {
		t.Fatalf("limit inherited: %+v", a)
	}
}
