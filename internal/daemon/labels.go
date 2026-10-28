package daemon

import (
	"strings"

	"github.com/DavidGodefroid/lea/internal/agents"
	"github.com/DavidGodefroid/lea/internal/store"
)

// Labels reach agents (#84) so they can name each other to the user as the deck shows them. A
// label is partly text another agent wrote (an auto-name summarises a brief): an agent sees one
// only for an agent of its own container, only while it still passes agents.CheckLabel (short,
// no control or invisible character), and always next to the name, quoted, as data. The name
// stays the only address: bus.Resolve never matches a label.

// safeLabel is label when it passes agents.CheckLabel unchanged, else "".
func safeLabel(label string) string {
	if l, err := agents.CheckLabel(label); err != nil || l != label {
		return ""
	}
	return label
}

// agentLabel is the label of a as the caller of container viewer sees it: the host ("") sees
// the stored label, an agent only a safe label of its own container.
func agentLabel(a store.Agent, viewer string) string {
	if viewer == "" {
		return a.Label
	}
	if a.Container != viewer {
		return ""
	}
	return safeLabel(a.Label)
}

// senderLabel is the label of the sender of a message to the agent me ("" for the user, an
// unknown sender or one of another container).
func (d *Daemon) senderLabel(from, me string) string {
	i := strings.LastIndexByte(me, '/')
	if from == "user" || i <= 0 {
		return ""
	}
	a, err := d.st.AgentByKey(from)
	if err != nil {
		return ""
	}
	return agentLabel(a, me[:i])
}
