package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/daemon"
	"github.com/DavidGodefroid/lea/internal/store"
)

// heavyDaemon is a daemon with slots heavy slots of workers each, and its container's bus.
func heavyDaemon(t *testing.T, slots, workers int) string {
	t.Helper()
	dir, _ := os.MkdirTemp("", "lea")
	t.Cleanup(func() { os.RemoveAll(dir) })
	st, err := store.Open(filepath.Join(dir, "lea.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	d := daemon.New(st, filepath.Join(dir, "run"), func() int64 { return time.Now().Unix() })
	d.SetHeavy(slots, workers)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	bus, err := d.AttachContainer(ctx, "lea-a")
	if err != nil {
		t.Fatal(err)
	}
	return bus
}

// lea heavy runs its command in a slot, on the slot's CPUs, with the worker caps the build and
// test tools read (#132), and returns its status.
func TestHeavyRunsTheCommandWithWorkerCaps(t *testing.T) {
	bus := heavyDaemon(t, 1, 2)
	rc, out, e := runEnv(t, map[string]string{"LEA_BUS_SOCK": bus, "LEA_AGENT": "dev-1", "MAKEFLAGS": ""},
		"heavy", "--", "sh", "-c", `echo "$GOMAXPROCS|$MAKEFLAGS|$CMAKE_BUILD_PARALLEL_LEVEL|$CARGO_BUILD_JOBS|$PYTEST_XDIST_AUTO_NUM_WORKERS|$LEA_HEAVY_WORKERS"; nproc`)
	if rc != 0 {
		t.Fatalf("rc=%d err=%q", rc, e)
	}
	w := strconv.Itoa(min(2, runtime.NumCPU()))
	if want := strings.Repeat(w+"|", 1) + "-j" + w + "|" + w + "|" + w + "|" + w + "|" + w + "\n" + w + "\n"; out != want {
		t.Fatalf("out=%q, want %q", out, want)
	}
	// Its status is the command's; the slot is free again.
	if rc, _, _ := runEnv(t, nil, "heavy", "--", "sh", "-c", "exit 3"); rc != 3 {
		t.Fatalf("rc=%d, want 3", rc)
	}
	// A MAKEFLAGS that sets -j already is kept.
	if _, out, _ := runEnv(t, map[string]string{"MAKEFLAGS": "-j7 -k"}, "heavy", "--", "sh", "-c", `echo "$MAKEFLAGS"`); out != "-j7 -k\n" {
		t.Fatalf("MAKEFLAGS=%q", out)
	}
}

// With one slot, heavy jobs of several agents never overlap: the CPU budget holds.
func TestHeavyJobsNeverExceedTheSlots(t *testing.T) {
	bus := heavyDaemon(t, 1, 1)
	t.Setenv("LEA_BUS_SOCK", bus)
	t.Setenv("LEA_AGENT", "dev-1")
	log := filepath.Join(t.TempDir(), "log")
	var wg sync.WaitGroup
	rcs := make([]int, 3)
	for i := range rcs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var out, errb bytes.Buffer
			rcs[i] = run([]string{"heavy", "--", "sh", "-c", `echo start >>"$0"; sleep 0.3; echo end >>"$0"`, log}, strings.NewReader(""), &out, &errb)
		}()
	}
	wg.Wait()
	b, _ := os.ReadFile(log)
	if got := strings.TrimSpace(string(b)); got != "start\nend\nstart\nend\nstart\nend" {
		t.Fatalf("jobs overlapped:\n%s", got)
	}
	for i, rc := range rcs {
		if rc != 0 {
			t.Fatalf("job %d: rc=%d", i, rc)
		}
	}
}

// A job that waits says for what; outside an agent or without a daemon, the command runs at once
// with the caps, and lea heavy says why it has no slot.
func TestHeavyWaitsVisiblyAndRunsWithoutADaemon(t *testing.T) {
	bus := heavyDaemon(t, 1, 1)
	held := make(chan struct{})
	t.Setenv("LEA_BUS_SOCK", bus)
	t.Setenv("LEA_AGENT", "dev-1")
	go func() {
		var out, errb bytes.Buffer
		run([]string{"heavy", "--", "sh", "-c", "sleep 0.6"}, strings.NewReader(""), &out, &errb)
		close(held)
	}()
	time.Sleep(200 * time.Millisecond)
	rc, _, e := runEnv(t, map[string]string{"LEA_BUS_SOCK": bus, "LEA_AGENT": "dev-2"}, "heavy", "--", "true")
	if rc != 0 || !strings.Contains(e, "waiting for a heavy slot") || !strings.Contains(e, "busy: dev-1;") {
		t.Fatalf("rc=%d err=%q", rc, e)
	}
	<-held
	rc, out, e := runEnv(t, map[string]string{"LEA_BUS_SOCK": filepath.Join(t.TempDir(), "none.sock"), "LEA_AGENT": "dev-2"},
		"heavy", "--", "sh", "-c", `echo "$GOMAXPROCS"`)
	if rc != 0 || out != strconv.Itoa(min(daemon.DefaultHeavyWorkers, runtime.NumCPU()))+"\n" || !strings.Contains(e, "without a heavy slot") {
		t.Fatalf("no daemon: rc=%d out=%q err=%q", rc, out, e)
	}
	if rc, _, _ := runEnv(t, nil, "heavy"); rc != 2 {
		t.Fatalf("no command: rc=%d", rc)
	}
}
