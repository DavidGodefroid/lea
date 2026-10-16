package tmux

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Event is a control-mode notification received outside a command block, for example
// "%window-close @3" or "%subscription-changed lea-dead $1 @2 0 %4 : 1".
type Event struct {
	Name string
	Args []string
}

var ErrClosed = errors.New("tmux: control connection closed")

type result struct {
	lines []string
	err   error
}

// Conn is one control-mode client. tmux answers commands in the order they are written, each
// with a %begin/%end (or %error) block. A block whose %begin flags field is 1 answers a command
// this client wrote; any other block (the attach command's own) is skipped.
type Conn struct {
	w       io.WriteCloser
	wmu     sync.Mutex // one command line at a time, in pending order
	pmu     sync.Mutex
	pending []chan result
	closed  bool
	events  chan Event
	done    chan struct{}
	proc    *exec.Cmd
	once    sync.Once
	created bool // Open created the session
}

// Created reports whether Server.Open created the session this client is attached to.
func (c *Conn) Created() bool { return c.created }

// NewConn runs the protocol over w (tmux's stdin) and r (tmux's stdout).
func NewConn(w io.WriteCloser, r io.Reader) *Conn { return newConn(w, r, nil) }

func newConn(w io.WriteCloser, r io.Reader, proc *exec.Cmd) *Conn {
	c := &Conn{w: w, events: make(chan Event, 256), done: make(chan struct{}), proc: proc}
	go c.read(r)
	return c
}

func (c *Conn) Events() <-chan Event { return c.events }

func (c *Conn) Run(ctx context.Context, args ...string) ([]string, error) {
	if len(args) == 0 {
		return nil, errors.New("tmux: empty command")
	}
	line, err := Command(args[0], args[1:]...)
	if err != nil {
		return nil, err
	}
	ch := make(chan result, 1)
	c.wmu.Lock()
	c.pmu.Lock()
	if c.closed {
		c.pmu.Unlock()
		c.wmu.Unlock()
		return nil, ErrClosed
	}
	c.pending = append(c.pending, ch)
	c.pmu.Unlock()
	_, werr := io.WriteString(c.w, line+"\n")
	c.wmu.Unlock()
	if werr != nil {
		c.Close()
		return nil, fmt.Errorf("tmux %s: %w", args[0], ErrClosed)
	}
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, fmt.Errorf("tmux %s: %w", args[0], r.err)
		}
		return r.lines, nil
	case <-ctx.Done():
		return nil, ctx.Err() // the late answer still goes to ch, keeping the order intact
	}
}

func (c *Conn) deliver(r result) {
	c.pmu.Lock()
	if len(c.pending) == 0 {
		c.pmu.Unlock()
		return
	}
	ch := c.pending[0]
	c.pending = c.pending[1:]
	c.pmu.Unlock()
	ch <- r
}

func (c *Conn) fail() {
	c.pmu.Lock()
	defer c.pmu.Unlock()
	c.closed = true
	for _, ch := range c.pending {
		ch <- result{err: ErrClosed}
	}
	c.pending = nil
}

func (c *Conn) read(r io.Reader) {
	defer func() {
		c.fail()
		close(c.events)
		if c.proc != nil {
			c.proc.Wait()
		}
		close(c.done)
	}()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	var (
		inBlock, ours bool
		guard         string // "<time> <number>" of the open block
		lines         []string
	)
	for sc.Scan() {
		line := sc.Text()
		if inBlock {
			if end, isErr := closes(line, guard); end {
				if ours {
					var err error
					if isErr {
						msg := strings.Join(lines, "; ")
						if msg == "" {
							msg = "error"
						}
						err = errors.New(msg)
					}
					c.deliver(result{lines: lines, err: err})
				}
				inBlock, lines = false, nil
				continue
			}
			lines = append(lines, line)
			continue
		}
		f := strings.Fields(line)
		if len(f) == 0 || !strings.HasPrefix(f[0], "%") {
			continue
		}
		if f[0] == "%begin" {
			inBlock, lines, guard = true, nil, ""
			ours = len(f) >= 4 && f[3] == "1"
			if len(f) >= 3 {
				guard = f[1] + " " + f[2]
			}
			continue
		}
		select {
		case c.events <- Event{Name: f[0], Args: f[1:]}:
		default: // a full queue already holds events that trigger the same refresh
		}
	}
}

// closes reports whether line ends the block opened with guard, and whether it is an error.
func closes(line, guard string) (end, isErr bool) {
	for _, k := range []string{"%end ", "%error "} {
		if strings.HasPrefix(line, k) {
			f := strings.Fields(line)
			if len(f) >= 3 && f[1]+" "+f[2] == guard {
				return true, k == "%error "
			}
		}
	}
	return false, false
}

// Close ends the client (tmux exits on end of input); a client still running 2 s later is killed.
func (c *Conn) Close() error {
	c.once.Do(func() {
		c.w.Close()
		if c.proc != nil {
			time.AfterFunc(2*time.Second, func() {
				select {
				case <-c.done:
				default:
					c.proc.Process.Kill()
				}
			})
		}
	})
	return nil
}
