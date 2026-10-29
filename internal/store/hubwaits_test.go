package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

const (
	t1 = "01JB0000000000000000000001"
	t2 = "01JB0000000000000000000002"
)

// A lea.db of an older lea (no hub_waits, no kv) gets both, its rows untouched (#159).
func TestHubWaitsMigration(t *testing.T) {
	p := filepath.Join(t.TempDir(), "lea.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	k, _ := s.UpsertAgent("lea-a", "dev-1", 1)
	s.Close()
	db, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE hub_waits; DROP TABLE kv`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err = Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if a, err := s.AgentByKey(k); err != nil || a.Name != "dev-1" {
		t.Fatalf("agent %+v %v", a, err)
	}
	if err := s.AddHubWait(k, t1, "locksql", "crm-prod", 5); err != nil {
		t.Fatal(err)
	}
	if c, err := s.HubCursor(); err != nil || c != "" {
		t.Fatalf("cursor %q %v", c, err)
	}
}

// A wait is recorded once, decided once for every agent on the ticket, delivered once, purged a
// week after delivery.
func TestHubWaitLifecycle(t *testing.T) {
	s := open(t)
	a, _ := s.UpsertAgent("lea-a", "dev-1", 1)
	b, _ := s.UpsertAgent("lea-a", "dev-2", 1)
	for _, w := range [][2]string{{a, t1}, {a, t1}, {b, t1}, {a, t2}} {
		if err := s.AddHubWait(w[0], w[1], "locksql", "crm-prod", 10); err != nil {
			t.Fatal(err)
		}
	}
	if ws, err := s.PendingHubWaits(a); err != nil || len(ws) != 2 || ws[0].Ticket != t1 || ws[0].Lock != "locksql" || ws[0].Instance != "crm-prod" {
		t.Fatalf("pending %+v %v", ws, err)
	}
	ds, err := s.DecideHubWaits(t1, DecisionApproved, "", 20)
	if err != nil || len(ds) != 2 || ds[0].Agent != a || ds[1].Agent != b || ds[0].Decision != DecisionApproved || ds[0].DecidedAt != 20 {
		t.Fatalf("decided %+v %v", ds, err)
	}
	// Final: a later decision on the same ticket changes nothing.
	if ds, err := s.DecideHubWaits(t1, DecisionDenied, "too late", 21); err != nil || len(ds) != 0 {
		t.Fatalf("decided again %+v %v", ds, err)
	}
	u, err := s.UndeliveredHubWaits(a)
	if err != nil || len(u) != 1 || u[0].Ticket != t1 || u[0].Decision != DecisionApproved {
		t.Fatalf("undelivered %+v %v", u, err)
	}
	if err := s.MarkHubWaitDelivered(a, t1, 30); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkHubWaitDelivered(a, t1, 31); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delivered twice: %v", err)
	}
	if err := s.MarkHubWaitDelivered(a, t2, 31); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a pending wait delivered: %v", err)
	}
	if u, _ := s.UndeliveredHubWaits(a); len(u) != 0 {
		t.Fatalf("still undelivered %+v", u)
	}
	if u, _ := s.UndeliveredHubWaits(b); len(u) != 1 {
		t.Fatalf("dev-2's decision %+v", u)
	}
	if _, err := s.DecideHubWaits(t2, DecisionDenied, "not now", 40); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.UndeliveredHubWaits(a); len(u) != 1 || u[0].Reason != "not now" {
		t.Fatalf("denied %+v", u)
	}
	// A week after delivery the delivered wait goes; the others stay.
	if err := s.PurgeHubWaits(30 + hubWaitKeep - 1); err != nil {
		t.Fatal(err)
	}
	if n := countWaits(t, s); n != 3 {
		t.Fatalf("%d waits after an early purge", n)
	}
	if err := s.PurgeHubWaits(30 + hubWaitKeep); err != nil {
		t.Fatal(err)
	}
	if n := countWaits(t, s); n != 2 {
		t.Fatalf("%d waits after the purge", n)
	}
}

func TestHubWaitRefusesBadInput(t *testing.T) {
	s := open(t)
	a, _ := s.UpsertAgent("lea-a", "dev-1", 1)
	for _, c := range [][3]string{{"not-a-ulid", "locksql", "crm"}, {t1, "Lock SQL", "crm"}, {t1, "locksql", "../x"}} {
		if err := s.AddHubWait(a, c[0], c[1], c[2], 1); err == nil {
			t.Errorf("%q accepted", c)
		}
	}
	if _, err := s.DecideHubWaits(t1, "maybe", "", 2); err == nil {
		t.Error("an unknown decision accepted")
	}
	for _, r := range []string{strings.Repeat("r", maxHubReason+1), "\xff"} {
		if _, err := s.DecideHubWaits(t1, DecisionDenied, r, 2); err == nil {
			t.Errorf("reason %.20q accepted", r)
		}
	}
}

// The states of a suspended agent; a cold one has no process, so it is no longer paused.
func TestSuspendedStates(t *testing.T) {
	s := open(t)
	ag, err := s.RegisterAgent("lea-a", "dev-1", "claude", "/p", 1)
	if err != nil {
		t.Fatal(err)
	}
	k := ag.Key
	if !Suspended(StateSuspendedWarm) || !Suspended(StateSuspendedCold) || Suspended("idle") || Suspended("stopped") {
		t.Fatal("Suspended")
	}
	s.SetState(k, "working", "", 2)
	s.SetPaused(k, true)
	s.SetState(k, StateSuspendedWarm, "", 3)
	if a, _ := s.AgentByKey(k); a.State != StateSuspendedWarm || !a.Paused {
		t.Fatalf("warm %+v", a)
	}
	s.SetState(k, StateSuspendedCold, "", 4)
	if a, _ := s.AgentByKey(k); a.State != StateSuspendedCold || a.Paused {
		t.Fatalf("cold %+v", a)
	}
}

func TestHubCursor(t *testing.T) {
	s := open(t)
	for _, c := range []string{"c-1", "c-2"} {
		if err := s.SetHubCursor(c); err != nil {
			t.Fatal(err)
		}
	}
	if c, err := s.HubCursor(); err != nil || c != "c-2" {
		t.Fatalf("%q %v", c, err)
	}
}

func countWaits(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM hub_waits`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
