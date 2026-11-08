package statusline

import (
	"bytes"
	"strings"
	"testing"
)

// A tower's line counts its questions to the user and says when the user paused its inbox (#95).
func TestStatusLineShowsTheTowersQuestionsAndHeldInbox(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	for _, c := range []struct {
		ans  Answer
		want string
	}{
		{Answer{}, "Opus"},
		{Answer{QuestionsOpen: 1}, "Opus · ❓ 1"},
		{Answer{QuestionsOpen: 1, QuestionsQueued: 3}, "Opus · ❓ 1 · 3 queued"},
		{Answer{Held: true}, "Opus · ⏸ messages paused (0)"},
		{Answer{QuestionsOpen: 1, QuestionsQueued: 2, Held: true, HeldCount: 4, LoxRun: "off"},
			"Opus · ❓ 1 · 2 queued · ⏸ messages paused (4) · no lox run: start it in the projects directory"},
	} {
		var out bytes.Buffer
		post := func(Report) Answer { return c.ans }
		if err := Run(strings.NewReader(`{"model":{"display_name":"Opus"}}`), &out, "", post); err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(out.String()); got != c.want {
			t.Errorf("%+v: %q, want %q", c.ans, got, c.want)
		}
	}
}
