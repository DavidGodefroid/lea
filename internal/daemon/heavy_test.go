package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/client"
	"github.com/DavidGodefroid/lea/internal/wire"
)

type heavyAnswer struct {
	Granted bool
	ID      int64
	Slot    int
	Slots   int
	Workers int
	Queued  int
	Busy    []string
}

func acquire(t *testing.T, e env, agent, label string, timeoutS int) heavyAnswer {
	t.Helper()
	var r heavyAnswer
	if err := call(t, e.bus, agent, "heavy_acquire", map[string]any{"label": label, "timeout_s": timeoutS}, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// acquireLater asks for a slot in the background; the answer comes on the channel.
func acquireLater(t *testing.T, e env, agent, label string) <-chan heavyAnswer {
	t.Helper()
	ch := make(chan heavyAnswer, 1)
	go func() {
		var r heavyAnswer
		p, _ := json.Marshal(map[string]any{"label": label, "timeout_s": 5})
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		_ = client.Call(ctx, e.bus, wire.Request{Method: "heavy_acquire", Agent: agent, Params: p}, &r)
		ch <- r
	}()
	time.Sleep(100 * time.Millisecond) // queued
	return ch
}

func release(t *testing.T, e env, agent string, id int64) error {
	t.Helper()
	return call(t, e.bus, agent, "heavy_release", map[string]any{"id": id}, nil)
}

// Heavy jobs (#132): N slots per container, the daemon's; the (N+1)-th job waits, in order, until
// one is released. Each grant names its slot and the workers a job may run.
func TestHeavySlotsQueueInOrder(t *testing.T) {
	e := start(t)
	e.d.SetHeavy(2, 3)
	a := acquire(t, e, "dev-1", "phpstan", 1)
	b := acquire(t, e, "dev-2", "phpunit", 1)
	if !a.Granted || !b.Granted || a.Slot == b.Slot || a.Slots != 2 || a.Workers != 3 {
		t.Fatalf("a=%+v b=%+v", a, b)
	}
	c := acquireLater(t, e, "dev-3", "infection")
	d := acquireLater(t, e, "dev-4", "playwright")
	// A third job only waits: a short ask says who holds the slots and how many wait.
	busy := acquire(t, e, "dev-5", "make", 0)
	if busy.Granted || busy.Queued != 3 || strings.Join(busy.Busy, ", ") != "dev-1, dev-2" {
		t.Fatalf("busy=%+v", busy)
	}
	if err := release(t, e, "dev-1", a.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-c:
		if !r.Granted || r.Slot != a.Slot {
			t.Fatalf("dev-3 got %+v", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("dev-3 never got the slot dev-1 released")
	}
	select {
	case r := <-d:
		t.Fatalf("dev-4 got a slot out of turn: %+v", r)
	case <-time.After(200 * time.Millisecond):
	}
	if err := release(t, e, "dev-2", b.ID); err != nil {
		t.Fatal(err)
	}
	if r := <-d; !r.Granted {
		t.Fatalf("dev-4 got %+v", r)
	}
}

// Only the holder releases its slot; a release of a slot already free is not an error.
func TestHeavyReleaseIsTheHoldersOnly(t *testing.T) {
	e := start(t)
	e.d.SetHeavy(1, 4)
	a := acquire(t, e, "dev-1", "phpstan", 1)
	if err := release(t, e, "dev-2", a.ID); err == nil {
		t.Fatal("another agent released dev-1's slot")
	}
	if err := release(t, e, "dev-1", a.ID); err != nil {
		t.Fatal(err)
	}
	if err := release(t, e, "dev-1", a.ID); err != nil {
		t.Fatalf("a second release: %v", err)
	}
	if r := acquire(t, e, "dev-2", "x", 1); !r.Granted {
		t.Fatalf("the slot is not free: %+v", r)
	}
}

// A holder that stops renewing (killed, its container gone) loses its slot after the TTL; a
// renewed one keeps it.
func TestHeavySlotsExpireWithoutRenewal(t *testing.T) {
	e := start(t)
	e.d.SetHeavy(1, 4)
	e.d.heavyTTL = 300 * time.Millisecond
	a := acquire(t, e, "dev-1", "phpstan", 1)
	for i := 0; i < 4; i++ {
		time.Sleep(150 * time.Millisecond)
		if err := call(t, e.bus, "dev-1", "heavy_renew", map[string]any{"id": a.ID}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if r := acquire(t, e, "dev-2", "x", 1); r.Granted {
		t.Fatal("a renewed slot was taken")
	}
	w := acquireLater(t, e, "dev-2", "x")
	select {
	case r := <-w:
		if !r.Granted {
			t.Fatalf("dev-2 got %+v", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("an expired slot was never given to the next job")
	}
	if err := call(t, e.bus, "dev-1", "heavy_renew", map[string]any{"id": a.ID}, nil); err == nil {
		t.Fatal("an expired slot was renewed")
	}
}

// A job that gives up waiting (its client is gone) leaves the queue; a grant that nobody reads
// goes back.
func TestHeavyWaiterThatLeavesIsDropped(t *testing.T) {
	e := start(t)
	e.d.SetHeavy(1, 4)
	a := acquire(t, e, "dev-1", "phpstan", 1)
	p, _ := json.Marshal(map[string]any{"label": "gone", "timeout_s": 30})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	_ = client.Call(ctx, e.bus, wire.Request{Method: "heavy_acquire", Agent: "dev-2", Params: p}, nil)
	cancel()
	time.Sleep(100 * time.Millisecond)
	if err := release(t, e, "dev-1", a.ID); err != nil {
		t.Fatal(err)
	}
	if r := acquire(t, e, "dev-3", "x", 1); !r.Granted {
		t.Fatalf("the slot went to a job that left: %+v", r)
	}
}

// Slots belong to a container; the host is no job.
func TestHeavySlotsArePerContainerAndAgentsOnly(t *testing.T) {
	e := start(t)
	e.d.SetHeavy(1, 4)
	if r := acquire(t, e, "dev-1", "x", 1); !r.Granted {
		t.Fatal(r)
	}
	other, err := e.d.AttachContainer(e.ctx, "lea-b")
	if err != nil {
		t.Fatal(err)
	}
	var r heavyAnswer
	if err := call(t, other, "dev-1", "heavy_acquire", map[string]any{"label": "x", "timeout_s": 1}, &r); err != nil || !r.Granted {
		t.Fatalf("lea-b: %+v %v", r, err)
	}
	if err := call(t, e.api, "", "heavy_acquire", map[string]any{"label": "x", "timeout_s": 1}, nil); err == nil {
		t.Fatal("the host took a slot")
	}
	for _, p := range []map[string]any{{"label": "x", "timeout_s": -1}, {"label": "x", "timeout_s": 601}, {"label": "a\nb", "timeout_s": 1}} {
		if err := call(t, e.bus, "dev-1", "heavy_acquire", p, nil); err == nil {
			t.Fatalf("%v accepted", p)
		}
	}
}

// The board shows the heavy jobs that run and how many wait.
func TestBoardShowsHeavyJobs(t *testing.T) {
	e := start(t)
	e.d.SetHeavy(1, 4)
	acquire(t, e, "dev-1", "phpstan", 1)
	acquireLater(t, e, "dev-2", "phpunit")
	var b struct {
		Heavy []struct {
			Container string
			Slots     int
			Running   []string
			Queued    int
		}
	}
	if err := call(t, e.api, "", "board_list", map[string]any{"container": "lea-a"}, &b); err != nil {
		t.Fatal(err)
	}
	if len(b.Heavy) != 1 || b.Heavy[0].Slots != 1 || strings.Join(b.Heavy[0].Running, ",") != "dev-1 phpstan" || b.Heavy[0].Queued != 1 {
		t.Fatalf("board heavy=%+v", b.Heavy)
	}
}
