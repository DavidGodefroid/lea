package agents

import (
	"context"

	"github.com/DavidGodefroid/lea/internal/tmux"
)

// Tmux is the Terminal of a real tmux server.
func Tmux(srv tmux.Server) Terminal { return tmuxTerminal{srv} }

type tmuxTerminal struct{ srv tmux.Server }

func (t tmuxTerminal) Open(ctx context.Context, session string, deckCmd []string) (Session, bool, error) {
	c, err := t.srv.Open(ctx, session, deckCmd)
	if err != nil {
		return nil, false, err // never a typed nil inside the interface
	}
	return c, c.Created(), nil
}

func (t tmuxTerminal) Sessions(ctx context.Context) ([]string, error) { return t.srv.Sessions(ctx) }

func (t tmuxTerminal) Popup(ctx context.Context, client string, argv []string) error {
	return t.srv.Popup(ctx, client, argv)
}
