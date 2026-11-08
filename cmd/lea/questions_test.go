package main

import (
	"strings"
	"testing"

	"github.com/DavidGodefroid/lea/internal/store"
)

// lea questions lists the towers' questions, the open one first; drop removes a queued one (#95).
func TestQuestionsCommand(t *testing.T) {
	api, _, st := startDaemonWithStore(t)
	env := map[string]string{"LEA_DAEMON_SOCK": api}
	if rc, out, _ := runEnv(t, env, "questions"); rc != 0 || !strings.Contains(out, "no question waits") {
		t.Fatalf("empty: rc=%d out=%q", rc, out)
	}
	st.AskUser(store.UserQuestion{Agent: "lea-a/tower", From: "lea-a/l1", Body: "deploy\nnow?", Priority: store.PriorityBlocking}, 1)
	q2, _ := st.AskUser(store.UserQuestion{Agent: "lea-a/tower", Body: "merge?", Priority: store.PriorityNormal}, 2)
	rc, out, e := runEnv(t, env, "questions")
	if rc != 0 || !strings.Contains(out, "#1  lea-a/tower  open    blocking  lea-a/l1  deploy now?") ||
		!strings.Contains(out, "#2  lea-a/tower  queued  normal    -         merge?") {
		t.Fatalf("rc=%d out=%q err=%q", rc, out, e)
	}
	if rc, _, e := runEnv(t, env, "questions", "drop", "1"); rc != 1 || !strings.Contains(e, "only a queued question") {
		t.Fatalf("drop open: rc=%d err=%q", rc, e)
	}
	if rc, out, _ := runEnv(t, env, "questions", "drop", "#2"); rc != 0 || !strings.Contains(out, "dropped question #2") {
		t.Fatalf("drop: rc=%d out=%q", rc, out)
	}
	if q, _ := st.GetQuestion(q2.ID); q.State != store.QuestionDropped {
		t.Fatalf("state = %q", q.State)
	}
	for _, bad := range [][]string{{"questions", "drop"}, {"questions", "drop", "x"}, {"questions", "bogus"}} {
		if rc, _, _ := runEnv(t, env, bad...); rc != 2 {
			t.Fatalf("%v: rc=%d", bad, rc)
		}
	}
}
