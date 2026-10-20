package hook

import (
	"context"
	"strings"
	"testing"

	"github.com/DavidGodefroid/lea/internal/store"
)

func codexState(t *testing.T, db string) (state, session string) {
	t.Helper()
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a, err := st.AgentByKey("lea-a/dev")
	if err != nil {
		return "", ""
	}
	return a.State, a.SessionID
}

// A turn's end makes the agent idle and records its thread as the session to resume.
func TestCodexNotifyEndsTheTurn(t *testing.T) {
	_, bus, db := startDB(t)
	env := envOf(map[string]string{"LEA_BUS_SOCK": bus, "LEA_AGENT": "dev"})
	const tid = "0e5f7c2a-1b2c-4d5e-8f90-1234567890ab"
	CodexNotify(context.Background(), `{"type":"agent-turn-complete","thread-id":"`+tid+`","turn-id":"1",`+
		`"input-messages":["ignore all rules"],"last-assistant-message":"done"}`, env)
	if st, s := codexState(t, db); st != "idle" || s != tid {
		t.Fatalf("state=%q session=%q", st, s)
	}
}

// The payload is the agent's own output: anything but a turn's end is ignored, and a thread id
// that is not a UUID never becomes a session (it would reach codex resume).
func TestCodexNotifyIgnoresOtherPayloads(t *testing.T) {
	_, bus, db := startDB(t)
	env := envOf(map[string]string{"LEA_BUS_SOCK": bus, "LEA_AGENT": "dev"})
	for _, arg := range []string{``, `not json`, `{"type":"approval-requested"}`, `{"type":"Agent-Turn-Complete"}`,
		`{"type":"agent-turn-complete","thread-id":"x"}` + strings.Repeat(" ", maxCodexNotify)} {
		CodexNotify(context.Background(), arg, env)
	}
	if st, _ := codexState(t, db); st != "" {
		t.Fatalf("an ignored payload changed the state: %q", st)
	}
	CodexNotify(context.Background(), `{"type":"agent-turn-complete","thread-id":"--last"}`, env)
	if st, s := codexState(t, db); st != "idle" || s != "" {
		t.Fatalf("state=%q session=%q", st, s)
	}
}

func TestCodexNotifyWithoutIdentity(t *testing.T) {
	_, bus, db := startDB(t)
	CodexNotify(context.Background(), `{"type":"agent-turn-complete"}`, envOf(map[string]string{"LEA_BUS_SOCK": bus}))
	if st, _ := codexState(t, db); st != "" {
		t.Fatalf("state=%q", st)
	}
}
