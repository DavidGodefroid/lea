package daemon

import (
	"context"
	"encoding/json"
	"errors"
)

// webAttach holds a lea web client attached as the container's deck (#414) for as long as its
// request lasts: lea web keeps one deck_web_attach open while a browser reads its event stream,
// and closing the connection (the browser gone, lea web stopped or killed) ends the attachment.
// Meanwhile deckAttached reports the container attached, so permission prompts wait for an answer
// in the browser as they do for the tmux deck. Host only (dispatch): no agent attaches a deck.
func (d *Daemon) webAttach(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Container string `json:"container"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, errors.New("deck_web_attach: invalid parameters")
	}
	if !containerName.MatchString(p.Container) {
		return nil, errors.New("deck_web_attach: invalid container name")
	}
	d.mu.Lock()
	d.webDecks[p.Container]++
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		if d.webDecks[p.Container]--; d.webDecks[p.Container] <= 0 {
			delete(d.webDecks, p.Container)
		}
		d.mu.Unlock()
	}()
	<-ctx.Done()
	return struct{}{}, nil
}

// webAttached reports whether a lea web client is attached as the container's deck.
func (d *Daemon) webAttached(container string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.webDecks[container] > 0
}
