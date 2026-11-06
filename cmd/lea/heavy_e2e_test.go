package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The CPU budget e2e (#132): a real container of the engine LEA_E2E_PRESSURE names (docker, or
// podman, rootless included) with a 2-CPU ceiling, as LEA_CPUS gives the lea container, and a real
// daemon with 2 heavy slots of 1 worker on its bus. Six agents each run a CPU burner through lea
// heavy at once; the burner starts one busy loop per CPU of /proc/cpuinfo, as a tool that sizes
// itself from the host would. The container's cpu.stat never shows more than its 2 CPUs (+10 %,
// sampled every second), at most 2 jobs run at once, each sees 1 CPU (nproc), and the others
// wait, say so, and finish. Bounded: about 20 s of 2 CPUs.
//
//	LEA_E2E_PRESSURE=docker go test -count=1 -run TestHeavyBudgetE2E -v ./cmd/lea/
//	LEA_E2E_PRESSURE=podman go test -count=1 -run TestHeavyBudgetE2E -v ./cmd/lea/
func TestHeavyBudgetE2E(t *testing.T) {
	engine := os.Getenv("LEA_E2E_PRESSURE")
	if engine == "" {
		t.Skip("LEA_E2E_PRESSURE=docker|podman runs it against that engine")
	}
	image := os.Getenv("LEA_E2E_PRESSURE_IMAGE")
	if image == "" {
		image = "docker.io/library/alpine:3.22"
	}
	const cpus, slots, jobs, burn = 2.0, 2, 6, 4
	run := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		out, err := exec.CommandContext(ctx, engine, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %s: %v\n%s", engine, strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}

	// lea itself, static, for the container.
	bin := filepath.Join(t.TempDir(), "lea")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	name := fmt.Sprintf("lea-e2e-heavy-%d", os.Getpid())
	bus := heavyDaemon(t, slots, 1, name)
	t.Cleanup(func() { _ = exec.Command(engine, "rm", "-f", name).Run() })
	run("run", "-d", "--name", name, "--cpus", strconv.FormatFloat(cpus, 'f', -1, 64),
		"--mount", "type=bind,src="+bin+",dst=/usr/local/bin/lea,readonly",
		"--mount", "type=bind,src="+filepath.Dir(bus)+",dst=/run/lea/bus",
		image, "sleep", "600")

	// cpu.stat's usage and the kernel's uptime, read in one exec: the rate does not depend on how
	// long the exec takes.
	sample := func() (usec, up float64) {
		out := run("exec", name, "sh", "-c", "grep '^usage_usec ' /sys/fs/cgroup/cpu.stat; cut -d' ' -f1 /proc/uptime")
		f := strings.Fields(out)
		if len(f) != 3 {
			t.Fatalf("cpu.stat and uptime: %q", out)
		}
		u, _ := strconv.ParseFloat(f[1], 64)
		s, _ := strconv.ParseFloat(f[2], 64)
		return u, s
	}

	burner := fmt.Sprintf(`echo "start $(nproc)" >>/tmp/jobs.log
n=$(grep -c ^processor /proc/cpuinfo); i=0
while [ $i -lt $n ]; do (end=$(( $(date +%%s) + %d )); while [ $(date +%%s) -lt $end ]; do :; done) & i=$((i + 1)); done
wait; echo end >>/tmp/jobs.log`, burn)
	for i := 1; i <= jobs; i++ {
		run("exec", "-d", "-e", "LEA_AGENT=dev-"+strconv.Itoa(i), "-e", "LEA_BUS_SOCK=/run/lea/bus/bus.sock", name,
			"sh", "-c", `lea heavy -- sh -c "$0" 2>/tmp/heavy.$1.err; echo "rc $?" >>/tmp/rc.log`, burner, strconv.Itoa(i))
	}

	u0, s0 := sample()
	worst := 0.0
	deadline := time.Now().Add(90 * time.Second)
	for {
		time.Sleep(time.Second)
		u1, s1 := sample()
		if s1 > s0 {
			rate := (u1 - u0) / 1e6 / (s1 - s0)
			worst = max(worst, rate)
			if rate > cpus*1.1 {
				t.Fatalf("the container used %.2f CPUs, over its %.0f", rate, cpus)
			}
		}
		u0, s0 = u1, s1
		if strings.Count(run("exec", name, "sh", "-c", "cat /tmp/rc.log 2>/dev/null || true"), "rc ") == jobs {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the jobs did not all finish within 90 s:\n%s", run("exec", name, "sh", "-c", "cat /tmp/jobs.log /tmp/rc.log /tmp/heavy.*.err 2>/dev/null || true"))
		}
	}
	t.Logf("busiest second: %.2f CPUs (ceiling %.0f)", worst, cpus)
	if worst < 1 {
		t.Errorf("the burners never used a full CPU (%.2f): the load did not run", worst)
	}

	// Every job ran, through a slot, on 1 CPU, and returned 0; never more than 2 at once.
	if rc := run("exec", name, "cat", "/tmp/rc.log"); strings.Count(rc, "rc 0") != jobs {
		t.Fatalf("statuses:\n%s", rc)
	}
	running, most, started := 0, 0, 0
	for _, line := range strings.Split(run("exec", name, "cat", "/tmp/jobs.log"), "\n") {
		switch {
		case strings.HasPrefix(line, "start "):
			started++
			running++
			most = max(most, running)
			if line != "start 1" {
				t.Errorf("a job saw %q CPUs, want 1", strings.TrimPrefix(line, "start "))
			}
		case line == "end":
			running--
		}
	}
	if started != jobs || most > slots {
		t.Fatalf("%d jobs started, at most %d at once (slots %d)", started, most, slots)
	}
	errs := run("exec", name, "sh", "-c", "cat /tmp/heavy.*.err")
	if !strings.Contains(errs, "waiting for a heavy slot (2/2 busy") {
		t.Fatalf("no job waited visibly:\n%s", errs)
	}
	if strings.Contains(errs, "without a heavy slot") {
		t.Fatalf("a job ran without a slot:\n%s", errs)
	}
}
