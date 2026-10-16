package store

import (
	"errors"
	"fmt"
	"path"
)

// ErrHeld: another agent holds an overlapping lease.
var ErrHeld = errors.New("held by another agent")

// Lease is an agent's advisory claim on the files a glob names, in its container, until Expires
// (spec §6.1). Nothing enforces it on the files: agents ask before they edit.
type Lease struct {
	ID                     int64
	Container, Agent, Glob string
	Expires, At            int64
}

const (
	// MaxGlobBytes and MaxGlobWild bound the patterns path.Match is run on.
	MaxGlobBytes = 256
	MaxGlobWild  = 6
)

// simpleGlob reports whether g is small enough to be matched.
func simpleGlob(g string) bool {
	if len(g) > MaxGlobBytes {
		return false
	}
	n := 0
	for i := 0; i < len(g); i++ {
		if c := g[i]; c == '*' || c == '?' || c == '[' {
			n++
		}
	}
	return n <= MaxGlobWild
}

const leaseCols = `id, container, agent, glob, expires, at`

// GlobsOverlap reports whether two lease patterns (path.Match syntax) are seen as naming the same
// files: they are equal, or one matches the other read as a path. It is advisory: "src/a*" and
// "src/*b" are not seen as overlapping.
//
// Matching is bounded: a pattern longer than MaxGlobBytes or with more than MaxGlobWild
// metacharacters ('*', '?', '[') is compared for equality only, so a hostile pattern cannot cost
// more than a few thousand steps.
func GlobsOverlap(a, b string) bool {
	if a == b {
		return true
	}
	if !simpleGlob(a) || !simpleGlob(b) {
		return false
	}
	if ok, _ := path.Match(a, b); ok {
		return true
	}
	ok, _ := path.Match(b, a)
	return ok
}

// AcquireLease grants agent a lease on glob for ttl seconds, or extends its own lease on the same
// glob. Expired leases go first; ErrHeld when another agent of the container holds an overlapping
// lease, ErrLimit when the agent already holds maxPer leases.
func (s *Store) AcquireLease(container, agent, glob string, ttl, now int64, maxPer int) (Lease, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Lease{}, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM leases WHERE expires<=?`, now); err != nil {
		return Lease{}, err
	}
	rows, err := tx.Query(`SELECT `+leaseCols+` FROM leases WHERE container=? ORDER BY id`, container)
	if err != nil {
		return Lease{}, err
	}
	var ls []Lease
	for rows.Next() {
		var l Lease
		if err := rows.Scan(&l.ID, &l.Container, &l.Agent, &l.Glob, &l.Expires, &l.At); err != nil {
			rows.Close()
			return Lease{}, err
		}
		ls = append(ls, l)
	}
	rows.Close() // the single connection must be free before the next statement
	if err := rows.Err(); err != nil {
		return Lease{}, err
	}
	own := 0
	for _, l := range ls {
		if l.Agent == agent {
			if l.Glob == glob {
				l.Expires = now + ttl
				if _, err := tx.Exec(`UPDATE leases SET expires=? WHERE id=?`, l.Expires, l.ID); err != nil {
					return Lease{}, err
				}
				return l, tx.Commit()
			}
			own++
			continue
		}
		if GlobsOverlap(l.Glob, glob) {
			return Lease{}, fmt.Errorf("%w: %s holds %q for %d s more", ErrHeld, nameOf(l.Agent), l.Glob, l.Expires-now)
		}
	}
	if own >= maxPer {
		return Lease{}, fmt.Errorf("%w: you hold %d leases (at most %d)", ErrLimit, own, maxPer)
	}
	res, err := tx.Exec(`INSERT INTO leases(container, agent, glob, expires, at) VALUES(?,?,?,?,?)`, container, agent, glob, now+ttl, now)
	if err != nil {
		return Lease{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Lease{}, err
	}
	if err := tx.Commit(); err != nil {
		return Lease{}, err
	}
	return Lease{ID: id, Container: container, Agent: agent, Glob: glob, Expires: now + ttl, At: now}, nil
}

// ReleaseLease ends one of agent's leases; ErrNotFound when it holds no lease of that id.
func (s *Store) ReleaseLease(id int64, agent string) error {
	res, err := s.db.Exec(`DELETE FROM leases WHERE id=? AND agent=?`, id, agent)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Leases lists the live leases of a container ("" : every container), by container and glob.
func (s *Store) Leases(container string, now int64) ([]Lease, error) {
	rows, err := s.db.Query(`SELECT `+leaseCols+` FROM leases WHERE expires>? AND (?='' OR container=?)
		ORDER BY container, glob`, now, container, container)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Lease
	for rows.Next() {
		var l Lease
		if err := rows.Scan(&l.ID, &l.Container, &l.Agent, &l.Glob, &l.Expires, &l.At); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
