package deck

import (
	"context"
	"encoding/json"

	"github.com/DavidGodefroid/lea/internal/client"
	"github.com/DavidGodefroid/lea/internal/wire"
)

// Client is the deck's API over the daemon's host socket.
type Client struct{ Sock string }

func (c Client) call(ctx context.Context, method string, params, result any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return client.Call(ctx, c.Sock, wire.Request{Method: method, Params: raw}, result)
}

func (c Client) Watch(ctx context.Context, container string, since int64) (Snapshot, error) {
	var s Snapshot
	err := c.call(ctx, "agents_watch", map[string]any{"container": container, "since": since, "timeout_s": watchWait}, &s)
	return s, err
}

func (c Client) New(ctx context.Context, container, provider string) (Agent, error) {
	var a Agent
	err := c.call(ctx, "agent_new", map[string]any{"container": container, "profile": provider, "focus": true}, &a)
	return a, err
}

func (c Client) Providers(ctx context.Context) ([]string, error) {
	var ps []struct{ Name string }
	if err := c.call(ctx, "providers_list", nil, &ps); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(ps))
	for _, p := range ps {
		names = append(names, p.Name)
	}
	return names, nil
}

func (c Client) Show(ctx context.Context, key string, focus bool) error {
	return c.call(ctx, "agent_focus", map[string]any{"key": key, "focus": focus}, nil)
}

func (c Client) Stop(ctx context.Context, key string) error {
	return c.call(ctx, "agent_stop", map[string]any{"key": key}, nil)
}

// RemoveEnded removes an agent the deck shows as done, only while it is still ended.
func (c Client) RemoveEnded(ctx context.Context, key string) error {
	return c.call(ctx, "agent_stop", map[string]any{"key": key, "ended": true}, nil)
}

// SetRole changes an agent's role (the deck's r): the host socket's caller is the user.
func (c Client) SetRole(ctx context.Context, key, role string) error {
	return c.call(ctx, "agent_role", map[string]any{"agent": key, "role": role}, nil)
}

func (c Client) Detach(ctx context.Context, container string) error {
	return c.call(ctx, "deck_detach", map[string]any{"container": container}, nil)
}

func (c Client) Decide(ctx context.Context, id int64, verdict string) error {
	return c.call(ctx, "approval_decide", map[string]any{"id": id, "verdict": verdict}, nil)
}

func (c Client) Popup(ctx context.Context, key string, compose bool) error {
	return c.call(ctx, "deck_popup", map[string]any{"key": key, "compose": compose}, nil)
}

// SetLabel renames an agent for display (the deck's e); "" clears the label.
func (c Client) SetLabel(ctx context.Context, key, label string) error {
	return c.call(ctx, "agent_label", map[string]any{"key": key, "label": label}, nil)
}

// SetPaused freezes an agent (the deck's p), or lets a frozen one continue where it stopped.
func (c Client) SetPaused(ctx context.Context, key string, paused bool) error {
	return c.call(ctx, "agent_pause", map[string]any{"key": key, "paused": paused}, nil)
}

// OpenShell opens a shell in the container (the deck's s).
func (c Client) OpenShell(ctx context.Context, container string) (Agent, error) {
	var a Agent
	err := c.call(ctx, "shell_open", map[string]any{"container": container}, &a)
	return a, err
}

// SplitShell opens a shell beside an agent (the deck's - and |): below it, or at its right.
func (c Client) SplitShell(ctx context.Context, key string, below bool) (Agent, error) {
	var a Agent
	err := c.call(ctx, "shell_split", map[string]any{"key": key, "below": below}, &a)
	return a, err
}

// LockOpen opens tool's console in the container's deck (the deck's L), or shows the open one,
// under the agent; it answers "opened" or "already-open".
func (c Client) LockOpen(ctx context.Context, container, tool string) (string, error) {
	var r struct{ Status string }
	err := c.call(ctx, "lock_open", map[string]any{"container": container, "tool": tool}, &r)
	return r.Status, err
}

// LockDecide answers an agent's request that the user open a lock console (the card's a or d):
// open opens it as L would, else the request is declined. It answers the console's status
// ("opened", "already-open") or "declined".
func (c Client) LockDecide(ctx context.Context, container string, id int64, open bool) (string, error) {
	var r struct{ Status string }
	err := c.call(ctx, "lock_request_decide", map[string]any{"container": container, "id": id, "open": open}, &r)
	return r.Status, err
}

// Label is an agent's display label ("" when none), from the host's agents_list.
func (c Client) Label(ctx context.Context, key string) (string, error) {
	var as []struct{ Key, Label string }
	if err := c.call(ctx, "agents_list", nil, &as); err != nil {
		return "", err
	}
	for _, a := range as {
		if a.Key == key {
			return a.Label, nil
		}
	}
	return "", nil
}

func (c Client) Thread(ctx context.Context, key string) ([]Message, error) {
	var ms []Message
	err := c.call(ctx, "thread_list", map[string]any{"key": key}, &ms)
	return ms, err
}

// Send writes to an agent as the user (a note): the host socket's caller is always "user".
func (c Client) Send(ctx context.Context, key, body string) error {
	return c.call(ctx, "msg_send", map[string]any{"to": key, "body": body, "kind": "note"}, nil)
}

// SetSound switches the daemon's sounds (the deck's S) and answers the new state.
func (c Client) SetSound(ctx context.Context, on bool) (string, error) {
	var r struct{ Sound string }
	err := c.call(ctx, "sound", map[string]any{"on": on}, &r)
	return r.Sound, err
}
