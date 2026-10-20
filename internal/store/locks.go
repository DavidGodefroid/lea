package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// ErrRate: a lock request for that tool came from that container too recently.
var ErrRate = errors.New("rate limit")

// ErrExpired: the lock request is past its time to live; it is closed now.
var ErrExpired = errors.New("expired")

// LockRequest is an agent's request that the user open a lock tool's console (issue #18, phase
// 2). The agent only asks: the user opens it, or not, in the deck. State is pending, opened (the
// user opened it), declined (the user said no), failed (the user said yes and the console did not
// open: Result says why), or expired (nobody answered in time). It is the audit record too: who
// asked (Agent), for what, when, and what came of it.
type LockRequest struct {
	ID                              int64
	Container, Agent, Tool, Profile string
	State, Result                   string
	At, Decided                     int64
}

const lockRequestCols = `id, container, agent, tool, profile, state, result, at, decided`

func scanLockRequest(sc interface{ Scan(...any) error }) (LockRequest, error) {
	var r LockRequest
	err := sc.Scan(&r.ID, &r.Container, &r.Agent, &r.Tool, &r.Profile, &r.State, &r.Result, &r.At, &r.Decided)
	if errors.Is(err, sql.ErrNoRows) {
		return LockRequest{}, ErrNotFound
	}
	return r, err
}

// AddLockRequest records a pending request. The container is the unit: any process in a
// container can claim any agent name, so the limits do not trust the agent. ErrLimit when a request
// for that tool is pending in the container; ErrRate when one for that tool came from the
// container less than gap seconds ago. Pending requests older than ttl expire first.
func (s *Store) AddLockRequest(container, agent, tool, profile string, ttl, gap, now int64) (LockRequest, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return LockRequest{}, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE lock_requests SET state='expired', decided=? WHERE state='pending' AND at<=?`, now, now-ttl); err != nil {
		return LockRequest{}, err
	}
	var pending, recent int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM lock_requests WHERE container=? AND tool=? AND state='pending'`,
		container, tool).Scan(&pending); err != nil {
		return LockRequest{}, err
	}
	if pending > 0 {
		return LockRequest{}, fmt.Errorf("a %s request of this container waits for the user: %w", tool, ErrLimit)
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM lock_requests WHERE container=? AND tool=? AND at>?`,
		container, tool, now-gap).Scan(&recent); err != nil {
		return LockRequest{}, err
	}
	if recent > 0 {
		return LockRequest{}, fmt.Errorf("one %s request per %d s from this container: %w", tool, gap, ErrRate)
	}
	res, err := tx.Exec(`INSERT INTO lock_requests(container, agent, tool, profile, at) VALUES(?,?,?,?,?)`,
		container, agent, tool, profile, now)
	if err != nil {
		return LockRequest{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return LockRequest{}, err
	}
	if err := tx.Commit(); err != nil {
		return LockRequest{}, err
	}
	return LockRequest{ID: id, Container: container, Agent: agent, Tool: tool, Profile: profile, State: "pending", At: now}, nil
}

// ClaimLockRequest moves a pending, unexpired request to state (opened or declined) at once, so
// it is answered only once: a second answer, or an answer to an old request, is refused. It
// returns the request. ErrNotFound: no such request in the container; ErrDecided: already
// answered; ErrExpired: past its ttl (closed as expired now).
func (s *Store) ClaimLockRequest(id int64, container, state string, ttl, now int64) (LockRequest, error) {
	if state != "opened" && state != "declined" {
		return LockRequest{}, fmt.Errorf("lock request state %q", state)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return LockRequest{}, err
	}
	defer tx.Rollback()
	r, err := scanLockRequest(tx.QueryRow(`SELECT `+lockRequestCols+` FROM lock_requests WHERE id=? AND container=?`, id, container))
	if err != nil {
		return LockRequest{}, err
	}
	switch {
	case r.State != "pending":
		return r, ErrDecided
	case r.At <= now-ttl:
		if _, err := tx.Exec(`UPDATE lock_requests SET state='expired', decided=? WHERE id=?`, now, id); err != nil {
			return LockRequest{}, err
		}
		if err := tx.Commit(); err != nil {
			return LockRequest{}, err
		}
		r.State, r.Decided = "expired", now
		return r, ErrExpired
	}
	if _, err := tx.Exec(`UPDATE lock_requests SET state=?, decided=? WHERE id=? AND state='pending'`, state, now, id); err != nil {
		return LockRequest{}, err
	}
	if err := tx.Commit(); err != nil {
		return LockRequest{}, err
	}
	r.State, r.Decided = state, now
	return r, nil
}

// FailLockRequest records that an opened request's console did not open, and why.
func (s *Store) FailLockRequest(id int64, result string) error {
	_, err := s.db.Exec(`UPDATE lock_requests SET state='failed', result=? WHERE id=? AND state='opened'`, result, id)
	return err
}

// PendingLockRequests are the container's pending requests younger than ttl, oldest first.
// Nothing is written: SweepLockRequests closes the old ones.
func (s *Store) PendingLockRequests(container string, ttl, now int64) ([]LockRequest, error) {
	rows, err := s.db.Query(`SELECT `+lockRequestCols+` FROM lock_requests WHERE container=? AND state='pending' AND at>? ORDER BY id`,
		container, now-ttl)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LockRequest
	for rows.Next() {
		r, err := scanLockRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SweepLockRequests closes the pending requests older than ttl as expired and returns them.
func (s *Store) SweepLockRequests(ttl, now int64) ([]LockRequest, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT `+lockRequestCols+` FROM lock_requests WHERE state='pending' AND at<=? ORDER BY id`, now-ttl)
	if err != nil {
		return nil, err
	}
	var out []LockRequest
	for rows.Next() {
		r, err := scanLockRequest(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		r.State, r.Decided = "expired", now
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, r := range out {
		if _, err := tx.Exec(`UPDATE lock_requests SET state='expired', decided=? WHERE id=? AND state='pending'`, now, r.ID); err != nil {
			return nil, err
		}
	}
	return out, tx.Commit()
}

// LockRequestByID reads one request (tests, audit).
func (s *Store) LockRequestByID(id int64) (LockRequest, error) {
	return scanLockRequest(s.db.QueryRow(`SELECT `+lockRequestCols+` FROM lock_requests WHERE id=?`, id))
}
