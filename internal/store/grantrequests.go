package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// GrantRequest is a lead's request for a grant (#342): it never is one. Only the user's decision
// on the host turns it into a grant (Grant, the id of the grant recorded). State is pending,
// approved, denied, expired (nobody answered in time) or withdrawn (the requester went away).
type GrantRequest struct {
	ID                                 int64
	Container, Requester, Action, Repo string
	Branch, GitHub, Reason, State      string
	// Tag is the tag glob of a release-create request (#353).
	Tag         string
	TTLS        int64
	Grant       int64
	At, Decided int64
}

const grantRequestCols = `id, container, requester, action, repo, branch, github, ttl_s, reason, state, grant_id, at, decided, tag`

func scanGrantRequest(sc interface{ Scan(...any) error }) (GrantRequest, error) {
	var r GrantRequest
	err := sc.Scan(&r.ID, &r.Container, &r.Requester, &r.Action, &r.Repo, &r.Branch, &r.GitHub, &r.TTLS, &r.Reason,
		&r.State, &r.Grant, &r.At, &r.Decided, &r.Tag)
	if errors.Is(err, sql.ErrNoRows) {
		return GrantRequest{}, ErrNotFound
	}
	return r, err
}

// AddGrantRequest records a pending grant request. The same request still pending (same
// requester, action, repo, branch, GitHub repository and tag) is returned as it is, with ErrExists; a
// requester with maxPending pending requests is refused (ErrLimit).
func (s *Store) AddGrantRequest(r GrantRequest, maxPending int, now int64) (GrantRequest, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return GrantRequest{}, err
	}
	defer tx.Rollback()
	same, err := scanGrantRequest(tx.QueryRow(`SELECT `+grantRequestCols+` FROM grant_requests
		WHERE state='pending' AND requester=? AND action=? AND repo=? AND branch=? AND github=? AND tag=? ORDER BY id LIMIT 1`,
		r.Requester, r.Action, r.Repo, r.Branch, r.GitHub, r.Tag))
	switch {
	case err == nil:
		return same, ErrExists
	case !errors.Is(err, ErrNotFound):
		return GrantRequest{}, err
	}
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM grant_requests WHERE requester=? AND state='pending'`, r.Requester).Scan(&n); err != nil {
		return GrantRequest{}, err
	}
	if n >= maxPending {
		return GrantRequest{}, fmt.Errorf("%w: %d grant requests of yours already wait for the user (at most %d)", ErrLimit, n, maxPending)
	}
	res, err := tx.Exec(`INSERT INTO grant_requests(container, requester, action, repo, branch, github, ttl_s, reason, at, tag)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, r.Container, r.Requester, r.Action, r.Repo, r.Branch, r.GitHub, r.TTLS, r.Reason, now, r.Tag)
	if err != nil {
		return GrantRequest{}, err
	}
	if r.ID, err = res.LastInsertId(); err != nil {
		return GrantRequest{}, err
	}
	if err := tx.Commit(); err != nil {
		return GrantRequest{}, err
	}
	r.State, r.At = "pending", now
	return r, nil
}

// GrantRequestByID is one grant request, ErrNotFound when there is none.
func (s *Store) GrantRequestByID(id int64) (GrantRequest, error) {
	return scanGrantRequest(s.db.QueryRow(`SELECT `+grantRequestCols+` FROM grant_requests WHERE id=?`, id))
}

// PendingGrantRequests lists the pending grant requests, oldest first, at most limit.
func (s *Store) PendingGrantRequests(limit int) ([]GrantRequest, error) {
	return s.queryGrantRequests(`SELECT `+grantRequestCols+` FROM grant_requests WHERE state='pending' ORDER BY id LIMIT ?`, limit)
}

func (s *Store) queryGrantRequests(q string, args ...any) ([]GrantRequest, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GrantRequest{}
	for rows.Next() {
		r, err := scanGrantRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DecideGrantRequest moves a pending request to state (approved, with the grant recorded for it,
// or denied). ErrDecided when it is no longer pending, ErrNotFound when there is none.
func (s *Store) DecideGrantRequest(id int64, state string, grant, now int64) error {
	res, err := s.db.Exec(`UPDATE grant_requests SET state=?, grant_id=?, decided=? WHERE id=? AND state='pending'`, state, grant, now, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	if _, err := s.GrantRequestByID(id); err != nil {
		return err
	}
	return ErrDecided
}

// SweepGrantRequests closes the pending requests nobody can answer any more: those older than
// ttl seconds expire, and those whose requester ended or is gone are withdrawn. It returns the
// requests it expired, for their requesters to be told.
func (s *Store) SweepGrantRequests(ttl, now int64) ([]GrantRequest, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE grant_requests SET state='withdrawn', decided=?
		WHERE state='pending' AND requester NOT IN (SELECT key FROM agents WHERE state<>'done')`, now); err != nil {
		return nil, err
	}
	rows, err := tx.Query(`SELECT `+grantRequestCols+` FROM grant_requests WHERE state='pending' AND at<=? ORDER BY id`, now-ttl)
	if err != nil {
		return nil, err
	}
	var out []GrantRequest
	for rows.Next() {
		r, err := scanGrantRequest(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if _, err := tx.Exec(`UPDATE grant_requests SET state='expired', decided=? WHERE id=?`, now, out[i].ID); err != nil {
			return nil, err
		}
		out[i].State, out[i].Decided = "expired", now
	}
	return out, tx.Commit()
}
