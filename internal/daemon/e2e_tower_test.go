package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/agents"
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

func lastBody(bs []string) string {
	if len(bs) == 0 {
		return ""
	}
	return bs[len(bs)-1]
}
