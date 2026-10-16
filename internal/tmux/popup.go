package tmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// popupSettle is how long Popup waits for a popup that fails to open.
var popupSettle = 300 * time.Millisecond

// ShellLine quotes argv as one POSIX shell command line: display-popup takes a shell command.
func ShellLine(argv []string) string {
	q := make([]string, len(argv))
	for i, a := range argv {
		q[i] = Quote(a)
	}
	return strings.Join(q, " ")
}

// Popup opens a popup running argv over one client (a user's terminal) of lea's server, with a
// one-shot tmux client. display-popup returns only when the popup closes, so it never goes
// through a control connection, whose command queue it would hold. Popup returns once the popup
// is up, or with tmux's error when it fails at once; the one-shot client is reaped when the
// popup closes. Like every tmux process lea starts, the client names lea's configuration (-f):
// the user's ~/.tmux.conf is never read.
func (s Server) Popup(ctx context.Context, client string, argv []string) error {
	if s.Conf == "" {
		return errors.New("tmux: no configuration file given (lea never lets tmux read ~/.tmux.conf)")
	}
	if err := s.check(); err != nil {
		return err
	}
	line := ShellLine(argv)
	if strings.Contains(line, "#") {
		return fmt.Errorf("tmux: popup command %q contains '#' (a tmux format)", line)
	}
	args := []string{"display-popup", "-E", "-c", client, "-w", "90%", "-h", "80%", line}
	if _, err := Command(args[0], args[1:]...); err != nil { // allow-list and control characters
		return err
	}
	cmd := exec.Command(s.bin(), append([]string{"-L", s.Socket, "-f", s.Conf}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("tmux display-popup: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("tmux display-popup: %v: %s", err, strings.TrimSpace(stderr.String()))
		}
		return nil
	case <-time.After(popupSettle):
		return nil
	case <-ctx.Done():
		return nil // the popup may well be open: leave it to the user
	}
}
