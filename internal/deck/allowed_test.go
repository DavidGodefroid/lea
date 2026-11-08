package deck

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/grants"
)

// allowAPI is fakeAPI with the Allowed screen's calls.
type allowAPI struct {
	fakeAPI
	rows []Allowance
}

func (f *allowAPI) Allowances(ctx context.Context, c string) ([]Allowance, error) {
	f.rec("allowances " + c)
	return f.rows, f.err
}

func (f *allowAPI) Revoke(ctx context.Context, c string, id int64) error {
	f.rec(fmt.Sprintf("revoke %s %d", c, id))
	var keep []Allowance
	for _, r := range f.rows {
		if r.ID != id {
			keep = append(keep, r)
		}
	}
	f.rows = keep
	return f.err
}

// TestAllowanceRows: a set reads as its capabilities (the preset's name for deliver), its
// repositories and pins, "until revoked" or the time left, and its last use.
func TestAllowanceRows(t *testing.T) {
	const now = 1_000_000
	g := func(action, repo string) grants.Grant {
		return grants.Grant{Action: action, Repo: repo, Pin: "github.com/o/" + repo, Expires: grants.Standing}
	}
	sets := []grants.Set{
		{ID: 3, Capabilities: []string{"push", "pr", "merge", "delete-branch"}, Expires: grants.Standing,
			Grants: []grants.Grant{g(grants.CommitIdentity, "api"), g(grants.PRCreate, "api"), g(grants.Merge, "api"), g(grants.DeleteOwnBranch, "api")}},
		{ID: 4, Capabilities: []string{"merge"}, Expires: now + 7200,
			Grants: []grants.Grant{{Action: grants.Merge, Repo: "web", Pin: "gitlab:git.example.com/g/web", Branch: "feat/*", Agent: "lea-a/lead", Expires: now + 7200}}},
		{ID: 5, Capabilities: []string{"stop-finished"}, Expires: grants.Standing, Grants: []grants.Grant{{Action: grants.StopFinished, Expires: grants.Standing}}},
	}
	rows := allowanceRows(sets, map[string]int64{"3": 1_700_000_000}, now)
	want := []Allowance{
		{3, "deliver", "api (github.com/o/api)", "until revoked", "used " + timeText(1_700_000_000)},
		{4, "merge", "web (gitlab:git.example.com/g/web), branches feat/*, for lea-a/lead and its children", "2h0m0s left", "never used"},
		{5, "stop-finished", "every agent", "until revoked", "never used"},
	}
	if fmt.Sprint(rows) != fmt.Sprint(want) {
		t.Fatalf("rows:\n%v\nwant\n%v", rows, want)
	}
}

// TestAllowedScreen: g opens the screen, read from the host; x asks the usual confirmation, y
// revokes the selected allowance and reads the rows again; n keeps it; cells are escaped.
func TestAllowedScreen(t *testing.T) {
	api := &allowAPI{rows: []Allowance{{ID: 1, Allows: "deliver", Where: "api (github.com/o/api)", Until: "until revoked", Last: "never used"},
		{ID: 2, Allows: "merge", Where: "web\x1b[31m (github.com/o/web)", Until: "1h0m0s left", Last: "never used"}}}
	m := sized(loaded(t, &api.fakeAPI, ""), 50, 20)
	m.api = api
	m, cmd := step(t, m, key("g"))
	if !m.allowOpen || cmd == nil {
		t.Fatal("g did not open the screen")
	}
	m, _ = step(t, m, cmd())
	view := m.View()
	for _, w := range []string{"Allowed", "#1 deliver", "until revoked", "api (github.com/o/api)", "#2 merge", "1h0m0s left", "x revoke"} {
		if !strings.Contains(view, w) {
			t.Fatalf("view lacks %q:\n%s", w, view)
		}
	}
	if strings.Contains(view, "\x1b[31m") {
		t.Fatalf("a control sequence of a cell reached the terminal:\n%q", view)
	}
	m, _ = step(t, m, key("j"))
	m, _ = step(t, m, key("x"))
	if m.confirm != "allow:2" || !strings.Contains(m.View(), "Revoke allowance #2?") {
		t.Fatalf("x: confirm %q\n%s", m.confirm, m.View())
	}
	m, _ = step(t, m, key("n"))
	if m.confirm != "" || !m.allowOpen {
		t.Fatalf("n: confirm %q open %v", m.confirm, m.allowOpen)
	}
	for _, c := range api.got() {
		if strings.HasPrefix(c, "revoke") {
			t.Fatalf("n revoked: %v", api.got())
		}
	}
	m, _ = step(t, m, key("x"))
	m, cmd = step(t, m, key("y"))
	if cmd == nil {
		t.Fatal("y: no call")
	}
	m, _ = step(t, m, cmd())
	if got := strings.Join(api.got(), ","); !strings.Contains(got, "revoke lea-a 2,allowances lea-a") {
		t.Fatalf("calls: %s", got)
	}
	if len(m.allowList) != 1 || m.allowSel != 0 || strings.Contains(m.View(), "#2 merge") {
		t.Fatalf("after revoke: %+v", m.allowList)
	}
	// A snapshot while it is open reads the rows again; esc closes it.
	m, cmd = step(t, m, snapshotMsg{snap: Snapshot{Version: 8}})
	if cmd == nil {
		t.Fatal("no read after a snapshot")
	}
	m, _ = step(t, m, key("esc"))
	if m.allowOpen {
		t.Fatal("esc did not close")
	}
	// Without the host's calls (another API) the screen says so.
	plainM := loaded(t, &fakeAPI{}, "")
	plainM, _ = step(t, plainM, key("g"))
	if plainM.allowOpen || plainM.status != "no allowances here" {
		t.Fatalf("no AllowAPI: %v %q", plainM.allowOpen, plainM.status)
	}
}

func timeText(at int64) string { return time.Unix(at, 0).Format("2006-01-02 15:04") }
