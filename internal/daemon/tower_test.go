package daemon

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// towerVerdict is grant_check's verdict for agent's tool call: "" when it gives none.
func towerVerdict(t *testing.T, e env, agent, tool string, input map[string]any) (string, string) {
	t.Helper()
	if input == nil {
		input = map[string]any{}
	}
	var r struct{ Verdict, Reason string }
	if err := call(t, e.bus, agent, "grant_check", map[string]any{"tool": tool, "input": input, "cwd": "/p"}, &r); err != nil {
		t.Fatal(err)
	}
	return r.Verdict, r.Reason
}

func towerPrompt(t *testing.T, e env, agent, session, prompt string) {
	t.Helper()
	if err := call(t, e.bus, agent, "tower_prompt", map[string]any{"prompt": prompt, "session_id": session}, nil); err != nil {
		t.Fatal(err)
	}
}

// towerAgent makes agent known to the daemon on session, as its first hook does.
func towerAgent(t *testing.T, e env, agent, session string) {
	t.Helper()
	if err := call(t, e.bus, agent, "hook_event", map[string]any{"event": "SessionStart", "session_id": session,
		"payload": map[string]any{"source": "startup"}}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestTowerModeHoldsTheAgentToTheTeamsTools(t *testing.T) {
	e := start(t)
	towerAgent(t, e, "claude-1", "s1")
	if v, _ := towerVerdict(t, e, "claude-1", "Edit", nil); v != "" {
		t.Fatalf("outside tower mode: %q", v)
	}
	towerPrompt(t, e, "claude-1", "s1", "/lea:control-tower")
	for _, tool := range []string{"mcp__lea__agents_spawn", "mcp__lea__agents_list", "mcp__lea__agents_stop", "mcp__lea__agents_request_spawn",
		"mcp__lea__msg_send", "mcp__lea__msg_reply", "mcp__lea__msg_inbox", "mcp__lea__msg_wait", "mcp__lea__board_read",
		"mcp__lea__task_update", "mcp__lea__grants_list", "mcp__lea__grants_request", "AskUserQuestion", "ToolSearch", "TodoWrite", "Skill"} {
		if v, _ := towerVerdict(t, e, "claude-1", tool, nil); v == "deny" {
			t.Fatalf("%s denied in tower mode", tool)
		}
	}
	for _, tool := range []string{"Edit", "Write", "Read", "Grep", "Glob", "Agent", "WebFetch", "NotebookEdit",
		"mcp__lea__lease_acquire", "mcp__github__create_pull_request", "mcp__lea__tower_prompt"} {
		if v, why := towerVerdict(t, e, "claude-1", tool, nil); v != "deny" || !strings.Contains(why, "Give this to an agent") ||
			!strings.Contains(why, "'/lea:control-tower off'") {
			t.Fatalf("%s in tower mode: %q %q", tool, v, why)
		}
	}
	// Bash runs the inbox wait only.
	for _, c := range []string{"lea inbox --agent --wait", "lea inbox --agent --wait 3600"} {
		if v, _ := towerVerdict(t, e, "claude-1", "Bash", map[string]any{"command": c}); v == "deny" {
			t.Fatalf("%q denied", c)
		}
	}
	for _, c := range []string{"git push", "gh pr create", "lea inbox --agent --wait 60; git push", "lea inbox --agent --wait 60 && rm -rf x",
		"lea inbox --agent --wait 60\ngit push", "lea inbox --agent --wait $(git push)", "lea inbox --agent --wait ",
		"curl https://api.github.com", "sqlite3 lea.db 'UPDATE agents SET tower=0'"} {
		if v, _ := towerVerdict(t, e, "claude-1", "Bash", map[string]any{"command": c}); v != "deny" {
			t.Fatalf("%q in tower mode: %q", c, v)
		}
	}
	// Another agent of the container is not a tower.
	towerAgent(t, e, "dev-1", "s9")
	if v, _ := towerVerdict(t, e, "dev-1", "Edit", nil); v != "" {
		t.Fatalf("another agent inherited tower mode: %q", v)
	}
	towerPrompt(t, e, "claude-1", "s1", "/lea:control-tower off")
	if v, _ := towerVerdict(t, e, "claude-1", "Edit", nil); v != "" {
		t.Fatalf("after off: %q", v)
	}
}

func TestTowerPromptsAndTheSkillTool(t *testing.T) {
	e := start(t)
	towerAgent(t, e, "claude-1", "s1")
	on := func() bool {
		t.Helper()
		v, _ := towerVerdict(t, e, "claude-1", "Edit", nil)
		return v == "deny"
	}
	for _, p := range []string{"explain what /lea:control-tower does", "/check", "/lea:check", "/lea:tower-control", "/tower-control", "/lea:control-towerx"} {
		towerPrompt(t, e, "claude-1", "s1", p)
		if on() {
			t.Fatalf("%q turned tower mode on", p)
		}
	}
	for _, p := range []string{"/lea:control-tower", "  /control-tower  ", "/lea:control-tower take over the team\nmore text", "/control-tower go"} {
		towerPrompt(t, e, "claude-1", "s1", p)
		if !on() {
			t.Fatalf("%q did not turn tower mode on", p)
		}
		towerPrompt(t, e, "claude-1", "s1", "/control-tower off")
		if on() {
			t.Fatal("/control-tower off did not turn it off")
		}
	}
	// The model loads the skill itself: on. The check skill keeps the mode as it is.
	for _, k := range []string{"lea:check", "check", "lea:tower-control", "tower-control"} {
		towerVerdict(t, e, "claude-1", "Skill", map[string]any{"skill": k})
		if on() {
			t.Fatalf("Skill %s turned tower mode on", k)
		}
	}
	for _, k := range []string{"lea:control-tower", "control-tower"} {
		towerVerdict(t, e, "claude-1", "Skill", map[string]any{"skill": k})
		if !on() {
			t.Fatalf("Skill %s did not turn tower mode on", k)
		}
		towerVerdict(t, e, "claude-1", "Skill", map[string]any{"skill": "lea:check"})
		if !on() {
			t.Fatal("Skill lea:check turned tower mode off")
		}
		towerPrompt(t, e, "claude-1", "s1", "/lea:control-tower off")
	}
	// A nested Claude under the agent's identity, on its own session, never turns it off.
	towerPrompt(t, e, "claude-1", "s1", "/lea:control-tower")
	towerPrompt(t, e, "claude-1", "nested", "/lea:control-tower off")
	if !on() {
		t.Fatal("a foreign session's prompt turned tower mode off")
	}
}

// A tower resumed on a new session (lea deck-open --fresh: a new container, Claude's --resume)
// keeps its mark from its first tool call (#383).
func TestTowerModeOutlivesTheContainer(t *testing.T) {
	e := start(t)
	towerAgent(t, e, "claude-1", "s1")
	towerPrompt(t, e, "claude-1", "s1", "/lea:control-tower")
	k := "lea-a/claude-1"
	if err := e.d.st.SetState(k, "stopped", "", 2000); err != nil {
		t.Fatal(err)
	}
	if err := e.d.st.SetState(k, "unknown", "", 2001); err != nil {
		t.Fatal(err)
	}
	if err := call(t, e.bus, "claude-1", "hook_event", map[string]any{"event": "SessionStart", "session_id": "s2",
		"payload": map[string]any{"source": "resume"}}, nil); err != nil {
		t.Fatal(err)
	}
	if v, _ := towerVerdict(t, e, "claude-1", "Bash", map[string]any{"command": "git push"}); v != "deny" {
		t.Fatalf("the resumed tower runs Bash: %q", v)
	}
}

// Every lea tool the plugin's control-tower and check skills tell a tower to call passes the
// tower guard.
func TestTowerModeLetsThroughEveryLeaToolItsSkillsName(t *testing.T) {
	e := start(t)
	towerAgent(t, e, "claude-1", "s1")
	towerPrompt(t, e, "claude-1", "s1", "/lea:control-tower")
	re := regexp.MustCompile("`((agents|msg|board|task|lease|grants)_[a-z_]+)`")
	for _, k := range []string{"control-tower", "check"} {
		b, err := os.ReadFile("../../image/claude-plugin/skills/" + k + "/SKILL.md")
		if err != nil {
			t.Fatal(err)
		}
		ms := re.FindAllStringSubmatch(string(b), -1)
		if len(ms) == 0 {
			t.Fatalf("skill %s names no lea tool", k)
		}
		for _, m := range ms {
			if v, _ := towerVerdict(t, e, "claude-1", "mcp__lea__"+m[1], nil); v == "deny" {
				t.Fatalf("skill %s names %s, which tower mode denies", k, m[1])
			}
		}
	}
}
