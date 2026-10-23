package store

import (
	"database/sql"
	"errors"

	"github.com/DavidGodefroid/lea/internal/grants"
)

// Grants are the user's decisions (lea grant add): only the host API writes them (issue #73).

const grantCols = `id, container, action, repo, branch, agent, created, expires, revoked`

func scanGrant(sc interface{ Scan(...any) error }) (grants.Grant, error) {
	var g grants.Grant
	err := sc.Scan(&g.ID, &g.Container, &g.Action, &g.Repo, &g.Branch, &g.Agent, &g.Created, &g.Expires, &g.Revoked)
	return g, err
}

// AddGrant records a grant and returns it with its id.
func (s *Store) AddGrant(g grants.Grant) (grants.Grant, error) {
	res, err := s.db.Exec(`INSERT INTO grants(container, action, repo, branch, agent, created, expires) VALUES(?,?,?,?,?,?,?)`,
		g.Container, g.Action, g.Repo, g.Branch, g.Agent, g.Created, g.Expires)
	if err != nil {
		return grants.Grant{}, err
	}
	g.ID, err = res.LastInsertId()
	return g, err
}

// RevokeGrant ends a grant at now. ErrNotFound: no such grant; ErrDecided: already revoked.
func (s *Store) RevokeGrant(id, now int64) error {
	res, err := s.db.Exec(`UPDATE grants SET revoked=? WHERE id=? AND revoked=0`, now, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	var r int64
	if err := s.db.QueryRow(`SELECT revoked FROM grants WHERE id=?`, id).Scan(&r); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return ErrDecided
}

// ActiveGrants returns the grants of container that apply at now ("" every container).
func (s *Store) ActiveGrants(container string, now int64) ([]grants.Grant, error) {
	return s.queryGrants(`SELECT `+grantCols+` FROM grants WHERE revoked=0 AND expires>? AND (?='' OR container=?) ORDER BY id`,
		now, container, container)
}

// RecentGrants returns the last limit grants, active or not, newest first.
func (s *Store) RecentGrants(limit int) ([]grants.Grant, error) {
	return s.queryGrants(`SELECT `+grantCols+` FROM grants ORDER BY id DESC LIMIT ?`, limit)
}

func (s *Store) queryGrants(q string, args ...any) ([]grants.Grant, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []grants.Grant{}
	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// GrantUse is one call a grant allowed. Target names what it acted on, never the tool input.
type GrantUse struct {
	ID, Grant             int64
	Agent, Action, Target string
	At                    int64
}

// AddGrantUse audits one call a grant allowed.
func (s *Store) AddGrantUse(u GrantUse) error {
	_, err := s.db.Exec(`INSERT INTO grant_uses(grant_id, agent, action, target, at) VALUES(?,?,?,?,?)`,
		u.Grant, u.Agent, u.Action, u.Target, u.At)
	return err
}

// GrantUses returns the last limit uses, newest first.
func (s *Store) GrantUses(limit int) ([]GrantUse, error) {
	rows, err := s.db.Query(`SELECT id, grant_id, agent, action, target, at FROM grant_uses ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GrantUse{}
	for rows.Next() {
		var u GrantUse
		if err := rows.Scan(&u.ID, &u.Grant, &u.Agent, &u.Action, &u.Target, &u.At); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
