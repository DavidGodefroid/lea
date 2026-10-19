package hook

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
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

// A superseded rewake job ends quietly: exit 0 wakes nobody.
func TestASupersededRewakeExitsZero(t *testing.T) {
	defer func(g time.Duration) { rewakeGrace = g }(rewakeGrace)
	rewakeGrace = 0
	_, bus := start(t)
	runHook(bus, "SessionStart", `{"session_id":"s1"}`, nil)
	runHook(bus, "Stop", `{"session_id":"s1"}`, nil)
	done := make(chan int, 1)
	go func() { c, _ := runRewake(bus); done <- c }()
	time.Sleep(150 * time.Millisecond)
	go runRewake(bus)
	select {
	case c := <-done:
		if c != 0 {
			t.Fatalf("code=%d", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the old rewake hook was not superseded")
	}
}

func TestRewakeIsSilentWhenBusMissing(t *testing.T) {
	defer func(g time.Duration) { rewakeGrace = g }(rewakeGrace)
	rewakeGrace = 0
	if code, out := runRewake("/nonexistent/bus.sock"); code != 0 || out != "" {
		t.Fatalf("code=%d out=%q", code, out)
	}
}
