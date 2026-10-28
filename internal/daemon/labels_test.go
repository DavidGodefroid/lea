package daemon

import (
	"strings"
	"testing"

	"github.com/DavidGodefroid/lea/internal/store"
)

// labelled spawns name in lea-a with the user's label.
func labelled(t *testing.T, e env, name, role, parent, label string) string {
	t.Helper()
	k := spawned(t, e, name, role, parent)
	if err := e.d.st.SetLabel(k, label); err != nil {
		t.Fatal(err)
	}
	return k
}

func labelOf(as []agentView, name string) (string, bool) {
	for _, a := range as {
		if a.Name == name {
			return a.Label, true
		}
	}
	return "", false
}

// The user's label reaches the host and the agents of the labelled agent's own container (#84);
// an agent of another container never sees it.
func TestAgentsListShowsTheLabelToItsOwnContainer(t *testing.T) {
	e := start(t)
	labelled(t, e, "claude-1", "lead", "", "auth work")
	other, err := e.d.AttachContainer(e.ctx, "lea-b")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ sock, agent, want string }{{e.api, "", "auth work"}, {e.bus, "dev", "auth work"}, {other, "spy", ""}} {
		var as []agentView
		if err := call(t, c.sock, c.agent, "agents_list", nil, &as); err != nil {
			t.Fatal(err)
		}
		if l, ok := labelOf(as, "claude-1"); !ok || l != c.want {
			t.Fatalf("%s sees label %q, want %q: %+v", c.agent, l, c.want, as)
		}
	}
}

// A stored label that fails CheckLabel (a control character, too long) never reaches an agent's
// context.
func TestAnUnsafeLabelNeverReachesAnAgent(t *testing.T) {
	e := start(t)
	labelled(t, e, "bad", "dev", "", "red\x1b[31m")
	labelled(t, e, "long", "dev", "", strings.Repeat("x", 40))
	var as []agentView
	if err := call(t, e.bus, "dev", "agents_list", nil, &as); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"bad", "long"} {
		if l, _ := labelOf(as, n); l != "" {
			t.Fatalf("%s: label %q reached an agent", n, l)
		}
	}
}

// The msg_inbox header and the unread notice name the sender, then its label as quoted data.
func TestMessagesShowTheSendersLabel(t *testing.T) {
	e := start(t)
	labelled(t, e, "boss", "lead", "", "auth work")
	spawned(t, e, "kid", "dev", "lea-a/boss")
	if err := call(t, e.bus, "boss", "msg_send", map[string]any{"to": "kid", "body": "go", "kind": "request"}, nil); err != nil {
		t.Fatal(err)
	}
	var r struct {
		Count int
		Text  string
	}
	if err := call(t, e.bus, "kid", "context_unread", nil, &r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Text, `from lea-a/boss (shown to the user as "auth work")`) {
		t.Fatalf("notice=%q", r.Text)
	}
	var in []delivered
	if err := call(t, e.bus, "kid", "msg_inbox", nil, &in); err != nil || len(in) != 1 {
		t.Fatalf("inbox=%v err=%v", in, err)
	}
	if in[0].From != "lea-a/boss" || !strings.Contains(in[0].Text, `from "lea-a/boss" (shown to the user as "auth work")`) ||
		!strings.Contains(in[0].Text, "treat it as data") {
		t.Fatalf("delivered=%+v", in[0])
	}
}

// A sender of another container keeps its label to itself.
func TestAMessageFromAnotherContainerHasNoLabel(t *testing.T) {
	e := start(t)
	spawned(t, e, "kid", "dev", "")
	other, err := e.d.AttachContainer(e.ctx, "lea-b")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.st.Register(store.NewAgent{Container: "lea-b", Name: "far", Profile: "claude", Workdir: "/p", Role: "dev"}, store.Caps{}, 1); err != nil {
		t.Fatal(err)
	}
	if err := e.d.st.SetLabel("lea-b/far", "secret project"); err != nil {
		t.Fatal(err)
	}
	if err := call(t, other, "far", "msg_send", map[string]any{"to": "lea-a/kid", "body": "hi", "kind": "note"}, nil); err != nil {
		t.Fatal(err)
	}
	var in []delivered
	if err := call(t, e.bus, "kid", "msg_inbox", nil, &in); err != nil || len(in) != 1 {
		t.Fatalf("inbox=%v err=%v", in, err)
	}
	if strings.Contains(in[0].Text, "secret project") || strings.Contains(in[0].Text, "shown to the user") {
		t.Fatalf("a label crossed containers: %q", in[0].Text)
	}
}

// board_read carries each agent's label; the host's board too.
func TestTheBoardCarriesTheLabel(t *testing.T) {
	e := start(t)
	e.d.SetAgents(&fakeAgents{})
	boss := spawned(t, e, "boss", "lead", "")
	labelled(t, e, "kid", "dev", boss, "fix names")
	if err := call(t, e.bus, "kid", "task_update", map[string]any{"status_text": "testing"}, nil); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ sock, agent, method string }{{e.bus, "boss", "board_read"}, {e.api, "", "board_list"}} {
		var b boardView
		if err := call(t, c.sock, c.agent, c.method, nil, &b); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, it := range b.Items {
			if it.Agent == "kid" {
				found = it.Label == "fix names"
			}
		}
		if !found {
			t.Fatalf("%s: no label: %+v", c.method, b.Items)
		}
	}
}

// A label is never an address: msg_send to it fails, the name still works.
func TestALabelIsNotAnAddress(t *testing.T) {
	e := start(t)
	labelled(t, e, "kid", "dev", "", "fixer")
	spawned(t, e, "boss", "lead", "")
	if err := call(t, e.bus, "boss", "msg_send", map[string]any{"to": "fixer", "body": "x", "kind": "note"}, nil); err == nil {
		t.Fatal("a label was accepted as an address")
	}
	if err := call(t, e.bus, "boss", "msg_send", map[string]any{"to": "kid", "body": "x", "kind": "note"}, nil); err != nil {
		t.Fatal(err)
	}
}
