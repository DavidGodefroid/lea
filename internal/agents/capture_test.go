package agents

import (
	"reflect"
	"strings"
	"testing"
)

// lea web's terminal view (#416) reads an agent's pane: its lines with their colours, its size
// and cursor, never typing into it.
func TestCaptureReadsTheAgentsPane(t *testing.T) {
	e := newEnv(t)
	s := e.ensure(t, "/p")
	a := e.spawn(t, "a1")
	var before int
	s.locked(func() { s.byAgent("a1").screen = []string{"\x1b[32mok\x1b[0m tests", "$ "}; before = len(s.log) })
	sc, err := e.m.Capture(bg, a.Key)
	if err != nil {
		t.Fatal(err)
	}
	want := Screen{Lines: []string{"\x1b[32mok\x1b[0m tests", "$ "}, Width: 80, Height: 24, CursorX: 5, CursorY: 2}
	if !reflect.DeepEqual(sc, want) {
		t.Fatalf("screen=%+v", sc)
	}
	id := s.byAgent("a1").id
	if !s.logged("capture-pane -p -e -t " + id) {
		t.Fatalf("log=%q", s.log)
	}
	for _, l := range s.log[before:] {
		if strings.HasPrefix(l, "send-keys") || strings.HasPrefix(l, "select-pane") {
			t.Fatalf("capture typed into or focused a pane: %q", l)
		}
	}
	if _, err := e.m.Capture(bg, C+"/nobody"); err == nil {
		t.Fatal("unknown agent captured")
	}
	if _, err := e.m.Capture(bg, ShellKey(C, "s1")); err == nil {
		t.Fatal("a shell captured")
	}
}

func TestCaptureOfAnAgentWithoutTerminal(t *testing.T) {
	e := newEnv(t)
	e.ensure(t, "/p")
	e.st.RegisterAgent(C, "gone", "claude", "/p", 900)
	if _, err := e.m.Capture(bg, C+"/gone"); err == nil || !strings.Contains(err.Error(), "no terminal") {
		t.Fatalf("err=%v", err)
	}
}
