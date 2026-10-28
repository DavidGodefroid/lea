package daemon

import (
	"context"
	"testing"
	"time"
)

// The first request of a container that waits in the lockhttp console rings once (D6); more
// requests, or another container's, do not ring it again for that one; the deck sees the counts.
func TestLockHTTPPendingRingsOnceAndReachesTheDeck(t *testing.T) {
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
	if got := waitRang(rang, 1); len(got) != 1 || got[0] != SoundAttention {
		t.Fatalf("rang %q", got)
	}
	e.d.SetLockHTTPStatus(ctx, LockHTTPStatus{Version: 1, Console: "up", Pending: 2, PendingByContainer: map[string]int{"lea-a": 2}})
	time.Sleep(50 * time.Millisecond)
	if got := rang(); len(got) != 1 {
		t.Fatalf("a second pending request rang again: %q", got)
	}
	if v := e.d.lockHTTPView("lea-a"); v.Pending != 2 {
		t.Fatalf("view %+v", v)
	}
	if v := e.d.lockHTTPView("lea-b"); v.Pending != 0 {
		t.Fatalf("another container's view %+v", v)
	}
}
