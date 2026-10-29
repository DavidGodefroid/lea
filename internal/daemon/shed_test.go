package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/store"
)

// shedAgents is fakeAgents whose SetPaused records the pause in the store, as the manager does.
type shedAgents struct {
	*fakeAgents
	st *store.Store
	mu sync.Mutex
	pp []string
}

func (s *shedAgents) SetPaused(ctx context.Context, k string, paused bool) error {
	s.mu.Lock()
	if paused {
		s.pp = append(s.pp, "pause "+k)
	} else {
		s.pp = append(s.pp, "resume "+k)
	}
	s.mu.Unlock()
	return s.st.SetPaused(k, paused)
}

func (s *shedAgents) log() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.pp, ", ")
}

func reg(t *testing.T, e env, name, role, parent string, at int64) string {
	t.Helper()
	a, err := e.d.st.Register(store.NewAgent{Container: "lea-a", Name: name, Profile: "claude", Workdir: "/p", Role: role, Parent: parent}, store.Caps{}, at)
	if err != nil {
		t.Fatal(err)
	}
	return a.Key
}

// shedEnv: a daemon whose pressure clock the test moves, with shedding on.
type shedEnv struct {
	env
	fp  *fakePressure
	sa  *shedAgents
	now time.Time
}

func newShedEnv(t *testing.T) *shedEnv {
	t.Helper()
	e := start(t)
	s := &shedEnv{env: e, now: time.Unix(10000, 0)}
	s.sa = &shedAgents{fakeAgents: &fakeAgents{}, st: e.d.st}
	e.d.SetAgents(s.sa)
	e.d.mu.Lock()
	e.d.pressClock = func() time.Time { return s.now }
	e.d.mu.Unlock()
	s.fp = withPressure(t, e)
	e.d.SetShed(DefaultShed)
	return s
}

// tick moves the clock d, samples with readings r for lea-a and waits for the pauses it decided.
func (s *shedEnv) tick(t *testing.T, d time.Duration, r Reading) {
	t.Helper()
	s.now = s.now.Add(d)
	s.fp.set(calm(), map[string]Reading{"lea-a": r})
	s.d.samplePressure(s.ctx)
	for i := 0; i < 200; i++ {
		s.d.mu.Lock()
		busy := s.d.shedBusy
		s.d.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("shedding still in flight")
}

func critical() Reading { return critAt(98) }

func critAt(pct float64) Reading {
	r := calm()
	r.MemMax = 24 << 30
	r.MemUsed = int64(pct / 100 * float64(r.MemMax))
	return r
}

func (s *shedEnv) calls() string {
	s.sa.fakeAgents.mu.Lock()
	defer s.sa.fakeAgents.mu.Unlock()
	return strings.Join(s.sa.fakeAgents.calls, ", ")
}

// Sustained critical memory pauses one agent per Shed.After, while each pause makes the reading
// better, up to half the agents it may pause: idle first, then the newest dev, reviewers after
// devs; never the lead, never a heavy slot holder. Memory being critical, each one's nested
// containers are stopped first. Calm for Shed.Resume resumes them, the last paused first, and
// starts their containers again. Each agent's lead hears it.
func TestShedPausesTheLowestPriorityAgentAndResumesLIFO(t *testing.T) {
	s := newShedEnv(t)
	s.sa.parkOut = "dev: stopped 1 container and 0 pods (freed 900 MiB)"
	boss := reg(t, s.env, "boss", "lead", "", 1)
	rev := reg(t, s.env, "rev", "reviewer", boss, 5)
	old := reg(t, s.env, "old", "dev", boss, 2)
	young := reg(t, s.env, "young", "dev", boss, 4)
	idle := reg(t, s.env, "idle", "dev", boss, 3)
	holder := reg(t, s.env, "holder", "dev", boss, 6)
	for i := 0; i < 4; i++ { // older reviewers: the other half
		reg(t, s.env, fmt.Sprintf("rev%d", i), "reviewer", boss, 1)
	}
	for _, k := range []string{boss, rev, old, young, holder} {
		s.d.st.SetState(k, "working", "", 7)
	}
	s.d.st.SetState(idle, "idle", "", 7)
	if a := acquire(t, s.env, "holder", "phpunit", 1); !a.Granted {
		t.Fatal("no slot")
	}
	s.tick(t, time.Second, critAt(99))
	s.tick(t, 20*time.Second, critAt(99))
	if l := s.sa.log(); l != "" {
		t.Fatalf("paused before Shed.After: %s", l)
	}
	s.tick(t, 10*time.Second, critAt(99))
	s.tick(t, 30*time.Second, critAt(98.4))
	s.tick(t, 30*time.Second, critAt(97.6))
	s.tick(t, 30*time.Second, critAt(96.6))
	s.tick(t, 30*time.Second, critAt(95.6)) // better again, but half the 8 agents are paused
	want := "pause " + idle + ", pause " + young + ", pause " + old + ", pause " + rev
	if l := s.sa.log(); l != want {
		t.Fatalf("paused %s, want %s", l, want)
	}
	if c := s.calls(); !strings.Contains(c, "park "+idle) || !strings.Contains(c, "park "+rev) {
		t.Fatalf("no stack stopped first: %s", c)
	}
	if err := call(t, s.bus, "young", "task_update", map[string]any{"status_text": "fixing"}, nil); err != nil {
		t.Fatal(err)
	}
	var b struct{ Items []boardItem }
	if err := call(t, s.api, "", "board_list", nil, &b); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range b.Items {
		if it.Agent == "young" {
			found = strings.Contains(it.Shed, "paused by lea at") && strings.Contains(it.Shed, "container memory 98% of 24.0 GiB > 95%")
		}
	}
	if !found {
		t.Fatalf("board: %+v", b.Items)
	}
	var ms []delivered
	if err := call(t, s.bus, "boss", "msg_inbox", nil, &ms); err != nil {
		t.Fatal(err)
	}
	if len(ms) != 4 || !strings.Contains(ms[0].Text, "lea paused idle, its nested containers included, under pressure (container memory 99%") {
		t.Fatalf("inbox=%+v", ms)
	}
	// Over the admission thresholds but not critical: nothing moves.
	over := calm()
	over.MemUsed, over.MemMax = 21<<30, 24<<30
	s.tick(t, 90*time.Second, over)
	if l := s.sa.log(); l != want {
		t.Fatalf("moved while over: %s", l)
	}
	// Calm: one resume per Shed.Resume, the last paused first.
	s.tick(t, time.Second, calm())
	s.tick(t, 59*time.Second, calm())
	if l := s.sa.log(); l != want {
		t.Fatalf("resumed before Shed.Resume: %s", l)
	}
	s.tick(t, time.Second, calm())
	s.tick(t, 60*time.Second, calm())
	if l := s.sa.log(); l != want+", resume "+rev+", resume "+old {
		t.Fatalf("log %s", l)
	}
	// The user resumed young meanwhile: lea starts its stack only, and goes on with idle.
	s.d.st.SetPaused(young, false)
	s.tick(t, 60*time.Second, calm())
	s.tick(t, 60*time.Second, calm())
	if l := s.sa.log(); l != want+", resume "+rev+", resume "+old+", resume "+idle {
		t.Fatalf("log %s", l)
	}
	for _, k := range []string{rev, old, young, idle} {
		if c := s.calls(); !strings.Contains(c, "unpark "+k) {
			t.Fatalf("the stack of %s not started again: %s", k, c)
		}
	}
}

// A pause that does not make the reading better is the last one: what weighs is elsewhere.
func TestShedStopsWhenAPauseDoesNotHelp(t *testing.T) {
	s := newShedEnv(t)
	boss := reg(t, s.env, "boss", "lead", "", 1)
	for i := 0; i < 6; i++ {
		reg(t, s.env, fmt.Sprintf("dev%d", i), "dev", boss, int64(2+i))
	}
	s.tick(t, time.Second, critAt(98))
	for i := 0; i < 5; i++ {
		s.tick(t, 30*time.Second, critAt(98))
	}
	if l := s.sa.log(); l != "pause lea-a/dev5" {
		t.Fatalf("log %s", l)
	}
}

// A reading that swings between critical and merely over the admission thresholds still sheds:
// the critical time goes on. CPU being critical, no stack is stopped.
func TestShedThroughASwingingReading(t *testing.T) {
	s := newShedEnv(t)
	boss := reg(t, s.env, "boss", "lead", "", 1)
	reg(t, s.env, "a", "dev", boss, 2)
	reg(t, s.env, "b", "dev", boss, 3)
	cpu := calm()
	cpu.CPUPSI = 85
	over := calm()
	over.CPUPSI = 50
	s.tick(t, time.Second, cpu)
	s.tick(t, 15*time.Second, over)
	s.tick(t, 15*time.Second, cpu)
	if l := s.sa.log(); l != "pause lea-a/b" {
		t.Fatalf("log %s", l)
	}
	if c := s.calls(); strings.Contains(c, "park ") {
		t.Fatalf("a stack stopped on cpu: %s", c)
	}
}

// The agents lea paused are kept across a daemon restart, which resumes them.
func TestShedSurvivesARestart(t *testing.T) {
	s := newShedEnv(t)
	boss := reg(t, s.env, "boss", "lead", "", 1)
	kid := reg(t, s.env, "kid", "dev", boss, 2)
	s.tick(t, time.Second, critical())
	s.tick(t, 30*time.Second, critical())
	if l := s.sa.log(); l != "pause "+kid {
		t.Fatalf("log %s", l)
	}
	if _, err := os.Stat(filepath.Join(s.d.runDir, shedFile)); err != nil {
		t.Fatal(err)
	}
	// A new daemon on the same run dir and store.
	d2 := New(s.d.st, s.d.runDir, s.d.now)
	sa2 := &shedAgents{fakeAgents: &fakeAgents{}, st: s.d.st}
	d2.SetAgents(sa2)
	now := s.now
	d2.pressClock = func() time.Time { return now }
	fp := &fakePressure{}
	fp.set(calm(), map[string]Reading{"lea-a": calm()})
	d2.SetPressure(fp, DefaultThresholds)
	d2.SetShed(DefaultShed)
	for _, step := range []time.Duration{time.Second, 60 * time.Second} {
		now = now.Add(step)
		d2.samplePressure(s.ctx)
		for i := 0; i < 100 && func() bool { d2.mu.Lock(); defer d2.mu.Unlock(); return d2.shedBusy }(); i++ {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if l := sa2.log(); l != "resume "+kid {
		t.Fatalf("log %s", l)
	}
	if _, err := os.Stat(filepath.Join(s.d.runDir, shedFile)); !os.IsNotExist(err) {
		t.Fatalf("shed file left: %v", err)
	}
}

// The host's critical pressure sheds too; After 0 turns shedding off.
func TestShedOnHostPressureAndOff(t *testing.T) {
	s := newShedEnv(t)
	reg(t, s.env, "boss", "lead", "", 1)
	kid := reg(t, s.env, "kid", "dev", "", 2)
	h := calm()
	h.CPUPSI = 85
	for _, d := range []time.Duration{time.Second, 30 * time.Second} {
		s.now = s.now.Add(d)
		s.fp.set(h, map[string]Reading{"lea-a": calm()})
		s.d.samplePressure(s.ctx)
	}
	time.Sleep(100 * time.Millisecond)
	if l := s.sa.log(); l != "pause "+kid {
		t.Fatalf("log %s", l)
	}
	s2 := newShedEnv(t)
	s2.d.SetShed(Shed{})
	reg(t, s2.env, "kid", "dev", "", 2)
	s2.tick(t, time.Second, critical())
	s2.tick(t, time.Hour, critical())
	if l := s2.sa.log(); l != "" {
		t.Fatalf("shed while off: %s", l)
	}
}
