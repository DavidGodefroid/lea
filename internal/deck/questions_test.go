package deck

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func (f *fakeAPI) SetHeld(ctx context.Context, k string, held bool) error {
	f.rec(fmt.Sprintf("held %s %v", k, held))
	return f.err
}

func (f *fakeAPI) Questions(ctx context.Context) ([]Question, error) {
	f.rec("questions")
	return f.questions, f.err
}

func (f *fakeAPI) DropQuestion(ctx context.Context, id int64) error {
	f.rec(fmt.Sprintf("drop %d", id))
	return f.err
}

func held(a Agent, n int) Agent { a.Held, a.HeldCount = true, n; return a }

// run steps m through the command's message, as Bubble Tea does.
func runCmd(t *testing.T, m Model, cmd func() any) Model {
	t.Helper()
	if cmd != nil {
		if msg := cmd(); msg != nil {
			m, _ = step(t, m, msg)
		}
	}
	return m
}

// H holds the selected agent's messages at once, and releases a held agent's; a shell has no
// inbox (#95).
func TestHHoldsAndReleasesTheSelectedAgentsMessages(t *testing.T) {
	api := &fakeAPI{}
	m := loaded(t, api, "lea-a/a", ag("a", "idle", 1), held(ag("b", "idle", 1), 3), Agent{Key: "lea-a/sh-1", Name: "sh-1", Kind: KindShell})
	m, cmd := step(t, m, key("H"))
	if cmd == nil || !strings.Contains(m.status, "holding the messages to a") {
		t.Fatalf("status %q", m.status)
	}
	cmd()
	m.selected = "lea-a/b"
	m, cmd = step(t, m, key("H"))
	if cmd == nil || !strings.Contains(m.status, "released") {
		t.Fatalf("status %q", m.status)
	}
	cmd()
	m.selected = "lea-a/sh-1"
	if _, cmd = step(t, m, key("H")); cmd != nil {
		t.Fatal("H on a shell")
	}
	if got := strings.Join(api.got(), ","); got != "held lea-a/a true,held lea-a/b false" {
		t.Fatalf("calls %q", got)
	}
}

// A held agent's row shows ⏸ and its held messages; its menu offers to release them, another's
// to hold them.
func TestAHeldAgentsRowAndMenu(t *testing.T) {
	api := &fakeAPI{}
	m := loaded(t, api, "lea-a/a", held(ag("a", "idle", 1), 3), ag("b", "idle", 1))
	m.width, m.height = 60, 30
	if v := m.View(); !strings.Contains(v, "⏸ 3") {
		t.Fatalf("no held mark:\n%s", v)
	}
	labels := func(a Agent) string {
		var ls []string
		for _, it := range menuItems(a, false) {
			ls = append(ls, it.label)
		}
		return strings.Join(ls, ",")
	}
	if l := labels(m.agents[0]); !strings.Contains(l, "Release messages (3)") {
		t.Fatalf("held menu %q", l)
	}
	if l := labels(m.agents[1]); !strings.Contains(l, "Hold messages") || strings.Contains(l, "Questions") {
		t.Fatalf("menu %q", l)
	}
	if h := hoverText(m.agents[0]); !strings.Contains(h, "messages held (3)") {
		t.Fatalf("hover %q", h)
	}
	found := false
	for _, l := range helpLines {
		found = found || strings.HasPrefix(l, "p H") || strings.HasPrefix(l, "r Q")
	}
	if !found {
		t.Fatal("H and Q are not in the keys")
	}
}

// Q opens the selected tower's questions: the open one, then the queue, where a digit drops one.
func TestQListsTheTowersQuestionsAndDropsAQueuedOne(t *testing.T) {
	api := &fakeAPI{questions: []Question{
		{ID: 1, Agent: "lea-a/a", State: "open", Priority: "normal", Body: "merge?"},
		{ID: 2, Agent: "lea-a/a", From: "lea-a/kid", State: "queued", Priority: "blocking", Body: "deploy\nnow?"},
		{ID: 3, Agent: "lea-a/other", State: "open", Priority: "normal", Body: "not mine"},
	}}
	tw := ag("a", "idle", 1)
	tw.QuestionsOpen, tw.QuestionsQueued = 1, 1
	m := loaded(t, api, "lea-a/a", tw)
	if l := menuItems(m.agents[0], false); !strings.Contains(fmt.Sprint(l), "Questions…") {
		t.Fatalf("menu %v", l)
	}
	m, cmd := step(t, m, key("Q"))
	m = runCmd(t, m, func() any { return cmd() })
	items := m.openItems()
	if m.menuFor != "lea-a/a" || len(items) != 2 || !strings.Contains(items[0].label, "#1 open: merge?") ||
		items[1].label != "Drop #2 (blocking, from kid): deploy now?" || items[1].key != "1" {
		t.Fatalf("menu for %q: %+v", m.menuFor, items)
	}
	m, cmd = step(t, m, key("1"))
	if cmd == nil || m.menuFor != "" || !strings.Contains(m.status, "dropped question #2") {
		t.Fatalf("drop: menu %q status %q", m.menuFor, m.status)
	}
	cmd()
	if got := strings.Join(api.got(), ","); got != "questions,drop 2" {
		t.Fatalf("calls %q", got)
	}
	// With none for this tower, Q says so and opens nothing.
	api.questions = api.questions[2:]
	m, cmd = step(t, m, key("Q"))
	m = runCmd(t, m, func() any { return cmd() })
	if m.menuFor != "" || !strings.Contains(m.status, "no question of a waits") {
		t.Fatalf("menu %q status %q", m.menuFor, m.status)
	}
}
