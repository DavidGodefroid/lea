package daemon

import (
	"fmt"
	"time"

	"github.com/DavidGodefroid/lea/internal/grants"
	"github.com/DavidGodefroid/lea/internal/managed"
)

// The company's managed configuration (#399): its [lea] section bounds the grants. The daemon
// reads it at each grant decision (lockkit caches it while the file is unchanged), so a change of
// the file applies at once. Fail closed: a file that no longer loads refuses every new grant, and
// no recorded grant applies until it loads again.

// SetManaged makes the daemon read the managed file with load (nil: no managed file).
func (d *Daemon) SetManaged(load func() (*managed.Policy, error)) { d.managedLoad = load }

// managedPolicy is the managed policy now; nil without a managed file.
func (d *Daemon) managedPolicy() (*managed.Policy, error) {
	if d.managedLoad == nil {
		return nil, nil
	}
	p, err := d.managedLoad()
	if err != nil {
		return nil, fmt.Errorf("the company's managed configuration does not load, no grant applies: %w", err)
	}
	return p, nil
}

// activeGrants are the grants of container that apply at now, within the managed bounds: a grant
// lives at most grant_ttl_max from its creation, and none holds until revoked under
// standing_grants = false.
func (d *Daemon) activeGrants(container string, now int64) ([]grants.Grant, error) {
	p, err := d.managedPolicy()
	if err != nil {
		return nil, err
	}
	gs, err := d.st.ActiveGrants(container, now)
	if err != nil {
		return nil, err
	}
	max, noStanding := p.GrantBounds()
	out := []grants.Grant{}
	for _, g := range grants.Bound(gs, max, noStanding) {
		if g.Active(now) {
			out = append(out, g)
		}
	}
	return out, nil
}

// boundGrants is activeGrants' bound for the agents' manager (agents.Config.BoundGrants): gs
// within the managed bounds, none when the file does not load.
func (d *Daemon) boundGrants(gs []grants.Grant) []grants.Grant {
	p, err := d.managedPolicy()
	if err != nil {
		return nil
	}
	max, noStanding := p.GrantBounds()
	return grants.Bound(gs, max, noStanding)
}

// BoundGrants is boundGrants for cmd/lea, which wires the agents' manager.
func (d *Daemon) BoundGrants(gs []grants.Grant) []grants.Grant { return d.boundGrants(gs) }

// grantTTL is a grant's life within the managed policy (managed.Policy.GrantTTL), and the managed
// file's fingerprint the grant records.
func (d *Daemon) grantTTL(ttl time.Duration, typed, standing bool) (time.Duration, string, error) {
	p, err := d.managedPolicy()
	if err != nil {
		return 0, "", err
	}
	ttl, err = p.GrantTTL(ttl, typed, standing)
	return ttl, p.Fingerprint(), err
}
