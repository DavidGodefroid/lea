package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/transcript"
)

// headless runs claude -p with args on the fake API; its JSON result and exit code. A run that
// hangs is killed after 90 s. It runs without lea's hooks: -p waits for their asyncRewake jobs
// (spike SP2 c), which wait for a bus event; the interactive resume below has them.
func (e *claudeE2E) headless(args ...string) (result, session string, rc int, out string) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.claude, append([]string{"-p", "--output-format", "json"}, args...)...)
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

// cutLine cuts the line pick chooses of transcript f in half, drops what follows it, and returns
// the half kept: the transcript of a crash mid-write.
func cutLine(t *testing.T, f string, pick func(ls [][]byte) int) []byte {
	t.Helper()
	b, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	ls := bytes.Split(bytes.TrimRight(b, "\n"), []byte("\n"))
	k := pick(ls)
	if k < 0 || k >= len(ls) {
		t.Fatalf("no line to cut in %d lines", len(ls))
	}
	half := ls[k][:len(ls[k])/2]
	cut := append(append(bytes.Join(ls[:k], []byte("\n")), '\n'), half...)
	if err := os.WriteFile(f, cut, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(f); !bytes.Equal(got, cut) || bytes.Equal(got, b) {
		t.Fatal("the transcript was not cut")
	}
	t.Logf("%d lines, cut line %d (%.60s)", len(ls), k+1, ls[k])
	return half
}

// lastOf picks the last line of type typ (any type when typ is ""), or the last line holding
// marker.
func lastOf(typ, marker string) func([][]byte) int {
	return func(ls [][]byte) int {
		for k := len(ls) - 1; k >= 0; k-- {
			var l struct{ Type string }
			if typ == "" && marker == "" ||
				typ != "" && json.Unmarshal(ls[k], &l) == nil && l.Type == typ ||
				marker != "" && bytes.Contains(ls[k], []byte(marker)) {
				return k
			}
		}
		return -1
	}
}

// allLinesValid checks that every line of f is JSON: nothing was glued to another record.
func allLinesValid(t *testing.T, f string) {
	t.Helper()
	b, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	var bad [][]byte
	for _, l := range bytes.Split(bytes.TrimRight(b, "\n"), []byte("\n")) {
		if !json.Valid(l) {
			bad = append(bad, l)
		}
	}
	if len(bad) != 0 {
		t.Fatalf("invalid lines %q", bad)
	}
}

// repair is what lea-session does before claude --resume (#219): the built lea binary's
// repair-transcript, in the agent's environment. The cut line is dropped, and the file as it was
// is kept beside it.
func (e *claudeE2E) repair(t *testing.T, session, f string, half []byte) {
	t.Helper()
	cut, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(e.lea, "repair-transcript", session)
	cmd.Dir, cmd.Env = e.wd, e.env
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), fmt.Sprintf("%d bytes dropped", len(half))) {
		t.Fatalf("lea repair-transcript: %v\n%s", err, out)
	}
	bs, _ := filepath.Glob(f + transcript.BackupSuffix + "*")
	if len(bs) != 1 {
		t.Fatalf("backups %v", bs)
	}
	if b, _ := os.ReadFile(bs[0]); !bytes.Equal(b, cut) {
		t.Fatal("the backup is not the transcript as it was")
	}
	if b, _ := os.ReadFile(f); !bytes.Equal(b, cut[:len(cut)-len(half)]) {
		t.Fatal("the transcript was not cut after its last valid line")
	}
}

// Spike SP2 (b) (#155): a real Claude resumes a session whose transcript's last JSONL line is
// cut (a crash mid-write): b1, the last line whatever it is; b2, the last assistant message,
// nothing after it; b3, the result of a tool call (an agent killed during a tool call). Claude
// Code 2.1.296 glues its next record onto a cut line (#219): lea repairs the transcript first, as
// lea-session does before every resume, and every line stays valid.
func TestClaudeResumesATruncatedTranscript(t *testing.T) {
	e := newClaudeE2E(t)
	e.setLimited(false)
	for _, c := range []struct {
		name, tool string
		pick       func([][]byte) int
	}{
		{"b1-last-line", "", lastOf("", "")},
		{"b2-last-assistant", "", lastOf("assistant", "")},
		{"b3-tool-result", "Read", lastOf("", `"tool_result"`)},
	} {
		t.Run(c.name, func(t *testing.T) {
			e.mu.Lock()
			e.toolUse = c.tool
			e.mu.Unlock()
			_, s, rc, out := e.headless("Reply with the single word READY.")
			e.mu.Lock()
			e.toolUse = ""
			e.mu.Unlock()
			if rc != 0 || len(s) != 36 {
				t.Fatalf("new session: rc=%d %s", rc, out)
			}
			f := e.transcript(s)
			half := cutLine(t, f, c.pick)
			e.repair(t, s, f, half)
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
			// Claude appended to the repaired file: a second resume still sees the first resume's
			// turn, and every line is valid (nothing was glued to another).
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
				t.Fatalf("the second resume lost the first resume's turn:\n%.3000s", sent)
			}
			allLinesValid(t, f)
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
	cutLine(t, e.transcript(s), lastOf("", ""))
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
