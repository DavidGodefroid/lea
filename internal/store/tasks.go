package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// Task is an agent's line on the board (spec A13): what it works on (Title, from its brief), its
// latest status, what comes next and what blocks it. It outlives its agent: Ended is set when the
// agent is forgotten or gets a new task.
type Task struct {
	ID                          int64
	Container, Agent, Title     string
	StatusText, Next, BlockedOn string
	Created, Updated, Ended     int64
}

const taskCols = `id, container, agent, title, status_text, next, blocked_on, created, updated, ended`

func scanTask(sc interface{ Scan(...any) error }) (Task, error) {
	var t Task
	err := sc.Scan(&t.ID, &t.Container, &t.Agent, &t.Title, &t.StatusText, &t.Next, &t.BlockedOn, &t.Created, &t.Updated, &t.Ended)
	return t, err
}

func (s *Store) queryTasks(q string, args ...any) ([]Task, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// CreateTask opens an agent's task and links it (agents.task_id); a task the agent's name had
// before is ended. ErrNotFound when the agent is not registered.
func (s *Store) CreateTask(container, agent, title string, now int64) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE tasks SET ended=? WHERE agent=? AND ended=0`, now, agent); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`INSERT INTO tasks(container, agent, title, created, updated) VALUES(?,?,?,?,?)`, container, agent, title, now, now)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	res, err = tx.Exec(`UPDATE agents SET task_id=? WHERE key=?`, id, agent)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return 0, fmt.Errorf("task: agent %s: %w", nameOf(agent), ErrNotFound)
	}
	return id, tx.Commit()
}

// DeleteTask removes a task (a spawn rolled back) and unlinks it from its agent.
func (s *Store) DeleteTask(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE agents SET task_id=0 WHERE task_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM tasks WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// TaskOf returns the open task of an agent; ErrNotFound when it has none.
func (s *Store) TaskOf(agent string) (Task, error) {
	t, err := scanTask(s.db.QueryRow(`SELECT `+taskCols+` FROM tasks
		WHERE id=(SELECT task_id FROM agents WHERE key=?) AND ended=0`, agent))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	return t, err
}

// Tasks lists the open tasks of a container ("" : every container), oldest first, then at most
// ended of the tasks that ended last, latest first.
func (s *Store) Tasks(container string, ended int) ([]Task, error) {
	open, err := s.queryTasks(`SELECT `+taskCols+` FROM tasks WHERE ended=0 AND (?='' OR container=?)
		ORDER BY created, id`, container, container)
	if err != nil || ended <= 0 {
		return open, err
	}
	old, err := s.queryTasks(`SELECT `+taskCols+` FROM tasks WHERE ended<>0 AND (?='' OR container=?)
		ORDER BY ended DESC, id DESC LIMIT ?`, container, container, ended)
	return append(open, old...), err
}

// UpdateTask records an agent's report on its own task (task_update) and reports whether anything
// changed. An agent without an open task (registered by a hook, or started before tasks existed)
// gets one. ErrNotFound when the agent is not registered.
func (s *Store) UpdateTask(agent, status, next, blocked string, now int64) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var container string
	var id int64
	err = tx.QueryRow(`SELECT container, task_id FROM agents WHERE key=?`, agent).Scan(&container, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}
	var open int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM tasks WHERE id=? AND ended=0`, id).Scan(&open); err != nil {
		return false, err
	}
	if open == 0 {
		res, err := tx.Exec(`INSERT INTO tasks(container, agent, created, updated) VALUES(?,?,?,?)`, container, agent, now, now)
		if err != nil {
			return false, err
		}
		if id, err = res.LastInsertId(); err != nil {
			return false, err
		}
		if _, err := tx.Exec(`UPDATE agents SET task_id=? WHERE key=?`, id, agent); err != nil {
			return false, err
		}
	}
	res, err := tx.Exec(`UPDATE tasks SET status_text=?, next=?, blocked_on=?, updated=?
		WHERE id=? AND (status_text<>? OR next<>? OR blocked_on<>?)`, status, next, blocked, now, id, status, next, blocked)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return n > 0 || open == 0, nil
}

// TaskStatuses maps the agents of a container to the status text of their open task.
func (s *Store) TaskStatuses(container string) (map[string]string, error) {
	rows, err := s.db.Query(`SELECT a.key, t.status_text FROM agents a JOIN tasks t ON t.id=a.task_id
		WHERE a.container=? AND t.ended=0 AND t.status_text<>''`, container)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}
