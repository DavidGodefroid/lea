package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/DavidGodefroid/lea/internal/daemon"
	"github.com/DavidGodefroid/lea/internal/identity"
)

// heavyGrant is the daemon's heavy_acquire answer.
type heavyGrant struct {
	Granted bool
	ID      int64
	Slot    int
	Slots   int
	Workers int
	Queued  int
	Busy    []string
}

// heavyWait is how long one heavy_acquire waits before lea heavy asks again.
const heavyWait = 600

// runHeavy is lea heavy -- CMD [ARGS...] (#132): CMD runs once the daemon gives this container a
// heavy slot, on the slot's CPUs, with the worker caps build and test tools read; the slot is
// renewed while it runs and released after. Outside an agent, or without a daemon, CMD runs at
// once with the caps. The status is CMD's.
func runHeavy(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 2 || args[0] != "--" {
		fmt.Fprintln(stderr, "usage: lea heavy -- <command> [args...]")
		return 2
	}
	argv := args[1:]
	workers, _ := strconv.Atoi(os.Getenv("LEA_HEAVY_WORKERS"))
	if workers < 1 {
		workers = daemon.DefaultHeavyWorkers
	}
	var g heavyGrant
	agent, sock := identity.Agent(os.Getenv), identity.BusSock(os.Getenv)
	if agent == "" {
		fmt.Fprintln(stderr, "lea heavy: not an agent (no LEA_AGENT): running without a heavy slot")
	} else {
		var err error
		if g, err = heavyAcquire(sock, agent, heavyLabel(argv), stderr); err != nil {
			fmt.Fprintln(stderr, "lea heavy: running without a heavy slot:", clean(err.Error(), false))
		} else {
			workers = g.Workers
		}
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	set, n := heavyCPUs(g.Slot, workers)
	cmd.Env = heavyEnv(os.Environ(), n, g)
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	err := startOn(cmd, set)
	stop := make(chan struct{})
	if err == nil {
		go func() {
			for {
				select {
				case s := <-sigs:
					_ = cmd.Process.Signal(s)
				case <-stop:
					return
				}
			}
		}()
		if g.Granted {
			go renewHeavy(sock, agent, g.ID, stderr, stop)
		}
		err = cmd.Wait()
	}
	close(stop)
	if g.Granted {
		if rerr := callSock(sock, agent, "heavy_release", map[string]any{"id": g.ID}, nil, defaultTimeout); rerr != nil {
			fmt.Fprintln(stderr, "lea heavy: release:", clean(rerr.Error(), false))
		}
	}
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return ee.ExitCode()
	default:
		fmt.Fprintln(stderr, "lea heavy:", err)
		return 127
	}
}

// heavyLabel names the job on the board: the command's base name and its first argument.
func heavyLabel(argv []string) string {
	l := filepath.Base(argv[0])
	if len(argv) > 1 {
		l += " " + argv[1]
	}
	return oneLine(l, 80)
}

// heavyAcquire waits for a slot; the first answer comes at once (timeout_s 0), so that a job that waits says
// what it waits for.
func heavyAcquire(sock, agent, label string, stderr io.Writer) (heavyGrant, error) {
	params := func(t int) map[string]any { return map[string]any{"label": label, "timeout_s": t} }
	var g heavyGrant
	if err := callSock(sock, agent, "heavy_acquire", params(0), &g, 5*time.Second); err != nil {
		if strings.Contains(err.Error(), "label") {
			label = "job"
			err = callSock(sock, agent, "heavy_acquire", params(0), &g, 5*time.Second)
		}
		if err != nil {
			return g, err
		}
	}
	for !g.Granted {
		fmt.Fprintf(stderr, "lea heavy: waiting for a heavy slot (%d/%d busy: %s; %d waiting)\n",
			len(g.Busy), g.Slots, clean(strings.Join(g.Busy, ", "), false), g.Queued)
		if err := callSock(sock, agent, "heavy_acquire", params(heavyWait), &g, (heavyWait+10)*time.Second); err != nil {
			return g, err
		}
	}
	return g, nil
}

// renewHeavy keeps the slot while the command runs.
func renewHeavy(sock, agent string, id int64, stderr io.Writer, stop <-chan struct{}) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if err := callSock(sock, agent, "heavy_renew", map[string]any{"id": id}, nil, defaultTimeout); err != nil {
				fmt.Fprintln(stderr, "lea heavy: renew:", clean(err.Error(), false))
			}
		}
	}
}

// heavyEnv caps the workers of the tools that read their count from the environment (Go, make,
// CMake, Cargo, pytest-xdist); a MAKEFLAGS with its own -j is kept.
func heavyEnv(env []string, workers int, g heavyGrant) []string {
	w := strconv.Itoa(workers)
	mf := os.Getenv("MAKEFLAGS")
	if !strings.Contains(mf, "-j") {
		mf = strings.TrimSpace(mf + " -j" + w)
	}
	env = append(env, "GOMAXPROCS="+w, "MAKEFLAGS="+mf, "CMAKE_BUILD_PARALLEL_LEVEL="+w, "CARGO_BUILD_JOBS="+w,
		"PYTEST_XDIST_AUTO_NUM_WORKERS="+w, "LEA_HEAVY_WORKERS="+w)
	if g.Granted {
		env = append(env, "LEA_HEAVY_SLOT="+strconv.Itoa(g.Slot))
	}
	return env
}
