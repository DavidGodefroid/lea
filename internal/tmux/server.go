package tmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DefaultSocket is lea's tmux server: tmux -L lea, never the user's default server.
const DefaultSocket = "lea"

// MaxSocketPath is the portable sun_path limit (macOS 104, Linux 108).
const MaxSocketPath = 104

// Server is lea's tmux server. Conf is read only when a call of this type starts the server.
type Server struct {
	Bin    string // tmux binary; "tmux" from PATH when empty
	Socket string // the -L name
	Conf   string
	Env    []string // the environment of the one-shot clients; the process's when nil
}

// SocketPath is where tmux puts a -L socket: $TMUX_TMPDIR (default /tmp)/tmux-<uid>/<name>.
func SocketPath(getenv func(string) string, uid int, name string) string {
	d := getenv("TMUX_TMPDIR")
	if d == "" {
		d = "/tmp"
	}
	return filepath.Join(d, fmt.Sprintf("tmux-%d", uid), name)
}

func (s Server) bin() string {
	if s.Bin == "" {
		return "tmux"
	}
	return s.Bin
}

func (s Server) command(ctx context.Context, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, s.bin(), append([]string{"-L", s.Socket}, args...)...)
	c.Env = s.Env
	return c
}

// attachCommand is the control-mode client. It always carries -f: attach-session can start the
// server, and a server started without lea's config would read the user's tmux.conf.
func (s Server) attachCommand(session string) *exec.Cmd {
	return exec.Command(s.bin(), "-L", s.Socket, "-f", s.Conf, "-C", "attach-session", "-f", "no-output,ignore-size", "-t", "="+session)
}

func (s Server) check() error {
	if p := SocketPath(os.Getenv, os.Getuid(), s.Socket); len(p) > MaxSocketPath {
		return fmt.Errorf("tmux socket path is %d bytes, the limit is %d: %s (use a shorter TMUX_TMPDIR)", len(p), MaxSocketPath, p)
	}
	if _, err := exec.LookPath(s.bin()); err != nil {
		return fmt.Errorf("tmux not found on the host: install tmux 3.2 or later (%v)", err)
	}
	return nil
}

// Sessions lists the server's sessions; none when no server runs.
func (s Server) Sessions(ctx context.Context) ([]string, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	cmd := s.command(ctx, "list-sessions", "-F", "#{session_name}")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		msg := strings.TrimSpace(stderr.String())
		if errors.As(err, &ee) && noServer(msg) {
			return nil, nil
		}
		if msg == "" {
			return nil, fmt.Errorf("tmux list-sessions: %w", err)
		}
		return nil, fmt.Errorf("tmux list-sessions: %w: %s", err, msg)
	}
	return strings.Fields(string(out)), nil
}

// noServer recognises the messages tmux prints when no server is running on the socket. Any
// other failure (unsafe socket directory permissions, for example) is a real error.
func noServer(msg string) bool {
	return strings.Contains(msg, "no server running") ||
		(strings.Contains(msg, "error connecting to") && strings.Contains(msg, "No such file or directory"))
}

// Open creates the session when it is missing (its first window, "deck", runs cmd), then
// attaches a control-mode client that neither sizes the windows nor receives pane output.
// Conn.Created tells whether the session is new.
func (s Server) Open(ctx context.Context, session string, cmd []string) (*Conn, error) {
	if s.Conf == "" {
		return nil, errors.New("tmux: no configuration file given (lea never lets tmux read ~/.tmux.conf)")
	}
	if err := s.check(); err != nil {
		return nil, err
	}
	// A server lea did not start (an older lea's, still running after an upgrade) never read
	// this configuration: it is sourced again. The file is idempotent. Not over the control
	// client: there every line of the file answers with a block of its own, flagged as the
	// client's, which would answer the commands that follow instead of their own output.
	running, err := s.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	// A line this tmux refuses (an option of a newer version) only warns, as it does when tmux
	// reads the file at start-up: the server keeps its earlier settings for that line, and every
	// deck on it keeps working.
	if len(running) > 0 {
		if out, err := s.command(ctx, "-f", s.Conf, "source-file", s.Conf).CombinedOutput(); err != nil {
			log.Printf("lea: warning: tmux: apply %s to the running server: %v: %s", s.Conf, err, strings.TrimSpace(string(out)))
		}
	}
	created := false
	if s.command(ctx, "has-session", "-t", "="+session).Run() != nil {
		args := []string{"-f", s.Conf, "new-session", "-d", "-s", session, "-n", "deck", "-x", "200", "-y", "50", "--"}
		if out, err := s.command(ctx, append(args, cmd...)...).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("tmux new-session %s: %v: %s", session, err, strings.TrimSpace(string(out)))
		}
		created = true
	}
	p := s.attachCommand(session)
	w, err := p.StdinPipe()
	if err != nil {
		return nil, err
	}
	r, err := p.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := p.Start(); err != nil {
		return nil, fmt.Errorf("tmux control client: %w", err)
	}
	c := newConn(w, r, p)
	c.created = created
	return c, nil
}

// Client runs one allow-listed command in a one-shot client of lea's server, always with lea's
// configuration: a call that starts the server must never read the user's ~/.tmux.conf.
// Output is discarded; the error is the client's exit status.
func (s Server) Client(ctx context.Context, name string, args ...string) error {
	if s.Conf == "" {
		return errors.New("tmux: no configuration file given (lea never lets tmux read ~/.tmux.conf)")
	}
	if _, err := Command(name, args...); err != nil {
		return err
	}
	return s.command(ctx, append([]string{"-f", s.Conf, name}, args...)...).Run()
}

// AttachArgv is the argv that attaches the user's terminal to session: exact target, lea's
// configuration. The launcher replaces itself with it.
func (s Server) AttachArgv(session string) ([]string, error) {
	if s.Conf == "" {
		return nil, errors.New("tmux: no configuration file given (lea never lets tmux read ~/.tmux.conf)")
	}
	if _, err := Command("attach-session", "-t", "="+session); err != nil {
		return nil, err
	}
	return []string{s.bin(), "-L", s.Socket, "-f", s.Conf, "attach-session", "-t", "=" + session}, nil
}
