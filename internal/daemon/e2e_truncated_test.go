package daemon

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// headless runs claude -p with args on the fake API; its JSON result and exit code.
func (e *claudeE2E) headless(args ...string) (result, session string, rc int, out string) {
	e.t.Helper()
	cmd := exec.Command(e.claude, append([]string{"-p", "--output-format", "json"}, args...)...)
	cmd.Dir, cmd.Env = e.wd, e.env
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	if x, ok := err.(*exec.ExitError); ok {
		rc = x.ExitCode()
	} else if err != nil {
		e.t.Fatal(err)
	}
	var r struct {
		Result    string `json:"result"`
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(so.Bytes(), &r)
	return r.Result, r.SessionID, rc, so.String() + se.String()
}

// transcript is the JSONL file of session under the test's Claude config.
func (e *claudeE2E) transcript(session string) string {
	e.t.Helper()
	var found, cfg string
	for _, v := range e.env {
		if c, ok := strings.CutPrefix(v, "CLAUDE_CONFIG_DIR="); ok {
			cfg = c
		}
	}
	filepath.WalkDir(filepath.Join(cfg, "projects"), func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Name() == session+".jsonl" {
			found = p
		}
		return nil
	})
	if found == "" {
		e.t.Fatalf("no transcript for %s", session)
	}
	return found
}

// Spike SP2 (b) (#155): a real Claude resumes a session whose transcript's last JSONL line is
// cut (a crash mid-write): b1, the last line whatever it is; b2, the last assistant message,
// nothing after it.
func TestClaudeResumesATruncatedTranscript(t *testing.T) {
	e := newClaudeE2E(t)
	e.setLimited(false)
	for _, c := range []string{"b1-last-line", "b2-last-assistant"} {
		t.Run(c, func(t *testing.T) {
			_, s, rc, out := e.headless("Reply with the single word READY.")
			if rc != 0 || len(s) != 36 {
				t.Fatalf("new session: rc=%d %s", rc, out)
			}
			f := e.transcript(s)
			b, _ := os.ReadFile(f)
			ls := bytes.Split(bytes.TrimRight(b, "\n"), []byte("\n"))
			k := len(ls) - 1
			if c == "b2-last-assistant" {
				for k = len(ls) - 1; k >= 0; k-- {
					var l struct{ Type string }
					if json.Unmarshal(ls[k], &l) == nil && l.Type == "assistant" {
						break
					}
				}
			}
			cut := append(bytes.Join(ls[:k], []byte("\n")), '\n')
			cut = append(cut, ls[k][:len(ls[k])/2]...)
			os.WriteFile(f, cut, 0o600)
			t.Logf("%s: %d lines, cut line %d (%.60s)", c, len(ls), k+1, ls[k])
			e.mu.Lock()
			before := len(e.bodies)
			e.mu.Unlock()
			r, _, rc, out := e.headless("--resume", s, "Reply with the single word OK.")
			t.Logf("resume: rc=%d result=%q\n%.300s", rc, r, out)
			if rc != 0 || !strings.Contains(r, "resumed ok") {
				t.Fatalf("the resume failed")
			}
			// The resumed turn carries the session's history: the first prompt, then the new one.
			e.mu.Lock()
			sent := strings.Join(e.bodies[before:], "\n")
			e.mu.Unlock()
			if !strings.Contains(sent, "Reply with the single word READY.") || !strings.Contains(sent, "Reply with the single word OK.") {
				t.Fatalf("the resumed turn lost the history:\n%.3000s", sent)
			}
			// Claude appended to the cut file: a second resume still sees the first resume's turn.
			e.mu.Lock()
			before = len(e.bodies)
			e.mu.Unlock()
			r, _, rc, out = e.headless("--resume", s, "Reply with the single word THIRD.")
			if rc != 0 || !strings.Contains(r, "resumed ok") {
				t.Fatalf("the second resume failed: rc=%d %.1500s", rc, out)
			}
			e.mu.Lock()
			sent = strings.Join(e.bodies[before:], "\n")
			e.mu.Unlock()
			if !strings.Contains(sent, "Reply with the single word OK.") || !strings.Contains(sent, "Reply with the single word READY.") {
				b, _ := os.ReadFile(f)
				t.Fatalf("the second resume lost the first resume's turn:\n%.3000s\n--- transcript:\n%s", sent, b)
			}
		})
	}
}

// The same cut, resumed as lea resumes a suspended agent: interactive, claude --resume <session>
// <prompt> in a terminal. The prompt reaches the model with the session's history.
func TestClaudeResumesATruncatedTranscriptInteractively(t *testing.T) {
	e := newClaudeE2E(t)
	e.setLimited(false)
	_, s, rc, out := e.headless("Reply with the single word READY.")
	if rc != 0 || len(s) != 36 {
		t.Fatalf("new session: rc=%d %s", rc, out)
	}
	f := e.transcript(s)
	b, _ := os.ReadFile(f)
	b = bytes.TrimRight(b, "\n")
	os.WriteFile(f, b[:len(b)-len(b[bytes.LastIndexByte(b, '\n')+1:])/2], 0o600)
	e.mu.Lock()
	before := len(e.bodies)
	e.mu.Unlock()
	e.run("--resume", s, "Reply with the single word OK.")
	for i := 0; ; i++ {
		e.mu.Lock()
		sent := strings.Join(e.bodies[before:], "\n")
		e.mu.Unlock()
		if strings.Contains(sent, "Reply with the single word OK.") {
			if !strings.Contains(sent, "Reply with the single word READY.") {
				t.Fatalf("the resumed turn lost the history:\n%.3000s", sent)
			}
			return
		}
		if i == 450 {
			t.Fatalf("no resumed turn in 90 s:\n%.3000s", e.out.String())
		}
		time.Sleep(200 * time.Millisecond)
	}
}
