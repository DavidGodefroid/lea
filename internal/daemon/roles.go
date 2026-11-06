package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/DavidGodefroid/lea/internal/bus"
	"github.com/DavidGodefroid/lea/internal/roles"
	"github.com/DavidGodefroid/lea/internal/store"
)

// resolveSpawned maps an agent name or key to the key of an agent lea spawned; container limits
// the search to one container ("" : every container).
func (d *Daemon) resolveSpawned(name, container string) (string, error) {
	if name == "user" {
		return "", errors.New("user is not an agent")
	}
	as, err := d.st.Agents()
	if err != nil {
		return "", err
	}
	var ks []string
	for _, a := range as {
		if a.Profile != "" && (container == "" || a.Container == container) {
			ks = append(ks, a.Key)
		}
	}
	return bus.Resolve(name, ks)
}

// setRole changes an agent's role: the user's call only (spec §5.2; agent_role is host-only).
// canSpawn comes from the built-in role, never from the request. The store applies the change in
// the transaction a spawn registers its child in, so a demotion cannot race a spawn. The agent is
// told with a note from the user, so its next turn knows what it may do.
func (d *Daemon) setRole(raw json.RawMessage) (any, error) {
	var p struct{ Agent, Role string }
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	r, err := roles.Get(p.Role)
	if err != nil {
		return nil, err
	}
	key, err := d.resolveSpawned(p.Agent, "")
	if err != nil {
		return nil, err
	}
	a, err := d.st.AgentByKey(key)
	if err != nil {
		return nil, err
	}
	out := map[string]string{"key": key, "role": r.Name, "parent": shortName(a.Parent)}
	if a.Role == r.Name {
		return out, nil
	}
	parent, err := d.st.SetRole(key, r)
	if err != nil {
		return nil, err
	}
	out["parent"] = shortName(parent)
	if _, err := d.st.Send(store.Message{From: "user", To: key, Kind: "note", Body: roleNotice(r, parent), At: d.now()}); err != nil {
		log.Printf("lea daemon: tell %s its role: %v", key, err)
	}
	if parent != a.Parent && parent != "" {
		// The tower that adopted a promoted lead hears of its new child.
		body := fmt.Sprintf("lea: the user made %s a %s; it is now your child (it was %s's).", a.Name, r.Name, shortName(a.Parent))
		if _, err := d.st.Send(store.Message{From: "user", To: parent, Kind: "note", Body: body, At: d.now()}); err != nil {
			log.Printf("lea daemon: tell %s of its new child: %v", parent, err)
		}
		d.wake(parent)
	}
	d.wake(key)
	d.changed()
	return out, nil
}

func roleNotice(r roles.Role, parent string) string {
	under := ""
	if parent != "" {
		under = fmt.Sprintf(" You report to %s.", shortName(parent))
	}
	switch {
	case r.Root:
		return fmt.Sprintf("lea: your role is now %s. You may start %s agents with agents_spawn (they are your children, and they start their own teams) and stop your own with agents_stop.",
			r.Name, strings.Join(r.SpawnRoles, " and "))
	case r.CanSpawn:
		return fmt.Sprintf("lea: your role is now %s. You may start %s agents with agents_spawn (they are your children) and stop your own with agents_stop.%s",
			r.Name, strings.Join(r.SpawnRoles, " and "), under)
	}
	return fmt.Sprintf("lea: your role is now %s. You cannot start agents: ask a lead with agents_request_spawn when you need one.%s", r.Name, under)
}
