package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/store"
)

const testSession = "11111111-2222-3333-4444-555555555555"

// coldWithMessage is a suspended_cold agent with one unread message.
func coldWithMessage(t *testing.T, e env, name string) string {
	t.Helper()
	k := spawned(t, e, name, "dev", "")
	if err := e.d.st.SetState(k, store.StateSuspendedCold, testSession, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.st.Send(store.Message{From: "user", To: k, Kind: "note", Body: "decided", At: 3}); err != nil {
		t.Fatal(err)
	}
	return k
}

// Under pressure the resume of a suspended agent waits in the resume lane, is never dropped
// (however long it waits), and runs once the pressure falls.
func TestASuspendedWakeWaitsForThePressureAndRuns(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{st: e.d.st}
	e.d.SetAgents(fa)
	fp := withPressure(t, e)
	fp.set(calm(), pressured())
	e.d.samplePressure(e.ctx)
	k := coldWithMessage(t, e, "dev-1")
	e.d.wakeSuspended(k)
	time.Sleep(50 * time.Millisecond)
	noCall(t, fa, "resume "+k)
	e.d.mu.Lock()
	n := len(e.d.resumeQueue)
	for _, r := range e.d.resumeQueue {
		r.at = time.Now().Add(-24 * time.Hour) // far past the spawns' TTL
	}
	e.d.mu.Unlock()
	if n != 1 {
		t.Fatalf("resume queue %d, want 1", n)
	}
	e.d.wakeSuspended(k) // a second message does not queue it twice
	e.d.samplePressure(e.ctx)
	settle(t, e)
	noCall(t, fa, "resume "+k)
	if g := e.d.gauges("lea-a"); len(g) != 2 || !strings.Contains(g[1].Line(), "1 resumes queued") {
		t.Fatalf("gauges %+v", g)
	}
	fp.set(calm(), map[string]Reading{"lea-a": calm()})
	e.d.samplePressure(e.ctx)
	settle(t, e)
	if c := waitCall(t, fa, "resume "+k); !strings.Contains(c, "unread message") {
		t.Fatalf("resume prompt %q", c)
	}
	e.d.mu.Lock()
	n = len(e.d.resumeQueue)
	e.d.mu.Unlock()
	if n != 0 {
		t.Fatalf("resume queue %d after the start", n)
	}
}

// When the pressure falls, the suspended wakes start first, then the launcher's resume, then the
// queued spawns, one per calm sample; a spawn asked while resumes wait is refused or queued.
func TestTheResumeLaneGoesBeforeTheSpawns(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{st: e.d.st}
	e.d.SetAgents(fa)
	spawned(t, e, "boss", "lead", "")
	fp := withPressure(t, e)
	fp.set(calm(), pressured())
	e.d.samplePressure(e.ctx)
	if err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"brief": "x", "queue": true}, nil); err != nil {
		t.Fatal(err)
	}
	var out struct{ Notice string }
	if err := call(t, e.api, "", "agents_resume_stopped", map[string]any{"container": "lea-a"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Notice, "queued") {
		t.Fatalf("notice %q", out.Notice)
	}
	k := coldWithMessage(t, e, "dev-1")
	e.d.wakeSuspended(k)
	time.Sleep(50 * time.Millisecond)
	noCall(t, fa, "resume")
	noCall(t, fa, "new")

	fp.set(calm(), map[string]Reading{"lea-a": calm()})
	e.d.samplePressure(e.ctx)
	settle(t, e)
	waitCall(t, fa, "resume "+k)
	noCall(t, fa, "resume-stopped")
	// Calm, but a resume still waits: a spawn without queue is refused, not let in before it.
	e.d.rates = rates{}
	if err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"brief": "y"}, nil); err == nil || !strings.Contains(err.Error(), "resumes wait") {
		t.Fatalf("err=%v", err)
	}
	e.d.samplePressure(e.ctx)
	settle(t, e)
	waitCall(t, fa, "resume-stopped lea-a")
	if newCalled(fa) {
		t.Fatalf("a spawn started before the resumes: %q", fa.calls)
	}
	e.d.samplePressure(e.ctx)
	settle(t, e)
	if !newCalled(fa) {
		t.Fatalf("the queued spawn did not start: %q", fa.calls)
	}
}

// Without pressure a wake and the launcher's resume run at once.
func TestResumesRunAtOnceWithoutPressure(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{st: e.d.st}
	e.d.SetAgents(fa)
	withPressure(t, e)
	k := coldWithMessage(t, e, "dev-1")
	e.d.wakeSuspended(k)
	waitCall(t, fa, "resume "+k)
	var out struct{ Notice string }
	if err := call(t, e.api, "", "agents_resume_stopped", map[string]any{"container": "lea-a"}, &out); err != nil {
		t.Fatal(err)
	}
	if out.Notice != "" {
		t.Fatalf("notice %q", out.Notice)
	}
	waitCall(t, fa, "resume-stopped lea-a")
}
