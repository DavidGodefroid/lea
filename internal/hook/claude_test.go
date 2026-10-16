package hook

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/DavidGodefroid/lea/internal/client"
	"github.com/DavidGodefroid/lea/internal/daemon"
	"github.com/DavidGodefroid/lea/internal/store"
	"github.com/DavidGodefroid/lea/internal/wire"
)

func start(t *testing.T) (api, bus string) {
	t.Helper()
	api, bus, _ = startDB(t)
	return
}

// startDB also returns the path of the daemon's database, for assertions on stored events.
func startDB(t *testing.T) (api, bus, db string) {
	t.Helper()
	return startWith(t, nil)
}

// startWith is startDB with setup run on the daemon before it serves.
func startWith(t *testing.T, setup func(*daemon.Daemon)) (api, bus, db string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "lea")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	db = filepath.Join(dir, "lea.db")
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	d := daemon.New(st, filepath.Join(dir, "run"), func() int64 { return time.Now().Unix() })
	if setup != nil {
		setup(d)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	api = filepath.Join(dir, "d.sock")
	l, err := daemon.Listen(api)
	if err != nil {
		t.Fatal(err)
	}
	go d.Serve(ctx, l, daemon.Origin{})
	bus, err = d.AttachContainer(ctx, "lea-a")
	if err != nil {
		t.Fatal(err)
	}
	return
}

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func userSend(t *testing.T, api, to, body string) {
	p, _ := json.Marshal(map[string]any{"to": to, "body": body, "kind": "note"})
	if err := client.Call(context.Background(), api, wire.Request{Method: "msg_send", Params: p}, nil); err != nil {
		t.Fatal(err)
	}
}

func runHook(bus, event, stdin string, extra map[string]string) string {
	env := map[string]string{"LEA_BUS_SOCK": bus, "LEA_AGENT": "dev"}
	for k, v := range extra {
		env[k] = v
	}
	var out bytes.Buffer
	Claude(context.Background(), event, strings.NewReader(stdin), &out, envOf(env))
	return out.String()
}

func TestSilentWhenBusMissing(t *testing.T) {
	began := time.Now()
	out := runHook("/nonexistent/bus.sock", "Stop", `{"session_id":"s"}`, nil)
	if out != "" || time.Since(began) > 2*time.Second {
		t.Fatalf("out=%q after %s", out, time.Since(began))
	}
}

func TestSilentWithoutIdentity(t *testing.T) {
	_, bus := start(t)
	var out bytes.Buffer
	Claude(context.Background(), "Stop", strings.NewReader(`{}`), &out, envOf(map[string]string{"LEA_BUS_SOCK": bus}))
	if out.String() != "" {
		t.Fatalf("out=%q", out.String())
	}
}

func TestStopBlocksWithMessages(t *testing.T) {
	api, bus := start(t)
	runHook(bus, "SessionStart", `{"session_id":"s1"}`, nil) // registers dev
	if out := runHook(bus, "Stop", `{"session_id":"s1","stop_hook_active":false}`, nil); out != "" {
		t.Fatalf("blocked with empty inbox: %q", out)
	}
	userSend(t, api, "dev", "please rebase")
	out := runHook(bus, "Stop", `{"session_id":"s1","stop_hook_active":false}`, nil)
	var r struct{ Decision, Reason string }
	if err := json.Unmarshal([]byte(out), &r); err != nil || r.Decision != "block" || !strings.Contains(r.Reason, "please rebase") {
		t.Fatalf("out=%q", out)
	}
}

func TestPromptSubmitAddsContext(t *testing.T) {
	api, bus := start(t)
	runHook(bus, "SessionStart", `{"session_id":"s1"}`, nil)
	userSend(t, api, "dev", "status?")
	out := runHook(bus, "UserPromptSubmit", `{"session_id":"s1","prompt":"go"}`, nil)
	var r struct {
		HookSpecificOutput struct{ HookEventName, AdditionalContext string }
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil || r.HookSpecificOutput.HookEventName != "UserPromptSubmit" ||
		!strings.Contains(r.HookSpecificOutput.AdditionalContext, "msg_inbox") {
		t.Fatalf("out=%q", out)
	}
}

func TestPermissionPassThroughByDefault(t *testing.T) {
	_, bus := start(t)
	if out := runHook(bus, "PermissionRequest", `{"tool_name":"Bash","tool_input":{"command":"ls"}}`, nil); out != "" {
		t.Fatalf("out=%q", out)
	}
}

// decide answers the first pending approval with verdict; goroutine errors are reported on the
// returned channel (nil when decided).
func decide(api, verdict string) <-chan error {
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 100; i++ {
			time.Sleep(20 * time.Millisecond)
			var pend []struct{ ID int64 }
			if err := client.Call(context.Background(), api, wire.Request{Method: "approval_list"}, &pend); err != nil {
				done <- err
				return
			}
			if len(pend) == 1 {
				p, _ := json.Marshal(map[string]any{"id": pend[0].ID, "verdict": verdict})
				done <- client.Call(context.Background(), api, wire.Request{Method: "approval_decide", Params: p}, nil)
				return
			}
		}
		done <- errors.New("no pending approval appeared")
	}()
	return done
}

var deck = map[string]string{"LEA_APPROVALS": "deck", "LEA_APPROVAL_TIMEOUT": "5"}

func TestPermissionDeckAllow(t *testing.T) {
	api, bus := start(t)
	done := decide(api, "allow")
	out := runHook(bus, "PermissionRequest", `{"tool_name":"Bash","tool_input":{"command":"make test"}}`, deck)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"behavior":"allow"`) || !strings.Contains(out, `"hookEventName":"PermissionRequest"`) {
		t.Fatalf("out=%q", out)
	}
}

func TestPermissionDeckDeny(t *testing.T) {
	api, bus := start(t)
	done := decide(api, "deny")
	out := runHook(bus, "PermissionRequest", `{"tool_name":"Bash","tool_input":{"command":"rm x"}}`, deck)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": "PermissionRequest",
		"decision":      map[string]any{"behavior": "deny", "message": "Denied from lea"}}}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestPermissionTimeoutIsSilent(t *testing.T) {
	_, bus := start(t)
	out := runHook(bus, "PermissionRequest", `{"tool_name":"Bash","tool_input":{}}`,
		map[string]string{"LEA_APPROVALS": "deck", "LEA_APPROVAL_TIMEOUT": "1"})
	if out != "" {
		t.Fatalf("out=%q", out)
	}
}

func TestMalformedInputIsSilent(t *testing.T) {
	_, bus := start(t)
	events := []string{"SessionStart", "Stop", "UserPromptSubmit", "PostToolUse", "PermissionRequest", "Unknown"}
	for _, ev := range events {
		for _, in := range []string{"", "not json", "{", "null", "[1,2]"} {
			if out := runHook(bus, ev, in, map[string]string{"LEA_APPROVALS": "deck", "LEA_APPROVAL_TIMEOUT": "1"}); out != "" {
				t.Errorf("%s %q: out=%q", ev, in, out)
			}
		}
	}
}

func TestPostToolUseAddsContext(t *testing.T) {
	api, bus := start(t)
	runHook(bus, "SessionStart", `{"session_id":"s1"}`, nil)
	userSend(t, api, "dev", "ping")
	out := runHook(bus, "PostToolUse", `{"session_id":"s1","tool_name":"Bash"}`, nil)
	var r struct {
		HookSpecificOutput struct{ HookEventName, AdditionalContext string }
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil || r.HookSpecificOutput.HookEventName != "PostToolUse" ||
		!strings.Contains(r.HookSpecificOutput.AdditionalContext, "msg_inbox") {
		t.Fatalf("out=%q", out)
	}
}

func TestUnknownEventIsSilent(t *testing.T) {
	_, bus := start(t)
	if out := runHook(bus, "Bogus", `{"session_id":"s1"}`, nil); out != "" {
		t.Fatalf("out=%q", out)
	}
}

func storedPayloads(t *testing.T, db, kind string) []string {
	t.Helper()
	h, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	rows, err := h.Query(`SELECT payload FROM events WHERE kind=? ORDER BY seq`, kind)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		rows.Scan(&p)
		out = append(out, p)
	}
	return out
}

func agentState(t *testing.T, api string) string {
	t.Helper()
	var as []struct{ State string }
	if err := client.Call(context.Background(), api, wire.Request{Method: "agents_list"}, &as); err != nil || len(as) != 1 {
		t.Fatalf("agents=%v err=%v", as, err)
	}
	return as[0].State
}

func TestPostToolUseForwardsOnlyNeededFields(t *testing.T) {
	api, bus, db := startDB(t)
	runHook(bus, "SessionStart", `{"session_id":"s1"}`, nil)
	in, _ := json.Marshal(map[string]any{"hook_event_name": "PostToolUse", "session_id": "s1", "tool_name": "Read",
		"tool_input": map[string]any{"file_path": "/secret"}, "tool_response": strings.Repeat("s", 900<<10)})
	runHook(bus, "PostToolUse", string(in), nil)
	if st := agentState(t, api); st != "working" {
		t.Fatalf("state=%s", st)
	}
	ps := storedPayloads(t, db, "PostToolUse")
	if len(ps) != 1 {
		t.Fatalf("payloads=%d", len(ps))
	}
	var got map[string]any
	json.Unmarshal([]byte(ps[0]), &got)
	want := map[string]any{"hook_event_name": "PostToolUse", "session_id": "s1", "tool_name": "Read"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stored %.300s", ps[0])
	}
}

func TestNotificationMessageTruncated(t *testing.T) {
	_, bus, db := startDB(t)
	in, _ := json.Marshal(map[string]any{"hook_event_name": "Notification", "session_id": "s1",
		"message": strings.Repeat("é", 2000)})
	runHook(bus, "Notification", string(in), nil)
	ps := storedPayloads(t, db, "Notification")
	if len(ps) != 1 {
		t.Fatalf("payloads=%d", len(ps))
	}
	var got struct{ Message string }
	json.Unmarshal([]byte(ps[0]), &got)
	if len(got.Message) > 1024 || len(got.Message) < 1000 || !utf8.ValidString(got.Message) {
		t.Fatalf("message len=%d valid=%v", len(got.Message), utf8.ValidString(got.Message))
	}
}

// One hook invocation spends at most one 2 s budget on the daemon, however many calls it makes.
func TestHookSharesOneTimeBudget(t *testing.T) {
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
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				var req wire.Request
				if wire.Read(wire.NewReader(c), &req) != nil {
					return
				}
				if req.Method == "hook_event" { // slow, but within the budget
					time.Sleep(1500 * time.Millisecond)
					wire.Write(c, wire.Response{Result: json.RawMessage(`{}`)})
					return
				}
				time.Sleep(10 * time.Second) // stop_check hangs
			}(c)
		}
	}()
	began := time.Now()
	if out := runHook(sock, "Stop", `{"session_id":"s1"}`, nil); out != "" {
		t.Fatalf("out=%q", out)
	}
	if d := time.Since(began); d > 2700*time.Millisecond {
		t.Fatalf("hook took %s", d)
	}
}

// Claude fires no Stop when the user interrupts a turn (Escape): its idle_prompt notification,
// a minute later, is what tells lea the agent waits for the user.
func TestIdlePromptNotificationEndsATurn(t *testing.T) {
	api, bus, db := startDB(t)
	runHook(bus, "UserPromptSubmit", `{"session_id":"s1"}`, nil)
	runHook(bus, "Notification", `{"hook_event_name":"Notification","session_id":"s1","notification_type":"permission_prompt","message":"Claude needs your permission"}`, nil)
	if st := agentState(t, api); st != "working" {
		t.Fatalf("after a permission notification: state=%s, want working", st)
	}
	runHook(bus, "Notification", `{"hook_event_name":"Notification","session_id":"s1","notification_type":"idle_prompt","message":"Claude is waiting for your input"}`, nil)
	if st := agentState(t, api); st != "idle" {
		t.Fatalf("after idle_prompt: state=%s, want idle", st)
	}
	ps := storedPayloads(t, db, "Notification")
	if len(ps) != 2 || !strings.Contains(ps[1], `"notification_type":"idle_prompt"`) {
		t.Fatalf("payloads=%q", ps)
	}
}

const suggestions = `[{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"make test:*"}],"behavior":"allow","destination":"localSettings"}]`

func TestPermissionDeckAlwaysAppliesClaudesSuggestions(t *testing.T) {
	api, bus := start(t)
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 100; i++ {
			time.Sleep(20 * time.Millisecond)
			var pend []struct {
				ID     int64
				Always bool
			}
			if err := client.Call(context.Background(), api, wire.Request{Method: "approval_list"}, &pend); err != nil {
				done <- err
				return
			}
			if len(pend) == 1 {
				if !pend[0].Always {
					done <- errors.New("the deck was not told that always is offered")
					return
				}
				p, _ := json.Marshal(map[string]any{"id": pend[0].ID, "verdict": "always"})
				done <- client.Call(context.Background(), api, wire.Request{Method: "approval_decide", Params: p}, nil)
				return
			}
		}
		done <- errors.New("no pending approval appeared")
	}()
	out := runHook(bus, "PermissionRequest", `{"tool_name":"Bash","tool_input":{"command":"make test"},"permission_suggestions":`+suggestions+`}`, deck)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var got struct {
		HookSpecificOutput struct {
			HookEventName string `json:"hookEventName"`
			Decision      struct {
				Behavior           string          `json:"behavior"`
				UpdatedPermissions json.RawMessage `json:"updatedPermissions"`
			} `json:"decision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.HookSpecificOutput.Decision.Behavior != "allow" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	var want, have bytes.Buffer
	json.Compact(&want, []byte(suggestions))
	json.Compact(&have, got.HookSpecificOutput.Decision.UpdatedPermissions)
	if want.String() != have.String() {
		t.Fatalf("updatedPermissions=%s, want %s", have.String(), want.String())
	}
}

func TestPermissionWithoutUsableSuggestionsOffersNoAlwaysAndAllowsOnce(t *testing.T) {
	for _, extra := range []string{``, `,"permission_suggestions":[]`, `,"permission_suggestions":"x"`} {
		api, bus := start(t)
		// The daemon refuses an `always` verdict the request did not offer, so deciding it here
		// proves the hook advertised always=false; the plain allow is what Claude receives.
		refused := make(chan error, 1)
		go func() {
			for i := 0; i < 100; i++ {
				time.Sleep(20 * time.Millisecond)
				var pend []struct {
					ID     int64
					Always bool
				}
				if err := client.Call(context.Background(), api, wire.Request{Method: "approval_list"}, &pend); err != nil {
					refused <- err
					return
				}
				if len(pend) == 1 {
					if pend[0].Always {
						refused <- errors.New("always was offered without usable suggestions")
						return
					}
					p, _ := json.Marshal(map[string]any{"id": pend[0].ID, "verdict": "allow"})
					refused <- client.Call(context.Background(), api, wire.Request{Method: "approval_decide", Params: p}, nil)
					return
				}
			}
			refused <- errors.New("no pending approval appeared")
		}()
		done := refused
		out := runHook(bus, "PermissionRequest", `{"tool_name":"Bash","tool_input":{"command":"ls"}`+extra+`}`, deck)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"hookSpecificOutput": map[string]any{
			"hookEventName": "PermissionRequest", "decision": map[string]any{"behavior": "allow"}}}
		var got map[string]any
		if err := json.Unmarshal([]byte(out), &got); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("suggestions %q: out=%q err=%v", extra, out, err)
		}
	}
}

func TestSuggestionsNeverReachTheEventLog(t *testing.T) {
	_, bus, db := startDB(t)
	runHook(bus, "PermissionRequest", `{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"ls"},"permission_suggestions":`+suggestions+`}`, nil)
	for _, p := range storedPayloads(t, db, "PermissionRequest") {
		if strings.Contains(p, "addRules") || strings.Contains(p, "tool_input") {
			t.Fatalf("stored payload %q", p)
		}
	}
}

// The first prompt goes to the daemon's namer, cut to maxNamePrompt; the event log never gets it.
func TestPromptSubmitSendsThePromptForNamingOnly(t *testing.T) {
	got := make(chan string, 1)
	_, bus, db := startWith(t, func(d *daemon.Daemon) {
		d.SetNamer(func(ctx context.Context, prompt string) (string, error) {
			got <- prompt
			return "", errors.New("test")
		})
	})
	prompt := "secret-plan " + strings.Repeat("é", maxNamePrompt)
	in, _ := json.Marshal(map[string]string{"session_id": "s1", "prompt": prompt})
	runHook(bus, "UserPromptSubmit", string(in), nil)
	select {
	case p := <-got:
		if len(p) > maxNamePrompt || !utf8.ValidString(p) || !strings.HasPrefix(p, "secret-plan ") {
			t.Fatalf("namer got %d bytes, valid %v", len(p), utf8.ValidString(p))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the namer got nothing")
	}
	for _, p := range storedPayloads(t, db, "hook") {
		if strings.Contains(p, "secret-plan") {
			t.Fatalf("the prompt reached the event log: %s", p)
		}
	}
}
