package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePressure hands out the readings a test sets.
type fakePressure struct {
	mu     sync.Mutex
	host   Reading
	scopes map[string]Reading
}

func (f *fakePressure) set(host Reading, scopes map[string]Reading) {
	f.mu.Lock()
	f.host, f.scopes = host, scopes
	f.mu.Unlock()
}

func (f *fakePressure) Sample(ctx context.Context, cs []string) (Reading, map[string]Reading) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.host, f.scopes
}

func calm() Reading {
	return Reading{CPUPSI: 2, MemPSI: 0, IOPSI: 0, MemUsed: 4 << 30, MemMax: 32 << 30, CPUs: 22, CPUUsed: 3, Load1: 4}
}

func pressured() map[string]Reading {
	return map[string]Reading{"lea-a": {CPUPSI: 63, MemPSI: 1, IOPSI: 0, MemUsed: 22 << 30, MemMax: 24 << 30, CPUs: 16, CPUUsed: 15.8, Load1: -1}}
}

func withPressure(t *testing.T, e env) *fakePressure {
	t.Helper()
	fp := &fakePressure{}
	fp.set(calm(), map[string]Reading{"lea-a": calm()})
	e.d.SetPressure(fp, DefaultThresholds)
	e.d.samplePressure(e.ctx)
	return fp
}

// Thresholds name the metric, its value and the threshold; the host's memory counts from 90 %.
func TestThresholdsOver(t *testing.T) {
	th := DefaultThresholds
	r := Reading{CPUPSI: 63, MemPSI: 12, IOPSI: 41, MemUsed: 22 << 30, MemMax: 24 << 30, CPUs: 22, Load1: 58}
	got := strings.Join(th.over(r, true), "; ")
	for _, w := range []string{"cpu PSI 63% > 40%", "memory 92% of 24.0 GiB > 90%", "memory PSI 12% > 10%", "io PSI 41% > 40%", "load 2.6 per CPU > 2.0"} {
		if !strings.Contains(got, w) {
			t.Errorf("host: %q lacks %q", got, w)
		}
	}
	r.MemUsed = 21 << 30 // 87.5 %: over the container's 85 %, under the host's 90 %
	if got := strings.Join(th.over(r, false), "; "); !strings.Contains(got, "memory 88% of 24.0 GiB > 85%") || strings.Contains(got, "load") {
		t.Errorf("container: %q", got)
	}
	if got := th.over(r, true); strings.Contains(strings.Join(got, ";"), "memory 8") {
		t.Errorf("host at 87.5%%: %q", got)
	}
	if o := (Thresholds{}).over(r, true); len(o) != 0 {
		t.Errorf("all off: %q", o)
	}
	if o := th.over(unknownReading(), true); len(o) != 0 {
		t.Errorf("unknown readings: %q", o)
	}
	if !th.near(Reading{CPUPSI: 35, MemPSI: -1, IOPSI: -1, MemUsed: -1, Load1: -1}, false) || th.near(calm(), false) {
		t.Error("near")
	}
}

// Above a threshold agents_spawn is refused with a reason naming the metric, its value and the
// threshold; the manager is not called. Once the readings fall, the same spawn starts.
func TestAgentsSpawnRefusedUnderPressure(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	spawned(t, e, "boss", "lead", "")
	fp := withPressure(t, e)
	fp.set(calm(), pressured())
	e.d.samplePressure(e.ctx)
	err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"brief": "x"}, nil)
	if err == nil || !strings.Contains(err.Error(), "under pressure (container cpu PSI 63% > 40%, memory 92% of 24.0 GiB > 85%)") ||
		!strings.Contains(err.Error(), "1 agents run, 0 heavy slots busy") || !strings.Contains(err.Error(), "queue: true") {
		t.Fatalf("err=%v", err)
	}
	if newCalled(fa) {
		t.Fatal("the manager was called")
	}
	fp.set(calm(), map[string]Reading{"lea-a": calm()})
	e.d.samplePressure(e.ctx)
	if err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"brief": "x"}, nil); err != nil {
		t.Fatal(err)
	}
	if !newCalled(fa) {
		t.Fatal("not started once the pressure fell")
	}
}

// The host's pressure refuses too; a stale sample (the sampler stopped) refuses nothing.
func TestAgentsSpawnHostPressureAndStaleSamples(t *testing.T) {
	e := start(t)
	e.d.SetAgents(&fakeAgents{})
	spawned(t, e, "boss", "lead", "")
	fp := withPressure(t, e)
	h := calm()
	h.Load1 = 58
	fp.set(h, map[string]Reading{"lea-a": calm()})
	e.d.samplePressure(e.ctx)
	if err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"brief": "x"}, nil); err == nil || !strings.Contains(err.Error(), "host load 2.6 per CPU > 2.0") {
		t.Fatalf("err=%v", err)
	}
	e.d.mu.Lock()
	e.d.pressAt = time.Now().Add(-pressureStale - time.Second)
	e.d.mu.Unlock()
	if err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"brief": "x"}, nil); err != nil {
		t.Fatalf("stale sample: %v", err)
	}
}

// queue: true under pressure queues the spawn; it starts once the readings fall and the lead gets
// a message. A lead queues at most maxQueuedSpawns.
func TestAgentsSpawnQueuedStartsWhenPressureFalls(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	boss := spawned(t, e, "boss", "lead", "")
	fp := withPressure(t, e)
	fp.set(calm(), pressured())
	e.d.samplePressure(e.ctx)
	var r struct {
		Queued int64
		Reason string
	}
	if err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"brief": "fix the cart", "queue": true}, &r); err != nil {
		t.Fatal(err)
	}
	if r.Queued != 1 || !strings.Contains(r.Reason, "cpu PSI 63%") || newCalled(fa) {
		t.Fatalf("result=%+v calls=%q", r, fa.calls)
	}
	if g := e.d.gauges("lea-a"); len(g) != 2 || g[1].Spawns != 1 || g[1].Level != "over" || !strings.Contains(g[1].Line(), "1 spawns queued") {
		t.Fatalf("gauges=%+v", g)
	}
	e.d.samplePressure(e.ctx) // still under pressure: still queued
	if newCalled(fa) {
		t.Fatal("started under pressure")
	}
	fp.set(calm(), map[string]Reading{"lea-a": calm()})
	e.d.samplePressure(e.ctx)
	if !newCalled(fa) || fa.req.Brief != "fix the cart" || fa.req.Parent != boss {
		t.Fatalf("calls=%q req=%+v", fa.calls, fa.req)
	}
	var ms []delivered
	if err := call(t, e.bus, "boss", "msg_inbox", nil, &ms); err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || !strings.Contains(ms[0].Text, "[status: ok] queued spawn #1 started: claude-1") {
		t.Fatalf("inbox=%+v", ms)
	}
	fp.set(calm(), pressured())
	e.d.samplePressure(e.ctx)
	for i := 0; i < maxQueuedSpawns; i++ {
		e.d.rates = rates{} // the rate is not what this checks
		if err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"brief": "x", "queue": true}, nil); err != nil {
			t.Fatal(i, err)
		}
	}
	e.d.rates = rates{}
	if err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"brief": "x", "queue": true}, nil); err == nil || !strings.Contains(err.Error(), "already have 4 spawns queued") {
		t.Fatalf("err=%v", err)
	}
}

// A queued spawn that waited queuedSpawnTTL is dropped, and the lead hears it.
func TestQueuedSpawnExpires(t *testing.T) {
	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	spawned(t, e, "boss", "lead", "")
	fp := withPressure(t, e)
	fp.set(calm(), pressured())
	e.d.samplePressure(e.ctx)
	if err := call(t, e.bus, "boss", "agents_spawn", map[string]any{"brief": "x", "queue": true}, nil); err != nil {
		t.Fatal(err)
	}
	e.d.mu.Lock()
	e.d.spawnQueue[0].at = time.Now().Add(-queuedSpawnTTL - time.Second)
	e.d.mu.Unlock()
	e.d.samplePressure(e.ctx)
	var ms []delivered
	if err := call(t, e.bus, "boss", "msg_inbox", nil, &ms); err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || !strings.Contains(ms[0].Text, "[status: fail] queued spawn #1 dropped") || newCalled(fa) {
		t.Fatalf("inbox=%+v calls=%q", ms, fa.calls)
	}
}

// Under pressure a container gets half its heavy slots (at least one): the jobs that run keep
// theirs, new ones wait; when the pressure falls the waiting ones get theirs.
func TestHeavySlotsShrinkUnderPressure(t *testing.T) {
	e := start(t)
	e.d.SetHeavy(4, 2)
	fp := withPressure(t, e)
	a := acquire(t, e, "dev-1", "phpstan", 1)
	fp.set(calm(), pressured())
	e.d.samplePressure(e.ctx)
	b := acquire(t, e, "dev-2", "phpunit", 1)
	if !a.Granted || !b.Granted {
		t.Fatalf("a=%+v b=%+v", a, b)
	}
	if c := acquire(t, e, "dev-3", "infection", 0); c.Granted {
		t.Fatalf("a third job ran under pressure with 4/2 slots: %+v", c)
	}
	ch := acquireLater(t, e, "dev-3", "infection")
	if g := e.d.gauges("lea-a"); g[1].Slots != 2 || g[1].Heavy != 2 || g[1].Queued != 1 || !strings.Contains(g[1].Line(), "heavy 2/2 (+1 queued)") {
		t.Fatalf("gauge=%+v %q", g[1], g[1].Line())
	}
	fp.set(calm(), map[string]Reading{"lea-a": calm()})
	e.d.samplePressure(e.ctx)
	select {
	case c := <-ch:
		if !c.Granted {
			t.Fatalf("c=%+v", c)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the waiting job did not get a slot once the pressure fell")
	}
}

// The gauge on one line.
func TestGaugeLine(t *testing.T) {
	g := Gauge{Scope: "lea-a", CPUUsed: 14.8, CPUs: 16, CPUPSI: 22, MemUsed: 18<<30 + 100<<20, MemMax: 24 << 30, Heavy: 3, Slots: 4, Queued: 2}
	if got, want := g.Line(), "CPU 14.8/16 · PSI 22% · mem 18.1/24.0 GiB · heavy 3/4 (+2 queued)"; got != want {
		t.Fatalf("%q, want %q", got, want)
	}
	h := Gauge{Scope: "host", CPUUsed: 3, CPUs: 22, CPUPSI: -1, MemUsed: -1, Load1: 4.25}
	if got, want := h.Line(), "CPU 3.0/22 · load 4.2"; got != want {
		t.Fatalf("%q, want %q", got, want)
	}
	if got := (Gauge{Scope: "lea-a", CPUUsed: 1, CPUs: 1.5, CPUPSI: -1, MemUsed: -1, Slots: 1}).Line(); got != "CPU 1.0/1.5 · heavy 0/1" {
		t.Fatalf("%q", got)
	}
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		f := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// HostPressure reads /proc and each container's cgroup v2, found through its pid.
func TestHostPressureSample(t *testing.T) {
	dir := t.TempDir()
	proc, cg := filepath.Join(dir, "proc"), filepath.Join(dir, "cg")
	psi := "some avg10=%s avg60=1.00 avg300=1.00 total=1\nfull avg10=%s avg60=0.00 avg300=0.00 total=1\n"
	f := func(some, full string) string { return sprintf2(psi, some, full) }
	writeTree(t, proc, map[string]string{
		"pressure/cpu":    f("12.50", "0.00"),
		"pressure/memory": f("3.00", "1.25"),
		"pressure/io":     f("9.00", "4.00"),
		"meminfo":         "MemTotal:       32000000 kB\nMemFree:  1000 kB\nMemAvailable:   8000000 kB\n",
		"loadavg":         "44.00 30.00 20.00 3/900 1234\n",
		"stat":            "cpu  100 0 100 800 0 0 0 0 0 0\ncpu0 1 1 1 1\n",
		"4242/cgroup":     "0::/user.slice/libpod-abc.scope\n",
	})
	sc := "user.slice/libpod-abc.scope/"
	writeTree(t, cg, map[string]string{
		sc + "cpu.pressure":    f("63.00", "10.00"),
		sc + "memory.pressure": f("5.00", "2.00"),
		sc + "io.pressure":     f("1.00", "0.50"),
		sc + "memory.current":  "25769803776\n",
		sc + "memory.stat":     "anon 1\ninactive_file 1073741824\nactive_file 5\n",
		sc + "memory.max":      "25769803776\n",
		sc + "cpu.max":         "1600000 100000\n",
		sc + "cpu.stat":        "usage_usec 1000000\nuser_usec 1\n",
	})
	now := time.Unix(1000, 0)
	pids := 0
	h := &HostPressure{Proc: proc, Cgroup: cg, Now: func() time.Time { return now },
		Pid: func(ctx context.Context, c string) (int, error) {
			pids++
			if c != "lea-a" {
				return 0, os.ErrNotExist
			}
			return 4242, nil
		}}
	host, scopes := h.Sample(context.Background(), []string{"lea-a", "lea-gone"})
	if host.CPUPSI != 12.5 || host.MemPSI != 1.25 || host.IOPSI != 4 || host.Load1 != 44 ||
		host.MemMax != 32000000<<10 || host.MemUsed != 24000000<<10 || host.CPUUsed != -1 {
		t.Fatalf("host=%+v", host)
	}
	a, ok := scopes["lea-a"]
	if !ok || len(scopes) != 1 {
		t.Fatalf("scopes=%+v", scopes)
	}
	if a.CPUPSI != 63 || a.MemPSI != 2 || a.IOPSI != 0.5 || a.MemUsed != 23<<30 || a.MemMax != 24<<30 || a.CPUs != 16 || a.CPUUsed != -1 {
		t.Fatalf("lea-a=%+v", a)
	}
	// Two seconds later: 4 s of CPU time in the container (2 CPUs), 200 ticks busy on the host.
	now = now.Add(2 * time.Second)
	writeTree(t, cg, map[string]string{sc + "cpu.stat": "usage_usec 5000000\n", sc + "memory.max": "max\n"})
	writeTree(t, proc, map[string]string{"stat": "cpu  200 0 200 900 0 0 0 0 0 0\n"})
	host, scopes = h.Sample(context.Background(), []string{"lea-a"})
	if a := scopes["lea-a"]; a.CPUUsed != 2 || a.MemMax != 0 || a.MemPct() != -1 {
		t.Fatalf("lea-a=%+v", a)
	}
	if host.CPUUsed != 1 {
		t.Fatalf("host cpu used %v", host.CPUUsed)
	}
	if pids != 2 { // lea-a once (then cached), lea-gone once
		t.Fatalf("%d pid lookups", pids)
	}
}

func sprintf2(f, a, b string) string {
	return strings.Replace(strings.Replace(f, "%s", a, 1), "%s", b, 1)
}

func TestParsePSI(t *testing.T) {
	s, f := parsePSI([]byte("some avg10=1.50 avg60=0 avg300=0 total=0\nfull avg10=0.25 avg60=0 avg300=0 total=0\n"))
	if s != 1.5 || f != 0.25 {
		t.Fatal(s, f)
	}
	if s, f := parsePSI([]byte("garbage\nsome avg10=x\n")); s != -1 || f != -1 {
		t.Fatal(s, f)
	}
}

// The deck gets the container's short gauge at the worse level of it and the host; the board gets
// every gauge.
func TestDeckAndBoardPressure(t *testing.T) {
	e := start(t)
	fp := withPressure(t, e)
	if v := e.d.deckPressure("lea-a"); v == nil || v.Level != "ok" || !strings.HasPrefix(v.Short, "cpu 3.0/22 mem 4.0/32G heavy 0/") {
		t.Fatalf("calm: %+v", v)
	}
	h := calm()
	h.MemUsed = 31 << 30
	fp.set(h, map[string]Reading{"lea-a": calm()})
	e.d.samplePressure(e.ctx)
	v := e.d.deckPressure("lea-a")
	if v == nil || v.Level != "over" || v.Over != "host memory 97% of 32.0 GiB > 90%" {
		t.Fatalf("host over: %+v", v)
	}
	if v := e.d.deckPressure("lea-b"); v != nil {
		t.Fatalf("an unsampled container: %+v", v)
	}
	var b struct{ Pressure []Gauge }
	if err := call(t, e.api, "", "board_list", nil, &b); err != nil {
		t.Fatal(err)
	}
	if len(b.Pressure) != 2 || b.Pressure[0].Scope != "host" || b.Pressure[0].Level != "over" || b.Pressure[1].Scope != "lea-a" {
		t.Fatalf("board: %+v", b.Pressure)
	}
}
