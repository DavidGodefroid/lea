package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/DavidGodefroid/lea/internal/agents"
	"github.com/DavidGodefroid/lea/internal/launcher"
)

// lockFamily is a projects directory with lox up's state for the locks named (all locksql):
// what ReadUpLocks trusts.
type lockFamily struct {
	proj, lf, project string
}

func newLockFamily(t *testing.T) lockFamily {
	t.Helper()
	proj, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lf := proj + "/.lockfamily"
	if err := os.Mkdir(lf, 0o755); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte(proj))
	return lockFamily{proj: proj, lf: lf, project: "lockfamily-" + hex.EncodeToString(h[:4])}
}

// write rewrites up.json as lox up does (a new file renamed over the old one), with mode.
func (f lockFamily) write(t *testing.T, mode os.FileMode, names ...string) {
	t.Helper()
	locks := map[string]any{}
	for _, n := range names {
		locks[n] = map[string]string{"type": "locksql", "socket_volume": f.project + "_" + n + "-run"}
	}
	b, _ := json.Marshal(map[string]any{"version": 1, "project": f.project, "workspace": f.proj, "locks": locks})
	tmp := f.lf + "/.up.json.tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(tmp, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, f.lf+"/up.json"); err != nil {
		t.Fatal(err)
	}
}

// watch is the daemon's lock watch on f, with launcher.ReadUpLocks (every lock tool on) and the
// mounts given, recording its events and its warnings.
type watchRec struct {
	mu     sync.Mutex
	events []LockEvent
	warns  []string
}

func (r *watchRec) snapshot() ([]LockEvent, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]LockEvent(nil), r.events...), append([]string(nil), r.warns...)
}

func runWatch(t *testing.T, d *Daemon, f lockFamily, mounts map[string][]string) *watchRec {
	t.Helper()
	rec := &watchRec{}
	d.SetLockWatch(LockWatch{
		Dir: f.lf,
		Read: func(logf func(string, ...any)) []UpLock {
			var out []UpLock
			for _, l := range launcher.ReadUpLocks(f.proj, logf, func(string) bool { return true }) {
				out = append(out, UpLock{Instance: l.Instance, Volume: l.Volume})
			}
			return out
		},
		Mounts: func(_ context.Context, container string) ([]string, error) {
			m, ok := mounts[container]
			if !ok {
				return nil, fmt.Errorf("no container %s", container)
			}
			return m, nil
		},
	})
	d.mu.Lock()
	d.lockDebounce = 20 * time.Millisecond
	d.afterLockEvent = func(e LockEvent) { rec.mu.Lock(); rec.events = append(rec.events, e); rec.mu.Unlock() }
	d.lockWarnf = func(format string, a ...any) {
		rec.mu.Lock()
		rec.warns = append(rec.warns, fmt.Sprintf(format, a...))
		rec.mu.Unlock()
	}
	d.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan struct{})
	go func() { defer close(done); _ = d.runLockWatch(ctx, ready) }()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("the lock watch did not start")
	}
	return rec
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out: %s", what)
}

func TestDaemonSeesNewLock(t *testing.T) {
	e := start(t)
	f := newLockFamily(t)
	f.write(t, 0o600, "shop")
	rec := runWatch(t, e.d, f, nil)

	// A lock lox up runs from now on: one event, named.
	f.write(t, 0o600, "shop", "orders")
	eventually(t, "lock-added orders", func() bool {
		ev, _ := rec.snapshot()
		return len(ev) == 1
	})
	if ev, w := rec.snapshot(); ev[0] != (LockEvent{Kind: "lock-added", Name: "orders"}) || len(w) != 0 {
		t.Fatalf("events %v, warnings %q", ev, w)
	}

	// A rewrite lea cannot trust (writable by the group): no event, one warning.
	f.write(t, 0o620, "shop", "orders", "inventory")
	eventually(t, "a warning", func() bool {
		_, w := rec.snapshot()
		return len(w) > 0
	})
	time.Sleep(150 * time.Millisecond) // any later event of the same rewrite has come
	ev, w := rec.snapshot()
	if len(ev) != 1 || len(w) != 1 || !slices.ContainsFunc(w, func(s string) bool { return strings.Contains(s, "writable by others than the user") }) {
		t.Fatalf("after the untrusted rewrite: events %v, warnings %q", ev, w)
	}
	// Nothing lea cannot trust reaches the agents' hints either.
	if got := e.d.upLockNames(); len(got) != 0 {
		t.Fatalf("locks from an untrusted up.json: %v", got)
	}

	// Trusted again: the lock new since the last trusted state is the only event.
	f.write(t, 0o600, "shop", "orders", "inventory")
	eventually(t, "lock-added inventory", func() bool {
		ev, _ := rec.snapshot()
		return len(ev) == 2
	})
	if ev, _ := rec.snapshot(); ev[1] != (LockEvent{Kind: "lock-added", Name: "inventory"}) {
		t.Fatalf("events %v", ev)
	}
}

// TestDeckRestartHint: the containers whose real mounts lack a lock now in up.json get its name
// (the deck's and the status line's hint); one that already mounts it gets nothing.
func TestDeckRestartHint(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	f := newLockFamily(t)
	f.write(t, 0o600, "shop")
	rec := runWatch(t, e.d, f, map[string][]string{
		"lea-a": {f.project + "_shop-run"},
		"lea-b": {f.project + "_shop-run", f.project + "_orders-run"},
	})
	f.write(t, 0o600, "shop", "orders")
	eventually(t, "lock-added orders", func() bool {
		ev, _ := rec.snapshot()
		return len(ev) == 1
	})
	watch := func(c string) []string {
		var snap agents.Snapshot
		if err := call(t, e.api, "", "agents_watch", map[string]any{"container": c, "since": 0, "timeout_s": 1}, &snap); err != nil {
			t.Fatal(err)
		}
		return snap.LocksAdded
	}
	if got := watch("lea-a"); !slices.Equal(got, []string{"orders"}) {
		t.Fatalf("lea-a lacks orders: hint %v", got)
	}
	if got := watch("lea-b"); len(got) != 0 {
		t.Fatalf("lea-b mounts orders: hint %v", got)
	}
	// The status line of an agent in lea-a hears it too.
	var ans struct {
		LocksAdded []string `json:"locks_added"`
	}
	if err := call(t, e.bus, "dev", "status_report", map[string]any{"ctx_pct": 1, "cost_usd": 0, "paths": []string{}}, &ans); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ans.LocksAdded, []string{"orders"}) {
		t.Fatalf("status_report answer %v", ans.LocksAdded)
	}
}

// A watch error (an overflow drops events): each distinct error is said once.
func TestLockWatchErrorSaidOnce(t *testing.T) {
	e := start(t)
	rec := &watchRec{}
	e.d.lockWarnf = func(format string, a ...any) {
		rec.mu.Lock()
		rec.warns = append(rec.warns, fmt.Sprintf(format, a...))
		rec.mu.Unlock()
	}
	for _, err := range []error{fsnotify.ErrEventOverflow, fsnotify.ErrEventOverflow, os.ErrClosed} {
		e.d.lockWatchError("/p/.lockfamily", err)
	}
	if _, w := rec.snapshot(); len(w) != 2 {
		t.Fatalf("warnings %q", w)
	}
}

// A container whose mounts cannot be read is asked again only after mountsTTL (the status line's
// budget is short), and expired entries go when the cache is written.
func TestLockMountsCache(t *testing.T) {
	e := start(t)
	calls := 0
	e.d.lockWatch.Mounts = func(context.Context, string) ([]string, error) { calls++; return nil, fmt.Errorf("no such container") }
	e.d.upLocks = []UpLock{{Instance: "orders", Volume: "v_orders-run"}}
	e.d.mountsCache = map[string]mountsEntry{"lea-old": {at: time.Now().Add(-2 * mountsTTL)}}
	for range 3 {
		if got := e.d.locksAdded(context.Background(), "lea-a"); got != nil {
			t.Fatalf("hint %v from unreadable mounts", got)
		}
	}
	if calls != 1 {
		t.Fatalf("mounts read %d times", calls)
	}
	if _, ok := e.d.mountsCache["lea-old"]; ok {
		t.Fatal("an expired entry was kept")
	}
}
