package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/wire"
)

func runRewake(bus string) (int, string) {
	var errOut bytes.Buffer
	code := Rewake(context.Background(), strings.NewReader(`{"session_id":"s1"}`), &errOut,
		envOf(map[string]string{"LEA_BUS_SOCK": bus, "LEA_AGENT": "dev"}))
	return code, errOut.String()
}

// The Stop hook's asyncRewake job exits 2 with the unread notice once a message reaches the
// idle agent: Claude Code then wakes the model with that text.
func TestRewakeExitsTwoOnAMessage(t *testing.T) {
	defer func(g time.Duration) { rewakeGrace = g }(rewakeGrace)
	rewakeGrace = 0
	api, bus := start(t)
	runHook(bus, "SessionStart", `{"session_id":"s1"}`, nil)
	runHook(bus, "Stop", `{"session_id":"s1"}`, nil)
	type res struct {
		code int
		out  string
	}
	done := make(chan res, 1)
	go func() { c, o := runRewake(bus); done <- res{c, o} }()
	time.Sleep(150 * time.Millisecond)
	userSend(t, api, "dev", "branch ready")
	select {
	case r := <-done:
		if r.code != 2 || !strings.Contains(r.out, "msg_inbox") || strings.Contains(r.out, "branch ready") {
			t.Fatalf("code=%d out=%q", r.code, r.out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the rewake hook never ended")
	}
}

// A superseded rewake job ends quietly: exit 0 wakes nobody. The superseding one also ends
// before the test returns (fastRewake's life): it never lingers to race another test's timings.
func TestASupersededRewakeExitsZero(t *testing.T) {
	fastRewake(t, 2*time.Second)
	_, bus := start(t)
	runHook(bus, "SessionStart", `{"session_id":"s1"}`, nil)
	runHook(bus, "Stop", `{"session_id":"s1"}`, nil)
	done := make(chan int, 2)
	go func() { c, _ := runRewake(bus); done <- c }()
	time.Sleep(150 * time.Millisecond)
	go func() { c, _ := runRewake(bus); done <- c }()
	for range 2 {
		select {
		case c := <-done:
			if c != 0 {
				t.Fatalf("code=%d", c)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a rewake hook never ended")
		}
	}
}

// fastRewake shortens the rewake job's waits for a test, its life to life.
func fastRewake(t *testing.T, life time.Duration) {
	t.Helper()
	g, sg, l, r, m := rewakeGrace, rewakeStartGrace, rewakeLife, rewakeRetry, rewakeRetryMax
	rewakeGrace, rewakeStartGrace, rewakeLife, rewakeRetry, rewakeRetryMax = 0, 0, life, 10*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { rewakeGrace, rewakeStartGrace, rewakeLife, rewakeRetry, rewakeRetryMax = g, sg, l, r, m })
}

// watchBus serves msg_watch on sock, answering each call with the next of answers (the last one
// repeated): a JSON result, or "error: ..." for an error the daemon answers. It counts the calls.
func watchBus(t *testing.T, sock string, answers ...string) (calls func() int) {
	t.Helper()
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	var mu sync.Mutex
	n := 0
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			var req wire.Request
			if wire.Read(wire.NewReader(c), &req) == nil && req.Method == "msg_watch" {
				mu.Lock()
				a := answers[min(n, len(answers)-1)]
				n++
				mu.Unlock()
				if msg, ok := strings.CutPrefix(a, "error: "); ok {
					wire.Write(c, wire.Response{Error: msg})
				} else {
					wire.Write(c, wire.Response{Result: json.RawMessage(a)})
				}
			}
			c.Close()
		}
	}()
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return n
	}
}

// Without a bus for its whole life, the job wakes nobody and says nothing.
func TestRewakeIsSilentWhenBusMissing(t *testing.T) {
	fastRewake(t, 300*time.Millisecond)
	began := time.Now()
	if code, out := runRewake("/nonexistent/bus.sock"); code != 0 || out != "" {
		t.Fatalf("code=%d out=%q", code, out)
	}
	if time.Since(began) > 5*time.Second {
		t.Fatalf("the job outlived its life: %s", time.Since(began))
	}
}

// A daemon that restarts takes the bus away for a while: the job tries again until the bus is
// back, then wakes the agent for its message (#297).
func TestRewakeWaitsForTheBusToComeBack(t *testing.T) {
	fastRewake(t, time.Minute)
	sock := filepath.Join(t.TempDir(), "bus.sock")
	done := make(chan [2]any, 1)
	go func() { c, o := runRewake(sock); done <- [2]any{c, o} }()
	time.Sleep(200 * time.Millisecond) // a few failed tries
	calls := watchBus(t, sock, `{"count":1,"text":"lea: 1 unread message(s) from user."}`)
	select {
	case r := <-done:
		if r[0] != 2 || !strings.Contains(r[1].(string), "unread") || calls() != 1 {
			t.Fatalf("code=%v out=%q calls=%d", r[0], r[1], calls())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the job never came back to the bus")
	}
}

// A watch that ends without a message (an hour went by) is renewed: an agent idle for longer
// than one watch still wakes (#297).
func TestRewakeRenewsItsWatch(t *testing.T) {
	fastRewake(t, time.Minute)
	sock := filepath.Join(t.TempDir(), "bus.sock")
	calls := watchBus(t, sock, `{"count":0,"text":""}`, `{"count":0,"text":""}`, `{"count":2,"text":"lea: 2 unread message(s) from lea-a/lead."}`)
	code, out := runRewake(sock)
	if code != 2 || !strings.Contains(out, "2 unread") || calls() != 3 {
		t.Fatalf("code=%d out=%q calls=%d", code, out, calls())
	}
}

// A watch the daemon refuses (an error it answers, not a missing bus) ends the job quietly.
func TestRewakeEndsWhenTheDaemonRefusesTheWatch(t *testing.T) {
	fastRewake(t, time.Minute)
	sock := filepath.Join(t.TempDir(), "bus.sock")
	calls := watchBus(t, sock, `error: unknown agent`)
	if code, out := runRewake(sock); code != 0 || out != "" || calls() != 1 {
		t.Fatalf("code=%d out=%q calls=%d", code, out, calls())
	}
}

// At a session start the job waits longer before its first watch: a resumed session given a
// prompt submits it first, and that turn reads the inbox.
func TestRewakeWaitsLongerAtASessionStart(t *testing.T) {
	fastRewake(t, time.Minute)
	rewakeStartGrace = 300 * time.Millisecond
	sock := filepath.Join(t.TempDir(), "bus.sock")
	calls := watchBus(t, sock, `{"count":1,"text":"lea: 1 unread message(s) from user."}`)
	var errOut bytes.Buffer
	began := time.Now()
	code := Rewake(context.Background(), strings.NewReader(`{"hook_event_name":"SessionStart","source":"resume"}`), &errOut,
		envOf(map[string]string{"LEA_BUS_SOCK": sock, "LEA_AGENT": "dev"}))
	if code != 2 || calls() != 1 || time.Since(began) < 300*time.Millisecond {
		t.Fatalf("code=%d calls=%d after %s", code, calls(), time.Since(began))
	}
}
