package store

import (
	"errors"
	"testing"
)

func TestLockRequestLimitsAreTheContainers(t *testing.T) {
	s := open(t)
	r, err := s.AddLockRequest("lea-a", "lea-a/dev", "locksql", "prod", 600, 60, 1000)
	if err != nil || r.ID == 0 || r.State != "pending" {
		t.Fatalf("add: %+v %v", r, err)
	}
	// Another agent name of the same container (any process there may claim any name): one
	// pending request per tool per container.
	if _, err := s.AddLockRequest("lea-a", "lea-a/other", "locksql", "staging", 600, 60, 1030); !errors.Is(err, ErrLimit) {
		t.Fatalf("second pending: %v", err)
	}
	// Another container is not limited by this one.
	if _, err := s.AddLockRequest("lea-b", "lea-b/dev", "locksql", "prod", 600, 60, 1000); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimLockRequest(r.ID, "lea-a", "declined", 600, 1010); err != nil {
		t.Fatal(err)
	}
	// Answered, but less than gap seconds ago it was asked: the rate limit holds.
	if _, err := s.AddLockRequest("lea-a", "lea-a/dev", "locksql", "prod", 600, 60, 1059); !errors.Is(err, ErrRate) {
		t.Fatalf("within the gap: %v", err)
	}
	if _, err := s.AddLockRequest("lea-a", "lea-a/dev", "locksql", "prod", 600, 60, 1060); err != nil {
		t.Fatalf("after the gap: %v", err)
	}
}

func TestLockRequestIsAnsweredOnceAndInItsContainer(t *testing.T) {
	s := open(t)
	r, err := s.AddLockRequest("lea-a", "lea-a/dev", "locksql", "prod", 600, 60, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimLockRequest(r.ID, "lea-b", "opened", 600, 1001); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other container: %v", err)
	}
	if _, err := s.ClaimLockRequest(r.ID+1, "lea-a", "opened", 600, 1001); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no such id: %v", err)
	}
	if _, err := s.ClaimLockRequest(r.ID, "lea-a", "pending", 600, 1001); err == nil {
		t.Fatal("a claim to pending was accepted")
	}
	got, err := s.ClaimLockRequest(r.ID, "lea-a", "opened", 600, 1001)
	if err != nil || got.Tool != "locksql" || got.Profile != "prod" || got.Agent != "lea-a/dev" || got.State != "opened" {
		t.Fatalf("claim: %+v %v", got, err)
	}
	// Replay: a second answer, either way, is refused.
	for _, st := range []string{"opened", "declined"} {
		if _, err := s.ClaimLockRequest(r.ID, "lea-a", st, 600, 1002); !errors.Is(err, ErrDecided) {
			t.Fatalf("replay %s: %v", st, err)
		}
	}
	if err := s.FailLockRequest(r.ID, "no image"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.LockRequestByID(r.ID); got.State != "failed" || got.Result != "no image" || got.Decided != 1001 {
		t.Fatalf("failed: %+v", got)
	}
	if ps, _ := s.PendingLockRequests("lea-a", 600, 1002); len(ps) != 0 {
		t.Fatalf("answered request still pending: %+v", ps)
	}
}

func TestLockRequestExpires(t *testing.T) {
	s := open(t)
	a, _ := s.AddLockRequest("lea-a", "lea-a/dev", "locksql", "prod", 600, 60, 1000)
	b, _ := s.AddLockRequest("lea-b", "lea-b/dev", "locksql", "prod", 600, 60, 1300)
	if ps, _ := s.PendingLockRequests("lea-a", 600, 1599); len(ps) != 1 || ps[0].ID != a.ID {
		t.Fatalf("pending before its ttl: %+v", ps)
	}
	// Past its ttl it is no longer shown, and an answer is refused and closes it.
	if ps, _ := s.PendingLockRequests("lea-a", 600, 1600); len(ps) != 0 {
		t.Fatalf("shown past its ttl: %+v", ps)
	}
	if r, err := s.ClaimLockRequest(a.ID, "lea-a", "opened", 600, 1600); !errors.Is(err, ErrExpired) || r.State != "expired" {
		t.Fatalf("late answer: %+v %v", r, err)
	}
	if got, _ := s.LockRequestByID(a.ID); got.State != "expired" {
		t.Fatalf("not closed: %+v", got)
	}
	gone, err := s.SweepLockRequests(600, 1900)
	if err != nil || len(gone) != 1 || gone[0].ID != b.ID || gone[0].State != "expired" {
		t.Fatalf("sweep: %+v %v", gone, err)
	}
	if gone, _ := s.SweepLockRequests(600, 5000); len(gone) != 0 {
		t.Fatalf("swept twice: %+v", gone)
	}
	// An expired pending request no longer blocks a new one.
	c, _ := s.AddLockRequest("lea-c", "lea-c/dev", "locksql", "prod", 600, 60, 1000)
	if _, err := s.AddLockRequest("lea-c", "lea-c/dev", "locksql", "prod", 600, 60, 1600); err != nil {
		t.Fatalf("after expiry: %v", err)
	}
	if got, _ := s.LockRequestByID(c.ID); got.State != "expired" {
		t.Fatalf("old one: %+v", got)
	}
}
