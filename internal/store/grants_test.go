package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/grants"
)

// oldGrants is the grants table as lea v0.14 created it: no set_id, no grant_sets.
const oldGrants = `CREATE TABLE grants(
  id INTEGER PRIMARY KEY AUTOINCREMENT, container TEXT NOT NULL, action TEXT NOT NULL,
  repo TEXT NOT NULL DEFAULT '', branch TEXT NOT NULL DEFAULT '', agent TEXT NOT NULL DEFAULT '',
  github TEXT NOT NULL DEFAULT '', created INTEGER NOT NULL, expires INTEGER NOT NULL, revoked INTEGER NOT NULL DEFAULT 0);
CREATE TABLE grant_uses(
  id INTEGER PRIMARY KEY AUTOINCREMENT, grant_id INTEGER NOT NULL, agent TEXT NOT NULL,
  action TEXT NOT NULL, target TEXT NOT NULL, at INTEGER NOT NULL);`

// TestGrantSetsMigration: a database of the previous release opens with its active grants in
// sets (#319): a lea's standing rows (lea delivery on) become one deliver set, every other active
// row a set of one; revoked and expired rows keep none; ids do not change; a second open changes
// nothing; and the hook's view (ActiveGrants) is the same.
func TestGrantSetsMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lea.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(oldGrants); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	rows := []struct {
		container, action, repo string
		expires, revoked        int64
	}{
		{"lea-a", "commit-identity", "api", grants.Standing, 0}, // 1
		{"lea-a", "pr-create", "api", grants.Standing, 0},       // 2
		{"lea-a", "merge", "api", grants.Standing, 0},           // 3
		{"lea-a", "delete-own-branch", "api", grants.Standing, 0},
		{"lea-a", "commit-identity", "web", grants.Standing, 0}, // 5
		{"lea-a", "close-issue", "api", now + 3600, 0},          // 6: a hand grant
		{"lea-a", "merge", "web", now + 3600, now - 10},         // 7: revoked
		{"lea-a", "merge", "web", now - 10, 0},                  // 8: expired
		{"lea-b", "commit-identity", "x", grants.Standing, 0},   // 9
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO grants(container, action, repo, github, created, expires, revoked) VALUES(?,?,?,?,?,?,?)`,
			r.container, r.action, r.repo, "github.com/o/"+r.repo, now-100, r.expires, r.revoked); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO grant_uses(grant_id, agent, action, target, at) VALUES(3, 'lea-a/dev', 'merge', 'pr 1', ?), (9, 'lea-b/dev', 'commit-identity', 'x', ?)`, now-5, now-4); err != nil {
		t.Fatal(err)
	}
	db.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	before, err := st.ActiveGrants("", now)
	if err != nil {
		t.Fatal(err)
	}
	sets, err := st.GrantSets("lea-a", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 2 || !sets[0].UntilRevoked() || grants.Short(sets[0].Capabilities) != grants.Deliver || len(sets[0].Grants) != 5 ||
		sets[1].Expires != now+3600 || len(sets[1].Grants) != 1 || sets[1].Grants[0].ID != 6 || strings.Join(sets[1].Capabilities, " ") != "issues" {
		t.Fatalf("lea-a sets: %+v", sets)
	}
	for i, g := range sets[0].Grants {
		if g.ID != int64(i+1) || g.SetID != sets[0].ID {
			t.Fatalf("deliver row %d: %+v", i, g)
		}
	}
	if b, _ := st.GrantSets("lea-b", now); len(b) != 1 || b[0].ID == sets[0].ID {
		t.Fatalf("lea-b sets: %+v", b)
	}
	all, _ := st.RecentGrants(20)
	for _, g := range all {
		if (g.ID == 7 || g.ID == 8) && g.SetID != 0 {
			t.Fatalf("revoked or expired row got a set: %+v", g)
		}
	}
	st.Close()
	// Idempotent: a second open finds nothing to do.
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	again, _ := st.GrantSets("lea-a", now)
	if len(again) != 2 || again[0].ID != sets[0].ID || again[1].ID != sets[1].ID {
		t.Fatalf("second open: %+v", again)
	}
	var n int
	if err := st.db.QueryRow(`SELECT count(*) FROM grant_sets`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("sets after a second open: %d %v", n, err)
	}
	after, _ := st.ActiveGrants("", now)
	if len(after) != len(before) {
		t.Fatalf("active grants changed: %d then %d", len(before), len(after))
	}
	for i := range after {
		after[i].SetID = 0
		before[i].SetID = 0
		if after[i] != before[i] {
			t.Fatalf("active grant changed: %+v then %+v", before[i], after[i])
		}
	}
	// Uses are per lea, with their set.
	us, err := st.GrantUses("lea-a", 10)
	if err != nil || len(us) != 1 || us[0].Grant != 3 || us[0].Set != sets[0].ID {
		t.Fatalf("lea-a uses: %+v %v", us, err)
	}
	if us, _ := st.GrantUses("", 10); len(us) != 2 {
		t.Fatalf("every use: %+v", us)
	}
	last, _ := st.LastGrantSetUses("lea-a")
	if len(last) != 1 || last[sets[0].ID] != now-5 {
		t.Fatalf("last uses: %v", last)
	}
	pins, _ := st.LastPins("lea-a")
	if pins["api"] != "github.com/o/api" || pins["web"] != "github.com/o/web" || len(pins) != 2 {
		t.Fatalf("pins: %v", pins)
	}
}

// TestGrantSetRevoke: a set ends with all its rows in one transaction, once; a row revoked on
// its own ends its set when it was the last active one; ChangeGrantSet adds and revokes rows.
func TestGrantSetRevoke(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "lea.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const now = 1000
	row := func(action, repo string) grants.Grant {
		return grants.Grant{Action: action, Repo: repo, Pin: "github.com/o/" + repo}
	}
	s, err := st.AddGrantSet(grants.Set{Container: "lea-a", Capabilities: []string{"deliver"}, Created: now, Expires: grants.Standing},
		[]grants.Grant{row(grants.CommitIdentity, "api"), row(grants.Merge, "api")})
	if err != nil || s.ID == 0 || len(s.Grants) != 2 || s.Grants[1].SetID != s.ID || s.Grants[1].Expires != grants.Standing {
		t.Fatalf("add: %+v %v", s, err)
	}
	if _, err := st.AddGrantSet(grants.Set{Container: "lea-a", Capabilities: []string{"rm-rf"}, Created: now, Expires: now + 60}, nil); err == nil {
		t.Fatal("an unknown capability")
	}
	if err := st.ChangeGrantSet(s.ID, []grants.Grant{row(grants.Merge, "web")}, []int64{s.Grants[1].ID}, now+1); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GrantSetByID(s.ID)
	if len(got.Grants) != 3 || got.Grants[1].Revoked != now+1 || got.Grants[2].Repo != "web" || got.Grants[2].Expires != grants.Standing {
		t.Fatalf("changed: %+v", got)
	}
	if err := st.RevokeGrantSet(s.ID, now+2); err != nil {
		t.Fatal(err)
	}
	if err := st.RevokeGrantSet(s.ID, now+3); !errors.Is(err, ErrDecided) {
		t.Fatalf("twice: %v", err)
	}
	if err := st.RevokeGrantSet(99, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("none: %v", err)
	}
	if err := st.ChangeGrantSet(s.ID, nil, nil, now+3); !errors.Is(err, ErrDecided) {
		t.Fatalf("change a revoked set: %v", err)
	}
	gs, _ := st.ActiveGrants("lea-a", now+4)
	if len(gs) != 0 {
		t.Fatalf("rows outlived their set: %+v", gs)
	}
	// The last active row of a set revoked on its own ends the set.
	one, err := st.AddGrant(grants.Grant{Container: "lea-a", Action: grants.StopFinished, Created: now, Expires: now + 60})
	if err != nil || one.SetID == 0 {
		t.Fatalf("set of one: %+v %v", one, err)
	}
	if err := st.RevokeGrant(one.ID, now+5); err != nil {
		t.Fatal(err)
	}
	if x, _ := st.GrantSetByID(one.SetID); x.Revoked != now+5 {
		t.Fatalf("set of a revoked row: %+v", x)
	}
	if sets, _ := st.GrantSets("lea-a", now+6); len(sets) != 0 {
		t.Fatalf("sets: %+v", sets)
	}
}
