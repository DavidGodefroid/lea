package launcher

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/creack/pty"
	"github.com/muesli/cancelreader"
)

// detachKeys are docker's default detach keys, Ctrl-P Ctrl-Q, the ones lox attach names.
const detachKeys = "\x10\x11"

// serveStep is how often lea checks, while the user is attached to a console, whether it serves.
var serveStep = 500 * time.Millisecond

// drainWait is how long lea waits for the console's last output once lox attach has ended.
const drainWait = time.Second

// errNoPty: lea cannot put lox attach on a pseudo-terminal of its own; nothing ran.
var errNoPty = errors.New("no pseudo-terminal")

// attachUntilServes runs c (lox attach) on a pseudo-terminal lea owns, relayed to the user's
// terminal (in, out, in raw mode meanwhile, as docker attach puts it): what the user types goes to
// the console, what the console writes comes back. Once serves says the console serves, lea types
// the detach keys for the user: docker's attach ends as on Ctrl-P Ctrl-Q, and the console keeps
// running (killing the attach client instead could proxy the signal to the console). served says
// lea detached; errNoPty says nothing ran.
func (l *Launcher) attachUntilServes(c *exec.Cmd, in, out *os.File, serves func() bool) (served bool, err error) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		return false, errNoPty
	}
	defer ptmx.Close()
	defer tty.Close()
	keys, err := cancelreader.NewReader(in)
	if err != nil {
		return false, errNoPty
	}
	defer keys.Close()
	raw, err := term.MakeRaw(in.Fd())
	if err != nil {
		return false, errNoPty
	}
	defer func() { _ = term.Restore(in.Fd(), raw) }()
	_ = pty.InheritSize(out, ptmx)
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)

	c.Stdin, c.Stdout, c.Stderr = tty, tty, tty
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	copied := make(chan struct{})
	go func() {
		_, _ = io.Copy(out, ptmx)
		close(copied)
	}()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(ptmx, keys)
	}()
	done := make(chan struct{})
	var detached atomic.Bool
	go func() {
		defer wg.Done()
		t := time.NewTicker(serveStep)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-winch:
				_ = pty.InheritSize(out, ptmx)
			case <-t.C:
				if serves() {
					detached.Store(true)
					_, _ = io.WriteString(ptmx, detachKeys)
					return
				}
			}
		}
	}()
	err = l.exec(c)
	close(done)
	keys.Cancel()
	wg.Wait()
	// The console's last words (lox's "detached from ...") are still in the pseudo-terminal: lea
	// shows them before it closes it. Closing lea's end of the terminal ends the copy.
	_ = tty.Close()
	select {
	case <-copied:
	case <-time.After(drainWait):
	}
	return detached.Load(), err
}
