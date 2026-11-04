package launcher

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// userTerminal is a pseudo-terminal standing for the user's: lea gets its terminal end, the test
// types and reads at the other.
func userTerminal(t *testing.T) (user, term *os.File) {
	t.Helper()
	user, term, err := pty.Open()
	if err != nil {
		t.Skipf("no pseudo-terminal: %v", err)
	}
	t.Cleanup(func() { user.Close(); term.Close() })
	return user, term
}

// At a terminal, lea relays the user's keys to lox attach and, once the console serves, types
// the detach keys itself (#282): lox attach gets "crm\r" from the user, then Ctrl-P Ctrl-Q.
func TestAttachUntilServesDetaches(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "attach")
	// A console asking its question: docker attach puts its terminal in raw mode.
	script := "#!/bin/sh\nstty raw -echo\necho asking\nhead -c 4 >" + dir + "/typed\nhead -c 2 | od -An -tx1 >" + dir +
		"/keys\necho detached\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	user, term := userTerminal(t)
	shown := make(chan string, 16)
	go func() {
		r := bufio.NewReader(user)
		for {
			s, err := r.ReadString('\n')
			if s != "" {
				shown <- s
			}
			if err != nil {
				close(shown)
				return
			}
		}
	}()
	go func() {
		for s := range shown {
			if strings.Contains(s, "asking") {
				_, _ = user.WriteString("crm\r")
			}
		}
	}()
	defer stubServeStep(5 * time.Millisecond)()
	serves := func() bool {
		b, _ := os.ReadFile(dir + "/typed")
		return len(b) == 4
	}
	var l Launcher
	l.Stderr = os.Stderr
	served, err := l.attachUntilServes(exec.Command(fake), term, term, serves)
	if err != nil || !served {
		t.Fatalf("attachUntilServes() = %v, %v; want served", served, err)
	}
	if b, _ := os.ReadFile(dir + "/typed"); string(b) != "crm\r" {
		t.Fatalf("console got %q from the user, want %q", b, "crm\r")
	}
	if b, _ := os.ReadFile(dir + "/keys"); strings.TrimSpace(string(b)) != "10 11" {
		t.Fatalf("console got %q after serving, want the detach keys 10 11", b)
	}
}

// The user detaches first (or the attach ends on its own): lea types nothing.
func TestAttachUntilServesEndsFirst(t *testing.T) {
	_, term := userTerminal(t)
	defer stubServeStep(5 * time.Millisecond)()
	var l Launcher
	served, err := l.attachUntilServes(exec.Command("true"), term, term, func() bool { return false })
	if err != nil || served {
		t.Fatalf("attachUntilServes() = %v, %v; want not served, no error", served, err)
	}
}

func stubServeStep(d time.Duration) func() {
	old := serveStep
	serveStep = d
	return func() { serveStep = old }
}
