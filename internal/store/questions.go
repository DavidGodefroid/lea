package store

import (
	"database/sql"
	"errors"
)

// Questions to the user (#95): a control tower has at most one question open to the user at a
// time; the others wait in its queue, by priority then in the order they came. The user's next
// prompt answers the open one, and the head of the queue opens. The queue is persisted: it
// survives a daemon restart.

// The states of a question.
const (
	QuestionOpen     = "open"
	QuestionQueued   = "queued"
	QuestionAnswered = "answered"
	QuestionDropped  = "dropped"
)

// The priorities of a question: an incident goes first, then what blocks an agent, then the rest.
const (
	PriorityIncident = "incident"
	PriorityBlocking = "blocking"
	PriorityNormal   = "normal"
)

var priorityRank = map[string]int{PriorityIncident: 0, PriorityBlocking: 1, PriorityNormal: 2}

// frontRank is the rank of a question asked again: it goes before every other one.
const frontRank = -1

// ValidPriority reports whether p is one of the three priorities.
func ValidPriority(p string) bool { _, ok := priorityRank[p]; return ok }

// UserQuestion is one question a tower (Agent) has for the user. From is the agent it comes from
// ("" when the tower's own) and Message the message to answer with the user's answer (0: none).
// Shown: the tower's turn ended (or it called AskUserQuestion) since the question opened, so the
// user has it. Reminded: lea reminded the tower of it once.
type UserQuestion struct {
	ID                           int64
	Agent, From                  string
	Message                      int64
	Body, Priority, State        string
	Shown, Reminded              bool
	Created, AskedAt, AnsweredAt int64
}

const questionCols = `id, agent, from_agent, message, body, priority, state, shown, reminded, created, asked_at, answered_at`

func scanQuestion(sc interface{ Scan(...any) error }) (UserQuestion, error) {
	var q UserQuestion
	err := sc.Scan(&q.ID, &q.Agent, &q.From, &q.Message, &q.Body, &q.Priority, &q.State, &q.Shown, &q.Reminded,
		&q.Created, &q.AskedAt, &q.AnsweredAt)
	return q, err
}

// queueOrder orders a tower's queue: its head first.
const queueOrder = ` ORDER BY rank, seq`

type querier interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}

func questions(db querier, where string, args ...any) ([]UserQuestion, error) {
	rows, err := db.Query(`SELECT `+questionCols+` FROM user_questions WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserQuestion
	for rows.Next() {
		q, err := scanQuestion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

func question(db querier, where string, args ...any) (UserQuestion, error) {
	q, err := scanQuestion(db.QueryRow(`SELECT `+questionCols+` FROM user_questions WHERE `+where, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return UserQuestion{}, ErrNotFound
	}
	return q, err
}

// AskUser records q (Agent, From, Message, Body, Priority) for the user: open when its tower has
// no open question, queued otherwise. It returns the question as recorded.
func (s *Store) AskUser(q UserQuestion, now int64) (UserQuestion, error) {
	rank, ok := priorityRank[q.Priority]
	if !ok {
		return UserQuestion{}, errors.New("priority must be incident, blocking or normal")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return UserQuestion{}, err
	}
	defer tx.Rollback()
	state, asked := QuestionQueued, int64(0)
	if _, err := question(tx, `agent=? AND state=?`, q.Agent, QuestionOpen); errors.Is(err, ErrNotFound) {
		state, asked = QuestionOpen, now
	} else if err != nil {
		return UserQuestion{}, err
	}
	r, err := tx.Exec(`INSERT INTO user_questions(agent, from_agent, message, body, priority, rank, seq, state, created, asked_at)
		VALUES(?,?,?,?,?,?,0,?,?,?)`, q.Agent, q.From, q.Message, q.Body, q.Priority, rank, state, now, asked)
	if err != nil {
		return UserQuestion{}, err
	}
	id, err := r.LastInsertId()
	if err != nil {
		return UserQuestion{}, err
	}
	if _, err := tx.Exec(`UPDATE user_questions SET seq=id WHERE id=?`, id); err != nil {
		return UserQuestion{}, err
	}
	out, err := question(tx, `id=?`, id)
	if err != nil {
		return UserQuestion{}, err
	}
	return out, tx.Commit()
}

// OpenQuestion is the question agent has open to the user; ErrNotFound when none.
func (s *Store) OpenQuestion(agent string) (UserQuestion, error) {
	return question(s.db, `agent=? AND state=?`, agent, QuestionOpen)
}

// QueuedQuestions is agent's queue, its head first.
func (s *Store) QueuedQuestions(agent string) ([]UserQuestion, error) {
	return questions(s.db, `agent=? AND state=?`+queueOrder, agent, QuestionQueued)
}

// PendingQuestions is every open and queued question, each tower's open one first, then its
// queue in order.
func (s *Store) PendingQuestions() ([]UserQuestion, error) {
	return questions(s.db, `state IN ('open','queued') ORDER BY agent, state='queued', rank, seq`)
}

// HasQuestions reports whether agent ever asked the user through lea: it is a tower that uses
// the queue.
func (s *Store) HasQuestions(agent string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM user_questions WHERE agent=? LIMIT 1`, agent).Scan(&n)
	return n > 0, err
}

// GetQuestion is question id; ErrNotFound when there is none.
func (s *Store) GetQuestion(id int64) (UserQuestion, error) {
	return question(s.db, `id=?`, id)
}

// promote opens the head of agent's queue, if any, and returns it (ID 0: the queue was empty).
func promote(tx *sql.Tx, agent string, now int64) (UserQuestion, error) {
	q, err := question(tx, `agent=? AND state=?`+queueOrder+` LIMIT 1`, agent, QuestionQueued)
	if errors.Is(err, ErrNotFound) {
		return UserQuestion{}, nil
	}
	if err != nil {
		return UserQuestion{}, err
	}
	if _, err := tx.Exec(`UPDATE user_questions SET state=?, asked_at=?, shown=0, reminded=0 WHERE id=?`,
		QuestionOpen, now, q.ID); err != nil {
		return UserQuestion{}, err
	}
	q.State, q.AskedAt, q.Shown, q.Reminded = QuestionOpen, now, false, false
	return q, nil
}

// AnswerQuestion records that the user answered agent's open question, and opens the head of its
// queue. It returns the answered question and the one now open (ID 0: none); ErrNotFound when
// agent had no open question.
func (s *Store) AnswerQuestion(agent string, now int64) (answered, next UserQuestion, err error) {
	return s.answer(now, `agent=? AND state=?`, agent, QuestionOpen)
}

// AnswerQuestionByID is AnswerQuestion for question id (lea web answers it, #415): ErrNotFound
// when there is no question id, ErrDecided when it is not the open one (queued or closed).
func (s *Store) AnswerQuestionByID(id, now int64) (answered, next UserQuestion, err error) {
	q, err := s.GetQuestion(id)
	if err != nil {
		return
	}
	if q.State != QuestionOpen {
		return q, UserQuestion{}, ErrDecided
	}
	answered, next, err = s.answer(now, `id=? AND state=?`, id, QuestionOpen)
	if errors.Is(err, ErrNotFound) {
		err = ErrDecided // answered meanwhile
	}
	return
}

// answer closes the open question where selects, and opens the head of its tower's queue.
func (s *Store) answer(now int64, where string, args ...any) (answered, next UserQuestion, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	if answered, err = question(tx, where, args...); err != nil {
		return
	}
	if _, err = tx.Exec(`UPDATE user_questions SET state=?, answered_at=? WHERE id=?`, QuestionAnswered, now, answered.ID); err != nil {
		return
	}
	answered.State, answered.AnsweredAt = QuestionAnswered, now
	if next, err = promote(tx, answered.Agent, now); err != nil {
		return
	}
	err = tx.Commit()
	return
}

// ShowQuestion records that the user has agent's open question (the tower's turn that asked it
// ended); it reports whether it was not shown before.
func (s *Store) ShowQuestion(agent string) (bool, error) {
	r, err := s.db.Exec(`UPDATE user_questions SET shown=1 WHERE agent=? AND state=? AND shown=0`, agent, QuestionOpen)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n > 0, err
}

// Reask asks the user again agent's answered question id (the prompt that closed it answered
// something else). With no question open, it opens again. With an open one the user does not
// have yet (it opened when id closed), the two swap: id opens and the other goes back to the head
// of the queue. Otherwise id goes to the head of the queue.
func (s *Store) Reask(agent string, id, now int64) (UserQuestion, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return UserQuestion{}, err
	}
	defer tx.Rollback()
	q, err := question(tx, `id=? AND agent=?`, id, agent)
	if err != nil {
		return UserQuestion{}, err
	}
	if q.State != QuestionAnswered {
		return UserQuestion{}, ErrDecided
	}
	front := func(qid int64) error {
		var min int64
		if err := tx.QueryRow(`SELECT COALESCE(MIN(seq), 0) FROM user_questions WHERE agent=?`, agent).Scan(&min); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE user_questions SET state=?, rank=?, seq=?, asked_at=0, shown=0, reminded=0 WHERE id=?`,
			QuestionQueued, frontRank, min-1, qid)
		return err
	}
	open, err := question(tx, `agent=? AND state=?`, agent, QuestionOpen)
	switch {
	case errors.Is(err, ErrNotFound) || (err == nil && !open.Shown):
		if err == nil {
			if err := front(open.ID); err != nil {
				return UserQuestion{}, err
			}
		}
		if _, err := tx.Exec(`UPDATE user_questions SET state=?, asked_at=?, answered_at=0, shown=0, reminded=0 WHERE id=?`,
			QuestionOpen, now, id); err != nil {
			return UserQuestion{}, err
		}
	case err != nil:
		return UserQuestion{}, err
	default:
		if err := front(id); err != nil {
			return UserQuestion{}, err
		}
		if _, err := tx.Exec(`UPDATE user_questions SET answered_at=0 WHERE id=?`, id); err != nil {
			return UserQuestion{}, err
		}
	}
	out, err := question(tx, `id=?`, id)
	if err != nil {
		return UserQuestion{}, err
	}
	return out, tx.Commit()
}

// DropQuestion drops a queued question (the user's choice): it is never asked. ErrDecided when it
// is not queued.
func (s *Store) DropQuestion(id, now int64) (UserQuestion, error) {
	r, err := s.db.Exec(`UPDATE user_questions SET state=?, answered_at=? WHERE id=? AND state=?`, QuestionDropped, now, id, QuestionQueued)
	if err != nil {
		return UserQuestion{}, err
	}
	q, err := s.GetQuestion(id)
	if err != nil {
		return UserQuestion{}, err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return q, ErrDecided
	}
	return q, nil
}

// QuestionsToRemind is the open questions the user has had since before before, never reminded.
func (s *Store) QuestionsToRemind(before int64) ([]UserQuestion, error) {
	return questions(s.db, `state=? AND shown=1 AND reminded=0 AND asked_at<=? ORDER BY id`, QuestionOpen, before)
}

// MarkReminded records that lea reminded the tower of question id; it reports whether it was not
// reminded before.
func (s *Store) MarkReminded(id int64) (bool, error) {
	r, err := s.db.Exec(`UPDATE user_questions SET reminded=1 WHERE id=? AND state=? AND reminded=0`, id, QuestionOpen)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n > 0, err
}

// QuestionCounts counts each tower's open (0 or 1) and queued questions.
func (s *Store) QuestionCounts() (open, queued map[string]int, err error) {
	if open, err = s.countBy(`SELECT agent, COUNT(*) FROM user_questions WHERE state=? GROUP BY agent`, QuestionOpen); err != nil {
		return
	}
	queued, err = s.countBy(`SELECT agent, COUNT(*) FROM user_questions WHERE state=? GROUP BY agent`, QuestionQueued)
	return
}
