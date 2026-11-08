package store

import (
	"errors"
	"testing"
)

// The user's pause of an agent's inbox (#95): hold_since is persisted, cleared when the agent
// ends or stops, and absent from a new agent reusing the name.
func TestSetHoldPersistsAndClearsWhenTheAgentEnds(t *testing.T) {
	s := open(t)
	if _, err := s.RegisterAgent("lea-a", "tower", "claude", "/p", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.SetHold("lea-a/tower", 100); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.AgentByKey("lea-a/tower"); a.HoldSince != 100 {
		t.Fatalf("hold_since=%d", a.HoldSince)
	}
	// Its own hooks and a restart (unknown) keep the pause.
	for _, st := range []string{"idle", "working", "unknown"} {
		s.SetState("lea-a/tower", st, "", 2)
		if a, _ := s.AgentByKey("lea-a/tower"); a.HoldSince != 100 {
			t.Fatalf("%s cleared the pause: %+v", st, a)
		}
	}
	if err := s.SetHold("lea-a/tower", 0); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.AgentByKey("lea-a/tower"); a.HoldSince != 0 {
		t.Fatalf("not released: %+v", a)
	}
	if err := s.SetHold("lea-a/nobody", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown agent: %v", err)
	}
	for _, st := range []string{"done", "stopped"} {
		s.SetHold("lea-a/tower", 100)
		s.SetState("lea-a/tower", st, "", 3)
		if a, _ := s.AgentByKey("lea-a/tower"); a.HoldSince != 0 {
			t.Fatalf("still held once %s: %+v", st, a)
		}
	}
	s.SetState("lea-a/tower", "done", "", 4)
	s.SetHold("lea-a/tower", 100)
	if _, err := s.RegisterAgent("lea-a", "tower", "claude", "/p", 5); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.AgentByKey("lea-a/tower"); a.HoldSince != 0 {
		t.Fatalf("a reused name inherits the pause: %+v", a)
	}
}

// A held inbox leaves out the agents' spontaneous messages, those sent before the pause too, and
// the replies in threads it opened before the pause; the user's messages and the replies in a
// thread it opened during the pause pass.
func TestHeldInboxLeavesOutSpontaneousAgentMessages(t *testing.T) {
	s := open(t)
	send := func(m Message) Message {
		t.Helper()
		m, err := s.Send(m)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	before := send(Message{From: "lea-a/tower", To: "lea-a/kid", Kind: "request", Body: "old ask", At: 5})
	send(Message{From: "lea-a/kid", To: "lea-a/tower", Kind: "note", Body: "early", At: 8})
	send(Message{From: "lea-a/kid", To: "lea-a/tower", Kind: "result", Body: "old reply", ReplyTo: before.ID, At: 12})
	during := send(Message{From: "lea-a/tower", To: "lea-a/kid", Kind: "request", Body: "new ask", At: 10})
	reply := send(Message{From: "lea-a/kid", To: "lea-a/tower", Kind: "result", Body: "new reply", ReplyTo: during.ID, At: 11})
	send(Message{From: "lea-a/kid", To: "lea-a/tower", Kind: "question", Body: "spont", At: 11})
	// Another agent replying in the tower's new thread to kid is held: the thread is not its own.
	send(Message{From: "lea-a/other", To: "lea-a/tower", Kind: "result", Body: "hijack", ReplyTo: during.ID, At: 11})
	user := send(Message{From: "user", To: "lea-a/tower", Kind: "note", Body: "from the user", At: 11})
	h := Hold{Since: 10}
	ms, err := s.UnreadHolding("lea-a/tower", 10, h)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]bool{}
	for _, m := range ms {
		got[m.ID] = true
	}
	if len(ms) != 2 || !got[reply.ID] || !got[user.ID] {
		t.Fatalf("delivered %+v", ms)
	}
	if n, _ := s.UnreadCountHolding("lea-a/tower", h); n != 2 {
		t.Fatalf("count %d", n)
	}
	if n, _ := s.HeldCount("lea-a/tower", 10); n != 4 {
		t.Fatalf("held %d, want early, old reply, spont and hijack", n)
	}
	if n, _ := s.UnreadCountHolding("lea-a/tower", Hold{}); n != 6 {
		t.Fatalf("no hold: %d", n)
	}
}

// HeldCounts counts, per held agent, the messages its pause holds, for the deck.
func TestHeldCountsAndExpiredHolds(t *testing.T) {
	s := open(t)
	for _, n := range []string{"tower", "other"} {
		if _, err := s.RegisterAgent("lea-a", n, "claude", "/p", 1); err != nil {
			t.Fatal(err)
		}
	}
	s.SetHold("lea-a/tower", 10)
	for i := 0; i < 2; i++ {
		s.Send(Message{From: "lea-a/kid", To: "lea-a/tower", Kind: "note", Body: "x", At: 11})
	}
	s.Send(Message{From: "lea-a/kid", To: "lea-a/other", Kind: "note", Body: "x", At: 11})
	c, err := s.HeldCounts()
	if err != nil {
		t.Fatal(err)
	}
	if len(c) != 1 || c["lea-a/tower"] != 2 {
		t.Fatalf("held counts %v", c)
	}
	if ks, err := s.ExpiredHolds(9); err != nil || len(ks) != 0 {
		t.Fatalf("expired before 9: %v %v", ks, err)
	}
	if ks, err := s.ExpiredHolds(10); err != nil || len(ks) != 1 || ks[0] != "lea-a/tower" {
		t.Fatalf("expired at 10: %v %v", ks, err)
	}
}
