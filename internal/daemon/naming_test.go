package daemon

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// namerEnv starts a daemon whose namer records the prompts it gets and answers with reply.
func namerEnv(t *testing.T, reply string, fail error) (env, *[]string, chan struct{}) {
	t.Helper()
	e := start(t)
	var mu sync.Mutex
	var got []string
	e.d.SetNamer(func(ctx context.Context, prompt string) (string, error) {
		mu.Lock()
		got = append(got, prompt)
		mu.Unlock()
		return reply, fail
	})
	done := make(chan struct{}, 4)
	e.d.afterName = func() { done <- struct{}{} }
	spawnedAt(t, e, "dev", "dev", 1000)
	return e, &got, done
}

func wait(t *testing.T, done chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the naming did not end")
	}
}

func label(t *testing.T, e env) string {
	t.Helper()
	a, err := e.d.st.AgentByKey("lea-a/dev")
	if err != nil {
		t.Fatal(err)
	}
	return a.Label
}

func TestTheFirstPromptNamesAnUnlabelledAgentOnce(t *testing.T) {
	e, got, done := namerEnv(t, "\n  \"fix login timeout.\"\nsecond line", nil)
	if err := call(t, e.bus, "dev", "name_hint", map[string]any{"prompt": "fix the infinite retry"}, nil); err != nil {
		t.Fatal(err)
	}
	wait(t, done)
	if l := label(t, e); l != "fix login timeout" {
		t.Fatalf("label %q", l)
	}
	call(t, e.bus, "dev", "name_hint", map[string]any{"prompt": "and now something else"}, nil)
	time.Sleep(50 * time.Millisecond)
	if len(*got) != 1 || (*got)[0] != "fix the infinite retry" {
		t.Fatalf("namer got %q", *got)
	}
}

func TestAUserLabelIsNeverReplaced(t *testing.T) {
	e, got, _ := namerEnv(t, "auto name", nil)
	if err := e.d.st.SetLabel("lea-a/dev", "mine"); err != nil {
		t.Fatal(err)
	}
	call(t, e.bus, "dev", "name_hint", map[string]any{"prompt": "do things"}, nil)
	time.Sleep(50 * time.Millisecond)
	if l := label(t, e); l != "mine" || len(*got) != 0 {
		t.Fatalf("label %q, namer got %q", l, *got)
	}
}

func TestAFailedNamingLeavesNoLabel(t *testing.T) {
	e, _, done := namerEnv(t, "", errors.New("no claude"))
	call(t, e.bus, "dev", "name_hint", map[string]any{"prompt": "do things"}, nil)
	wait(t, done)
	if l := label(t, e); l != "" {
		t.Fatalf("label %q", l)
	}
}

func TestNameHintIsForAgentsAndBounded(t *testing.T) {
	e, _, _ := namerEnv(t, "x", nil)
	if err := call(t, e.api, "", "name_hint", map[string]any{"prompt": "p"}, nil); err == nil {
		t.Fatal("the host API named an agent")
	}
	if err := call(t, e.bus, "dev", "name_hint", map[string]any{"prompt": strings.Repeat("x", MaxNamePrompt+1)}, nil); err == nil {
		t.Fatal("an oversized prompt was accepted")
	}
}

func TestAutoName(t *testing.T) {
	for in, want := range map[string]string{
		"add dark mode":                             "add dark mode",
		"`Name:` refactor\x1b[2J auth":              "Name:` refactor[2J auth",
		"a very long name that goes past the limit": "a very long name that",
		strings.Repeat("x", 30):                     strings.Repeat("x", 24),
	} {
		if got := autoName(in); got != want {
			t.Errorf("autoName(%q) = %q, want %q", in, got, want)
		}
	}
}
