package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/store"
)

// The admission e2e (#132, P2): a real container of the engine LEA_E2E_PRESSURE names (docker or
// podman, rootless included) under a real load; the daemon's real sampler finds its cgroup by
// its ID and agents_spawn is refused with the metric that crossed, then let through once the
// load stops. Bounded: a 64 MiB, half-CPU container for a minute at most.
//
//	LEA_E2E_PRESSURE=docker go test -count=1 -run TestPressureE2E -v ./internal/daemon/
//	LEA_E2E_PRESSURE=podman go test -count=1 -run TestPressureE2E -v ./internal/daemon/
func TestPressureE2E(t *testing.T) {
	bin := os.Getenv("LEA_E2E_PRESSURE")
	if bin == "" {
		t.Skip("LEA_E2E_PRESSURE=docker|podman runs it against that engine")
	}
	image := os.Getenv("LEA_E2E_PRESSURE_IMAGE")
	if image == "" {
		image = "docker.io/library/alpine:3.22"
	}
	name := fmt.Sprintf("lea-e2e-pressure-%d", os.Getpid())
	run := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %s: %v\n%s", bin, strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	t.Cleanup(func() { _ = exec.Command(bin, "rm", "-f", name).Run() })
	run("run", "-d", "--name", name, "--memory", "64m", "--memory-swap", "64m", "--cpus", "0.5", "--shm-size", "60m",
		image, "sleep", "300")

	e := start(t)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	bus, err := e.d.AttachContainer(e.ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.st.Register(store.NewAgent{Container: name, Name: "boss", Profile: "claude", Workdir: "/p", Role: "lead"}, store.Caps{}, 1); err != nil {
		t.Fatal(err)
	}
	// The host's own thresholds are off: a busy CI runner must not decide the outcome.
	th := Thresholds{CPUPSI: 30, MemPct: 85}
	e.d.SetPressure(&hostOnlyOff{HostPressure{Bin: bin}}, th)
	spawn := func() error {
		e.d.rates = rates{}
		return call(t, bus, "boss", "agents_spawn", map[string]any{"brief": "x"}, nil)
	}
	reading := func() Reading {
		e.d.samplePressure(e.ctx)
		e.d.mu.Lock()
		defer e.d.mu.Unlock()
		return e.d.pressScopes[name]
	}
	r := reading()
	if r.MemMax != 64<<20 || r.CPUs != 0.5 {
		t.Fatalf("the container's cgroup was not read (or its limits not applied): %+v", r)
	}
	if err := spawn(); err != nil {
		t.Fatalf("refused while idle: %v", err)
	}

	// Memory: 56 MiB in /dev/shm, which the kernel cannot drop: 87 % of 64 MiB.
	run("exec", name, "sh", "-c", "head -c 58720256 /dev/zero >/dev/shm/fill")
	r = reading()
	t.Logf("memory load: %+v", r)
	if err := spawn(); err == nil || !strings.Contains(err.Error(), "container memory") || !strings.Contains(err.Error(), "of 0.1 GiB > 85%") {
		t.Fatalf("err=%v", err)
	}
	run("exec", name, "rm", "/dev/shm/fill")
	if r := reading(); r.MemPct() >= 85 {
		t.Fatalf("still %f %% after the load stopped", r.MemPct())
	}
	if err := spawn(); err != nil {
		t.Fatalf("refused once the memory load stopped: %v", err)
	}

	// CPU: four busy loops on half a CPU stall; cpu PSI avg10 climbs past 30 % within seconds.
	run("exec", "-d", name, "sh", "-c", "for i in 1 2 3 4; do (while :; do :; done) & done; wait")
	deadline := time.Now().Add(45 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		r = reading()
		if last = spawn(); last != nil {
			break
		}
		time.Sleep(2 * time.Second)
	}
	t.Logf("cpu load: %+v", r)
	if last == nil || !strings.Contains(last.Error(), "container cpu PSI") || !strings.Contains(last.Error(), "> 30%") {
		t.Fatalf("not refused on cpu PSI within 45 s: %v (reading %+v)", last, r)
	}
}

// hostOnlyOff samples the containers for real and leaves the host unknown.
type hostOnlyOff struct{ HostPressure }

func (h *hostOnlyOff) Sample(ctx context.Context, cs []string) (Reading, map[string]Reading) {
	_, scopes := h.HostPressure.Sample(ctx, cs)
	return unknownReading(), scopes
}
