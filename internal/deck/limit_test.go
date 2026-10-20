package deck

import (
	"strings"
	"testing"
	"time"
)

func limited(name string, until int64) Agent {
	a := ag(name, "idle", clock.Unix())
	a.LimitUntil = until
	return a
}

// An agent stopped on its usage limit shows its own mark and when lea resumes it (local time,
// the day too beyond a day), or that the user must resume it; never its task status meanwhile.
func TestALimitedAgentShowsWhenItResumes(t *testing.T) {
	soon := clock.Unix() + 3600
	later := clock.Unix() + 2*24*3600
	for _, c := range []struct {
		until int64
		want  string
	}{
		{soon, "usage limit, resumes " + time.Unix(soon, 0).In(clock.Location()).Format("15:04")},
		{later, "usage limit, resumes " + time.Unix(later, 0).In(clock.Location()).Format("Mon 15:04")},
		{-1, "usage limit, resume it"},
	} {
		a := limited("a", c.until)
		a.Status = "writing tests"
		if got := shownState(a); got != "limited" {
			t.Fatalf("shownState=%q", got)
		}
		s := strings.Join(viewLines(sized(loaded(t, &fakeAPI{}, "", a), 64, 8)), "\n")
		if !strings.Contains(s, c.want) || strings.Contains(s, "writing tests") || !strings.Contains(s, "◷") {
			t.Errorf("until %d: want %q in\n%s", c.until, c.want, s)
		}
	}
	// A working agent (resumed, its turn under way) is no longer shown limited.
	w := limited("a", soon)
	w.State = "working"
	if got := shownState(w); got != "working" {
		t.Fatalf("shownState=%q", got)
	}
	if g, _ := stateMark("limited", 0, clock.Unix(), 0, true); g != "~" {
		t.Fatalf("ascii mark %q", g)
	}
}

// A folder shows limited when an agent is and none works or needs the user.
func TestAFolderWithALimitedAgent(t *testing.T) {
	st := &memStore{l: Layout{Folders: []Folder{{ID: "f1", Name: "web", Folded: true, Agents: []string{"lea-a/a", "lea-a/b"}}}}}
	for _, c := range []struct {
		b    Agent
		want string
	}{
		{ag("b", "idle", 9_990), "limited"},
		{ag("b", "working", 9_990), "working"},
		{ag("b", "blocked", 9_990), "blocked"},
	} {
		m := withFolders(t, st, limited("a", clock.Unix()+60), c.b)
		if got, _ := m.folderState(0); got != c.want {
			t.Errorf("with %s: %q, want %q", c.b.State, got, c.want)
		}
	}
}
