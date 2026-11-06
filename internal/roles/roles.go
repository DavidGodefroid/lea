// Package roles holds lea's built-in agent roles (spec §5.2, A17) and the rules the daemon
// enforces whatever a role says: a tower starts leads, a lead starts agents that never spawn,
// nothing else spawns. The agent tree is at most two levels below its root, never a cascade.
package roles

import (
	"fmt"
	"slices"
	"strings"
)

const (
	Tower    = "tower"
	Lead     = "lead"
	Dev      = "dev"
	Reviewer = "reviewer"
	// Default is the role of an agent started without one (lea new, n, agents_spawn).
	Default = Dev
	// MaxChildren caps the running agents one lead started (spec §5.2 max_children).
	MaxChildren = 20
)

// Role is what an agent of that role may do. Root: an agent of the role never has a parent
// (only the user gives it, never a spawn), and it is the only kind of role that may start agents
// that spawn.
type Role struct {
	Name        string
	CanSpawn    bool
	SpawnRoles  []string // the roles it may give its children
	MaxChildren int
	Root        bool
}

var builtin = []Role{
	{Name: Tower, CanSpawn: true, SpawnRoles: []string{Lead}, MaxChildren: MaxChildren, Root: true},
	{Name: Lead, CanSpawn: true, SpawnRoles: []string{Dev, Reviewer}, MaxChildren: MaxChildren},
	{Name: Dev},
	{Name: Reviewer},
}

func init() {
	if err := Check(builtin); err != nil {
		panic(err)
	}
}

// Names lists the roles, tower first.
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

// Check applies the rules every role set follows, so that no role set allows a tree deeper than
// two levels below its root: unique non-empty names; spawn_roles only on a role that spawns,
// naming known roles; no role names a root role (a root never has a parent); only a root role
// names a role that spawns, and such a role names only roles that do not; max_children from 0
// to MaxChildren.
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
			if cr.Root {
				return fmt.Errorf("role %s: spawn_roles may not contain %s, a root role: it never has a parent", r.Name, c)
			}
			if !cr.CanSpawn {
				continue
			}
			if !r.Root {
				return fmt.Errorf("role %s: spawn_roles may not contain %s, which spawns: only a root role starts agents that spawn", r.Name, c)
			}
			for _, g := range cr.SpawnRoles {
				gr, err := find(rs, g)
				if err != nil {
					return fmt.Errorf("role %s: spawn_roles: %w", c, err)
				}
				if gr.CanSpawn {
					return fmt.Errorf("role %s: started by root role %s, it may start only roles that do not spawn, not %s", c, r.Name, g)
				}
			}
		}
	}
	return nil
}

// CanGive reports whether an agent of role parent may start an agent of role child: the child's
// role is one parent names, never a root role, and spawns only when parent is a root role.
func CanGive(parent Role, child string) error {
	if !parent.CanSpawn {
		return fmt.Errorf("a %s cannot start agents: ask your lead with agents_request_spawn", parent.Name)
	}
	if !slices.Contains(parent.SpawnRoles, child) {
		return fmt.Errorf("a %s may start only %s agents, not %q", parent.Name, strings.Join(parent.SpawnRoles, " and "), child)
	}
	c, err := Get(child)
	if err != nil {
		return err
	}
	if c.Root {
		return fmt.Errorf("the %s role is the user's to give: no agent starts one", child)
	}
	if c.CanSpawn && !parent.Root {
		return fmt.Errorf("a %s's child never spawns: a %s cannot give the %s role", parent.Name, parent.Name, child)
	}
	return nil
}

// RootRoles names the built-in root roles.
func RootRoles() []string {
	var out []string
	for _, r := range builtin {
		if r.Root {
			out = append(out, r.Name)
		}
	}
	return out
}
