package daemon

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// noDeckEnv is a daemon in the no-deck mode (LEA_DECK=0, #135) whose notifications are recorded,
// with a sounder that must never ring, and agent managers that show no deck.
func noDeckEnv(t *testing.T) (env, func() []string, func() []string) {
	t.Helper()
	e, rang := soundEnv(t)
	e.d.SetAgents(&fakeAgents{unattached: true})
	var mu sync.Mutex
	var notes []string
	e.d.SetNoDeck(func(title, body string) {
		mu.Lock()
		notes = append(notes, title+": "+body)
		mu.Unlock()
	})
	return e, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), notes...)
	}, rang
}

func waitNotes(notes func() []string, n int) []string {
	for end := time.Now().Add(5 * time.Second); len(notes()) < n && time.Now().Before(end); {
		time.Sleep(5 * time.Millisecond)
	}
	return notes()
}

// Without the deck, what waits for the user comes as a desktop notification naming how to
// answer it, never as a sound: a request to the user, an answer that needs them, a blocked task.
func TestWithoutTheDeckTheUserIsNotifiedNotRung(t *testing.T) {
	e, notes, rang := noDeckEnv(t)
	if err := call(t, e.bus, "dev", "msg_send", map[string]any{"to": "user", "body": "merge?", "kind": "request"}, nil); err != nil {
		t.Fatal(err)
	}
	got := waitNotes(notes, 1)
	if len(got) != 1 || got[0] != "lea needs you: dev sends you a request: lea inbox" {
		t.Fatalf("notes %q", got)
	}
	// Another event about the same agent at once is the same call for the user: one notification.
	if err := call(t, e.bus, "dev", "msg_send", map[string]any{"to": "user", "body": "and?", "kind": "request"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := notes(); len(got) != 1 {
		t.Fatalf("notes %q", got)
	}
	spawned(t, e, "kid", "dev", "")
	if err := call(t, e.bus, "kid", "task_update", map[string]any{"status_text": "waiting", "blocked_on": "your review"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := waitNotes(notes, 2); len(got) != 2 || got[1] != "lea needs you: kid is blocked on you: lea board" {
		t.Fatalf("notes %q", got)
	}
	if r := rang(); len(r) != 0 {
		t.Fatalf("rang %q without the deck", r)
	}
}

// A spawn request routed to the user notifies it (and rings while a deck shows the container).
func TestASpawnRequestToTheUserCallsThem(t *testing.T) {
	e, notes, _ := noDeckEnv(t)
	spawned(t, e, "kid", "dev", "")
	r, err := requestFrom(t, e, "kid", map[string]any{"role": "reviewer", "brief": "review the cart"})
	if err != nil || r.SentTo != "user" {
		t.Fatalf("r=%+v err=%v", r, err)
	}
	want := "lea needs you: kid asks for a reviewer agent: lea approve s1 or lea deny s1 (lea requests)"
	if got := waitNotes(notes, 1); len(got) != 1 || got[0] != want {
		t.Fatalf("notes %q, want %q", got, want)
	}
	d, rang := soundEnv(t)
	d.d.SetAgents(&fakeAgents{})
	spawned(t, d, "kid", "dev", "")
	if _, err := requestFrom(t, d, "kid", map[string]any{"role": "reviewer", "brief": "b"}); err != nil {
		t.Fatal(err)
	}
	if got := waitRang(rang, 1); len(got) != 1 {
		t.Fatalf("rang %q with a deck", got)
	}
}

// A notification shows the agents' text safely: no control character, no markup, capped.
func TestNoticeText(t *testing.T) {
	if got := noticeText("a\x1b[2J<b>x</b> & ‮b\n"); got != "a?[2J‹b›x‹/b› + ?b?" {
		t.Fatalf("%q", got)
	}
	if got := noticeText(strings.Repeat("é", maxNotice+10)); len([]rune(got)) != maxNotice+1 || !strings.HasSuffix(got, "…") {
		t.Fatalf("%d runes", len([]rune(got)))
	}
}

// With the deck no notification is shown: the deck shows it all.
func TestNoNotificationWithTheDeck(t *testing.T) {
	e := start(t)
	e.d.SetAgents(&fakeAgents{})
	e.d.desktop("lea-a/dev", "x")
	got := ask(t, e, 1, false)
	waitPending(t, e)
	receive(t, got)
	e.d.mu.Lock()
	n := len(e.d.notified)
	e.d.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d notifications with the deck", n)
	}
}

// Without the deck, an agent that starts waiting in its terminal is notified once; when its
// approval card follows (lea's permission path), the card's notification alone says it.
func TestWithoutTheDeckAWaitingAgentIsNotifiedOnce(t *testing.T) {
	old := waitsDelay
	waitsDelay = 300 * time.Millisecond
	t.Cleanup(func() { waitsDelay = old })
	e, notes, _ := noDeckEnv(t)
	hookEv(t, e, "UserPromptSubmit", nil)
	hookEv(t, e, "PermissionRequest", nil)
	if got := waitNotes(notes, 1); len(got) != 1 || got[0] != "lea needs you: dev waits for you in its terminal: lea attach dev" {
		t.Fatalf("notes %q", got)
	}
	e.d.mu.Lock()
	e.d.notified = map[string]time.Time{}
	e.d.mu.Unlock()
	hookEv(t, e, "UserPromptSubmit", nil)
	hookEv(t, e, "PermissionRequest", nil)
	got := ask(t, e, 3, false)
	p := waitPending(t, e)
	time.Sleep(3 * waitsDelay)
	n := notes()
	if len(n) != 2 || !strings.Contains(n[1], "dev asks to use Bash: lea approve p") {
		t.Fatalf("notes %q", n)
	}
	call(t, e.api, "", "approval_decide", map[string]any{"id": p.ID, "verdict": "deny"}, nil)
	receive(t, got)
}
