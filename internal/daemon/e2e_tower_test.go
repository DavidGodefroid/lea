package daemon

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/store"
)

// A Control Tower resumed with its session in a new container (lea deck-open --fresh: lea marks
// it stopped, the next container starts Claude again with --resume) keeps its tower mark (#383):
// the real Claude Code, with lea's hooks and plugin, runs /lea:control-tower, ends with its
// container, and its resumed session's first tool call, a Bash git push, is denied without the
// skill running again. Set LEA_E2E_CLAUDE to a claude binary to run it.
func TestClaudeResumedTowerKeepsItsGuard(t *testing.T) {
	e := newClaudeE2E(t)
	e.setLimited(false)
	// Absolute: claude runs in its working directory (e.wd).
	plugin, err := filepath.Abs("../../image/claude-plugin")
	if err != nil {
		t.Fatal(err)
	}
	kill := e.run("--plugin-dir", plugin, "/lea:control-tower")
	a := e.until("the tower's first turn", 45, func(a store.Agent) bool {
		ps, _ := e.st.EventPayloads(a.Key, "Stop")
		return a.State == "idle" && len(a.SessionID) == 36 && len(ps) > 0
	})
	if on, err := e.st.Tower(a.Key); err != nil || !on {
		t.Fatalf("/lea:control-tower did not turn tower mode on: on=%v err=%v\n%s", on, err, e.out.String())
	}
	if err := e.st.SetState(a.Key, "stopped", "", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	kill()
	if err := e.st.SetState(a.Key, "unknown", "", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.bodies = nil
	e.answer = func(body string) (string, string) {
		if strings.Contains(body, `"tool_result"`) {
			return "", ""
		}
		return "Bash", `{"command":"git push","description":"push"}`
	}
	e.mu.Unlock()
	e.run("--plugin-dir", plugin, "--resume", a.SessionID, "push the branch")
	for end := time.Now().Add(60 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		e.mu.Lock()
		var denied, ran bool
		for _, b := range e.bodies {
			if strings.Contains(b, `"tool_result"`) {
				denied = denied || strings.Contains(b, "you are the control tower, you do not act yourself")
				ran = true
			}
		}
		e.mu.Unlock()
		if denied {
			break
		}
		if ran || time.Now().After(end) {
			e.mu.Lock()
			defer e.mu.Unlock()
			t.Fatalf("the resumed tower's Bash call was not denied by the tower guard (tool result seen: %v)\n%.4000s\n%s",
				ran, strings.Join(e.bodies, "\n"), e.out.String())
		}
	}
}
