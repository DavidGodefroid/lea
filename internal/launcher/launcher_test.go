package launcher

import (
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/tmux"
)

func TestStatusMapsErrors(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	l := w.launcher()
	if got := l.status(die("boom %d", 1)); got != 1 || !strings.Contains(w.errb.String(), "lea: error: boom 1") {
		t.Fatalf("die: %d %q", got, w.errb.String())
	}
	if got := l.status(exitError{2}); got != 2 {
		t.Fatalf("exitError: %d", got)
	}
	err := exec.Command("sh", "-c", "exit 7").Run()
	if got := l.status(err); got != 7 {
		t.Fatalf("child status: %d", got)
	}
	err = exec.Command("sh", "-c", "kill -TERM $$").Run()
	if got := l.status(err); got != 128+15 {
		t.Fatalf("killed child: %d, want 143 as sh reports it", got)
	}
	if got := l.status(errors.New("plain")); got != 1 {
		t.Fatalf("plain: %d", got)
	}
}

func TestInterruptReleasesHeldLocks(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	l := w.launcher()
	if err := l.lock(w.id()); err != nil {
		t.Fatal(err)
	}
	if err := l.lock("global"); err != nil {
		t.Fatal(err)
	}
	ran := 0
	finish := l.atExit(func(*Launcher) { ran++ })
	l.Interrupt()
	l.Interrupt() // a second signal runs nothing twice
	finish()      // nor does the normal path once the trap ran
	for _, n := range []string{w.id(), "global"} {
		if exists(w.hostDir() + "/locks/" + n) {
			t.Errorf("lock %s left behind", n)
		}
	}
	if ran != 1 {
		t.Fatalf("exit function ran %d times", ran)
	}
}

func TestLockTimesOutWithTheShellMessage(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["LEA_LOCK_TIMEOUT"] = "3"
	l := w.launcher()
	if err := os.MkdirAll(w.hostDir()+"/locks/x", 0o755); err != nil {
		t.Fatal(err)
	}
	slept := 0
	l.Sleep = func(time.Duration) { slept++ }
	err := l.lock("x")
	if err == nil || err.Error() != "another lea holds the 'x' lock (remove "+w.hostDir()+"/locks/x if it is stale)" {
		t.Fatalf("err = %v", err)
	}
	if slept != 2 {
		t.Fatalf("slept %d times, the shell sleeps timeout-1 times", slept)
	}
}

func TestSessionCountDropsDeadAndForeignEntries(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	l := w.launcher()
	if err := l.sessionBegin("abc", os.Getpid()); err != nil {
		t.Fatal(err)
	}
	d := w.hostDir() + "/sessions/abc"
	os.WriteFile(d+"/999999999", nil, 0o600)         // dead pid
	os.WriteFile(d+"/notapid", nil, 0o600)           // not a pid
	os.Symlink("/etc/passwd", d+"/"+strconv.Itoa(1)) // a symlink is never a session
	n, err := l.sessionCount("abc")
	if err != nil || n != 1 {
		t.Fatalf("count = %d, %v", n, err)
	}
	entries, _ := os.ReadDir(d)
	if len(entries) != 1 {
		t.Fatalf("stale entries kept: %v", entries)
	}
	l.sessionEnd("abc", os.Getpid())
	if n, _ := l.sessionCount("abc"); n != 0 {
		t.Fatalf("count after end = %d", n)
	}
}

// running is the number of children the launcher is waiting for.
func (l *Launcher) runningChildren() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.running
}

// sh defers a trapped INT/TERM until the command it runs ends, whatever runs it: Interrupt waits
// for every child (run, quiet, output, foreground) before it releases the locks.
func TestInterruptWaitsForEveryChild(t *testing.T) {
	for _, how := range []string{"run", "quiet", "output", "foreground"} {
		t.Run(how, func(t *testing.T) {
			w := newWorld(t, repoRoot(t))
			l := w.launcher()
			if err := l.lock(w.id()); err != nil {
				t.Fatal(err)
			}
			lockDir := w.hostDir() + "/locks/" + w.id()
			ok := w.dir + "/held-to-the-end"
			script := "sleep 0.3; test -d '" + lockDir + "' && touch '" + ok + "'"
			done := make(chan error, 1)
			go func() {
				var err error
				switch how {
				case "run":
					err = l.run("sh", "-c", script)
				case "quiet":
					err = l.quiet("sh", "-c", script)
				case "output":
					_, err = l.output(false, "sh", "-c", script)
				case "foreground":
					err = l.foreground("sh", "-c", script)
				}
				done <- err
			}()
			for i := 0; l.runningChildren() == 0; i++ {
				if i > 200 {
					t.Fatal("the child never started")
				}
				time.Sleep(5 * time.Millisecond)
			}
			l.Interrupt()
			if err := <-done; err != nil {
				t.Fatalf("child: %v", err)
			}
			if !exists(ok) {
				t.Fatal("the lock was released while the child still ran")
			}
			if exists(lockDir) {
				t.Fatal("the lock was not released after the child ended")
			}
		})
	}
}

// After Interrupt the run is over: an exit function registered later runs at once, a lock taken
// later is refused and leaves no directory, and no new child starts.
func TestNothingIsHeldAfterInterrupt(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	l := w.launcher()
	l.Interrupt()
	ran := 0
	finish := l.atExit(func(*Launcher) { ran++ })
	if ran != 1 {
		t.Fatalf("a late exit function ran %d times, want at once", ran)
	}
	finish()
	if ran != 1 {
		t.Fatal("and only once")
	}
	if err := l.lock(w.id()); err == nil {
		t.Fatal("a lock after Interrupt is refused")
	}
	if exists(w.hostDir() + "/locks/" + w.id()) {
		t.Fatal("lock directory left behind after Interrupt")
	}
	if err := l.quiet("sh", "-c", "touch '"+w.dir+"/late'"); err == nil || exists(w.dir+"/late") {
		t.Fatalf("a child started after Interrupt: %v", err)
	}
}

// Interrupt while lock() waits for another lea: the wait ends and nothing is left behind.
func TestInterruptStopsALockWait(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["LEA_LOCK_TIMEOUT"] = "1000"
	l := w.launcher()
	if err := os.MkdirAll(w.hostDir()+"/locks/x", 0o755); err != nil {
		t.Fatal(err)
	}
	l.Sleep = func(time.Duration) {
		os.Remove(w.hostDir() + "/locks/x") // the other lea releases ...
		l.Interrupt()                       // ... as Ctrl-C arrives
	}
	if err := l.lock("x"); err == nil {
		t.Fatal("lock() went on after Interrupt")
	}
	if exists(w.hostDir() + "/locks/x") {
		t.Fatal("lock directory left behind")
	}
}

// A non-numeric LEA_LOCK_TIMEOUT: the shell's [ -lt ] fails at the first contention, and so does
// the Go launcher, with the same message.
func TestLockTimeoutNotANumber(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["LEA_LOCK_TIMEOUT"] = "abc"
	l := w.launcher()
	if err := l.lock("free"); err != nil {
		t.Fatalf("no contention, no failure: %v", err)
	}
	if err := os.MkdirAll(w.hostDir()+"/locks/x", 0o755); err != nil {
		t.Fatal(err)
	}
	slept := 0
	l.Sleep = func(time.Duration) { slept++ }
	err := l.lock("x")
	if err == nil || !strings.Contains(err.Error(), "another lea holds the 'x' lock") || slept != 0 {
		t.Fatalf("err = %v, slept %d", err, slept)
	}
}

// The exit functions Interrupt runs get a view of the launcher that may start children and take
// locks while they run; the main path may not: a child it started during the drain would outlive
// the launcher once the caller exits 130.
func TestInterruptRefusesMainPathChildrenDuringTheDrain(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	l := w.launcher()
	inDrain, mainTried, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var drainRun, drainLock error
	drainLockHeld := false
	l.atExit(func(x *Launcher) {
		close(inDrain)
		<-mainTried
		drainRun = x.quiet("true")
		if drainLock = x.lock("d"); drainLock == nil {
			drainLockHeld = exists(w.hostDir() + "/locks/d")
			x.unlock("d")
		}
	})
	go func() { l.Interrupt(); close(done) }()
	<-inDrain
	mainRun := l.quiet("sh", "-c", "touch '"+w.dir+"/main'")
	mainLock := l.lock("m")
	mainTmux := l.tmuxClient(tmuxServerFor(l), "list-sessions")
	close(mainTried)
	<-done
	if mainRun == nil || exists(w.dir+"/main") {
		t.Fatalf("a main-path child ran during the drain: %v", mainRun)
	}
	if mainLock == nil || exists(w.hostDir()+"/locks/m") {
		t.Fatalf("a main-path lock was taken during the drain: %v", mainLock)
	}
	if mainTmux == nil {
		t.Fatal("a main-path tmux call ran during the drain")
	}
	if drainRun != nil || drainLock != nil || !drainLockHeld {
		t.Fatalf("the exit function could not work: run %v, lock %v (held %v)", drainRun, drainLock, drainLockHeld)
	}
	if exists(w.hostDir() + "/locks/d") {
		t.Fatal("the exit function's lock was left behind")
	}
	if err := l.drainView().quiet("true"); err == nil {
		t.Fatal("a drain view admits children once the drain is over")
	}
}

func tmuxServerFor(l *Launcher) tmux.Server {
	s, _ := l.tmuxServer()
	return s
}

// The view handed to exit functions is the same launcher in every exported field.
func TestDrainViewCopiesEveryExportedField(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	l := w.launcher()
	d := l.drainView()
	lv, dv := reflect.ValueOf(l).Elem(), reflect.ValueOf(d).Elem()
	for i := 0; i < lv.NumField(); i++ {
		f := lv.Type().Field(i)
		if !f.IsExported() {
			continue
		}
		a, b := lv.Field(i), dv.Field(i)
		if f.Type.Kind() == reflect.Func {
			if a.Pointer() != b.Pointer() {
				t.Errorf("field %s not copied", f.Name)
			}
			continue
		}
		if !reflect.DeepEqual(a.Interface(), b.Interface()) {
			t.Errorf("field %s not copied", f.Name)
		}
	}
	if d.root() != l {
		t.Fatal("the view shares the launcher's run state")
	}
}

// An exit function registered after the drain runs at once, with a view that may start the
// children it needs (as the shell's EXIT trap would).
func TestALateExitFunctionCanWork(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	l := w.launcher()
	l.Interrupt()
	var err error
	l.atExit(func(x *Launcher) { err = x.quiet("sh", "-c", "touch '"+w.dir+"/late'") })
	if err != nil || !exists(w.dir+"/late") {
		t.Fatalf("the late exit function could not run its child: %v", err)
	}
	if l.quiet("true") == nil {
		t.Fatal("the main path is still refused")
	}
}

// The lock timeout defaults to 120 tries, a second apart: it covers a container start.
func TestLockTimeoutDefaultsTo120(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	delete(w.env, "LEA_LOCK_TIMEOUT")
	l := w.launcher()
	os.MkdirAll(w.hostDir()+"/locks/x", 0o755)
	slept := 0
	l.Sleep = func(time.Duration) { slept++ }
	if err := l.lock("x"); err == nil || slept != 119 {
		t.Fatalf("err = %v after %d sleeps, want 119", err, slept)
	}
}

// A symlinked session dir is refused by every session function, and nothing is removed or
// written through it (the retired sessions.bats).
func TestSessionFunctionsRefuseASymlinkedDir(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	l := w.launcher()
	out := w.dir + "/out"
	os.MkdirAll(out, 0o755)
	os.WriteFile(out+"/999999", nil, 0o600)
	os.MkdirAll(w.hostDir()+"/sessions", 0o755)
	os.Symlink(out, w.hostDir()+"/sessions/abc")
	if _, err := l.sessionCount("abc"); err == nil {
		t.Error("count went through the symlink")
	}
	if err := l.sessionBegin("abc", 123); err == nil || exists(out+"/123") {
		t.Error("begin wrote through the symlink")
	}
	l.sessionEnd("abc", 999999)
	if !exists(out + "/999999") {
		t.Error("end removed through the symlink")
	}
}

// A symlinked locks dir is refused, and no lock is taken through it (the retired common.bats).
func TestLockRefusesASymlinkedLocksDir(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	out := w.dir + "/out"
	os.MkdirAll(out, 0o755)
	os.Symlink(out, w.hostDir()+"/locks")
	if err := w.launcher().lock("abc"); err == nil || !strings.Contains(err.Error(), "is a symbolic link") {
		t.Fatalf("err = %v", err)
	}
	if exists(out + "/abc") {
		t.Fatal("a lock was taken through the symlink")
	}
}
