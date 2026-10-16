// Package tmux drives lea's private tmux server (tmux -L lea) in control mode: one connection
// per session, commands answered in order, notifications delivered as events. The package has no
// way to type into a pane: lea never sends keystrokes to an agent's terminal.
package tmux

import (
	"fmt"
	"regexp"
	"strings"
)

var cmdName = regexp.MustCompile(`^[a-z][a-z-]*$`)

// allowed lists, by full name, the tmux commands lea runs. tmux also resolves aliases and unique
// prefixes ("send", "pasteb"), so a deny-list of the commands that type into a pane cannot be
// exact: anything not listed here is refused, which keeps every typing command out.
// display-popup is run only by Server.Popup, in a one-shot client: it waits until the popup closes.
// source-file is not listed: Server.Open runs it in a one-shot client, on lea's own configuration
// only (lea.tmux.conf), never over the control connection.
var allowed = map[string]bool{
	"attach-session": true, "break-pane": true, "detach-client": true, "display-message": true, "display-popup": true,
	"has-session": true, "join-pane": true, "kill-pane": true, "kill-server": true, "kill-session": true,
	"list-clients": true, "list-panes": true, "list-sessions": true, "list-windows": true,
	"new-session": true, "new-window": true, "refresh-client": true, "resize-pane": true,
	"respawn-pane": true, "select-layout": true, "select-pane": true, "select-window": true, "set-option": true,
	"split-window": true, "swap-pane": true,
}

// Quote makes s one literal word of the tmux command language. Single quotes stop variable,
// format, tilde and separator processing; an embedded quote is closed, escaped and reopened.
func Quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Command renders one control-mode command line: a checked command name and quoted arguments.
// Control characters are refused: a newline would end the line and start a second command.
func Command(name string, args ...string) (string, error) {
	if !cmdName.MatchString(name) {
		return "", fmt.Errorf("tmux: invalid command name %q", name)
	}
	if !allowed[name] {
		return "", fmt.Errorf("tmux: command %q is not one lea runs (lea never types into a terminal)", name)
	}
	var b strings.Builder
	b.WriteString(name)
	for _, a := range args {
		for _, r := range a {
			if r < 0x20 || r == 0x7f {
				return "", fmt.Errorf("tmux: argument %q contains a control character", a)
			}
		}
		b.WriteByte(' ')
		b.WriteString(Quote(a))
	}
	return b.String(), nil
}
