package statusline

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// UserTimeout is how long the user's own status line command may take before lea kills it and
// prints its own line: Claude waits for the line.
const UserTimeout = 1500 * time.Millisecond

// ChainedEnv is set on the user's command: a `lea statusline` it runs itself prints lea's line
// without chaining again.
const ChainedEnv = "LEA_STATUSLINE_CHAINED"

// maxUserOutput bounds what lea keeps of the user's command's output.
const maxUserOutput = 64 << 10

// User is the user's own status line chained behind lea's (#374): the shell command and how long
// it may run. An empty Command chains nothing.
type User struct {
	Command string
	Timeout time.Duration
}

// UserCommand is the status line command of the Claude settings file at path (the container's
// copy of the user's ~/.claude/settings.json): empty when there is none, when it is not a
// command, or when it is lea's own (no recursion).
func UserCommand(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var s struct {
		StatusLine *struct {
			Type    string `json:"type"`
			Command string `json:"command"`
		} `json:"statusLine"`
	}
	if json.Unmarshal(b, &s) != nil || s.StatusLine == nil {
		return ""
	}
	c := strings.TrimSpace(s.StatusLine.Command)
	if (s.StatusLine.Type != "" && s.StatusLine.Type != "command") || c == "" || isLeas(c) {
		return ""
	}
	return c
}

// isLeas tells lea's own status line command: `lea statusline`, by any path to lea.
func isLeas(c string) bool {
	f := strings.Fields(c)
	for i := 0; i+1 < len(f); i++ {
		if (f[i] == "lea" || strings.HasSuffix(f[i], "/lea")) && strings.TrimRight(f[i+1], ";&|)") == "statusline" {
			return true
		}
	}
	return false
}

// start runs the command with in on stdin and returns a function waiting for its output: the
// output without trailing white space, or empty when there is no command, it failed, printed
// nothing or was killed at the timeout (with whatever it started).
func (u User) start(in []byte) func() string {
	if u.Command == "" {
		return func() string { return "" }
	}
	d := u.Timeout
	if d <= 0 {
		d = UserTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), d)
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", u.Command)
	cmd.Stdin = bytes.NewReader(in)
	var out capped
	cmd.Stdout = &out
	cmd.Env = append(os.Environ(), ChainedEnv+"=1")
	// Its own process group, killed whole: a script's children must not outlive it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 100 * time.Millisecond
	if err := cmd.Start(); err != nil {
		cancel()
		return func() string { return "" }
	}
	done := make(chan string, 1)
	go func() {
		defer cancel()
		err := cmd.Wait()
		if err != nil || ctx.Err() != nil {
			done <- ""
			return
		}
		done <- strings.TrimRight(out.String(), " \t\r\n")
	}()
	return func() string { return <-done }
}

// capped keeps the first maxUserOutput bytes written to it and drops the rest.
type capped struct{ bytes.Buffer }

func (c *capped) Write(p []byte) (int, error) {
	if room := maxUserOutput - c.Len(); room > 0 {
		if len(p) > room {
			c.Buffer.Write(p[:room])
		} else {
			c.Buffer.Write(p)
		}
	}
	return len(p), nil
}
