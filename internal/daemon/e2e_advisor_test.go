package daemon

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/providers"
	"github.com/DavidGodefroid/lea/internal/store"
)

// A tower picks its child's model and advisor (#407), end to end with the real Claude Code: the
// lead's agents_spawn with model sonnet and advisor opus reaches the manager, whose command line
// (providers.Argv) starts Claude Code with --model sonnet --advisor opus, and the child's session
// asks the Messages API for Sonnet with Opus as its advisor. Set LEA_E2E_CLAUDE to a claude binary
// to run it.
func TestClaudeTowerSpawnsASonnetChildWithAnOpusAdvisor(t *testing.T) {
	e := newClaudeE2E(t)
	e.setLimited(false)
	fa := &fakeAgents{}
	e.d.SetAgents(fa)
	if _, err := e.st.Register(store.NewAgent{Container: "lea-a", Name: "dev", Profile: "claude", Workdir: e.wd, Role: "lead"}, store.Caps{}, 1); err != nil {
		t.Fatal(err)
	}
	if err := call(t, e.bus, "dev", "agents_spawn", map[string]any{"model": "sonnet", "advisor": "opus", "brief": "review the PR"}, nil); err != nil {
		t.Fatal(err)
	}
	if fa.req.Model != "sonnet" || fa.req.Advisor != "opus" {
		t.Fatalf("request=%+v", fa.req)
	}
	p, err := providers.Builtin().Get(providers.Claude)
	if err != nil {
		t.Fatal(err)
	}
	argv, err := p.Argv("kid", e.wd, fa.req.Model, fa.req.Advisor, "", nil, "hi")
	if err != nil {
		t.Fatal(err)
	}
	// Claude Code offers the advisor tool when its server-side flag says so; the fake API serves
	// none, and this variable stands for it.
	e.env = append(e.env, "CLAUDE_CODE_ENABLE_EXPERIMENTAL_ADVISOR_TOOL=1")
	e.run(argv...)
	for end := time.Now().Add(60 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		e.mu.Lock()
		var turn map[string]any
		for _, b := range e.bodies {
			var m map[string]any
			if _, raw, _ := strings.Cut(b, " "); json.Unmarshal([]byte(raw), &m) == nil && strings.Contains(raw, `"hi"`) && strings.Contains(raw, `"Bash"`) {
				turn = m
				break
			}
		}
		n := len(e.bodies)
		e.mu.Unlock()
		if turn != nil {
			model, _ := turn["model"].(string)
			var advisor string
			tools, _ := turn["tools"].([]any)
			for _, x := range tools {
				if tool, _ := x.(map[string]any); tool != nil && strings.HasPrefix(str(tool["type"]), "advisor") {
					advisor = str(tool["model"])
				}
			}
			if !strings.Contains(model, "sonnet") || !strings.Contains(advisor, "opus") {
				raw, _ := json.Marshal(turn)
				t.Fatalf("the child's session runs on %q with advisor %q:\n%.4000s", model, advisor, raw)
			}
			return
		}
		if time.Now().After(end) {
			t.Fatalf("no turn of the child in 60 s (%d requests)\n%s", n, e.out.String())
		}
	}
}

func str(v any) string { s, _ := v.(string); return s }
