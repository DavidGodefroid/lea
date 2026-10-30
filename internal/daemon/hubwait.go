package daemon

import (
	"encoding/json"
	"errors"
)

// hubWaitRegister is the suspend hook's call (#160): the agent's Lock call answered pending, so
// the agent waits on that ticket, and its turn ends. An agent lea started is suspended
// (suspended_warm, its keepalive armed); another one only gets the decision in its inbox. The
// wait is the caller's own: an agent cannot register one for another. It can register a wait on a
// ticket another agent asked for, and so read that ticket's decision and reason: lea does not ask
// the hub whose ticket it is. Running it stays refused, by the Lock, to any token but the one that
// asked (LEA_AGENT_TOKEN); the agents of a container are one trust domain anyway (one UID).
func (d *Daemon) hubWaitRegister(o Origin, me string, raw json.RawMessage) (any, error) {
	if o.Container == "" {
		return nil, errors.New("hub_wait_register: agents only")
	}
	if d.hubDisabled() {
		return nil, errHubDisabled
	}
	var p struct {
		Ticket   string `json:"ticket"`
		Lock     string `json:"lock"`
		Instance string `json:"instance"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	a, err := d.st.AgentByKey(me)
	if err != nil {
		return nil, err
	}
	if err := d.st.AddHubWait(me, p.Ticket, p.Lock, p.Instance, d.now()); err != nil {
		return nil, err
	}
	suspended := a.Profile != "" && a.State != "done"
	if suspended {
		if err := d.suspend(me); err != nil {
			return nil, err
		}
	}
	return map[string]bool{"suspended": suspended}, nil
}
