package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"
	"unicode/utf8"

	"github.com/DavidGodefroid/lea/internal/bus"
	"github.com/DavidGodefroid/lea/internal/store"
)

// One question at a time to the user (#95): a control tower asks the user through user_ask. With
// no question open, the question opens and the tower asks it; otherwise it waits in the tower's
// queue (store.AskUser). The user's next prompt answers the open question and opens the head of
// the queue, which lea gives the tower in that prompt's context. While the user has a question,
// the questions agents ask the tower (ask) wait for the next prompt, and the tower may show no
// other one (AskUserQuestion is denied).

const (
	// maxQuestion caps a question's text, which reaches the tower's context again.
	maxQuestion = 4 << 10
	// DefaultQuestionRemind is how long an unanswered question waits before lea reminds the tower
	// of it, once (LEA_QUESTION_REMIND).
	DefaultQuestionRemind = 2 * time.Hour
	// questionTick is how often lea looks for a question to remind.
	questionTick = time.Minute
)

// askQuestionTool is Claude Code's tool that puts a question to the user.
const askQuestionTool = "AskUserQuestion"

// SetQuestionRemind is how long an open question waits for the user before lea reminds the tower
// of it; 0 never reminds.
func (d *Daemon) SetQuestionRemind(r time.Duration) {
	d.mu.Lock()
	d.questionRemind = r
	d.mu.Unlock()
}

type questionView struct {
	ID       int64  `json:"id"`
	Agent    string `json:"agent"`
	From     string `json:"from,omitempty"`
	Message  int64  `json:"message,omitempty"`
	Priority string `json:"priority"`
	State    string `json:"state"`
	Body     string `json:"body"`
	AskedAt  int64  `json:"asked_at,omitempty"`
	Created  int64  `json:"created"`
}

func viewQuestion(q store.UserQuestion) questionView {
	return questionView{q.ID, q.Agent, q.From, q.Message, q.Priority, q.State, q.Body, q.AskedAt, q.Created}
}

// userAsk is the tower's user_ask: a new question for the user, or (id) an answered one asked
// again.
func (d *Daemon) userAsk(o Origin, me string, raw json.RawMessage) (any, error) {
	if o.Container == "" {
		return nil, errors.New("user_ask: agents only")
	}
	a, err := d.st.AgentByKey(me)
	if err != nil {
		return nil, err
	}
	if !canSpawn(a.Role) {
		return nil, errors.New("user_ask: the control tower's (leads only): send your question to your lead")
	}
	var p struct {
		Question string `json:"question"`
		Priority string `json:"priority"`
		From     string `json:"from"`
		Message  int64  `json:"message"`
		ID       int64  `json:"id"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.ID != 0 {
		if p.Question != "" || p.From != "" || p.Message != 0 {
			return nil, errors.New("user_ask: id asks an answered question again: give no question, from or message")
		}
		q, err := d.st.Reask(me, p.ID, d.now())
		if errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("user_ask: you have no question #%d", p.ID)
		}
		if errors.Is(err, store.ErrDecided) {
			return nil, fmt.Errorf("user_ask: question #%d is not answered: only an answered question is asked again", p.ID)
		}
		if err != nil {
			return nil, err
		}
		d.changed()
		return d.askAnswer(q)
	}
	if p.Priority == "" {
		p.Priority = store.PriorityNormal
	}
	if !store.ValidPriority(p.Priority) {
		return nil, errors.New("user_ask: priority must be incident, blocking or normal")
	}
	if p.Question == "" || len(p.Question) > maxQuestion || !utf8.ValidString(p.Question) {
		return nil, fmt.Errorf("user_ask: question must be UTF-8 text, 1 to %d bytes", maxQuestion)
	}
	from := ""
	if p.From != "" {
		ks, err := d.keys()
		if err != nil {
			return nil, err
		}
		if from, err = bus.Resolve(p.From, ks); err != nil {
			return nil, fmt.Errorf("user_ask: from: %w", err)
		}
	}
	if p.Message != 0 {
		m, err := d.st.Get(p.Message)
		if err != nil || m.To != me {
			return nil, fmt.Errorf("user_ask: message %d is not addressed to you", p.Message)
		}
		if from == "" {
			from = m.From
		} else if m.From != from {
			return nil, fmt.Errorf("user_ask: message %d does not come from %s", p.Message, p.From)
		}
	}
	q, err := d.st.AskUser(store.UserQuestion{Agent: me, From: from, Message: p.Message, Body: p.Question, Priority: p.Priority}, d.now())
	if err != nil {
		return nil, err
	}
	d.changed()
	return d.askAnswer(q)
}

// askAnswer is user_ask's answer for question q as it now stands.
func (d *Daemon) askAnswer(q store.UserQuestion) (map[string]any, error) {
	if q.State == store.QuestionOpen {
		return map[string]any{"state": "ask_now", "id": q.ID,
			"text": fmt.Sprintf("Question #%d is open: ask it to the user now, in this reply, and no other question. The user's next prompt answers it.", q.ID)}, nil
	}
	qs, err := d.st.QueuedQuestions(q.Agent)
	if err != nil {
		return nil, err
	}
	pos := 0
	for i, x := range qs {
		if x.ID == q.ID {
			pos = i + 1
		}
	}
	return map[string]any{"state": "queued", "id": q.ID, "position": pos,
		"text": fmt.Sprintf("queued #%d (position %d): the user has another question open. Do not mention this one to the user: lea gives it back to you once the open one is answered.", q.ID, pos)}, nil
}

// questionAnswered is what a prompt of the user to me answers: the open question it closes and
// the one that opens, for that prompt's context ("" when me had none open). A prompt lea typed
// (lea is true: a resume, a first prompt) answers nothing.
func (d *Daemon) questionAnswered(me string, lea bool) string {
	d.mu.Lock()
	if d.leaPrompts[me] {
		delete(d.leaPrompts, me)
		lea = true
	}
	d.mu.Unlock()
	if lea {
		return ""
	}
	a, next, err := d.st.AnswerQuestion(me, d.now())
	if errors.Is(err, store.ErrNotFound) {
		return ""
	}
	if err != nil {
		log.Printf("lea daemon: answer the question of %s: %v", me, err)
		return ""
	}
	d.changed()
	text := fmt.Sprintf("lea: question #%d is answered by this prompt.", a.ID)
	switch {
	case a.Message != 0:
		text += fmt.Sprintf(" Relay the answer to %s with msg_reply on message %d.", a.From, a.Message)
	case a.From != "":
		text += fmt.Sprintf(" Relay the answer to %s with msg_send.", a.From)
	}
	text += fmt.Sprintf(" If this prompt answers something else, call user_ask with id %d: lea asks it again.", a.ID)
	if next.ID != 0 {
		from := ""
		if next.From != "" {
			from = " from " + next.From
		}
		text += fmt.Sprintf(" Question #%d is now open (%s%s): %q. Ask it to the user in this reply, after handling the answer, and no other question.",
			next.ID, next.Priority, from, next.Body)
	} else {
		text += " No other question waits."
	}
	return text
}

// questionShown records that me's turn ended: the user has its open question.
func (d *Daemon) questionShown(me string) {
	if first, err := d.st.ShowQuestion(me); err != nil {
		log.Printf("lea daemon: show the question of %s: %v", me, err)
	} else if first {
		d.changed()
	}
}

// askGuard is lea's verdict on a tower's AskUserQuestion ("" lets it through): a tower that asks
// through user_ask may show the user only the question it has open, once.
func (d *Daemon) askGuard(me string) string {
	q, err := d.st.OpenQuestion(me)
	if errors.Is(err, store.ErrNotFound) {
		if used, err := d.st.HasQuestions(me); err != nil || !used {
			return "" // not a tower that uses user_ask
		}
		return "no question is open: call user_ask first; it says whether to ask now or queues the question"
	}
	if err != nil {
		return "lea could not read your open question: retry in a moment"
	}
	if q.Shown {
		return fmt.Sprintf("a question is already open (#%d): use user_ask, it will be queued", q.ID)
	}
	d.questionShown(me)
	return ""
}

// holdsAsks says whether me's inbox holds the questions agents ask it: the user has its open
// question, so a new one would wait for the next prompt anyway.
func (d *Daemon) holdsAsks(me string) bool {
	q, err := d.st.OpenQuestion(me)
	return err == nil && q.Shown
}

// RunQuestionReminders reminds a tower, once, of a question the user has left unanswered for the
// remind delay, until ctx ends.
func (d *Daemon) RunQuestionReminders(ctx context.Context) {
	t := time.NewTicker(questionTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.remindQuestions()
		}
	}
}

func (d *Daemon) remindQuestions() {
	d.mu.Lock()
	r := d.questionRemind
	d.mu.Unlock()
	if r <= 0 {
		return
	}
	qs, err := d.st.QuestionsToRemind(d.now() - int64(r/time.Second))
	if err != nil {
		log.Printf("lea daemon: questions to remind: %v", err)
		return
	}
	for _, q := range qs {
		if first, err := d.st.MarkReminded(q.ID); err != nil || !first {
			continue
		}
		d.post("lea", q.Agent, "note", fmt.Sprintf("lea: the user has not answered question #%d yet. Remind them once: write it to them again, exactly as before, "+
			"prefixed with \"reminder: \", and ask nothing else. The question: %q", q.ID, q.Body), 0)
	}
}

// questionList is lea questions: every open and queued question.
func (d *Daemon) questionList() (any, error) {
	qs, err := d.st.PendingQuestions()
	if err != nil {
		return nil, err
	}
	out := make([]questionView, 0, len(qs))
	for _, q := range qs {
		out = append(out, viewQuestion(q))
	}
	return out, nil
}

// questionDrop is lea questions drop: the user drops a queued question; it is never asked.
func (d *Daemon) questionDrop(raw json.RawMessage) (any, error) {
	var p struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	q, err := d.st.DropQuestion(p.ID, d.now())
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("no question #%d", p.ID)
	}
	if errors.Is(err, store.ErrDecided) {
		return nil, fmt.Errorf("question #%d is %s: only a queued question is dropped", p.ID, q.State)
	}
	if err != nil {
		return nil, err
	}
	d.changed()
	return viewQuestion(q), nil
}
