package launcher

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// exitRecorder stands for os.Exit: it records each status on a channel and returns.
func exitRecorder() (func(int), chan int) {
	ch := make(chan int, 4)
	return func(c int) { ch <- c }, ch
}

func noExitWithin(t *testing.T, ch chan int, d time.Duration, why string) {
	t.Helper()
	select {
	case c := <-ch:
		t.Fatalf("exited %d %s", c, why)
	case <-time.After(d):
	}
}

func exitWithin(t *testing.T, ch chan int, d time.Duration) int {
	t.Helper()
	select {
	case c := <-ch:
		return c
	case <-time.After(d):
		t.Fatal("never exited")
		return -1
	}
}

// A late exit function (registered after Interrupt returned, so run at once on the main path)
// is waited for before the process exits: it is not cut short.
func TestExitWaitsForALateExitFunction(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	l := w.launcher()
	l.Interrupt()
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go l.atExit(func(x *Launcher) {
		close(started)
		<-release
		if err := x.quiet("true"); err != nil {
			t.Errorf("the late exit function's child: %v", err)
		}
		close(done)
	})
	<-started
	exit, codes := exitRecorder()
	go l.exitSettled(130, exit)
	noExitWithin(t, codes, 200*time.Millisecond, "while a late exit function runs")
	close(release)
	if c := exitWithin(t, codes, 5*time.Second); c != 130 {
		t.Fatalf("exit %d", c)
	}
	select {
	case <-done:
	default:
		t.Fatal("exited before the late exit function ended")
	}
}

// Once the exit path holds the run state, a late exit function cannot start half-way: it starts
// only if the process did not exit (here, a fake exit that returns), and the exit is called once.
func TestExitHappensOnce(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	l := w.launcher()
	exit, codes := exitRecorder()
	l.exitSettled(0, exit)
	l.exitSettled(0, exit)
	if c := exitWithin(t, codes, time.Second); c != 0 {
		t.Fatalf("exit %d", c)
	}
	noExitWithin(t, codes, 50*time.Millisecond, "twice")
}

// The main path ending while a signal's drain still runs does not exit under it: the process
// exits 130 once every pending exit function has run.
func TestMainPathExitWaitsForTheDrain(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	l := w.launcher()
	started, release := make(chan struct{}), make(chan struct{})
	ran := false
	l.atExit(func(*Launcher) { close(started); <-release; ran = true })
	go l.Interrupt()
	<-started
	exit, codes := exitRecorder()
	go l.exitSettled(0, exit)
	noExitWithin(t, codes, 200*time.Millisecond, "during the drain")
	close(release)
	if c := exitWithin(t, codes, 5*time.Second); c != 130 || !ran {
		t.Fatalf("exit %d, exit function ran: %v", c, ran)
	}
}

// Run: a signal while a child runs interrupts the run; the process exits 130 once the child has
// ended and the exit functions have run, and not before.
func TestRunExits130OnASignal(t *testing.T) {
	root := copyRoot(t)
	w := newWorld(t, root)
	script := "#!/bin/sh\ntouch '" + w.dir + "/started'\nwhile [ ! -e '" + w.dir + "/go' ]; do sleep 0.01; done\n"
	if err := os.WriteFile(root+"/libexec/lea-sh", []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	l := w.launcher()
	sigs := make(chan os.Signal, 1)
	exit, codes := exitRecorder()
	returned := make(chan struct{})
	go func() { l.Run([]string{"kube", "refresh"}, sigs, exit); close(returned) }()
	for i := 0; !exists(w.dir + "/started"); i++ {
		if i > 500 {
			t.Fatal("the child never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	sigs <- syscall.SIGINT
	for !l.isInterrupted() {
		time.Sleep(time.Millisecond)
	}
	noExitWithin(t, codes, 200*time.Millisecond, "while the child runs")
	os.WriteFile(w.dir+"/go", nil, 0o644)
	if c := exitWithin(t, codes, 5*time.Second); c != 130 {
		t.Fatalf("exit %d", c)
	}
	<-returned
	noExitWithin(t, codes, 50*time.Millisecond, "twice")
}

// Run without a signal exits with Main's status.
func TestRunExitsWithTheStatus(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	l := w.launcher()
	exit, codes := exitRecorder()
	l.Run([]string{"kube", "nope"}, make(chan os.Signal), exit)
	if c := exitWithin(t, codes, time.Second); c != 2 {
		t.Fatalf("exit %d", c)
	}
}

// When the child Interrupt waits for ends, the exit path wakes up too: it must not exit before
// Interrupt has run the pending exit functions (the race is repeated to catch it).
func TestExitWaitsForPendingExitFunctionsAfterTheLastChild(t *testing.T) {
	for i := 0; i < 50; i++ {
		w := newWorld(t, repoRoot(t))
		l := w.launcher()
		ran := make(chan struct{})
		l.atExit(func(*Launcher) { close(ran) })
		release := make(chan struct{})
		childStarted := make(chan struct{})
		go l.track(func() error { close(childStarted); <-release; return nil })
		<-childStarted
		go l.Interrupt()
		for !l.isInterrupted() {
			time.Sleep(time.Millisecond)
		}
		ranAtExit := make(chan bool, 1)
		exited := make(chan struct{})
		go l.exitSettled(0, func(int) {
			select {
			case <-ran:
				ranAtExit <- true
			default:
				ranAtExit <- false
			}
			close(exited)
		})
		time.Sleep(time.Millisecond)
		close(release)
		<-exited
		if !<-ranAtExit {
			t.Fatalf("round %d: exited before the pending exit function ran", i)
		}
	}
}
