package store

// The user's pause of an agent's inbox (#95, the deck's H): the agents' messages to it stay
// unread and out of its deliveries until the user releases it. Never held: the user's own
// messages (sender user), and the replies in a thread the agent opened during the pause.

// Hold is what an inbox holds back from its deliveries.
type Hold struct {
	// Asks holds the questions agents ask (ask, kind question) that answer nothing: the user has
	// the tower's open question.
	Asks bool
	// Since > 0 is when the user paused the inbox (Agent.HoldSince).
	Since int64
}

// heldAsks is the condition of a question an agent asks that answers nothing.
const heldAsks = `(kind='question' AND reply_to=0)`

// heldByPause is the condition of a message a pause since ? holds: from an agent (or lea), and not
// a reply in a thread its recipient opened, during the pause, with that sender.
const heldByPause = `(sender<>'user' AND NOT EXISTS (SELECT 1 FROM messages r
	WHERE r.id=messages.thread AND r.sender=messages.recipient AND r.recipient=messages.sender AND r.at>=?))`

// cond is the WHERE condition (and its arguments) that leaves out what h holds.
func (h Hold) cond() (string, []any) {
	cond, args := "", []any{}
	if h.Asks {
		cond += ` AND NOT ` + heldAsks
	}
	if h.Since > 0 {
		cond += ` AND NOT ` + heldByPause
		args = append(args, h.Since)
	}
	return cond, args
}

// SetHold pauses key's inbox since since (unix seconds), or releases it (0).
func (s *Store) SetHold(key string, since int64) error {
	res, err := s.db.Exec(`UPDATE agents SET hold_since=? WHERE key=?`, since, key)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

// HeldCount counts the unread messages to to that a pause since since holds.
func (s *Store) HeldCount(to string, since int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE recipient=? AND read_at=0 AND `+heldByPause, to, since).Scan(&n)
	return n, err
}

// HeldCounts counts, per paused agent, the unread messages its pause holds.
func (s *Store) HeldCounts() (map[string]int, error) {
	return s.countBy(`SELECT a.key, COUNT(m.id) FROM agents a LEFT JOIN messages m ON m.recipient=a.key AND m.read_at=0
		AND m.sender<>'user' AND NOT EXISTS (SELECT 1 FROM messages r WHERE r.id=m.thread AND r.sender=m.recipient AND r.recipient=m.sender AND r.at>=a.hold_since)
		WHERE a.hold_since>0 GROUP BY a.key`)
}

// ExpiredHolds lists the agents whose pause started at or before at.
func (s *Store) ExpiredHolds(at int64) ([]string, error) {
	rows, err := s.db.Query(`SELECT key FROM agents WHERE hold_since>0 AND hold_since<=? ORDER BY key`, at)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
