package daemon

import (
	"encoding/json"
	"errors"
)

// hubWaitRegister is the suspend hook's call (#160): the agent's Lock call answered pending, so
// the agent waits on that ticket, and its turn ends. An agent lea started is suspended
// (suspended_warm, its keepalive armed); another one only gets the decision in its inbox. The
// wait is the caller's own: an agent cannot register one for another.
func (d *Daemon) hubWaitRegister(o Origin, me string, raw json.RawMessage) (any, error) {
	if o.Container == "" {
		return nil, errors.New("hub_wait_register: agents only")
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
