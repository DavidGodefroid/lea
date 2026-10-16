package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// ErrDeclined: this requester's request with the same brief was declined before (spec §5.2).
var ErrDeclined = errors.New("declined before")

// SpawnRequest is an agent's request for another agent (spec §5.2), addressed to Lead: a lead's
// key, or "user". State is pending; accepting (a spawn for it runs); accepted (Agent is the key of
// the agent started for it); declined; expired (nobody answered in time, or the lead went away);
// withdrawn (the requester went away).
type SpawnRequest struct {
	ID                                         int64
	Container, Requester, Lead                 string
	Role, Profile, Repo, Branch, Brief, Reason string
	Message                                    int64
	State, Agent                               string
	At, Decided                                int64
}

const requestCols = `id, container, requester, lead, role, profile, repo, branch, brief, reason, message, state, agent, at, decided`

func scanRequest(sc interface{ Scan(...any) error }) (SpawnRequest, error) {
	var r SpawnRequest
	err := sc.Scan(&r.ID, &r.Container, &r.Requester, &r.Lead, &r.Role, &r.Profile, &r.Repo, &r.Branch, &r.Brief, &r.Reason,
		&r.Message, &r.State, &r.Agent, &r.At, &r.Decided)
	if errors.Is(err, sql.ErrNoRows) {
		return SpawnRequest{}, ErrNotFound
	}
	return r, err
}

// briefHash identifies a brief for the "declined once" rule: the same text, outer blanks aside.
func briefHash(b string) string {
	h := sha256.Sum256([]byte(strings.TrimSpace(b)))
	return hex.EncodeToString(h[:])
}

// AddSpawnRequest records a pending request and its message (kind spawn_request, from the
// requester to the lead, text body(id)) in one transaction. Pending requests older than ttl
// seconds expire first; then a requester with maxPending open requests is refused (ErrLimit), and
// so is a brief this requester had declined (ErrDeclined).
func (s *Store) AddSpawnRequest(r SpawnRequest, body func(id int64) string, maxPending int, ttl, now int64) (SpawnRequest, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return SpawnRequest{}, err
	}
	defer tx.Rollback()
	// An accepting request whose spawn never finished (a daemon stopped mid-spawn) expires too.
	if _, err := tx.Exec(`UPDATE spawn_requests SET state='expired', decided=?
		WHERE (state='pending' AND at<=?) OR (state='accepting' AND decided<=?)`, now, now-ttl, now-ttl); err != nil {
		return SpawnRequest{}, err
	}
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM spawn_requests WHERE requester=? AND state IN ('pending','accepting')`,
		r.Requester).Scan(&n); err != nil {
		return SpawnRequest{}, err
	}
	if n >= maxPending {
		return SpawnRequest{}, fmt.Errorf("%w: %d spawn requests of yours already wait for an answer (at most %d)", ErrLimit, n, maxPending)
	}
	h := briefHash(r.Brief)
	if err := tx.QueryRow(`SELECT COUNT(*) FROM spawn_requests WHERE requester=? AND brief_hash=? AND state='declined'`,
		r.Requester, h).Scan(&n); err != nil {
		return SpawnRequest{}, err
	}
	if n > 0 {
		return SpawnRequest{}, fmt.Errorf("%w: this brief was declined; change it, or ask the user", ErrDeclined)
	}
	res, err := tx.Exec(`INSERT INTO spawn_requests(container, requester, lead, role, profile, repo, branch, brief, brief_hash, reason, at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`, r.Container, r.Requester, r.Lead, r.Role, r.Profile, r.Repo, r.Branch, r.Brief, h, r.Reason, now)
	if err != nil {
		return SpawnRequest{}, err
	}
	if r.ID, err = res.LastInsertId(); err != nil {
		return SpawnRequest{}, err
	}
	m, err := sendTx(tx, Message{From: r.Requester, To: r.Lead, Kind: "spawn_request", Body: body(r.ID), At: now})
	if err != nil {
		return SpawnRequest{}, err
	}
	if _, err := tx.Exec(`UPDATE spawn_requests SET message=? WHERE id=?`, m.ID, r.ID); err != nil {
		return SpawnRequest{}, err
	}
	if err := tx.Commit(); err != nil {
		return SpawnRequest{}, err
	}
	r.Message, r.State, r.At = m.ID, "pending", now
	return r, nil
}

func (s *Store) SpawnRequestByID(id int64) (SpawnRequest, error) {
	return scanRequest(s.db.QueryRow(`SELECT `+requestCols+` FROM spawn_requests WHERE id=?`, id))
}

func (s *Store) SpawnRequestByMessage(msg int64) (SpawnRequest, error) {
	return scanRequest(s.db.QueryRow(`SELECT `+requestCols+` FROM spawn_requests WHERE message=?`, msg))
}

// PendingSpawnRequest is the oldest pending request of requester addressed to lead.
func (s *Store) PendingSpawnRequest(requester, lead string) (SpawnRequest, error) {
	return scanRequest(s.db.QueryRow(`SELECT `+requestCols+` FROM spawn_requests
		WHERE requester=? AND lead=? AND state='pending' ORDER BY id LIMIT 1`, requester, lead))
}

// PendingSpawnRequests lists the pending requests addressed to lead, oldest first.
func (s *Store) PendingSpawnRequests(lead string) ([]SpawnRequest, error) {
	rows, err := s.db.Query(`SELECT `+requestCols+` FROM spawn_requests WHERE lead=? AND state='pending' ORDER BY id`, lead)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SpawnRequest
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MoveSpawnRequest moves a request from state from to state to, recording agent when given.
// ErrDecided when the request is not in state from, ErrNotFound when there is none.
func (s *Store) MoveSpawnRequest(id int64, from, to, agent string, now int64) error {
	res, err := s.db.Exec(`UPDATE spawn_requests SET state=?, agent=CASE WHEN ?='' THEN agent ELSE ? END, decided=?
		WHERE id=? AND state=?`, to, agent, agent, now, id, from)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	if _, err := s.SpawnRequestByID(id); err != nil {
		return err
	}
	return ErrDecided
}

// SweepSpawnRequests closes what can no longer be answered, before a pending request is looked
// up: requests older than ttl seconds expire (an accepting one counts from its decision), and a
// pending request whose requester ended or is gone is withdrawn: nobody waits for its agent.
func (s *Store) SweepSpawnRequests(ttl, now int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE spawn_requests SET state='expired', decided=?
		WHERE (state='pending' AND at<=?) OR (state='accepting' AND decided<=?)`, now, now-ttl, now-ttl); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE spawn_requests SET state='withdrawn', decided=?
		WHERE state='pending' AND requester NOT IN (SELECT key FROM agents WHERE state<>'done')`, now); err != nil {
		return err
	}
	return tx.Commit()
}
