// Package store keeps lea's facts in SQLite: agents, their events, messages, approvals and the
// agent each deck showed last.
// The daemon is its only writer.
package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/DavidGodefroid/lea/internal/roles"
	_ "modernc.org/sqlite"
)

var (
	ErrNotFound = errors.New("not found")
	ErrDecided  = errors.New("already decided")
	ErrExists   = errors.New("already exists")
	// ErrLimit: a cap (max_children, pending requests, leases) is reached.
	ErrLimit = errors.New("limit reached")
	// ErrTree: the change would break the agent tree (spec A17: a lead and its children).
	ErrTree = errors.New("agent tree rule")
)

const schema = `
CREATE TABLE IF NOT EXISTS agents(
  key TEXT PRIMARY KEY, container TEXT NOT NULL, name TEXT NOT NULL,
  session_id TEXT NOT NULL DEFAULT '', state TEXT NOT NULL DEFAULT 'unknown', updated INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS events(
  seq INTEGER PRIMARY KEY AUTOINCREMENT, agent TEXT NOT NULL, source TEXT NOT NULL,
  kind TEXT NOT NULL, payload TEXT NOT NULL, at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS messages(
  id INTEGER PRIMARY KEY AUTOINCREMENT, thread INTEGER NOT NULL DEFAULT 0, sender TEXT NOT NULL,
  recipient TEXT NOT NULL, kind TEXT NOT NULL, body TEXT NOT NULL, reply_to INTEGER NOT NULL DEFAULT 0,
  hops INTEGER NOT NULL DEFAULT 0, at INTEGER NOT NULL, read_at INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS messages_unread ON messages(recipient, read_at);
CREATE INDEX IF NOT EXISTS messages_sender ON messages(sender);
CREATE INDEX IF NOT EXISTS messages_reply ON messages(reply_to);
CREATE TABLE IF NOT EXISTS approvals(
  id INTEGER PRIMARY KEY AUTOINCREMENT, agent TEXT NOT NULL, tool TEXT NOT NULL,
  input TEXT NOT NULL, verdict TEXT NOT NULL DEFAULT '', at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS decks(container TEXT PRIMARY KEY, shown TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS tasks(
  id INTEGER PRIMARY KEY AUTOINCREMENT, container TEXT NOT NULL, agent TEXT NOT NULL,
  title TEXT NOT NULL DEFAULT '', status_text TEXT NOT NULL DEFAULT '', next TEXT NOT NULL DEFAULT '',
  blocked_on TEXT NOT NULL DEFAULT '', created INTEGER NOT NULL, updated INTEGER NOT NULL,
  ended INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS tasks_agent ON tasks(agent, ended);
CREATE TABLE IF NOT EXISTS spawn_requests(
  id INTEGER PRIMARY KEY AUTOINCREMENT, container TEXT NOT NULL, requester TEXT NOT NULL, lead TEXT NOT NULL,
  role TEXT NOT NULL, profile TEXT NOT NULL, repo TEXT NOT NULL, branch TEXT NOT NULL, brief TEXT NOT NULL,
  brief_hash TEXT NOT NULL, reason TEXT NOT NULL, message INTEGER NOT NULL DEFAULT 0,
  state TEXT NOT NULL DEFAULT 'pending', agent TEXT NOT NULL DEFAULT '', at INTEGER NOT NULL,
  decided INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS spawn_requests_requester ON spawn_requests(requester, state);

CREATE TABLE IF NOT EXISTS leases(
  id INTEGER PRIMARY KEY AUTOINCREMENT, container TEXT NOT NULL, agent TEXT NOT NULL, glob TEXT NOT NULL,
  expires INTEGER NOT NULL, at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS leases_container ON leases(container, expires);

CREATE TABLE IF NOT EXISTS lock_requests(
  id INTEGER PRIMARY KEY AUTOINCREMENT, container TEXT NOT NULL, agent TEXT NOT NULL, tool TEXT NOT NULL,
  profile TEXT NOT NULL, state TEXT NOT NULL DEFAULT 'pending', result TEXT NOT NULL DEFAULT '',
  at INTEGER NOT NULL, decided INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS lock_requests_container ON lock_requests(container, tool, state);

CREATE TABLE IF NOT EXISTS grants(
  id INTEGER PRIMARY KEY AUTOINCREMENT, container TEXT NOT NULL, action TEXT NOT NULL,
  repo TEXT NOT NULL DEFAULT '', branch TEXT NOT NULL DEFAULT '', agent TEXT NOT NULL DEFAULT '',
  github TEXT NOT NULL DEFAULT '', created INTEGER NOT NULL, expires INTEGER NOT NULL, revoked INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS grants_container ON grants(container, revoked, expires);
CREATE TABLE IF NOT EXISTS grant_uses(
  id INTEGER PRIMARY KEY AUTOINCREMENT, grant_id INTEGER NOT NULL, agent TEXT NOT NULL,
  action TEXT NOT NULL, target TEXT NOT NULL, at INTEGER NOT NULL);

CREATE TABLE IF NOT EXISTS hub_waits(
  agent_key TEXT NOT NULL, ticket TEXT NOT NULL, lock TEXT NOT NULL, instance TEXT NOT NULL,
  created INTEGER NOT NULL, decided_at INTEGER NOT NULL DEFAULT 0, decision TEXT NOT NULL DEFAULT '',
  reason TEXT NOT NULL DEFAULT '', delivered INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(agent_key, ticket));
CREATE INDEX IF NOT EXISTS hub_waits_ticket ON hub_waits(ticket);
CREATE TABLE IF NOT EXISTS kv(key TEXT PRIMARY KEY, value TEXT NOT NULL);
`

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: schema: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Agent is one agent row. Profile is set only for agents lea spawned (lea new, the deck); rows
// of agents registered by a hook only have none. Role is the agent's role (rows older than
// roles are "dev"); Parent is the key of the lead that started it ("" when the user did);
// Repo and Branch are what it was started on; TaskID is its row on the board (0: none).
// Label is the display name the user gave it ("" when none): Name, its identity, never changes.
// Args are the Claude arguments it was started with and Prompt its first prompt ("" when none):
// what a new container starts it again with (SetLaunch). Paused: the user froze its process
// (SetPaused).
type Agent struct {
	Key, Container, Name, SessionID, State string
	Updated                                int64
	Profile, Workdir                       string
	// Model is the model the agent was started with ("" for its CLI's default).
	Model                      string
	Created                    int64
	Role, Parent, Repo, Branch string
	TaskID                     int64
	Label                      string
	Args                       []string
	Prompt                     string
	Paused                     bool
	// LimitUntil says the agent stopped on its provider's usage limit: 0 it did not, > 0 lea
	// resumes it at that time (unix seconds), -1 it waits for the user (auto-resume off, or lea
	// gave up). LimitTries counts the resumes since the agent last made progress.
	LimitUntil int64
	LimitTries int
	// Incarnation is random, new each time the agent is registered (created or reused under its
	// name), and kept across its resumes: what its requester token is bound to (#160).
	Incarnation string
}

// agentColumns are the agents columns added after phase 0; migrate adds the missing ones to a
// database created by an older lea. ctx_pct and cost_usd are -1 until the agent's status line
// reports them.
var agentColumns = [][2]string{
	{"profile", "TEXT NOT NULL DEFAULT ''"},
	{"workdir", "TEXT NOT NULL DEFAULT ''"},
	{"created", "INTEGER NOT NULL DEFAULT 0"},
	{"ctx_pct", "INTEGER NOT NULL DEFAULT -1"},
	{"cost_usd", "REAL NOT NULL DEFAULT -1"},
	{"role", "TEXT NOT NULL DEFAULT 'dev'"},
	{"parent", "TEXT NOT NULL DEFAULT ''"},
	{"repo", "TEXT NOT NULL DEFAULT ''"},
	{"branch", "TEXT NOT NULL DEFAULT ''"},
	{"task_id", "INTEGER NOT NULL DEFAULT 0"},
	{"label", "TEXT NOT NULL DEFAULT ''"},
	{"args", "TEXT NOT NULL DEFAULT ''"},
	{"prompt", "TEXT NOT NULL DEFAULT ''"},
	{"paused", "INTEGER NOT NULL DEFAULT 0"},
	{"subagents", "INTEGER NOT NULL DEFAULT 0"},
	{"model", "TEXT NOT NULL DEFAULT ''"},
	{"limit_until", "INTEGER NOT NULL DEFAULT 0"},
	{"limit_tries", "INTEGER NOT NULL DEFAULT 0"},
	{"incarnation", "TEXT NOT NULL DEFAULT ''"},
}

// requestColumns are the spawn_requests columns added after it was created.
var requestColumns = [][2]string{
	{"model", "TEXT NOT NULL DEFAULT ''"},
}

// approvalColumns are the approvals columns added after phase 0.
var approvalColumns = [][2]string{
	{"always", "INTEGER NOT NULL DEFAULT 0"},
}

func migrate(db *sql.DB) error {
	if err := addColumns(db, "agents", agentColumns); err != nil {
		return err
	}
	if err := addColumns(db, "spawn_requests", requestColumns); err != nil {
		return err
	}
	return addColumns(db, "approvals", approvalColumns)
}

// addColumns adds the columns table lacks. table is one of this file's constants, never input.
func addColumns(db *sql.DB, table string, cols [][2]string) error {
	rows, err := db.Query(`SELECT name FROM pragma_table_info('` + table + `')`)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		have[n] = true
	}
	rows.Close() // the single connection must be free before the ALTERs
	if err := rows.Err(); err != nil {
		return err
	}
	for _, c := range cols {
		if !have[c[0]] {
			if _, err := db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + c[0] + ` ` + c[1]); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) UpsertAgent(container, name string, now int64) (string, error) {
	key := container + "/" + name
	_, err := s.db.Exec(`INSERT INTO agents(key, container, name, updated) VALUES(?,?,?,?)
		ON CONFLICT(key) DO UPDATE SET updated=excluded.updated`, key, container, name, now)
	return key, err
}

// SetState records an agent's state. A process that ended (done), stopped with its container
// (stopped) or starts again (unknown) is no longer paused. Only an ended one stops waiting on a
// usage limit: a stopped or restarted agent keeps its resume time and tries, which its resumed
// session takes over (daemon.limitWait, restart), unless the user had paused it: its pending
// resume is then given up (-1), so the restart that unpauses it does not resume it either.
func (s *Store) SetState(key, state, sessionID string, now int64) error {
	_, err := s.db.Exec(`UPDATE agents SET state=?,
		session_id=CASE WHEN ?='' THEN session_id ELSE ? END, updated=?,
		paused=CASE WHEN ? IN ('done','stopped','unknown','suspended_cold') THEN 0 ELSE paused END,
		limit_until=CASE WHEN ?='done' THEN 0
			WHEN ? IN ('stopped','unknown') AND paused=1 AND limit_until>0 THEN -1 ELSE limit_until END,
		limit_tries=CASE WHEN ?='done' THEN 0 ELSE limit_tries END WHERE key=?`,
		state, sessionID, sessionID, now, state, state, state, state, key)
	return err
}

const agentCols = `key, container, name, session_id, state, updated, profile, workdir, created, role, parent, repo, branch, task_id, label, args, prompt, paused, model, limit_until, limit_tries, incarnation`

func scanAgent(sc interface{ Scan(...any) error }) (Agent, error) {
	var a Agent
	var args string
	err := sc.Scan(&a.Key, &a.Container, &a.Name, &a.SessionID, &a.State, &a.Updated, &a.Profile, &a.Workdir, &a.Created,
		&a.Role, &a.Parent, &a.Repo, &a.Branch, &a.TaskID, &a.Label, &args, &a.Prompt, &a.Paused, &a.Model, &a.LimitUntil, &a.LimitTries, &a.Incarnation)
	if err == nil && args != "" {
		err = json.Unmarshal([]byte(args), &a.Args)
	}
	return a, err
}

// SetLaunch records what an agent lea spawns is started with: its Claude arguments and its
// first prompt ("" when none).
func (s *Store) SetLaunch(key string, args []string, prompt string) error {
	b := []byte{}
	if len(args) > 0 {
		var err error
		if b, err = json.Marshal(args); err != nil {
			return err
		}
	}
	res, err := s.db.Exec(`UPDATE agents SET args=?, prompt=? WHERE key=? AND profile<>''`, string(b), prompt, key)
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

func (s *Store) queryAgents(q string, args ...any) ([]Agent, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) Agents() ([]Agent, error) {
	return s.queryAgents(`SELECT ` + agentCols + ` FROM agents ORDER BY key`)
}

// SpawnedAgents lists the agents lea spawned in one container, by name.
func (s *Store) SpawnedAgents(container string) ([]Agent, error) {
	return s.queryAgents(`SELECT `+agentCols+` FROM agents WHERE container=? AND profile<>'' ORDER BY name`, container)
}

func (s *Store) AgentByKey(key string) (Agent, error) {
	a, err := scanAgent(s.db.QueryRow(`SELECT `+agentCols+` FROM agents WHERE key=?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, ErrNotFound
	}
	return a, err
}

// NewAgent is an agent lea is about to spawn.
type NewAgent struct {
	Container, Name, Profile, Workdir string
	Role, Parent, Repo, Branch        string
	Model                             string
}

// Caps bound a spawn: at most MaxChildren running children of the parent (0: no bound). An ended
// agent (done) does not count, nor does a row a hook registered.
type Caps struct{ MaxChildren int }

// Register records an agent lea spawns, checking the caps in the same transaction: two spawns at
// the cap never both pass. A row of the same name whose agent ended (state done) is reused from
// scratch (its children are detached: a new agent never inherits them); any other existing row is
// ErrExists.
func (s *Store) Register(n NewAgent, c Caps, now int64) (Agent, error) {
	if n.Role == "" {
		return Agent{}, errors.New("register: an agent needs a role")
	}
	key := n.Container + "/" + n.Name
	tx, err := s.db.Begin()
	if err != nil {
		return Agent{}, err
	}
	defer tx.Rollback()
	var state string
	reuse := false
	switch err := tx.QueryRow(`SELECT state FROM agents WHERE key=?`, key).Scan(&state); {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return Agent{}, err
	case state == "done":
		reuse = true
	default:
		return Agent{}, fmt.Errorf("agent %s: %w", n.Name, ErrExists)
	}
	if n.Parent != "" {
		if n.Parent == key {
			return Agent{}, fmt.Errorf("%w: %s cannot be its own parent", ErrTree, n.Name)
		}
		var grand, prole string
		switch err := tx.QueryRow(`SELECT parent, role FROM agents WHERE key=?`, n.Parent).Scan(&grand, &prole); {
		case errors.Is(err, sql.ErrNoRows):
			return Agent{}, fmt.Errorf("parent %s: %w", nameOf(n.Parent), ErrNotFound)
		case err != nil:
			return Agent{}, err
		case grand != "":
			return Agent{}, fmt.Errorf("%w: %s was started by %s, and a lead's child never spawns", ErrTree, nameOf(n.Parent), nameOf(grand))
		}
		// Checked here, in the transaction SetRole also runs in, so a demotion cannot slip
		// between the spawner's role check and the registration.
		pr, err := roles.Get(prole)
		if err != nil {
			return Agent{}, err
		}
		if err := roles.CanGive(pr, n.Role); err != nil {
			return Agent{}, err
		}
	}
	if n.Parent != "" && c.MaxChildren > 0 {
		var kids int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM agents WHERE parent=? AND state<>'done'`, n.Parent).Scan(&kids); err != nil {
			return Agent{}, err
		}
		if kids >= c.MaxChildren {
			return Agent{}, fmt.Errorf("%w: %s already runs %d agents (max_children %d)", ErrLimit, nameOf(n.Parent), kids, c.MaxChildren)
		}
	}
	inc, err := newIncarnation()
	if err != nil {
		return Agent{}, err
	}
	if reuse {
		if _, err := tx.Exec(`UPDATE agents SET parent='' WHERE parent=?`, key); err != nil {
			return Agent{}, err
		}
		_, err = tx.Exec(`UPDATE agents SET profile=?, workdir=?, created=?, updated=?, state='unknown', session_id='',
			ctx_pct=-1, cost_usd=-1, role=?, parent=?, repo=?, branch=?, task_id=0, label='', args='', prompt='', paused=0, subagents=0, limit_until=0, limit_tries=0, model=?, incarnation=? WHERE key=?`,
			n.Profile, n.Workdir, now, now, n.Role, n.Parent, n.Repo, n.Branch, n.Model, inc, key)
	} else {
		_, err = tx.Exec(`INSERT INTO agents(key, container, name, profile, workdir, created, updated, role, parent, repo, branch, model, incarnation)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, key, n.Container, n.Name, n.Profile, n.Workdir, now, now, n.Role, n.Parent, n.Repo, n.Branch, n.Model, inc)
	}
	if err != nil {
		return Agent{}, err
	}
	if err := tx.Commit(); err != nil {
		return Agent{}, err
	}
	return Agent{Key: key, Container: n.Container, Name: n.Name, State: "unknown", Updated: now, Profile: n.Profile, Model: n.Model,
		Workdir: n.Workdir, Created: now, Role: n.Role, Parent: n.Parent, Repo: n.Repo, Branch: n.Branch, Incarnation: inc}, nil
}

// newIncarnation is a fresh agent incarnation: 16 random bytes in hex.
func newIncarnation() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// RegisterAgent registers a dev agent without caps (callers before roles, tests).
func (s *Store) RegisterAgent(container, name, profile, workdir string, now int64) (Agent, error) {
	return s.Register(NewAgent{Container: container, Name: name, Profile: profile, Workdir: workdir, Role: "dev"}, Caps{}, now)
}

// SetRole changes the role of an agent lea spawned. A role that spawns (canSpawn) is refused to a
// lead's child, and a role that does not is refused to an agent with running children: the tree
// stays a lead and its children (spec A17). Only the user calls it. canSpawn is trusted: callers
// must derive it from roles.Get(role).CanSpawn, never from user or agent input.
func (s *Store) SetRole(key, role string, canSpawn bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var parent, profile string
	err = tx.QueryRow(`SELECT parent, profile FROM agents WHERE key=?`, key).Scan(&parent, &profile)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if profile == "" {
		return fmt.Errorf("agent %s was not started by lea", nameOf(key))
	}
	if canSpawn && parent != "" {
		return fmt.Errorf("%w: %s was started by %s, and a lead's child never spawns", ErrTree, nameOf(key), nameOf(parent))
	}
	if !canSpawn {
		var kids int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM agents WHERE parent=? AND state<>'done'`, key).Scan(&kids); err != nil {
			return err
		}
		if kids > 0 {
			return fmt.Errorf("%w: %s runs %d agents: stop them first", ErrTree, nameOf(key), kids)
		}
	}
	if _, err := tx.Exec(`UPDATE agents SET role=? WHERE key=?`, role, key); err != nil {
		return err
	}
	return tx.Commit()
}

// SetLabel sets the display label of an agent lea spawned ("" clears it). Only the user calls
// it; the caller checks the text (agents.CheckLabel).
func (s *Store) SetLabel(key, label string) error {
	res, err := s.db.Exec(`UPDATE agents SET label=? WHERE key=? AND profile<>''`, label, key)
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

// SetPaused records that the user froze an agent lea spawned (the deck's p), or let it continue.
func (s *Store) SetPaused(key string, paused bool) error {
	res, err := s.db.Exec(`UPDATE agents SET paused=? WHERE key=? AND profile<>''`, paused, key)
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

// SetLimit records an agent's usage-limit state (Agent.LimitUntil, Agent.LimitTries).
func (s *Store) SetLimit(key string, until int64, tries int) error {
	res, err := s.db.Exec(`UPDATE agents SET limit_until=?, limit_tries=? WHERE key=?`, until, tries, key)
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

// Children lists the running agents (state not done) a parent started, by name.
func (s *Store) Children(parent string) ([]Agent, error) {
	return s.queryAgents(`SELECT `+agentCols+` FROM agents WHERE parent=? AND state<>'done' ORDER BY name`, parent)
}

// nameOf is the agent name of a key (container/name); other strings are returned as they are.
func nameOf(key string) string {
	if i := strings.LastIndexByte(key, '/'); i >= 0 {
		return key[i+1:]
	}
	return key
}

// SetLastShown records the agent last shown in a container's slot; LastShown reads it back
// ("" when none). It outlives lea's tmux server and the daemon.
func (s *Store) SetLastShown(container, key string) error {
	_, err := s.db.Exec(`INSERT INTO decks(container, shown) VALUES(?,?)
		ON CONFLICT(container) DO UPDATE SET shown=excluded.shown`, container, key)
	return err
}

func (s *Store) LastShown(container string) (string, error) {
	var k string
	err := s.db.QueryRow(`SELECT shown FROM decks WHERE container=?`, container).Scan(&k)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return k, err
}

// ForgetAgent removes the agent's row and marks its unread messages read: a later agent of the
// same name must not inherit them. Both statements run in one transaction.
func (s *Store) ForgetAgent(key string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE messages SET read_at=CAST(strftime('%s','now') AS INTEGER) WHERE recipient=? AND read_at=0`, key); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE messages SET read_at=CAST(strftime('%s','now') AS INTEGER) WHERE sender=? AND recipient='user' AND read_at=0`, key); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE approvals SET verdict='expired' WHERE agent=? AND verdict=''`, key); err != nil {
		return err
	}
	// Its children stay, without a parent: a later agent of the same name never inherits them.
	if _, err := tx.Exec(`UPDATE agents SET parent='' WHERE parent=?`, key); err != nil {
		return err
	}
	// Its task stays on the board as ended.
	if _, err := tx.Exec(`UPDATE tasks SET ended=CAST(strftime('%s','now') AS INTEGER) WHERE agent=? AND ended=0`, key); err != nil {
		return err
	}
	// Its pending requests are withdrawn; requests waiting for it expire (their senders may ask again).
	if _, err := tx.Exec(`UPDATE spawn_requests SET state='withdrawn', decided=CAST(strftime('%s','now') AS INTEGER)
		WHERE requester=? AND state='pending'`, key); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE spawn_requests SET state='expired', decided=CAST(strftime('%s','now') AS INTEGER)
		WHERE lead=? AND state='pending'`, key); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM leases WHERE agent=?`, key); err != nil {
		return err
	}
	// Its hub waits are abandoned (lea stop --forget, an agent removed for good): no later agent
	// of the same name gets their decisions or is resumed for them. lea cannot withdraw a ticket
	// on the hub (only its Lock can): an abandoned ticket shows on no agent of the deck as
	// verified, only as unverified on a live agent its requester names, if any; it expires on the
	// hub unless answered there; its run, asked by the forgotten agent's token, has no one to call
	// it.
	if _, err := tx.Exec(`DELETE FROM hub_waits WHERE agent_key=?`, key); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM agents WHERE key=?`, key); err != nil {
		return err
	}
	return tx.Commit()
}

// Usage is an agent's latest context use (percent) and cost (USD), -1 when unknown. It lives in
// the agents table (ctx_pct, cost_usd) but not in Agent: only the status line writes it.
type Usage struct {
	CtxPct  int     `json:"ctx_pct"`
	CostUSD float64 `json:"cost_usd"`
}

// SetUsage records an agent's usage and reports whether it changed (an unknown key changes
// nothing).
func (s *Store) SetUsage(key string, u Usage) (bool, error) {
	res, err := s.db.Exec(`UPDATE agents SET ctx_pct=?, cost_usd=? WHERE key=? AND (ctx_pct<>? OR cost_usd<>?)`,
		u.CtxPct, u.CostUSD, key, u.CtxPct, u.CostUSD)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Usages returns the usage of every agent of a container, by key.
func (s *Store) Usages(container string) (map[string]Usage, error) {
	rows, err := s.db.Query(`SELECT key, ctx_pct, cost_usd FROM agents WHERE container=?`, container)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Usage{}
	for rows.Next() {
		var k string
		var u Usage
		if err := rows.Scan(&k, &u.CtxPct, &u.CostUSD); err != nil {
			return nil, err
		}
		out[k] = u
	}
	return out, rows.Err()
}

// SetSubagents records how many subagents an agent runs and reports whether it changed (an
// unknown key changes nothing). Only the daemon writes it, from the Subagent hooks.
func (s *Store) SetSubagents(key string, n int) (bool, error) {
	res, err := s.db.Exec(`UPDATE agents SET subagents=? WHERE key=? AND subagents<>?`, n, key, n)
	if err != nil {
		return false, err
	}
	c, err := res.RowsAffected()
	return c > 0, err
}

// ClearSubagents sets every agent's subagent count to 0: the daemon keeps the running subagents
// in memory, so a count it did not write since it started is stale.
func (s *Store) ClearSubagents() error {
	_, err := s.db.Exec(`UPDATE agents SET subagents=0 WHERE subagents<>0`)
	return err
}

// Subagents returns the agents of a container ("" for every container) that run subagents, by
// key.
func (s *Store) Subagents(container string) (map[string]int, error) {
	rows, err := s.db.Query(`SELECT key, subagents FROM agents WHERE (?='' OR container=?) AND subagents>0`, container, container)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}

func (s *Store) AddEvent(agent, source, kind, payload string, now int64) error {
	_, err := s.db.Exec(`INSERT INTO events(agent, source, kind, payload, at) VALUES(?,?,?,?,?)`,
		agent, source, kind, payload, now)
	return err
}

// EventPayloads returns the payloads of an agent's events of one kind, oldest first.
func (s *Store) EventPayloads(agent, kind string) ([]string, error) {
	rows, err := s.db.Query(`SELECT payload FROM events WHERE agent=? AND kind=? ORDER BY seq`, agent, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

type Message struct {
	ID, Thread           int64
	From, To, Kind, Body string
	ReplyTo              int64
	Hops                 int
	At, ReadAt           int64
}

func (s *Store) Send(m Message) (Message, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Message{}, err
	}
	defer tx.Rollback()
	if m, err = sendTx(tx, m); err != nil {
		return Message{}, err
	}
	if err := tx.Commit(); err != nil {
		return Message{}, err
	}
	return m, nil
}

// sendTx inserts a message in tx: a reply joins its parent's thread, a new message starts its own.
func sendTx(tx *sql.Tx, m Message) (Message, error) {
	if m.ReplyTo != 0 {
		err := tx.QueryRow(`SELECT thread FROM messages WHERE id=?`, m.ReplyTo).Scan(&m.Thread)
		if errors.Is(err, sql.ErrNoRows) {
			return Message{}, fmt.Errorf("reply_to %d: %w", m.ReplyTo, ErrNotFound)
		}
		if err != nil {
			return Message{}, err
		}
	}
	res, err := tx.Exec(`INSERT INTO messages(thread, sender, recipient, kind, body, reply_to, hops, at)
		VALUES(?,?,?,?,?,?,?,?)`, m.Thread, m.From, m.To, m.Kind, m.Body, m.ReplyTo, m.Hops, m.At)
	if err != nil {
		return Message{}, err
	}
	if m.ID, err = res.LastInsertId(); err != nil {
		return Message{}, err
	}
	if m.Thread == 0 {
		m.Thread = m.ID
		if _, err := tx.Exec(`UPDATE messages SET thread=? WHERE id=?`, m.ID, m.ID); err != nil {
			return Message{}, err
		}
	}
	return m, nil
}

const msgCols = `id, thread, sender, recipient, kind, body, reply_to, hops, at, read_at`

func scanMsg(sc interface{ Scan(...any) error }) (Message, error) {
	var m Message
	err := sc.Scan(&m.ID, &m.Thread, &m.From, &m.To, &m.Kind, &m.Body, &m.ReplyTo, &m.Hops, &m.At, &m.ReadAt)
	return m, err
}

func (s *Store) Get(id int64) (Message, error) {
	m, err := scanMsg(s.db.QueryRow(`SELECT `+msgCols+` FROM messages WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	return m, err
}

func (s *Store) Unread(to string, limit int) ([]Message, error) {
	rows, err := s.db.Query(`SELECT `+msgCols+` FROM messages WHERE recipient=? AND read_at=0
		ORDER BY id LIMIT ?`, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMsg(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MarkRead marks messages read, all or none.
func (s *Store) MarkRead(ids []int64, now int64) error {
	return s.eachID(ids, `UPDATE messages SET read_at=? WHERE id=? AND read_at=0`, now)
}

// MarkUnread puts messages back in their recipient's inbox: a delivery that never reached it.
func (s *Store) MarkUnread(ids []int64) error {
	return s.eachID(ids, `UPDATE messages SET read_at=0 WHERE id=?`)
}

// eachID runs q once per id in one transaction; lead are the arguments before the id.
func (s *Store) eachID(ids []int64, q string, lead ...any) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		args := append(append([]any{}, lead...), id)
		if _, err := tx.Exec(q, args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UnreadCount counts the unread messages of one recipient.
func (s *Store) UnreadCount(to string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE recipient=? AND read_at=0`, to).Scan(&n)
	return n, err
}

// UnreadCounts counts unread messages by recipient.
func (s *Store) UnreadCounts() (map[string]int, error) {
	return s.countBy(`SELECT recipient, COUNT(*) FROM messages WHERE read_at=0 GROUP BY recipient`)
}

// UnreadBySender counts the unread messages to one recipient by sender.
func (s *Store) UnreadBySender(to string) (map[string]int, error) {
	return s.countBy(`SELECT sender, COUNT(*) FROM messages WHERE recipient=? AND read_at=0 GROUP BY sender`, to)
}

func (s *Store) countBy(q string, args ...any) (map[string]int, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}

// Thread lists the last limit messages key sent or received, oldest first.
func (s *Store) Thread(key string, limit int) ([]Message, error) {
	rows, err := s.db.Query(`SELECT `+msgCols+` FROM (SELECT `+msgCols+` FROM messages
		WHERE sender=? OR recipient=? ORDER BY id DESC LIMIT ?) ORDER BY id`, key, key, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMsg(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ReplyFrom returns the first message from sent to to in reply to message id, read or not;
// ErrNotFound while there is none. A reply from sent to anyone else is never to's: reply_to is
// whatever the sender chose.
func (s *Store) ReplyFrom(id int64, from, to string) (Message, error) {
	m, err := scanMsg(s.db.QueryRow(`SELECT `+msgCols+` FROM messages WHERE reply_to=? AND sender=?
		AND recipient=? ORDER BY id LIMIT 1`, id, from, to))
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	return m, err
}

func (s *Store) SentSince(from string, since int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE sender=? AND at>=?`, from, since).Scan(&n)
	return n, err
}

// Approval is one permission request. Verdict is "" while pending, then "allow", "deny",
// "always" or "expired" (nobody answered in time: the agent's own dialog took over). Always says
// whether the agent offered an "always allow" rule for it.
type Approval struct {
	ID                          int64
	Agent, Tool, Input, Verdict string
	At                          int64
	Always                      bool
}

const approvalCols = `id, agent, tool, input, verdict, at, always`

func scanApproval(sc interface{ Scan(...any) error }) (Approval, error) {
	var a Approval
	var always int
	err := sc.Scan(&a.ID, &a.Agent, &a.Tool, &a.Input, &a.Verdict, &a.At, &always)
	a.Always = always != 0
	return a, err
}

func (s *Store) AddApproval(agent, tool, input string, always bool, now int64) (int64, error) {
	b := 0
	if always {
		b = 1
	}
	res, err := s.db.Exec(`INSERT INTO approvals(agent, tool, input, always, at) VALUES(?,?,?,?,?)`, agent, tool, input, b, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ExpireApproval closes a pending approval nobody answered in time. ErrDecided means a verdict
// was stored first (read it with ApprovalByID); ErrNotFound means there is no such approval.
func (s *Store) ExpireApproval(id int64) error { return s.SetVerdict(id, "expired") }

// ExpirePendingApprovals closes every pending approval: at startup no hook waits for any.
func (s *Store) ExpirePendingApprovals() error {
	_, err := s.db.Exec(`UPDATE approvals SET verdict='expired' WHERE verdict=''`)
	return err
}

func (s *Store) ApprovalByID(id int64) (Approval, error) {
	a, err := scanApproval(s.db.QueryRow(`SELECT `+approvalCols+` FROM approvals WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Approval{}, ErrNotFound
	}
	return a, err
}

func (s *Store) SetVerdict(id int64, verdict string) error {
	res, err := s.db.Exec(`UPDATE approvals SET verdict=? WHERE id=? AND verdict=''`, verdict, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	var v string
	if err := s.db.QueryRow(`SELECT verdict FROM approvals WHERE id=?`, id).Scan(&v); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return ErrDecided
}

func (s *Store) PendingApprovals() ([]Approval, error) {
	rows, err := s.db.Query(`SELECT ` + approvalCols + ` FROM approvals WHERE verdict='' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Approval
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
