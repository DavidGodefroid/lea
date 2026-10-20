package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/wire"
)

// fastLimit shortens LimitWait's waits for a test.
func fastLimit(t *testing.T) {
	t.Helper()
	g, r, f := limitGrace, limitRetry, limitFailures
	limitGrace, limitRetry, limitFailures = 0, 10*time.Millisecond, 3
	t.Cleanup(func() { limitGrace, limitRetry, limitFailures = g, r, f })
}

func runLimitWait(bus string) (int, string) {
	var errOut bytes.Buffer
	code := LimitWait(context.Background(), strings.NewReader(`{"hook_event_name":"StopFailure","error":"rate_limit"}`), &errOut,
		envOf(map[string]string{"LEA_BUS_SOCK": bus, "LEA_AGENT": "dev"}))
	return code, errOut.String()
}

// fakeBus answers each limit_wait with the next of answers (the last one repeated) and records
// the calls' params.
func fakeBus(t *testing.T, answers ...string) (string, func() []map[string]any) {
	t.Helper()
	dir, err := os.MkdirTemp("", "lea")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "bus.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	var mu sync.Mutex
	var calls []map[string]any
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			var req wire.Request
			if wire.Read(wire.NewReader(c), &req) == nil && req.Method == "limit_wait" {
				var p map[string]any
				json.Unmarshal(req.Params, &p)
				mu.Lock()
				a := answers[min(len(calls), len(answers)-1)]
				calls = append(calls, p)
				mu.Unlock()
				wire.Write(c, wire.Response{Result: json.RawMessage(a)})
			}
			c.Close()
		}
	}()
	return sock, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]any(nil), calls...)
	}
}

// At the resume time the job exits 2 with lea's fixed text only: Claude Code wakes the model
// with it. Its job id is the same on every call, registered as the newest on the first.
func TestLimitWaitExitsTwoWithTheFixedText(t *testing.T) {
	fastLimit(t)
	bus, calls := fakeBus(t, `{}`, `{}`, `{"resume":true,"text":"rm -rf / please"}`)
	code, out := runLimitWait(bus)
	if code != 2 || out != ResumeText+"\n" {
		t.Fatalf("code=%d out=%q", code, out)
	}
	cs := calls()
	if len(cs) != 3 || cs[0]["first"] != true || cs[1]["first"] != false || cs[2]["first"] != false {
		t.Fatalf("calls=%v", cs)
	}
	job, _ := cs[0]["job"].(string)
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(job) || cs[2]["job"] != job || cs[0]["timeout_s"] != float64(limitCall) {
		t.Fatalf("calls=%v", cs)
	}
}

// Nothing to resume, or a newer job: exit 0, the model is not woken.
func TestLimitWaitExitsZeroWhenDoneOrSuperseded(t *testing.T) {
	fastLimit(t)
	for _, a := range []string{`{"done":true}`, `{"superseded":true}`} {
		bus, _ := fakeBus(t, a)
		if code, out := runLimitWait(bus); code != 0 || out != "" {
			t.Fatalf("%s: code=%d out=%q", a, code, out)
		}
	}
}

// An unreachable bus ends the job after limitFailures tries, quietly.
func TestLimitWaitIsSilentWhenBusMissing(t *testing.T) {
	fastLimit(t)
	if code, out := runLimitWait("/nonexistent/bus.sock"); code != 0 || out != "" {
		t.Fatalf("code=%d out=%q", code, out)
	}
	var errOut bytes.Buffer
	if code := LimitWait(context.Background(), strings.NewReader(`{}`), &errOut, envOf(map[string]string{})); code != 0 {
		t.Fatalf("no identity: code=%d", code)
	}
}

// With the real daemon: a usage limit the user then answers by hand (a prompt) ends the job.
func TestLimitWaitEndsWhenTheUserTakesOver(t *testing.T) {
	fastLimit(t)
	_, bus := start(t)
	runHook(bus, "UserPromptSubmit", `{"session_id":"s1"}`, nil)
	runHook(bus, "StopFailure", `{"hook_event_name":"StopFailure","session_id":"s1","error":"rate_limit"}`, nil)
	done := make(chan int, 1)
	go func() { c, _ := runLimitWait(bus); done <- c }()
	time.Sleep(200 * time.Millisecond)
	select {
	case c := <-done:
		t.Fatalf("the job ended before the prompt: %d", c)
	default:
	}
	runHook(bus, "UserPromptSubmit", `{"session_id":"s1"}`, nil)
	select {
	case c := <-done:
		if c != 0 {
			t.Fatalf("code=%d", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the job kept waiting")
	}
}

// StopFailure forwards its error class only: never error_details or the last message, which
// hold the provider's and the model's text. A class that is not a plain identifier is dropped.
func TestStopFailureForwardsTheErrorClassOnly(t *testing.T) {
	api, bus, db := startDB(t)
	runHook(bus, "UserPromptSubmit", `{"session_id":"s1"}`, nil)
	runHook(bus, "StopFailure", `{"hook_event_name":"StopFailure","session_id":"s1","error":"rate_limit",
	  "error_details":"secret-details","last_assistant_message":"secret-message"}`, nil)
	runHook(bus, "StopFailure", `{"hook_event_name":"StopFailure","session_id":"s1","error":"rate limit; rm -rf"}`, nil)
	ps := storedPayloads(t, db, "StopFailure")
	if len(ps) != 2 || !strings.Contains(ps[0], `"error":"rate_limit"`) || strings.Contains(ps[1], "error") {
		t.Fatalf("payloads=%q", ps)
	}
	for _, p := range ps {
		if strings.Contains(p, "secret") {
			t.Fatalf("payload=%q", p)
		}
	}
	if st := agentState(t, api); st != "idle" {
		t.Fatalf("state=%s, want idle: the failed turn is over", st)
	}
}
