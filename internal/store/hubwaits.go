package store

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"unicode/utf8"
)

// Hub waits (the Lock Family approval hub, #159): an agent whose Lock call is pending a human's
// decision ends its turn and waits, suspended. Each wait is one (agent, ticket); the decision the
// hub reports is recorded once, then delivered to the agent once (its inbox, or the context of its
// resumed session). Delivered waits are purged hubWaitKeep later.

// The states of a suspended agent: warm, its process still runs (a keepalive); cold, its
// process ended, its session kept, for a resume once the decision comes.
const (
	StateSuspendedWarm = "suspended_warm"
	StateSuspendedCold = "suspended_cold"
)

// Suspended reports whether state is one of a suspended agent.
func Suspended(state string) bool { return state == StateSuspendedWarm || state == StateSuspendedCold }

// The decisions of a hub ticket (lockhub's state machine): "" while it is pending.
const (
	DecisionApproved  = "approved"
	DecisionDenied    = "denied"
	DecisionWithdrawn = "withdrawn"
	DecisionExpired   = "expired"
	// DecisionAbandoned is lea's own: lea stopped waiting on the ticket (the hub is disabled,
	// LEA_HUB=0), whatever the hub decides later.
	DecisionAbandoned = "abandoned"
)

// hubWaitKeep is how long a delivered wait is kept (seconds); maxHubReason caps the hub's reason,
// which reaches the agent's context.
const (
	hubWaitKeep  = 7 * 24 * 3600
	maxHubReason = 500
)

var (
	ticketRe   = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`) // a ULID
	hubNameRe  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	decisionOK = map[string]bool{DecisionApproved: true, DecisionDenied: true, DecisionWithdrawn: true, DecisionExpired: true, DecisionAbandoned: true}
)

// HubWait is one agent's wait on a hub ticket.
type HubWait struct {
	Agent, Ticket, Lock, Instance string
	Created, DecidedAt            int64
	// Decision is "" while pending; Reason is the hub's (denied, withdrawn).
	Decision, Reason string
	// Delivered is when the decision reached the agent (0: not yet).
	Delivered int64
}

const hubWaitCols = `agent_key, ticket, lock, instance, created, decided_at, decision, reason, delivered`

func scanHubWait(sc interface{ Scan(...any) error }) (HubWait, error) {
	var w HubWait
	err := sc.Scan(&w.Agent, &w.Ticket, &w.Lock, &w.Instance, &w.Created, &w.DecidedAt, &w.Decision, &w.Reason, &w.Delivered)
	return w, err
}

// AddHubWait records that agent waits on ticket of lock's instance. The same wait again is no
// error (a Lock's run returns the same ticket when called again).
func (s *Store) AddHubWait(agent, ticket, lock, instance string, now int64) error {
	if !ticketRe.MatchString(ticket) {
		return errors.New("hub wait: invalid ticket id")
	}
	if !hubNameRe.MatchString(lock) || !hubNameRe.MatchString(instance) {
		return errors.New("hub wait: invalid lock or instance name")
	}
	// Only for an agent lea knows: a wait registered while its agent was forgotten would be left
	// for a later agent of the same name.
	_, err := s.db.Exec(`INSERT INTO hub_waits(agent_key, ticket, lock, instance, created)
		SELECT ?,?,?,?,? WHERE EXISTS(SELECT 1 FROM agents WHERE key=?)
		ON CONFLICT(agent_key, ticket) DO NOTHING`, agent, ticket, lock, instance, now, agent)
	return err
}

// DecideHubWaits records the hub's decision on ticket of instance for every agent that waits on
// it, once: a decision already recorded stays (a decision is final). It returns the waits it
// decided.
func (s *Store) DecideHubWaits(ticket, instance, decision, reason string, now int64) ([]HubWait, error) {
	if !decisionOK[decision] {
		return nil, fmt.Errorf("hub wait: unknown decision %q", decision)
	}
	if len(reason) > maxHubReason || !utf8.ValidString(reason) {
		return nil, fmt.Errorf("hub wait: the reason is not valid UTF-8 of at most %d bytes", maxHubReason)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT `+hubWaitCols+` FROM hub_waits WHERE ticket=? AND instance=? AND decision='' ORDER BY agent_key`, ticket, instance)
	if err != nil {
		return nil, err
	}
	out, err := collectHubWaits(rows)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE hub_waits SET decision=?, reason=?, decided_at=? WHERE ticket=? AND instance=? AND decision=''`,
		decision, reason, now, ticket, instance); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Decision, out[i].Reason, out[i].DecidedAt = decision, reason, now
	}
	return out, tx.Commit()
}

// EndSuspended ends an agent that is still suspended_cold (done), and reports whether it was:
// one that woke or ended meanwhile is left as it is.
func (s *Store) EndSuspended(key string, now int64) (bool, error) {
	r, err := s.db.Exec(`UPDATE agents SET state='done', updated=?, paused=0, limit_until=0, limit_tries=0
		WHERE key=? AND state=?`, now, key, StateSuspendedCold)
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	return n == 1, nil
}

// AbandonAllHubWaits records, on every wait with no decision yet, that lea stopped waiting
// (DecisionAbandoned, with reason), and returns those waits. A decision the hub takes later does
// not replace it (a decision is final).
func (s *Store) AbandonAllHubWaits(reason string, now int64) ([]HubWait, error) {
	if len(reason) > maxHubReason || !utf8.ValidString(reason) {
		return nil, fmt.Errorf("hub wait: the reason is not valid UTF-8 of at most %d bytes", maxHubReason)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT ` + hubWaitCols + ` FROM hub_waits WHERE decision='' ORDER BY agent_key, ticket`)
	if err != nil {
		return nil, err
	}
	out, err := collectHubWaits(rows)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE hub_waits SET decision=?, reason=?, decided_at=? WHERE decision=''`, DecisionAbandoned, reason, now); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Decision, out[i].Reason, out[i].DecidedAt = DecisionAbandoned, reason, now
	}
	return out, tx.Commit()
}

// UndeliveredHubWaits are agent's decided waits that have not reached it yet, oldest first.
func (s *Store) UndeliveredHubWaits(agent string) ([]HubWait, error) {
	rows, err := s.db.Query(`SELECT `+hubWaitCols+` FROM hub_waits WHERE agent_key=? AND decision<>'' AND delivered=0 ORDER BY decided_at, ticket`, agent)
	if err != nil {
		return nil, err
	}
	return collectHubWaits(rows)
}

// UndeliveredAllHubWaits are every agent's decided waits not delivered yet, oldest first.
func (s *Store) UndeliveredAllHubWaits() ([]HubWait, error) {
	rows, err := s.db.Query(`SELECT ` + hubWaitCols + ` FROM hub_waits WHERE decision<>'' AND delivered=0 ORDER BY decided_at, agent_key, ticket`)
	if err != nil {
		return nil, err
	}
	return collectHubWaits(rows)
}

// AllPendingHubWaits are every agent's waits with no decision yet, oldest first.
func (s *Store) AllPendingHubWaits() ([]HubWait, error) {
	rows, err := s.db.Query(`SELECT ` + hubWaitCols + ` FROM hub_waits WHERE decision='' ORDER BY created, ticket`)
	if err != nil {
		return nil, err
	}
	return collectHubWaits(rows)
}

// PendingHubWaits are agent's waits with no decision yet, oldest first.
func (s *Store) PendingHubWaits(agent string) ([]HubWait, error) {
	rows, err := s.db.Query(`SELECT `+hubWaitCols+` FROM hub_waits WHERE agent_key=? AND decision='' ORDER BY created, ticket`, agent)
	if err != nil {
		return nil, err
	}
	return collectHubWaits(rows)
}

// MarkHubWaitDelivered records that the decision on ticket reached agent; ErrNotFound when there
// is no such decided, undelivered wait (delivered once).
func (s *Store) MarkHubWaitDelivered(agent, ticket string, now int64) error {
	r, err := s.db.Exec(`UPDATE hub_waits SET delivered=? WHERE agent_key=? AND ticket=? AND decision<>'' AND delivered=0`, now, agent, ticket)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// PurgeHubWaits removes the waits delivered hubWaitKeep ago or more, and those of agents lea no
// longer knows.
func (s *Store) PurgeHubWaits(now int64) error {
	_, err := s.db.Exec(`DELETE FROM hub_waits WHERE (delivered>0 AND delivered<=?) OR agent_key NOT IN (SELECT key FROM agents)`, now-hubWaitKeep)
	return err
}

func collectHubWaits(rows *sql.Rows) ([]HubWait, error) {
	defer rows.Close()
	var out []HubWait
	for rows.Next() {
		w, err := scanHubWait(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// The hub's decisions cursor (#164): where lea's reading of the hub's decided tickets stopped.
const kvHubCursor = "hub_cursor"

// HubCursor is the last hub decisions cursor lea recorded, "" when none.
func (s *Store) HubCursor() (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM kv WHERE key=?`, kvHubCursor).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetHubCursor records the hub decisions cursor.
func (s *Store) SetHubCursor(c string) error {
	if len(c) > 256 {
		return errors.New("hub cursor too long")
	}
	_, err := s.db.Exec(`INSERT INTO kv(key, value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, kvHubCursor, c)
	return err
}
