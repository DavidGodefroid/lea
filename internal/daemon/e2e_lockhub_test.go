package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"
)

// The deck approves a ticket in a real lockhub (the phase-2 exit demo, lea's side): a lox up
// stack with a real Lock holds a request and asks the hub; lea's deck reads the hub's queue from
// the real facade socket, shows the ticket on the agent that waits on it, and the deck's approval
// (hub_decide, as the deck's key sends it) reaches the hub as a signed decision of facade "deck".
// tests/integration/hubdeck.sh starts the stack and the held request, runs this test, then checks
// that the Lock let the request through. Set LEA_E2E_HUB_SOCKET (the hub's facade socket) and
// LEA_E2E_HUB_INSTANCE (the Lock's instance) to run it.
func TestHubDeckApprovesARealLockhubTicket(t *testing.T) {
	sock, inst := os.Getenv("LEA_E2E_HUB_SOCKET"), os.Getenv("LEA_E2E_HUB_INSTANCE")
	if sock == "" || inst == "" {
		t.Skip("LEA_E2E_HUB_SOCKET and LEA_E2E_HUB_INSTANCE are not set (tests/integration/hubdeck.sh)")
	}
	f := SocketFacade{Path: sock}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// The Lock's pending ticket, as the hub queues it.
	var tk HubTicket
	for found := false; !found; {
		ts, _, err := f.Queue(ctx, 0, 2)
		if err != nil {
			t.Fatalf("the hub's queue: %v", err)
		}
		for _, x := range ts {
			if x.Instance == inst && !x.Expired {
				tk, found = x, true
			}
		}
		if !found {
			if ctx.Err() != nil {
				t.Fatalf("no pending ticket of %s in the hub's queue: %+v", inst, ts)
			}
			time.Sleep(time.Second)
		}
	}
	t.Logf("ticket %s/%s: lock %s, risk %s, %q", tk.Instance, tk.ID, tk.Lock, tk.Risk, tk.Header)

	e := start(t)
	dev := spawned(t, e, "dev-1", "dev", "")
	e.d.SetAgents(&fakeAgents{})
	e.d.SetHubFacade(f)
	// The agent's own wait names the ticket, as its hook registers it (hub_wait_register).
	if err := e.d.st.AddHubWait(dev, tk.ID, tk.Lock, tk.Instance, 1000); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.pollHubQueue(e.ctx, 0); err != nil {
		t.Fatalf("the deck's poll of the real hub: %v", err)
	}
	cards := cardsOf(e, dev).Agents[0].Hub
	if len(cards) != 1 || cards[0].ID != tk.ID || cards[0].Instance != inst || !cards[0].Verified {
		t.Fatalf("the deck does not show the ticket on its agent: %+v", cards)
	}
	decide := func(container string) error {
		return call(t, e.api, "", "hub_decide", map[string]any{"container": container, "instance": tk.Instance,
			"id": tk.ID, "approved": true}, nil)
	}
	// Another container's deck cannot answer it.
	if err := decide("lea-b"); err == nil {
		t.Fatal("another container's deck decided the ticket")
	}
	if err := decide("lea-a"); err != nil {
		t.Fatalf("the deck's approval: %v", err)
	}
	if cards := cardsOf(e, dev).Agents[0].Hub; len(cards) != 0 {
		t.Fatalf("the ticket still shows after its approval: %+v", cards)
	}

	// The hub recorded it: a second answer is refused, the ticket left the queue, and the hub's
	// signed decision is an approval from the deck.
	if err := f.Decide(ctx, tk.Instance, tk.ID, HubVerdict{Approved: false}); !errors.Is(err, ErrHubDecided) {
		t.Fatalf("a second decision: %v, want %v", err, ErrHubDecided)
	}
	ts, _, err := f.Queue(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range ts {
		if x.ID == tk.ID && !x.Expired {
			t.Fatalf("the approved ticket is still pending: %+v", x)
		}
	}
	hc := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}}
	res, err := hc.Get("http://lockhub/v1/decided?since=0")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct {
		Decisions []struct {
			Ticket   string          `json:"ticket"`
			Decision json.RawMessage `json:"decision"`
		} `json:"decisions"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out); err != nil {
		t.Fatalf("GET /v1/decided: %s: %v", res.Status, err)
	}
	n := 0
	for _, d := range out.Decisions {
		var dec struct {
			Ticket, Instance, Decision, Facade, Sig string
		}
		if json.Unmarshal(d.Decision, &dec) != nil || dec.Ticket != tk.ID {
			continue
		}
		n++
		if dec.Instance != tk.Instance || dec.Decision != "approved" || dec.Facade != "deck" || dec.Sig == "" {
			t.Fatalf("the hub's decision is not a signed approval from the deck: %s", d.Decision)
		}
	}
	if n != 1 {
		t.Fatalf("%d decisions of %s in the hub, want 1: %+v", n, tk.ID, out.Decisions)
	}
}
