// Package roles holds lea's built-in agent roles (spec §5.2, A17) and the rules the daemon
// enforces whatever a role says: a role a lead may give never spawns, so the agent tree is a
// lead and its children, never a cascade.
package roles

import (
	"fmt"
	"slices"
	"strings"
)

const (
	Lead     = "lead"
	Dev      = "dev"
	Reviewer = "reviewer"
	// Default is the role of an agent started without one (lea new, n, agents_spawn).
	Default = Dev
	// MaxChildren caps the running agents one lead started (spec §5.2 max_children).
	MaxChildren = 20
)

// Role is what an agent of that role may do.
type Role struct {
	Name        string
	CanSpawn    bool
	SpawnRoles  []string // the roles it may give its children
	MaxChildren int
}

var builtin = []Role{
	{Name: Lead, CanSpawn: true, SpawnRoles: []string{Dev, Reviewer}, MaxChildren: MaxChildren},
	{Name: Dev},
	{Name: Reviewer},
}

func init() {
	if err := Check(builtin); err != nil {
		panic(err)
	}
}

// Names lists the roles, lead first.
func Names() []string {
	out := make([]string, len(builtin))
	for i, r := range builtin {
		out[i] = r.Name
	}
	return out
}

// Get returns a built-in role by name.
func Get(name string) (Role, error) { return find(builtin, name) }

func find(rs []Role, name string) (Role, error) {
	names := make([]string, len(rs))
	for i, r := range rs {
		if r.Name == name {
			return r, nil
		}
		names[i] = r.Name
	}
	return Role{}, fmt.Errorf("unknown role %q (%s)", name, strings.Join(names, ", "))
}

// Check applies the rules every role set follows: unique non-empty names; spawn_roles only on a
// role that spawns, naming known roles that do not spawn (a child never spawns); max_children
// from 0 to MaxChildren.
func Check(rs []Role) error {
	seen := map[string]bool{}
	for _, r := range rs {
		if r.Name == "" || seen[r.Name] {
			return fmt.Errorf("role %q: empty or duplicate name", r.Name)
		}
		seen[r.Name] = true
	}
	for _, r := range rs {
		if !r.CanSpawn && len(r.SpawnRoles) > 0 {
			return fmt.Errorf("role %s: spawn_roles without can_spawn", r.Name)
		}
		if r.MaxChildren < 0 || r.MaxChildren > MaxChildren {
			return fmt.Errorf("role %s: max_children must be 0 to %d", r.Name, MaxChildren)
		}
		for _, c := range r.SpawnRoles {
			cr, err := find(rs, c)
			if err != nil {
				return fmt.Errorf("role %s: spawn_roles: %w", r.Name, err)
			}
			if cr.CanSpawn {
				return fmt.Errorf("role %s: spawn_roles may not contain %s, which spawns: a child never spawns", r.Name, c)
			}
		}
	}
	return nil
}

// CanGive reports whether an agent of role parent may start an agent of role child.
func CanGive(parent Role, child string) error {
	if !parent.CanSpawn {
		return fmt.Errorf("a %s cannot start agents: ask a lead with agents_request_spawn", parent.Name)
	}
	if !slices.Contains(parent.SpawnRoles, child) {
		return fmt.Errorf("a %s may start only %s agents, not %q", parent.Name, strings.Join(parent.SpawnRoles, " and "), child)
	}
	c, err := Get(child)
	if err != nil {
		return err
	}
	if c.CanSpawn {
		return fmt.Errorf("a child never spawns: a lead cannot give the %s role", child)
	}
	return nil
}
