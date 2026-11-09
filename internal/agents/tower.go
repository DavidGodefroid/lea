package agents

import (
	"fmt"
	"strings"

	"github.com/DavidGodefroid/lea/internal/providers"
	"github.com/DavidGodefroid/lea/internal/roles"
)

const (
	// TowerLabel is a Control Tower's label (#339); the next ones are "Control Tower 2"…
	TowerLabel = "Control Tower"
	// TowerCommand runs the control-tower skill: the first line of a tower's first prompt, the
	// only place the tower-guard hook turns the guard on from.
	TowerCommand = "/lea:control-tower"
	// towerProvider runs every tower: the skill and its guard ship in the Claude plugin only.
	towerProvider = "claude"
)

// checkTower makes r a Control Tower's request: a lead started by the user on Claude. Another
// role, provider or a parent is refused, as is a registry without Claude.
func checkTower(reg *providers.Registry, r *NewRequest) error {
	if r.Parent != "" {
		return fmt.Errorf("a %s is started by the user, never by a lead", TowerLabel)
	}
	if r.Role != "" && r.Role != roles.Lead {
		return fmt.Errorf("a %s is a lead, not a %s", TowerLabel, r.Role)
	}
	if r.Profile != "" && r.Profile != towerProvider {
		return fmt.Errorf("a %s runs on %s, not on %s", TowerLabel, towerProvider, r.Profile)
	}
	if _, err := reg.Get(towerProvider); err != nil {
		return fmt.Errorf("a %s runs on %s, which is not an enabled provider", TowerLabel, towerProvider)
	}
	r.Role, r.Profile = roles.Lead, towerProvider
	return nil
}

// towerLabel is the label of a new tower: TowerLabel, or "Control Tower N" with the lowest N no
// other agent of the container (but key, the new one) is labelled with.
func (m *Manager) towerLabel(container, key string) (string, error) {
	as, err := m.cfg.Store.SpawnedAgents(container)
	if err != nil {
		return "", err
	}
	taken := map[string]bool{}
	for _, a := range as {
		if a.Key != key && a.State != "done" {
			taken[strings.TrimSpace(a.Label)] = true
		}
	}
	if !taken[TowerLabel] {
		return TowerLabel, nil
	}
	for i := 2; ; i++ {
		if l := fmt.Sprintf("%s %d", TowerLabel, i); !taken[l] {
			return l, nil
		}
	}
}
