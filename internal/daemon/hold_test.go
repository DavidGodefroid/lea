package daemon

import (
	"strings"
	"testing"
	"time"
)

type holdResult struct {
	Agent string `json:"agent"`
	Held  bool   `json:"held"`
	Count int    `json:"count"`
}

func hold(t *testing.T, e env, agent string, held bool) holdResult {
	t.Helper()
	var r holdResult
	if err := call(t, e.api, "", "agent_hold", map[string]any{"agent": agent, "held": held}, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func sends(t *testing.T, e env, from, to, kind, body string, replyTo int64) int64 {
	t.Helper()
	var v struct{ ID int64 }
	if err := call(t, e.bus, from, "msg_send", map[string]any{"to": to, "kind": kind, "body": body, "reply_to": replyTo}, &v); err != nil {
		t.Fatal(err)
	}
	return v.ID
}

func heldInbox(t *testing.T, e env, agent string) []delivered {
	t.Helper()
	var ms []delivered
	if err := call(t, e.bus, agent, "msg_inbox", nil, &ms); err != nil {
		t.Fatal(err)
	}
	return ms
}

// The user's pause of the tower's inbox (#95): the children's messages reach it by no path until
// the user releases it; then the tower hears once of all of them.
func TestHeldInboxDeliversNothingUntilReleased(t *testing.T) {
	e := start(t)
	tw := spawned(t, e, "tower", "lead", "")
	spawned(t, e, "kid", "dev", tw)
	early := sends(t, e, "kid", "tower", "note", "before the pause", 0)
	if r := hold(t, e, "tower", true); !r.Held || r.Agent != tw || r.Count != 1 {
		t.Fatalf("hold %+v", r)
	}
	hookEvent(t, e, "tower", "SessionStart")
	hookEvent(t, e, "tower", "Stop")
	w := rewakeWatch(t, e, "tower")
	for i := 0; i < 3; i++ {
		sends(t, e, "kid", "tower", "note", "spontaneous", 0)
	}
	quiet(t, w, "a held message woke the tower")
	var r struct {
		Count int    `json:"count"`
		Text  string `json:"text"`
	}
	if err := call(t, e.bus, "tower", "context_unread", nil, &r); err != nil || r.Count != 0 {
		t.Fatalf("context_unread %+v %v", r, err)
	}
	var sv stopVerdict
	if err := call(t, e.bus, "tower", "stop_check", nil, &sv); err != nil || sv.Block {
		t.Fatalf("stop_check %+v %v", sv, err)
	}
	ms := heldInbox(t, e, "tower")
	if len(ms) != 1 || ms[0].From != "lea" || !strings.Contains(ms[0].Text, "4 message(s)") {
		t.Fatalf("msg_inbox while held: %+v", ms)
	}
	// An ask to the held tower fails at once; a send succeeds.
	if err := call(t, e.bus, "kid", "ask", map[string]any{"to": "tower", "question": "ok?", "timeout_s": 5}, nil); err == nil ||
		!strings.Contains(err.Error(), "holds its messages") {
		t.Fatalf("ask to a held tower: %v", err)
	}
	// The user's note passes.
	userSends(t, e, "tower", "from the user")
	if n := next(t, w); n.Count != 1 {
		t.Fatalf("the user's note woke %+v", n)
	}
	if ms := heldInbox(t, e, "tower"); len(ms) != 2 || ms[0].From != "user" {
		t.Fatalf("the user's note: %+v", ms)
	}
	hookEvent(t, e, "tower", "Stop")
	w = rewakeWatch(t, e, "tower")
	if r := hold(t, e, "tower", false); r.Held || r.Count != 4 {
		t.Fatalf("release %+v", r)
	}
	if n := next(t, w); n.Count != 5 {
		t.Fatalf("the release woke %+v, want the 4 held and lea's note", n)
	}
	got := map[int64]bool{}
	for {
		ms := heldInbox(t, e, "tower")
		if len(ms) == 0 {
			break
		}
		for _, m := range ms {
			got[m.ID] = true
			if m.From == "lea" && !strings.Contains(m.Text, "4 message(s) were held") {
				t.Fatalf("lea's note: %q", m.Text)
			}
		}
	}
	if len(got) != 5 || !got[early] {
		t.Fatalf("after release: %v", got)
	}
}

// A reply in a thread the tower opened during the pause arrives at once; one in a thread it
// opened before stays held, like a spontaneous message.
func TestHeldInboxLetsRepliesToTheTowersNewThreadsThrough(t *testing.T) {
	e := start(t)
	tw := spawned(t, e, "tower", "lead", "")
	spawned(t, e, "kid", "dev", tw)
	old := sends(t, e, "tower", "kid", "request", "old question", 0)
	e.clock.Add(5)
	hold(t, e, "tower", true)
	q := sends(t, e, "tower", "kid", "request", "new question", 0)
	sends(t, e, "kid", "tower", "result", "old answer", old)
	sends(t, e, "kid", "tower", "note", "spontaneous", 0)
	ans := sends(t, e, "kid", "tower", "result", "new answer", q)
	ms := heldInbox(t, e, "tower")
	if len(ms) != 2 || ms[0].ID != ans || ms[1].From != "lea" || !strings.Contains(ms[1].Text, "2 message(s)") {
		t.Fatalf("msg_inbox: %+v", ms)
	}
}

// lea releases a pause on its own after the hold max; 0 never does.
func TestHoldExpires(t *testing.T) {
	e := start(t)
	tw := spawned(t, e, "tower", "lead", "")
	spawned(t, e, "kid", "dev", tw)
	hold(t, e, "tower", true)
	sends(t, e, "kid", "tower", "note", "x", 0)
	e.d.SetHoldMax(0)
	e.clock.Add(int64(DefaultHoldMax/time.Second) + 1)
	e.d.expireHolds()
	if a, _ := e.d.st.AgentByKey(tw); a.HoldSince == 0 {
		t.Fatal("released with no hold max")
	}
	e.d.SetHoldMax(DefaultHoldMax)
	e.d.expireHolds()
	if a, _ := e.d.st.AgentByKey(tw); a.HoldSince != 0 {
		t.Fatal("not released after the hold max")
	}
	ms := heldInbox(t, e, "tower")
	if len(ms) != 2 || !strings.Contains(ms[1].Text, "lea released it") {
		t.Fatalf("after expiry: %+v", ms)
	}
}

// The tower's status line hears of its questions to the user and of its paused inbox.
func TestStatusReportAnswersTheTowersQuestionsAndHold(t *testing.T) {
	e := start(t)
	tw := spawned(t, e, "tower", "lead", "")
	spawned(t, e, "kid", "dev", tw)
	userAsk(t, e, "tower", map[string]any{"question": "a?"})
	userAsk(t, e, "tower", map[string]any{"question": "b?"})
	hold(t, e, "tower", true)
	sends(t, e, "kid", "tower", "note", "x", 0)
	var ans struct {
		QuestionsOpen   int  `json:"questions_open"`
		QuestionsQueued int  `json:"questions_queued"`
		Held            bool `json:"held"`
		HeldCount       int  `json:"held_count"`
	}
	if err := call(t, e.bus, "tower", "status_report", map[string]any{"ctx_pct": 10, "cost_usd": -1}, &ans); err != nil {
		t.Fatal(err)
	}
	if ans.QuestionsOpen != 1 || ans.QuestionsQueued != 1 || !ans.Held || ans.HeldCount != 1 {
		t.Fatalf("answer %+v", ans)
	}
}

// Only the user pauses an inbox: agent_hold is host only, and with no agent named it is the
// only live lead's.
func TestAgentHoldIsTheUsers(t *testing.T) {
	e := start(t)
	tw := spawned(t, e, "tower", "lead", "")
	spawned(t, e, "kid", "dev", tw)
	for _, a := range []string{"tower", "kid"} {
		if err := call(t, e.bus, a, "agent_hold", map[string]any{"agent": "tower", "held": true}, nil); err == nil {
			t.Fatalf("%s paused the tower's inbox", a)
		}
	}
	if r := hold(t, e, "", true); r.Agent != tw {
		t.Fatalf("no agent: %+v", r)
	}
	spawned(t, e, "lead2", "lead", "")
	if err := call(t, e.api, "", "agent_hold", map[string]any{"held": false}, nil); err == nil {
		t.Fatal("two leads and no agent named")
	}
}
