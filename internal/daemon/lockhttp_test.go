package daemon

import (
	"context"
	"testing"
	"time"
)

// A request of a container that waits for lox run rings nothing in lea (#300: lox run calls the
// user, outside lea); the deck sees the counts.
func TestLockHTTPPendingRingsNothingAndReachesTheDeck(t *testing.T) {
	e, rang := soundEnv(t)
	e.d.SetAgents(&fakeAgents{})
	ctx := context.Background()
	if v := e.d.lockHTTPView("lea-a"); v != nil {
		t.Fatalf("LockHTTP off: view %+v", v)
	}
	e.d.SetLockHTTPStatus(ctx, LockHTTPStatus{Version: 1, Console: "up"})
	if v := e.d.lockHTTPView("lea-a"); v == nil || v.Console != "up" || v.Pending != 0 {
		t.Fatalf("view %+v", v)
	}
	e.d.SetLockHTTPStatus(ctx, LockHTTPStatus{Version: 1, Console: "up", Pending: 1, PendingByContainer: map[string]int{"lea-a": 1}})
	e.d.SetLockHTTPStatus(ctx, LockHTTPStatus{Version: 1, Console: "up", Pending: 2, PendingByContainer: map[string]int{"lea-a": 2}})
	time.Sleep(50 * time.Millisecond)
	if got := rang(); len(got) != 0 {
		t.Fatalf("a pending request rang: %q", got)
	}
	if v := e.d.lockHTTPView("lea-a"); v.Pending != 2 {
		t.Fatalf("view %+v", v)
	}
	if v := e.d.lockHTTPView("lea-b"); v.Pending != 0 {
		t.Fatalf("another container's view %+v", v)
	}
}

// Every Lock's approvals reach the deck, per container and by name (#153); none rings (#300).
func TestLockStatusOfSeveralLocks(t *testing.T) {
	e, rang := soundEnv(t)
	e.d.SetAgents(&fakeAgents{})
	ctx := context.Background()
	if v := e.d.lockViews("lea-a"); len(v) != 0 {
		t.Fatalf("no Lock known: %+v", v)
	}
	e.d.SetLockHTTPStatus(ctx, LockHTTPStatus{Version: 1, Console: "up", PendingByContainer: map[string]int{"lea-a": 1}})
	e.d.SetLockStatus(ctx, "locksql", LockStatus{Console: "up", PendingByContainer: map[string]int{"lea-a": 2, "lea-b": 1}})
	e.d.SetLockStatus(ctx, "Bad Name", LockStatus{PendingByContainer: map[string]int{"lea-a": 9}})
	v := e.d.lockViews("lea-a")
	if len(v) != 2 || v[0].Lock != "lockhttp" || v[0].Pending != 1 || v[1].Lock != "locksql" || v[1].Pending != 2 {
		t.Fatalf("views %+v", v)
	}
	if v := e.d.lockViews("lea-b"); len(v) != 2 || v[0].Pending != 0 || v[1].Pending != 1 {
		t.Fatalf("lea-b views %+v", v)
	}
	e.d.SetLockStatus(ctx, "locksql", LockStatus{Console: "up", PendingByContainer: map[string]int{"lea-a": 3, "lea-b": 1}})
	time.Sleep(50 * time.Millisecond)
	if got := rang(); len(got) != 0 {
		t.Fatalf("a Lock's requests rang: %q", got)
	}
}
