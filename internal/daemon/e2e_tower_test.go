package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/agents"
	"github.com/DavidGodefroid/lea/internal/store"
)

// A Control Tower's first prompt (#339) starts with the control-tower skill's command: in the
// real Claude Code, with lea's plugin (image/claude-plugin) as the image loads it, that first
// line turns the tower guard on, and the model's first tool outside the team's, Bash, is denied.
func TestClaudeTowerFirstPromptTurnsTheGuardOn(t *testing.T) {
	e := newClaudeE2E(t)
	e.setLimited(false)
	e.answer = func(body string) (string, string) {
		if strings.Contains(body, `"tool_result"`) {
			return "", ""
		}
		return "Bash", `{"command":"echo tower-e2e","description":"echo"}`
	}
	plugin, err := filepath.Abs("../../image/claude-plugin")
	if err != nil {
		t.Fatal(err)
	}
	e.env = append(e.env, "LEA_TOWER_DIR="+filepath.Join(e.dir, "tower"))
	if _, err := os.Stat(filepath.Join(plugin, ".claude-plugin", "plugin.json")); err != nil {
		t.Fatal(err)
	}
	prompt := agents.TowerCommand + " You are the lea agent dev (role lead), shown to the user as \"" + agents.TowerLabel + "\"."
	e.run("--plugin-dir", plugin, prompt)
	const denied = "control-tower: you are the control tower, you do not act yourself."
	for end := time.Now().Add(60 * time.Second); time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
		e.mu.Lock()
		var found bool
		for _, b := range e.bodies {
			if strings.Contains(b, `"tool_result"`) && strings.Contains(b, denied) {
				found = true
			}
		}
		e.mu.Unlock()
		if found {
			return
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	t.Fatalf("the tower guard did not deny Bash in 60 s; requests=%d last=%.3000s\n%s", len(e.bodies), lastBody(e.bodies), e.out.String())
}

// claudeTypingPty is claudePty that also types: each line appended to the keys file (argv[2]) is
// typed into the terminal, then Enter.
const claudeTypingPty = `
import os, pty, sys, time, select, signal, struct, fcntl, termios
secs = float(sys.argv[1]); keys = sys.argv[2]; argv = sys.argv[3:]
pid, fd = pty.fork()
if pid == 0:
    os.execvp(argv[0], argv)
fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack('HHHH', 45, 160, 0, 0))
end = time.time() + secs
done = 0
while time.time() < end:
    r, _, _ = select.select([fd], [], [], 0.2)
    if r:
        try:
            d = os.read(fd, 65536)
        except OSError:
            break
        if not d:
            break
        sys.stdout.buffer.write(d)
        sys.stdout.flush()
        if b'\x1b[6n' in d:
            os.write(fd, b'\x1b[1;1R')
    try:
        lines = open(keys, 'rb').read().split(b'\n')
    except OSError:
        lines = [b'']
    for line in lines[done:-1]:
        os.write(fd, line)
        time.sleep(1.5)
        os.write(fd, b'\r')
        done += 1
os.kill(pid, signal.SIGKILL)
`

// /lea:clear-ct (#410) in the real Claude Code, with lea's plugin: a tower with two children
// writes its handoff snapshot (the guard allows that one file), the user types /clear, and the
// new session is still a tower: it starts fresh (none of the old conversation), lea's SessionStart
// context tells it to resume, it reads the snapshot back, agents_list gives both children (parent:
// the tower), Bash is still denied, and a child's message sent after the clear reaches it.
func TestClaudeTowerClearCtResumesTheBoard(t *testing.T) {
	e := newClaudeE2E(t)
	e.setLimited(false)
	towerDir := filepath.Join(e.dir, "tower")
	handoff, resume := filepath.Join(towerDir, "dev.handoff.md"), filepath.Join(towerDir, "dev.resume.md")
	const snapshot = "# tower handoff\\n## Ledger\\nchild-a · working · e2e task a\\nchild-b · review · e2e task b\\n"
	e.answer = func(body string) (string, string) {
		results := strings.Count(body, `"tool_result"`)
		switch {
		case strings.Contains(body, "clear-ct: the handoff file is "+handoff) && results == 0:
			return "Write", `{"file_path":"` + handoff + `","content":"` + snapshot + `"}`
		case strings.Contains(body, "Read "+resume+" first"):
			switch results {
			case 0:
				return "Read", `{"file_path":"` + resume + `"}`
			case 1:
				return "mcp__lea__agents_list", `{}`
			case 2:
				return "Bash", `{"command":"echo tower-e2e","description":"echo"}`
			}
		}
		return "", ""
	}
	plugin, err := filepath.Abs("../../image/claude-plugin")
	if err != nil {
		t.Fatal(err)
	}
	e.env = append(e.env, "LEA_TOWER_DIR="+towerDir)
	mcp := `{"mcpServers":{"lea":{"type":"stdio","command":"` + e.lea + `","args":["mcp"]}}}`
	keys := filepath.Join(e.dir, "keys")
	if err := os.WriteFile(keys, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	typeLine := func(s string) {
		f, err := os.OpenFile(keys, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(s + "\n")
		f.Close()
	}
	first := agents.TowerCommand + " You are the lea agent dev (role lead), shown to the user as \"" + agents.TowerLabel + "\"."
	argv := []string{"-I", "-c", claudeTypingPty, "150", keys, e.claude, first, "--settings", e.sf, "--plugin-dir", plugin,
		"--mcp-config", mcp, "--allowedTools", "mcp__lea__agents_list"} // first first: the last two take every argument after them
	// The tower is a lead lea started, as a Control Tower is.
	if _, err := e.st.Register(store.NewAgent{Container: "lea-a", Name: "dev", Profile: "claude", Role: "lead"}, store.Caps{}, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("python3", argv...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = e.wd, e.env, &e.out, &e.out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })

	// waitBody waits for a request body that ok accepts and returns it.
	waitBody := func(what string, secs int, ok func(string) bool) string {
		t.Helper()
		for end := time.Now().Add(time.Duration(secs) * time.Second); time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
			e.mu.Lock()
			for _, b := range e.bodies {
				if ok(b) {
					e.mu.Unlock()
					return b
				}
			}
			e.mu.Unlock()
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		t.Fatalf("%s: not in %d s; requests=%d last=%.3000s\n%s", what, secs, len(e.bodies), lastBody(e.bodies), e.out.String())
		return ""
	}
	waitBody("the tower's first turn", 60, func(b string) bool {
		return strings.Contains(b, "reply to the user in the language of this message")
	})
	e.until("the tower's turn ends", 30, func(a store.Agent) bool { return a.State == "idle" })
	for _, c := range []string{"child-a", "child-b"} {
		if _, err := e.st.Register(store.NewAgent{Container: "lea-a", Name: c, Profile: "claude", Role: "dev", Parent: "lea-a/dev"}, store.Caps{}, time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
	}

	typeLine("/lea:clear-ct")
	waitBody("the handoff Write's result", 60, func(b string) bool {
		return strings.Contains(b, "clear-ct: the handoff file is") && strings.Contains(b, `"tool_result"`)
	})
	if b, err := os.ReadFile(handoff); err != nil || !strings.Contains(string(b), "child-b · review") {
		t.Fatalf("no snapshot in %s: %v %q\n%s", handoff, err, b, e.out.String())
	}
	e.until("the clear-ct turn ends", 30, func(a store.Agent) bool { return a.State == "idle" })

	typeLine("/clear")
	for end := time.Now().Add(30 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		ps, _ := e.st.EventPayloads("lea-a/dev", "SessionStart")
		if len(ps) > 0 && strings.Contains(ps[len(ps)-1], `"source":"clear"`) {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("no SessionStart clear: %q\n%s", ps, e.out.String())
		}
	}
	// A child writes to the tower after the clear: the new session hears of it.
	if _, err := e.d.send("lea-a/child-a", "dev", "e2e: done", "result", 0); err != nil {
		t.Fatal(err)
	}
	typeLine("status?")

	fresh := waitBody("the resumed session's first request", 60, func(b string) bool {
		return strings.Contains(b, "Read "+resume+" first") && !strings.Contains(b, `"tool_result"`)
	})
	for _, old := range []string{"You are the lea agent dev", "clear-ct: the handoff file is", "e2e task a"} {
		if strings.Contains(fresh, old) {
			t.Errorf("the cleared session still carries %q", old)
		}
	}
	if !strings.Contains(fresh, "control-tower: your session was cleared") {
		t.Errorf("no resume context in the cleared session's request:\n%.3000s", fresh)
	}
	read := waitBody("the snapshot read back", 60, func(b string) bool {
		return strings.Contains(b, "Read "+resume+" first") && strings.Count(b, `"tool_result"`) >= 1 && strings.Contains(b, "e2e task b")
	})
	if strings.Contains(read, "control-tower: you are the control tower, you do not act yourself.") {
		t.Errorf("the resume file's Read was denied")
	}
	listed := waitBody("agents_list after the clear", 60, func(b string) bool {
		return strings.Contains(b, "Read "+resume+" first") && strings.Count(b, `"tool_result"`) >= 2
	})
	for _, c := range []string{"child-a", "child-b"} {
		if !strings.Contains(listed, `\"name\":\"`+c+`\"`) {
			t.Errorf("agents_list after the clear misses %s:\n%.4000s", c, listed)
		}
	}
	if !strings.Contains(listed, `\"parent\":\"dev\"`) {
		t.Errorf("agents_list after the clear gives no child of the tower:\n%.4000s", listed)
	}
	waitBody("Bash denied after the clear", 60, func(b string) bool {
		return strings.Contains(b, "Read "+resume+" first") && strings.Count(b, `"tool_result"`) >= 3 &&
			strings.Contains(b, "control-tower: you are the control tower, you do not act yourself.")
	})
	waitBody("the child's message noticed after the clear", 30, func(b string) bool {
		return strings.Contains(b, "Read "+resume+" first") && strings.Contains(b, "unread message(s) from lea-a/child-a")
	})
}

func lastBody(bs []string) string {
	if len(bs) == 0 {
		return ""
	}
	return bs[len(bs)-1]
}
