package launcher

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The limits e2e (#132): a real container of the engine LEA_E2E_PRESSURE names (docker, or
// podman, rootless included), started as a lea older than its limits left it (no CPU, memory or
// pids limit), gets the limits the configuration resolves to at the next lea (applyLimits, which
// up runs on a running container), with no manual docker update. Then a process inside it that
// allocates past LEA_MEMORY is OOM-killed inside the container, while the host keeps answering.
// Bounded: one 128 MiB, 1.5-CPU container for a few seconds.
//
//	LEA_E2E_PRESSURE=docker go test -count=1 -run TestLimitsE2E -v ./internal/launcher/
//	LEA_E2E_PRESSURE=podman go test -count=1 -run TestLimitsE2E -v ./internal/launcher/
func TestLimitsE2E(t *testing.T) {
	engine := os.Getenv("LEA_E2E_PRESSURE")
	if engine == "" {
		t.Skip("LEA_E2E_PRESSURE=docker|podman runs it against that engine")
	}
	bin, err := exec.LookPath(engine)
	if err != nil {
		t.Fatal(err)
	}
	image := os.Getenv("LEA_E2E_PRESSURE_IMAGE")
	if image == "" {
		image = "docker.io/library/alpine:3.22"
	}
	name := fmt.Sprintf("lea-e2e-limits-%d", os.Getpid())
	run := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	must := func(args ...string) string {
		t.Helper()
		out, err := run(args...)
		if err != nil {
			t.Fatalf("%s %s: %v\n%s", engine, strings.Join(args, " "), err, out)
		}
		return out
	}
	t.Cleanup(func() { _, _ = run("rm", "-f", name) })
	// As an older lea started it: no limit at all.
	must("run", "-d", "--name", name, image, "sleep", "300")
	if got := must("inspect", "--format", "{{.HostConfig.NanoCpus}} {{.HostConfig.Memory}}", name); got != "0 0" {
		t.Fatalf("the container starts with limits already: %s", got)
	}

	// The launcher, with the engine as its docker. The engine runs with this test's own
	// environment (HOME, XDG_RUNTIME_DIR: rootless Podman's storage and socket), not the world's,
	// which only gives lea its directories.
	w := newWorld(t, repoRoot(t))
	shim := t.TempDir()
	script := "#!/bin/sh\nexec env -i"
	for _, kv := range os.Environ() {
		script += " " + shellQuote(kv)
	}
	script += " " + shellQuote(bin) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(shim, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	w.env["PATH"] = shim + ":" + os.Getenv("PATH")
	writeUserConf(t, w, "LEA_CPUS=1.5\nLEA_MEMORY=128m\nLEA_PIDS_LIMIT=256\n")
	l := w.launcher()
	if restart := l.applyLimits(name); restart {
		t.Fatalf("the update was refused or held: %s", w.errb.String())
	}
	if !strings.Contains(w.errb.String(), "applied the limits") {
		out, err := exec.Command(filepath.Join(shim, "docker"), "inspect", "--format",
			"{{.Id}} {{.HostConfig.NanoCpus}} {{.HostConfig.Memory}} {{.HostConfig.MemorySwap}} "+pidsLimitFormat, name).CombinedOutput()
		t.Fatalf("nothing applied: the launcher's inspect answers %q (%v); stderr %q", out, err, w.errb.String())
	}
	t.Logf("launcher: %s", strings.TrimSpace(w.errb.String()))
	got := must("inspect", "--format", "{{.HostConfig.NanoCpus}} {{.HostConfig.Memory}} {{.HostConfig.MemorySwap}} {{.HostConfig.PidsLimit}}", name)
	if got != "1500000000 134217728 134217728 256" {
		t.Fatalf("limits after the next lea: %s, want 1500000000 134217728 134217728 256", got)
	}
	// The engine applied them to the cgroup, not only to its records.
	if got := must("exec", name, "cat", "/sys/fs/cgroup/cpu.max", "/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/pids.max"); got != "150000 100000\n134217728\n256" {
		t.Fatalf("the container's cgroup: %q", got)
	}
	// A second lea finds nothing to do.
	w.errb.Reset()
	if restart := w.launcher().applyLimits(name); restart || strings.Contains(w.errb.String(), "applied") {
		t.Fatalf("second lea: restart %v, %q", restart, w.errb.String())
	}

	// Memory: a process that allocates 256 MiB in a 128 MiB container (no swap: --memory-swap is
	// the memory) is OOM-killed inside it; the container goes on, and a probe outside answers
	// within a second all along.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				if line, err := bufio.NewReader(c).ReadString('\n'); err == nil {
					_, _ = c.Write([]byte(line))
				}
			}()
		}
	}()
	probe := func() time.Duration {
		start := time.Now()
		c, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
		if err != nil {
			return time.Hour
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := c.Write([]byte("ping\n")); err != nil {
			return time.Hour
		}
		if _, err := bufio.NewReader(c).ReadString('\n'); err != nil {
			return time.Hour
		}
		return time.Since(start)
	}
	done := make(chan struct{})
	worst := make(chan time.Duration, 1)
	go func() {
		var w time.Duration
		for {
			select {
			case <-done:
				worst <- w
				return
			default:
			}
			w = max(w, probe())
			time.Sleep(100 * time.Millisecond)
		}
	}()
	out, err := run("exec", name, "dd", "if=/dev/zero", "of=/dev/null", "bs=256M", "count=1")
	close(done)
	if err == nil {
		t.Fatalf("256 MiB allocated in a 128 MiB container: %s", out)
	}
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 137 {
		t.Fatalf("the allocation did not end with SIGKILL (137): %v %s", err, out)
	}
	if d := <-worst; d > time.Second {
		t.Fatalf("the host's probe took %v during the OOM", d)
	}
	events := must("exec", name, "cat", "/sys/fs/cgroup/memory.events")
	kills := 0
	for _, line := range strings.Split(events, "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "oom_kill" {
			kills, _ = strconv.Atoi(f[1])
		}
	}
	if kills < 1 {
		t.Fatalf("no OOM kill inside the container:\n%s", events)
	}
	if got := must("inspect", "-f", "{{.State.Running}}", name); got != "true" {
		t.Fatalf("the container itself stopped: running=%s", got)
	}
}

// shellQuote is s in single quotes for sh.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
