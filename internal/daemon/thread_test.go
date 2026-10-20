package daemon

import (
	"encoding/json"
	"strings"
	"testing"
)

type threadItem struct {
	ID      int64  `json:"id"`
	From    string `json:"from"`
	To      string `json:"to"`
	Kind    string `json:"kind"`
	Body    string `json:"body"`
	ReadAt  int64  `json:"read_at"`
	ReplyTo int64  `json:"reply_to"`
}

// An answer carries the message it answers, so the deck shows a question and its answer as a
// pair (#28); a message that answers none carries no reply_to.
func TestThreadListTiesAnAnswerToItsMessage(t *testing.T) {
	e := start(t)
	call(t, e.bus, "dev", "agents_list", nil, nil)
	call(t, e.bus, "ops", "agents_list", nil, nil)
	var q struct{ ID int64 }
	if err := call(t, e.bus, "dev", "msg_send", map[string]any{"to": "ops", "body": "port?", "kind": "request"}, &q); err != nil {
		t.Fatal(err)
	}
	if err := call(t, e.bus, "ops", "msg_reply", map[string]any{"id": q.ID, "status": "ok", "body": "8080"}, nil); err != nil {
		t.Fatal(err)
	}
	var th []threadItem
	if err := call(t, e.api, "", "thread_list", map[string]any{"key": "lea-a/dev"}, &th); err != nil {
		t.Fatal(err)
	}
	if len(th) != 2 || th[0].ReplyTo != 0 || th[1].ReplyTo != q.ID || th[1].From != "lea-a/ops" {
		t.Fatalf("thread=%+v question=%d", th, q.ID)
	}
}

func TestThreadListShowsBothDirectionsAndReadsOnlyTheUsersMessages(t *testing.T) {
	e := start(t)
	call(t, e.bus, "dev", "agents_list", nil, nil)
	call(t, e.bus, "ops", "agents_list", nil, nil)
	userSends(t, e, "dev", "do x")
	if err := call(t, e.bus, "dev", "msg_send", map[string]any{"to": "user", "body": "done", "kind": "result"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := call(t, e.bus, "ops", "msg_send", map[string]any{"to": "dev", "body": "fyi", "kind": "note"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := call(t, e.bus, "ops", "msg_send", map[string]any{"to": "user", "body": "not dev's", "kind": "note"}, nil); err != nil {
		t.Fatal(err)
	}
	var th []threadItem
	if err := call(t, e.api, "", "thread_list", map[string]any{"key": "lea-a/dev"}, &th); err != nil {
		t.Fatal(err)
	}
	if len(th) != 3 || th[0].Body != "do x" || th[1].Body != "done" || th[1].To != "user" || th[2].From != "lea-a/ops" {
		t.Fatalf("thread=%+v", th)
	}
	if n, _ := e.d.st.UnreadCount("user"); n != 1 {
		t.Fatalf("user unread=%d: only dev's message to the user is read by showing dev's thread", n)
	}
	if n, _ := e.d.st.UnreadCount("lea-a/dev"); n != 2 {
		t.Fatalf("dev unread=%d: the agent's inbox must not be touched", n)
	}
	if err := call(t, e.bus, "dev", "thread_list", map[string]any{"key": "lea-a/dev"}, nil); err == nil || !strings.Contains(err.Error(), "host only") {
		t.Fatalf("from a container: err=%v", err)
	}
	if err := call(t, e.api, "", "thread_list", map[string]any{"key": "lea-a/nobody"}, nil); err == nil {
		t.Fatal("an unknown agent's thread")
	}
	for _, l := range []int{-1, 501} {
		if err := call(t, e.api, "", "thread_list", map[string]any{"key": "lea-a/dev", "limit": l}, nil); err == nil {
			t.Fatalf("limit %d accepted", l)
		}
	}
}

func TestThreadListFitsOnTheWire(t *testing.T) {
	e := start(t)
	call(t, e.bus, "dev", "agents_list", nil, nil)
	// 60 KiB bodies, cut to 16 KiB in the view; each '<' encodes as < (6 bytes), so only
	// a few of the 30 fit under batchBytes.
	big := strings.Repeat("<", 60<<10)
	for i := 0; i < 30; i++ {
		if i%10 == 0 {
			*e.clock += 61
		}
		if err := call(t, e.bus, "dev", "msg_send", map[string]any{"to": "user", "body": big, "kind": "note"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	var th []threadItem
	if err := call(t, e.api, "", "thread_list", map[string]any{"key": "lea-a/dev"}, &th); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(th)
	if len(th) == 0 || len(th) == 30 || len(b) > batchBytes {
		t.Fatalf("%d messages, %d bytes", len(th), len(b))
	}
	if th[len(th)-1].ID != 30 || len(th[0].Body) > threadBody+len(cutMark) {
		t.Fatalf("newest id %d, first body %d bytes", th[len(th)-1].ID, len(th[0].Body))
	}
	if n, _ := e.d.st.UnreadCount("user"); n != 30-len(th) {
		t.Fatalf("user unread=%d: messages not returned were marked read", n)
	}
}
