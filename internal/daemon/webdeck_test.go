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

// webAttachFor holds a deck_web_attach for container on the host API until the returned cancel.
func webAttachFor(t *testing.T, e env, container string) (cancel func(), done <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan error, 1)
	go func() {
		p, _ := json.Marshal(map[string]any{"container": container})
		ch <- client.Call(ctx, e.api, wire.Request{Method: "deck_web_attach", Params: p}, nil)
	}()
	for i := 0; i < 100 && !e.d.webAttached(container); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if !e.d.webAttached(container) {
		cancel()
		t.Fatal("deck_web_attach did not attach")
	}
	return cancel, ch
}

func TestAWebDeckMakesPermissionPromptsWaitUntilItDisconnects(t *testing.T) {
	e := start(t)
	e.d.SetAgents(&fakeAgents{unattached: true})
	detach, done := webAttachFor(t, e, "lea-a")
	// Attached in the browser: the request waits for its verdict there.
	got := ask(t, e, 3, true)
	p := waitPending(t, e)
	if err := call(t, e.api, "", "approval_decide", map[string]any{"id": p.ID, "verdict": "always"}, nil); err != nil {
		t.Fatal(err)
	}
	if r := receive(t, got); r.Verdict != "always" {
		t.Fatalf("verdict=%+v", r)
	}
	detach()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("deck_web_attach did not return once its client was gone")
	}
	for i := 0; i < 100 && e.d.webAttached("lea-a"); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if e.d.webAttached("lea-a") {
		t.Fatal("the web deck stayed attached after its client left")
	}
	// Gone: today's behaviour again, the agent's own dialog asks at once.
	began := time.Now()
	var r verdictResult
	if err := call(t, e.bus, "dev", "approval_request", map[string]any{"tool": "Bash", "input": map[string]any{}, "timeout_s": 30}, &r); err != nil {
		t.Fatal(err)
	}
	if r.Verdict != "" || r.ID != 0 || time.Since(began) > 2*time.Second {
		t.Fatalf("r=%+v after %s: the request waited for a web deck that left", r, time.Since(began))
	}
}

func TestAWebDeckCountsOnlyForItsContainerAndEachClient(t *testing.T) {
	e := start(t)
	e.d.SetAgents(&fakeAgents{unattached: true})
	first, _ := webAttachFor(t, e, "lea-a")
	second, _ := webAttachFor(t, e, "lea-a")
	if e.d.deckAttached(e.ctx, "lea-b") {
		t.Fatal("a web deck of lea-a attached lea-b")
	}
	first()
	time.Sleep(50 * time.Millisecond)
	if !e.d.deckAttached(e.ctx, "lea-a") {
		t.Fatal("one browser left and the other no longer counted")
	}
	second()
	for i := 0; i < 100 && e.d.webAttached("lea-a"); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if e.d.deckAttached(e.ctx, "lea-a") {
		t.Fatal("still attached after both browsers left")
	}
}

func TestDeckWebAttachIsHostOnlyAndChecksTheContainer(t *testing.T) {
	e := start(t)
	err := call(t, e.bus, "dev", "deck_web_attach", map[string]any{"container": "lea-a"}, nil)
	if err == nil || !strings.Contains(err.Error(), "host only") {
		t.Fatalf("from a container: err=%v", err)
	}
	if e.d.webAttached("lea-a") {
		t.Fatal("an agent attached a web deck")
	}
	for _, c := range []string{"", "../x", "Lea-A", strings.Repeat("a", 70)} {
		if err := call(t, e.api, "", "deck_web_attach", map[string]any{"container": c}, nil); err == nil ||
			!strings.Contains(err.Error(), "invalid container") {
			t.Fatalf("container %q: err=%v", c, err)
		}
	}
}
