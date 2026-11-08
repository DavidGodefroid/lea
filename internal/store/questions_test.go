package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func ask(t *testing.T, s *Store, agent, body, prio string, now int64) UserQuestion {
	t.Helper()
	q, err := s.AskUser(UserQuestion{Agent: agent, From: "lea-a/lead-2", Body: body, Priority: prio}, now)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// One question open at a time; the queue goes by priority, then in order; an answer opens the
// head of the queue (#95).
func TestUserQuestionsQueue(t *testing.T) {
	s := open(t)
	const tw = "lea-a/claude-1"
	q1 := ask(t, s, tw, "one", PriorityNormal, 10)
	q2 := ask(t, s, tw, "two", PriorityNormal, 11)
	q3 := ask(t, s, tw, "three", PriorityIncident, 12)
	q4 := ask(t, s, tw, "four", PriorityBlocking, 13)
	if q1.State != QuestionOpen || q1.AskedAt != 10 || q2.State != QuestionQueued || q3.State != QuestionQueued {
		t.Fatalf("states: %+v %+v %+v", q1, q2, q3)
	}
	if _, err := s.AskUser(UserQuestion{Agent: tw, Body: "x", Priority: "urgent"}, 14); err == nil {
		t.Fatal("unknown priority accepted")
	}
	qs, err := s.QueuedQuestions(tw)
	if err != nil || len(qs) != 3 || qs[0].ID != q3.ID || qs[1].ID != q4.ID || qs[2].ID != q2.ID {
		t.Fatalf("queue = %+v, %v", qs, err)
	}
	// Another tower's queue is its own.
	if o := ask(t, s, "lea-a/claude-9", "other", PriorityNormal, 15); o.State != QuestionOpen {
		t.Fatalf("other tower: %+v", o)
	}
	a, next, err := s.AnswerQuestion(tw, 20)
	if err != nil || a.ID != q1.ID || a.State != QuestionAnswered || a.AnsweredAt != 20 || next.ID != q3.ID || next.AskedAt != 20 {
		t.Fatalf("answer = %+v %+v %v", a, next, err)
	}
	if o, err := s.OpenQuestion(tw); err != nil || o.ID != q3.ID || o.Shown {
		t.Fatalf("open = %+v %v", o, err)
	}
	if first, err := s.ShowQuestion(tw); err != nil || !first {
		t.Fatalf("show: %v %v", first, err)
	}
	if again, _ := s.ShowQuestion(tw); again {
		t.Fatal("shown twice")
	}
	open, queued, err := s.QuestionCounts()
	if err != nil || open[tw] != 1 || queued[tw] != 2 || open["lea-a/claude-9"] != 1 {
		t.Fatalf("counts %v %v %v", open, queued, err)
	}
	for range 3 {
		if _, _, err := s.AnswerQuestion(tw, 30); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.AnswerQuestion(tw, 31); !errors.Is(err, ErrNotFound) {
		t.Fatalf("answer with none open: %v", err)
	}
}

// A prompt that answered something else: the question is asked again, before the rest.
func TestUserQuestionsReask(t *testing.T) {
	s := open(t)
	const tw = "lea-a/claude-1"
	q1 := ask(t, s, tw, "one", PriorityNormal, 10)
	q2 := ask(t, s, tw, "two", PriorityNormal, 11)
	q3 := ask(t, s, tw, "three", PriorityIncident, 12)
	// q1 answered, q3 opens but the user does not have it yet: they swap.
	if _, next, _ := s.AnswerQuestion(tw, 20); next.ID != q3.ID {
		t.Fatalf("next = %+v", next)
	}
	r, err := s.Reask(tw, q1.ID, 21)
	if err != nil || r.State != QuestionOpen || r.AnsweredAt != 0 || r.AskedAt != 21 {
		t.Fatalf("reask = %+v %v", r, err)
	}
	if qs, _ := s.QueuedQuestions(tw); len(qs) != 2 || qs[0].ID != q3.ID || qs[1].ID != q2.ID {
		t.Fatalf("queue after swap = %+v", qs)
	}
	// Only an answered question is asked again.
	if _, err := s.Reask(tw, q2.ID, 22); !errors.Is(err, ErrDecided) {
		t.Fatalf("reask queued: %v", err)
	}
	if _, err := s.Reask("lea-a/claude-9", q1.ID, 22); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reask by another tower: %v", err)
	}
	// q1 answered again; q3 opens and is shown: q1 asked again goes to the head of the queue.
	s.AnswerQuestion(tw, 30)
	s.ShowQuestion(tw)
	if r, err := s.Reask(tw, q1.ID, 31); err != nil || r.State != QuestionQueued {
		t.Fatalf("reask behind a shown one = %+v %v", r, err)
	}
	if o, _ := s.OpenQuestion(tw); o.ID != q3.ID {
		t.Fatalf("open = %+v", o)
	}
	if qs, _ := s.QueuedQuestions(tw); len(qs) != 2 || qs[0].ID != q1.ID {
		t.Fatalf("queue = %+v", qs)
	}
	// No question open: it opens at once.
	s.AnswerQuestion(tw, 40) // q1 opens
	s.AnswerQuestion(tw, 41) // q2 opens
	s.AnswerQuestion(tw, 42) // none
	if r, err := s.Reask(tw, q2.ID, 43); err != nil || r.State != QuestionOpen {
		t.Fatalf("reask with none open = %+v %v", r, err)
	}
}

func TestUserQuestionsDropRemindForget(t *testing.T) {
	s := open(t)
	tw, err := s.UpsertAgent("lea-a", "claude-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	q1 := ask(t, s, tw, "one", PriorityNormal, 10)
	q2 := ask(t, s, tw, "two", PriorityNormal, 11)
	if _, err := s.DropQuestion(q1.ID, 12); !errors.Is(err, ErrDecided) {
		t.Fatalf("drop open: %v", err)
	}
	if d, err := s.DropQuestion(q2.ID, 12); err != nil || d.State != QuestionDropped {
		t.Fatalf("drop = %+v %v", d, err)
	}
	if _, next, _ := s.AnswerQuestion(tw, 13); next.ID != 0 {
		t.Fatalf("a dropped question opened: %+v", next)
	}
	q3 := ask(t, s, tw, "three", PriorityNormal, 100)
	if qs, _ := s.QuestionsToRemind(1000); len(qs) != 0 {
		t.Fatalf("not shown yet, reminded: %+v", qs)
	}
	s.ShowQuestion(tw)
	if qs, _ := s.QuestionsToRemind(99); len(qs) != 0 {
		t.Fatalf("too early: %+v", qs)
	}
	if qs, _ := s.QuestionsToRemind(100); len(qs) != 1 || qs[0].ID != q3.ID {
		t.Fatalf("to remind = %+v", qs)
	}
	if first, _ := s.MarkReminded(q3.ID); !first {
		t.Fatal("not marked")
	}
	if qs, _ := s.QuestionsToRemind(1000); len(qs) != 0 {
		t.Fatalf("reminded twice: %+v", qs)
	}
	if err := s.ForgetAgent(tw); err != nil {
		t.Fatal(err)
	}
	if qs, _ := s.PendingQuestions(); len(qs) != 0 {
		t.Fatalf("questions of a forgotten agent: %+v", qs)
	}
}

// The queue survives a restart: it is in lea.db.
func TestUserQuestionsPersist(t *testing.T) {
	p := filepath.Join(t.TempDir(), "lea.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	ask(t, s, "lea-a/claude-1", "one", PriorityNormal, 10)
	ask(t, s, "lea-a/claude-1", "two", PriorityBlocking, 11)
	s.Close()
	s, err = Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	qs, err := s.PendingQuestions()
	if err != nil || len(qs) != 2 || qs[0].Body != "one" || qs[0].State != QuestionOpen || qs[1].State != QuestionQueued {
		t.Fatalf("after restart: %+v %v", qs, err)
	}
}
