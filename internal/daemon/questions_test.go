package daemon

import (
	"strings"
	"testing"
	"time"
)

type userAskResult struct {
	State    string `json:"state"`
	ID       int64  `json:"id"`
	Position int    `json:"position"`
	Text     string `json:"text"`
}

func userAsk(t *testing.T, e env, agent string, p map[string]any) userAskResult {
	t.Helper()
	var r userAskResult
	if err := call(t, e.bus, agent, "user_ask", p, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// prompt is the user's prompt to agent: the hook's UserPromptSubmit and its context_unread.
func prompt(t *testing.T, e env, agent string, lea bool) string {
	t.Helper()
	call(t, e.bus, agent, "hook_event", map[string]any{"event": "UserPromptSubmit", "session_id": "s"}, nil)
	var r struct {
		Text string `json:"text"`
	}
	if err := call(t, e.bus, agent, "context_unread", map[string]any{"session_id": "s", "prompt": true, "lea": lea}, &r); err != nil {
		t.Fatal(err)
	}
	return r.Text
}

func turnEnds(t *testing.T, e env, agent string) {
	t.Helper()
	if err := call(t, e.bus, agent, "hook_event", map[string]any{"event": "Stop", "session_id": "s"}, nil); err != nil {
		t.Fatal(err)
	}
}

func askVerdict(t *testing.T, e env, agent string) (string, string) {
	t.Helper()
	var r struct{ Verdict, Reason string }
	if err := call(t, e.bus, agent, "grant_check", map[string]any{"tool": "AskUserQuestion", "input": map[string]any{}, "cwd": "/p"}, &r); err != nil {
		t.Fatal(err)
	}
	return r.Verdict, r.Reason
}

// Three leads' questions reach the user one at a time, by priority then in order, and each
// answer names the agent it goes back to (#95).
func TestUserAskOneQuestionAtATime(t *testing.T) {
	e := start(t)
	tw := spawned(t, e, "tower", "lead", "")
	for _, n := range []string{"l1", "l2", "l3"} {
		spawned(t, e, n, "dev", tw)
	}
	q1 := userAsk(t, e, "tower", map[string]any{"question": "deploy?", "from": "l1"})
	if q1.State != "ask_now" || !strings.Contains(q1.Text, "ask it to the user now") {
		t.Fatalf("first = %+v", q1)
	}
	q2 := userAsk(t, e, "tower", map[string]any{"question": "merge?", "from": "l2"})
	q3 := userAsk(t, e, "tower", map[string]any{"question": "prod down", "from": "l3", "priority": "incident"})
	if q2.State != "queued" || q2.Position != 1 || q3.State != "queued" || q3.Position != 1 {
		t.Fatalf("queued = %+v %+v", q2, q3)
	}
	if !strings.Contains(q2.Text, "Do not mention") {
		t.Fatalf("queued text = %q", q2.Text)
	}
	// The tower shows q1: AskUserQuestion goes through once, then no other is allowed.
	if v, why := askVerdict(t, e, "tower"); v == "deny" {
		t.Fatalf("the open question was denied: %s", why)
	}
	if v, why := askVerdict(t, e, "tower"); v != "deny" || !strings.Contains(why, "already open (#") {
		t.Fatalf("a second question = %s %q", v, why)
	}
	turnEnds(t, e, "tower")
	// The incident went to the front, but did not interrupt q1: the user's answer opens it.
	text := prompt(t, e, "tower", false)
	for _, want := range []string{"question #1 is answered", "Relay the answer to lea-a/l1", "#3 is now open (incident from lea-a/l3)", `"prod down"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("after the first answer, %q lacks %q", text, want)
		}
	}
	turnEnds(t, e, "tower")
	text = prompt(t, e, "tower", false)
	if !strings.Contains(text, "lea-a/l3") || !strings.Contains(text, "#2 is now open (normal from lea-a/l2)") {
		t.Fatalf("after the second answer: %q", text)
	}
	turnEnds(t, e, "tower")
	if text = prompt(t, e, "tower", false); !strings.Contains(text, "No other question waits") {
		t.Fatalf("after the last answer: %q", text)
	}
	// With nothing open, a tower that uses the queue asks through user_ask first.
	if v, why := askVerdict(t, e, "tower"); v != "deny" || !strings.Contains(why, "call user_ask first") {
		t.Fatalf("no open question = %s %q", v, why)
	}
	if text = prompt(t, e, "tower", false); text != "" {
		t.Fatalf("a prompt with no question open: %q", text)
	}
}

// A lead that never used user_ask asks the user as before; only leads have user_ask.
func TestUserAskLeadsOnlyAndOptIn(t *testing.T) {
	e := start(t)
	spawned(t, e, "boss", "lead", "")
	spawned(t, e, "dev", "dev", "")
	if v, why := askVerdict(t, e, "boss"); v == "deny" {
		t.Fatalf("a lead not using user_ask was denied: %s", why)
	}
	if err := call(t, e.bus, "dev", "user_ask", map[string]any{"question": "x"}, nil); err == nil || !strings.Contains(err.Error(), "leads only") {
		t.Fatalf("dev user_ask: %v", err)
	}
	for _, p := range []map[string]any{
		{"question": ""},
		{"question": "x", "priority": "urgent"},
		{"question": strings.Repeat("x", maxQuestion+1)},
		{"question": "x", "from": "nobody"},
		{"question": "x", "message": 999},
		{"id": 1, "question": "x"},
		{"id": 42},
	} {
		if err := call(t, e.bus, "boss", "user_ask", p, nil); err == nil {
			t.Fatalf("user_ask %v accepted", p)
		}
	}
	if err := call(t, e.api, "", "user_ask", map[string]any{"question": "x"}, nil); err == nil {
		t.Fatal("user_ask from the host accepted")
	}
}

// The message the question answers: the answer goes back with msg_reply on it.
func TestUserAskMessageRoutesTheAnswer(t *testing.T) {
	e := start(t)
	tw := spawned(t, e, "tower", "lead", "")
	spawned(t, e, "l1", "dev", tw)
	var sent struct{ ID int64 }
	if err := call(t, e.bus, "l1", "msg_send", map[string]any{"to": "tower", "body": "may I deploy?", "kind": "request"}, &sent); err != nil {
		t.Fatal(err)
	}
	userAsk(t, e, "tower", map[string]any{"question": "deploy?", "message": sent.ID})
	turnEnds(t, e, "tower")
	if text := prompt(t, e, "tower", false); !strings.Contains(text, "Relay the answer to lea-a/l1 with msg_reply on message") {
		t.Fatalf("answer text: %q", text)
	}
	if err := call(t, e.bus, "tower", "user_ask", map[string]any{"question": "x", "message": sent.ID, "from": "tower"}, nil); err == nil {
		t.Fatal("a message from another agent than from accepted")
	}
}

// A prompt lea typed (a resume, a first prompt) answers nothing.
func TestUserAskLeaPromptsAnswerNothing(t *testing.T) {
	e := start(t)
	spawned(t, e, "tower", "lead", "")
	userAsk(t, e, "tower", map[string]any{"question": "deploy?"})
	turnEnds(t, e, "tower")
	if text := prompt(t, e, "tower", true); text != "" {
		t.Fatalf("lea's first prompt answered: %q", text)
	}
	e.d.mu.Lock()
	e.d.leaPrompts["lea-a/tower"] = true
	e.d.mu.Unlock()
	if text := prompt(t, e, "tower", false); text != "" {
		t.Fatalf("a resume prompt answered: %q", text)
	}
	if text := prompt(t, e, "tower", false); !strings.Contains(text, "question #1 is answered") {
		t.Fatalf("the user's prompt: %q", text)
	}
}

// A prompt that answered something else: the question is asked again before the rest.
func TestUserAskReask(t *testing.T) {
	e := start(t)
	spawned(t, e, "tower", "lead", "")
	userAsk(t, e, "tower", map[string]any{"question": "deploy?"})
	userAsk(t, e, "tower", map[string]any{"question": "merge?"})
	turnEnds(t, e, "tower")
	if text := prompt(t, e, "tower", false); !strings.Contains(text, "#2 is now open") || !strings.Contains(text, "call user_ask with id 1") {
		t.Fatalf("prompt: %q", text)
	}
	// "status?": the tower asks #1 again; #2, not shown yet, waits at the front.
	if r := userAsk(t, e, "tower", map[string]any{"id": 1}); r.State != "ask_now" || r.ID != 1 {
		t.Fatalf("reask = %+v", r)
	}
	turnEnds(t, e, "tower")
	if text := prompt(t, e, "tower", false); !strings.Contains(text, "question #1 is answered") || !strings.Contains(text, "#2 is now open") {
		t.Fatalf("after the reask: %q", text)
	}
}

// While the user has a question, the questions agents ask the tower wait for the next prompt;
// results, notes and replies still come.
func TestUserAskHoldsAgentsAsks(t *testing.T) {
	e := start(t)
	tw := spawned(t, e, "tower", "lead", "")
	spawned(t, e, "l1", "dev", tw)
	userAsk(t, e, "tower", map[string]any{"question": "deploy?"})
	turnEnds(t, e, "tower")
	go call(t, e.bus, "l1", "ask", map[string]any{"to": "tower", "question": "which branch?", "timeout_s": 2}, nil)
	waitFor(t, func() bool { n, _ := e.d.st.UnreadCount("lea-a/tower"); return n == 1 })
	if err := call(t, e.bus, "l1", "msg_send", map[string]any{"to": "tower", "body": "done", "kind": "result"}, nil); err != nil {
		t.Fatal(err)
	}
	var ms []delivered
	if err := call(t, e.bus, "tower", "msg_inbox", nil, &ms); err != nil || len(ms) != 1 || ms[0].Kind != "result" {
		t.Fatalf("inbox while a question is open = %+v %v", ms, err)
	}
	// The user answers: the held question comes.
	text := prompt(t, e, "tower", false)
	if !strings.Contains(text, "1 unread message(s)") {
		t.Fatalf("after the answer: %q", text)
	}
	if err := call(t, e.bus, "tower", "msg_inbox", nil, &ms); err != nil || len(ms) != 1 || ms[0].Kind != "question" {
		t.Fatalf("held question = %+v %v", ms, err)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met")
}

// A question left unanswered: lea reminds the tower once, with the same text.
func TestUserAskReminder(t *testing.T) {
	e := start(t)
	spawned(t, e, "tower", "lead", "")
	e.d.SetQuestionRemind(2 * time.Hour)
	userAsk(t, e, "tower", map[string]any{"question": "deploy?"})
	turnEnds(t, e, "tower")
	e.clock.Add(7199)
	e.d.remindQuestions()
	if n, _ := e.d.st.UnreadCount("lea-a/tower"); n != 0 {
		t.Fatalf("reminded early: %d", n)
	}
	e.clock.Add(1)
	e.d.remindQuestions()
	e.d.remindQuestions()
	var ms []delivered
	if err := call(t, e.bus, "tower", "msg_inbox", nil, &ms); err != nil || len(ms) != 1 ||
		!strings.Contains(ms[0].Text, `prefixed with \"reminder: \"`) && !strings.Contains(ms[0].Text, `prefixed with "reminder: "`) ||
		!strings.Contains(ms[0].Text, "deploy?") {
		t.Fatalf("reminder = %+v %v", ms, err)
	}
	e.d.SetQuestionRemind(0)
	e.clock.Add(100000)
	e.d.remindQuestions()
	if n, _ := e.d.st.UnreadCount("lea-a/tower"); n != 0 {
		t.Fatalf("reminded twice: %d", n)
	}
}

// lea questions lists the open and queued questions; the user drops a queued one.
func TestQuestionListAndDrop(t *testing.T) {
	e := start(t)
	spawned(t, e, "tower", "lead", "")
	userAsk(t, e, "tower", map[string]any{"question": "deploy?"})
	q2 := userAsk(t, e, "tower", map[string]any{"question": "merge?"})
	var qs []questionView
	if err := call(t, e.api, "", "question_list", nil, &qs); err != nil || len(qs) != 2 || qs[0].State != "open" || qs[1].Body != "merge?" {
		t.Fatalf("list = %+v %v", qs, err)
	}
	if err := call(t, e.bus, "tower", "question_list", nil, nil); err == nil {
		t.Fatal("an agent listed the questions")
	}
	if err := call(t, e.bus, "tower", "question_drop", map[string]any{"id": q2.ID}, nil); err == nil {
		t.Fatal("an agent dropped a question")
	}
	if err := call(t, e.api, "", "question_drop", map[string]any{"id": 1}, nil); err == nil || !strings.Contains(err.Error(), "only a queued") {
		t.Fatalf("drop open: %v", err)
	}
	if err := call(t, e.api, "", "question_drop", map[string]any{"id": q2.ID}, nil); err != nil {
		t.Fatal(err)
	}
	turnEnds(t, e, "tower")
	if text := prompt(t, e, "tower", false); !strings.Contains(text, "No other question waits") {
		t.Fatalf("a dropped question opened: %q", text)
	}
}
