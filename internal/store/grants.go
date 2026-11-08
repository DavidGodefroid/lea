package store

import (
	"database/sql"
	"errors"
	"strings"

	"github.com/DavidGodefroid/lea/internal/grants"
)

// Grants are the user's decisions (lea allow): only the host API writes them (issue #73). Each
// row belongs to a set (#319), the rows one lea allow recorded: the unit the user sees and
// revokes. A set is revoked with its rows, in one transaction.

const grantCols = `id, set_id, container, action, repo, branch, agent, github, created, expires, revoked, tag`

// grantColumns are the grants columns added after it was created. An older lea ignores set_id.
var grantColumns = [][2]string{
	{"set_id", "INTEGER NOT NULL DEFAULT 0"},
}

func scanGrant(sc interface{ Scan(...any) error }) (grants.Grant, error) {
	var g grants.Grant
	err := sc.Scan(&g.ID, &g.SetID, &g.Container, &g.Action, &g.Repo, &g.Branch, &g.Agent, &g.Pin, &g.Created, &g.Expires, &g.Revoked, &g.Tag)
	return g, err
}

// migrateGrantSets gives every active grant without a set one (#319), in one transaction: the
// standing rows of a container (the old lea delivery on) become one deliver set, every other active row a
// set of one. Revoked and expired rows keep no set; ids never change. A second run finds nothing
// to do.
func migrateGrantSets(db *sql.DB, now int64) error {
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS grants_set ON grants(set_id)`); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT `+grantCols+` FROM grants WHERE set_id=0 AND revoked=0 AND expires>? ORDER BY id`, now)
	if err != nil {
		return err
	}
	var gs []grants.Grant
	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			rows.Close()
			return err
		}
		gs = append(gs, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(gs) == 0 {
		return nil
	}
	deliver := map[string]int64{} // container -> its deliver set
	for _, g := range gs {
		id, ok := int64(0), false
		if g.Expires == grants.Standing {
			id, ok = deliver[g.Container]
		}
		if !ok {
			caps := grants.Deliver
			if g.Expires != grants.Standing {
				c, _ := grants.CapabilityOf(g.Action)
				caps = c.Name
			}
			if id, err = insertSet(tx, grants.Set{Container: g.Container, Capabilities: []string{caps}, Created: g.Created, Expires: g.Expires}); err != nil {
				return err
			}
			if g.Expires == grants.Standing {
				deliver[g.Container] = id
			}
		}
		if _, err := tx.Exec(`UPDATE grants SET set_id=? WHERE id=?`, id, g.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type execer interface {
	Exec(string, ...any) (sql.Result, error)
}

// insertSet records a set's row; its capabilities are stored by name, a preset expanded.
func insertSet(x execer, s grants.Set) (int64, error) {
	caps, err := grants.Expand(s.Capabilities)
	if err != nil {
		return 0, err
	}
	names := make([]string, len(caps))
	for i, c := range caps {
		names[i] = c.Name
	}
	res, err := x.Exec(`INSERT INTO grant_sets(container, capabilities, created, expires) VALUES(?,?,?,?)`,
		s.Container, strings.Join(names, " "), s.Created, s.Expires)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func insertGrant(x execer, g grants.Grant) (grants.Grant, error) {
	res, err := x.Exec(`INSERT INTO grants(set_id, container, action, repo, branch, agent, github, created, expires, tag) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		g.SetID, g.Container, g.Action, g.Repo, g.Branch, g.Agent, g.Pin, g.Created, g.Expires, g.Tag)
	if err != nil {
		return grants.Grant{}, err
	}
	g.ID, err = res.LastInsertId()
	return g, err
}

// AddGrant records a grant as a set of one and returns it with its id and its set's.
func (s *Store) AddGrant(g grants.Grant) (grants.Grant, error) {
	c, ok := grants.CapabilityOf(g.Action)
	if !ok {
		return grants.Grant{}, errors.New("store: unknown action " + g.Action)
	}
	set, err := s.AddGrantSet(grants.Set{Container: g.Container, Capabilities: []string{c.Name}, Created: g.Created, Expires: g.Expires}, []grants.Grant{g})
	if err != nil {
		return grants.Grant{}, err
	}
	return set.Grants[0], nil
}

// AddGrantSet records a set and its rows (each of its container, created and expiry), in one
// transaction, and returns it with its ids.
func (s *Store) AddGrantSet(set grants.Set, rows []grants.Grant) (grants.Set, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return grants.Set{}, err
	}
	defer tx.Rollback()
	if set.ID, err = insertSet(tx, set); err != nil {
		return grants.Set{}, err
	}
	set.Grants = nil
	for _, g := range rows {
		g.SetID, g.Container, g.Created, g.Expires = set.ID, set.Container, set.Created, set.Expires
		if g, err = insertGrant(tx, g); err != nil {
			return grants.Set{}, err
		}
		set.Grants = append(set.Grants, g)
	}
	return set, tx.Commit()
}

// ChangeGrantSet adds rows to an active set and revokes some of its rows at now, in one
// transaction. ErrNotFound: no such set; ErrDecided: it is no longer active.
func (s *Store) ChangeGrantSet(id int64, add []grants.Grant, revoke []int64, now int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var set grants.Set
	err = tx.QueryRow(`SELECT container, created, expires, revoked FROM grant_sets WHERE id=?`, id).Scan(&set.Container, &set.Created, &set.Expires, &set.Revoked)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ErrNotFound
	case err != nil:
		return err
	case set.Revoked != 0 || set.Expires <= now:
		return ErrDecided
	}
	for _, g := range revoke {
		if _, err := tx.Exec(`UPDATE grants SET revoked=? WHERE id=? AND set_id=? AND revoked=0`, now, g, id); err != nil {
			return err
		}
	}
	for _, g := range add {
		g.SetID, g.Container, g.Created, g.Expires = id, set.Container, now, set.Expires
		if _, err := insertGrant(tx, g); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RevokeGrantSet ends a set and its rows at now, in one transaction. ErrNotFound: no such set;
// ErrDecided: already revoked.
func (s *Store) RevokeGrantSet(id, now int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE grant_sets SET revoked=? WHERE id=? AND revoked=0`, now, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		var r int64
		if err := tx.QueryRow(`SELECT revoked FROM grant_sets WHERE id=?`, id).Scan(&r); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return ErrDecided
	}
	if _, err := tx.Exec(`UPDATE grants SET revoked=? WHERE set_id=? AND revoked=0`, now, id); err != nil {
		return err
	}
	return tx.Commit()
}

// RevokeGrant ends a grant at now; a set left with no active row ends with it. ErrNotFound: no
// such grant; ErrDecided: already revoked.
func (s *Store) RevokeGrant(id, now int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE grants SET revoked=? WHERE id=? AND revoked=0`, now, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		var r int64
		if err := tx.QueryRow(`SELECT revoked FROM grants WHERE id=?`, id).Scan(&r); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return ErrDecided
	}
	if _, err := tx.Exec(`UPDATE grant_sets SET revoked=? WHERE revoked=0 AND id=(SELECT set_id FROM grants WHERE id=?)
		AND NOT EXISTS(SELECT 1 FROM grants WHERE set_id=grant_sets.id AND revoked=0)`, now, id); err != nil {
		return err
	}
	return tx.Commit()
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

// GrantSets returns the sets of container that apply at now, oldest first, each with all its
// rows (revoked ones too).
func (s *Store) GrantSets(container string, now int64) ([]grants.Set, error) {
	rows, err := s.db.Query(`SELECT id, container, capabilities, created, expires, revoked FROM grant_sets
		WHERE container=? AND revoked=0 AND expires>? ORDER BY id`, container, now)
	if err != nil {
		return nil, err
	}
	var sets []grants.Set
	for rows.Next() {
		var x grants.Set
		var caps string
		if err := rows.Scan(&x.ID, &x.Container, &caps, &x.Created, &x.Expires, &x.Revoked); err != nil {
			rows.Close()
			return nil, err
		}
		x.Capabilities = strings.Fields(caps)
		sets = append(sets, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []grants.Set{}
	for _, x := range sets {
		if x.Grants, err = s.queryGrants(`SELECT `+grantCols+` FROM grants WHERE set_id=? ORDER BY id`, x.ID); err != nil {
			return nil, err
		}
		if x.Active(now) {
			out = append(out, x)
		}
	}
	return out, nil
}

// GrantSetByID returns a set with all its rows. ErrNotFound: no such set.
func (s *Store) GrantSetByID(id int64) (grants.Set, error) {
	var x grants.Set
	var caps string
	err := s.db.QueryRow(`SELECT id, container, capabilities, created, expires, revoked FROM grant_sets WHERE id=?`, id).
		Scan(&x.ID, &x.Container, &caps, &x.Created, &x.Expires, &x.Revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return grants.Set{}, ErrNotFound
	}
	if err != nil {
		return grants.Set{}, err
	}
	x.Capabilities = strings.Fields(caps)
	x.Grants, err = s.queryGrants(`SELECT `+grantCols+` FROM grants WHERE set_id=? ORDER BY id`, x.ID)
	return x, err
}

// LastPins returns, for each repository of container, the pin of its newest grant, active or not:
// what lea allow compares an origin with before it records it again without asking.
func (s *Store) LastPins(container string) (map[string]string, error) {
	rows, err := s.db.Query(`SELECT repo, github FROM grants WHERE container=? AND repo!='' AND github!='' ORDER BY id DESC`, container)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var r, p string
		if err := rows.Scan(&r, &p); err != nil {
			return nil, err
		}
		if _, seen := out[r]; !seen {
			out[r] = p
		}
	}
	return out, rows.Err()
}

// LastGrantSetUses returns, for each set of container used at least once, the time of its last use.
func (s *Store) LastGrantSetUses(container string) (map[int64]int64, error) {
	rows, err := s.db.Query(`SELECT g.set_id, MAX(u.at) FROM grant_uses u JOIN grants g ON g.id=u.grant_id
		WHERE g.container=? AND g.set_id!=0 GROUP BY g.set_id`, container)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var id, at int64
		if err := rows.Scan(&id, &at); err != nil {
			return nil, err
		}
		out[id] = at
	}
	return out, rows.Err()
}

// GrantUse is one call a grant allowed. Target names what it acted on, never the tool input. Set
// is the set of its grant (0 for a grant recorded before sets).
type GrantUse struct {
	ID, Grant, Set        int64
	Agent, Action, Target string
	At                    int64
}

// AddGrantUse audits one call a grant allowed.
func (s *Store) AddGrantUse(u GrantUse) error {
	_, err := s.db.Exec(`INSERT INTO grant_uses(grant_id, agent, action, target, at) VALUES(?,?,?,?,?)`,
		u.Grant, u.Agent, u.Action, u.Target, u.At)
	return err
}

// GrantUses returns the last limit uses of the grants of container ("" every container), newest
// first.
func (s *Store) GrantUses(container string, limit int) ([]GrantUse, error) {
	rows, err := s.db.Query(`SELECT u.id, u.grant_id, COALESCE(g.set_id, 0), u.agent, u.action, u.target, u.at
		FROM grant_uses u LEFT JOIN grants g ON g.id=u.grant_id
		WHERE ?='' OR g.container=? ORDER BY u.id DESC LIMIT ?`, container, container, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GrantUse{}
	for rows.Next() {
		var u GrantUse
		if err := rows.Scan(&u.ID, &u.Grant, &u.Set, &u.Agent, &u.Action, &u.Target, &u.At); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
