package launcher

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/DavidGodefroid/lea/internal/managed"
)

// The company's managed configuration (#399, internal/managed): its [lea] section is read after
// the user's lea.conf and wins over it. LoadConfig loads it, and refuses to load (lea refuses to
// start) when the file is refused; resolve applies it to the keys it bounds, and a user value
// beyond it is ignored with a warning (ManagedWarnings).

func init() { managed.CheckClaude = checkManagedClaude }

// checkManagedClaude validates a claude_enforced key of the managed file: defaultMode, the
// preferences lea config claude knows, and disableBypassPermissionsMode, which lea already keeps
// at "disable". Any other key is refused: lea would not know how to enforce it.
func checkManagedClaude(k string, v any) error {
	switch {
	case k == "disableBypassPermissionsMode":
		if v != "disable" {
			return fmt.Errorf("can only be \"disable\" (lea keeps it so)")
		}
		return nil
	case k == "defaultMode":
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("is a permission mode")
		}
		return checkClaudeMode(s)
	case claudePrefs[k] != nil:
		if k == "alwaysThinkingEnabled" {
			if _, ok := v.(bool); !ok {
				return fmt.Errorf("is true or false")
			}
			return nil
		}
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("is a string")
		}
		_, err := claudePrefs[k](s)
		return err
	case claudeEnforced[k] != "":
		return fmt.Errorf("is lea's to keep: %s", claudeEnforced[k])
	}
	return fmt.Errorf("is not a setting lea can enforce (defaultMode, %s or disableBypassPermissionsMode)", strings.Join(sortedKeys(claudePrefs), ", "))
}

// withManagedClaude is c with the managed file's claude_enforced on top: what lea hands the
// entrypoint. c itself is left as the user wrote it.
func (c *Config) withManagedClaude(cc *claudeConfig) *claudeConfig {
	m := c.managed
	if m == nil || len(m.ClaudeEnforced) == 0 {
		return cc
	}
	out := *cc
	out.Settings = map[string]any{}
	for k, v := range cc.Settings {
		out.Settings[k] = v
	}
	for k, v := range m.ClaudeEnforced {
		switch {
		case k == "defaultMode":
			out.DefaultMode = v.(string)
		case claudePrefs[k] != nil:
			out.Settings[k] = v
		}
	}
	return &out
}

// managedClaudeRefuses is the refusal of lea config claude changing key k when the managed file
// enforces it; nil otherwise.
func (c *Config) managedClaudeRefuses(k string) error {
	if c.managed == nil {
		return nil
	}
	if v, ok := c.managed.ClaudeEnforced[k]; ok {
		return managed.Blocked("claude_enforced."+k, fmt.Sprint(v), "set for every agent")
	}
	return nil
}

// loadManaged reads the machine's managed file; tests replace it.
var loadManaged = func() (*managed.Policy, error) { return managed.Load(context.Background()) }

// Managed is the company's managed policy, nil without a managed file.
func (c *Config) Managed() *managed.Policy { return c.managed }

// ManagedWarnings are the user's values the managed file overrides, one line each.
func (c *Config) ManagedWarnings() []string { return c.managedWarn }

// managedEnforce applies an enforced value of the managed file to key: the user's value v (from a
// file or the environment), when it differs, is ignored with a warning.
func (c *Config) managedEnforce(key, rule, bound, v string) string {
	if v != bound {
		c.managedWarn = append(c.managedWarn, fmt.Sprintf("%s=%s is ignored: %v", key, v, managed.Blocked(rule, bound, "lea runs with "+key+"="+bound)))
	}
	return bound
}

// resolveManaged applies the managed file to the resolved keys.
func (c *Config) resolveManaged() error {
	m := c.managed
	if m == nil {
		return nil
	}
	if m.Approvals != "" {
		c.final["LEA_APPROVALS"] = c.managedEnforce("LEA_APPROVALS", "approvals", m.Approvals, c.final["LEA_APPROVALS"])
	}
	if m.Autoupdate {
		c.final["LEA_AUTOUPDATE"] = c.managedEnforce("LEA_AUTOUPDATE", "autoupdate", "1", c.final["LEA_AUTOUPDATE"])
	}
	if m.EgressAllow == nil {
		return nil
	}
	// LockHTTP replaces lea-proxy's allowlist with its own gate, whose policy is [lockhttp]'s:
	// without that section, the gate would bypass egress_allow.
	if c.final["LEA_LOCKHTTP"] == "1" && !m.File.Has("lockhttp") {
		return fmt.Errorf("LEA_LOCKHTTP=1 is refused: blocked by company policy: %s is set and the managed file has no [lockhttp] section for the LockHTTP gate (set LEA_LOCKHTTP=0, or ask your administrator)", managed.Key("egress_allow"))
	}
	user := c.final["LEA_EGRESS_ALLOW"]
	if _, dropped := managedEgress(user, m.EgressAllow); len(dropped) > 0 && user != "" {
		c.managedWarn = append(c.managedWarn, fmt.Sprintf("LEA_EGRESS_ALLOW: %s ignored: blocked by company policy: %s = %s",
			strings.Join(dropped, ", "), managed.Key("egress_allow"), strings.Join(m.EgressAllow, ",")))
	}
	return nil
}

// EgressAllow is the allowlist lea-proxy applies: LEA_EGRESS_ALLOW, within the managed file's
// egress_allow.
func (c *Config) EgressAllow() string { return c.egressEffective(c.Get("LEA_EGRESS_ALLOW")) }

// egressEffective is the allowlist lea-proxy applies for the LEA_EGRESS_ALLOW value v.
func (c *Config) egressEffective(v string) string {
	if c.managed == nil || c.managed.EgressAllow == nil {
		return v
	}
	eff, _ := managedEgress(v, c.managed.EgressAllow)
	return eff
}

// egressExpand is a list of egress entries with "default" replaced by lea's default list.
func egressExpand(names []string) []string {
	var out []string
	for _, n := range names {
		if n == "default" {
			out = append(out, egressDefault...)
			continue
		}
		out = append(out, n)
	}
	return out
}

// managedEgress is the user's allowlist v (a normalized LEA_EGRESS_ALLOW) within the managed
// domains m: each user domain a managed one covers, and each managed domain a user one covers
// (a user's "*" covers them all). dropped are the user's entries no managed domain covers. The
// model API is always allowed, by the proxy itself: a list with nothing else left is that alone.
func managedEgress(v string, m []string) (eff string, dropped []string) {
	bound := egressExpand(m)
	var out []string
	add := func(d string) {
		if egressCovers(out, d) == "" {
			out = append(out, d)
		}
	}
	if v == "*" {
		for _, d := range bound {
			add(d)
		}
	} else {
		user := egressExpand(egressNames(v))
		for _, d := range user {
			if egressCovers(bound, d) != "" {
				add(d)
			}
		}
		for _, d := range bound {
			if egressCovers(user, d) != "" {
				add(d)
			}
		}
		for _, n := range egressNames(v) {
			if n == "default" {
				if !slices.ContainsFunc(egressDefault, func(d string) bool { return egressCovers(bound, d) != "" }) {
					dropped = append(dropped, "default")
				}
				continue
			}
			if egressCovers(bound, n) == "" && !slices.ContainsFunc(bound, func(b string) bool { return egressCovers([]string{n}, b) != "" }) {
				dropped = append(dropped, n)
			}
		}
	}
	if len(out) == 0 {
		out = slices.Clone(egressAlways)
	}
	return strings.Join(out, ","), dropped
}

// managedEgressRefuses is the refusal of lea egress allow d when the managed file bounds the
// egress and no managed domain covers d; nil when it may be allowed.
func (c *Config) managedEgressRefuses(d string) error {
	m := c.managed
	if m == nil || m.EgressAllow == nil || egressCovers(egressExpand(m.EgressAllow), d) != "" || egressCovers(egressAlways, d) != "" {
		return nil
	}
	return managed.Blocked("egress_allow", strings.Join(m.EgressAllow, ","), d+" is not in it")
}

// managedRefusesKey is the refusal of a shared key (lea config import) set to v when v goes
// beyond the managed file; nil when it stays within it.
func (c *Config) managedRefusesKey(key, v string) error {
	m := c.managed
	if m == nil {
		return nil
	}
	switch key {
	case "LEA_APPROVALS":
		if m.Approvals != "" && nonEmpty(v, "deck") != m.Approvals {
			return managed.Blocked("approvals", m.Approvals, "LEA_APPROVALS must be "+m.Approvals)
		}
	case "LEA_EGRESS_ALLOW":
		if m.EgressAllow == nil {
			return nil
		}
		n, err := egressAllow(v)
		if err != nil {
			return err
		}
		if n == "*" {
			return managed.Blocked("egress_allow", strings.Join(m.EgressAllow, ","), "'*' opens the web to any domain")
		}
		if _, dropped := managedEgress(n, m.EgressAllow); len(dropped) > 0 {
			return managed.Blocked("egress_allow", strings.Join(m.EgressAllow, ","), strings.Join(dropped, ", ")+" not in it")
		}
	}
	return nil
}

// ManagedValue is the value the managed file enforces for key, and the managed key that does;
// ok is false when it enforces none.
func (c *Config) ManagedValue(key string) (value, rule string, ok bool) {
	m := c.managed
	switch {
	case m == nil:
	case key == "LEA_APPROVALS" && m.Approvals != "":
		return m.Approvals, managed.Key("approvals"), true
	case key == "LEA_AUTOUPDATE" && m.Autoupdate:
		return "1", managed.Key("autoupdate"), true
	}
	return "", "", false
}

// ManagedOrgVar names, in the launcher's children, the organisation of the managed file ("" none).
const ManagedOrgVar = "LEA_MANAGED_ORG"

// managedOrg is what the deck's header shows for m.
func managedOrg(m *managed.Policy) string {
	switch {
	case m == nil:
		return ""
	case m.Organisation() != "":
		return m.Organisation()
	}
	return "managed"
}

// managedDoctor is lea doctor's part on the company's managed configuration: who manages lea and
// its active [lea] rules, then the user's values it overrides. Nothing without a managed file.
func (l *Launcher) managedDoctor() {
	m := l.Cfg.Managed()
	if m == nil {
		return
	}
	fmt.Fprintf(l.Stdout, "ok    %s: %s\n", m.Describe(), m.File.Path)
	for _, r := range m.Rules() {
		fmt.Fprintf(l.Stdout, "ok    managed %s\n", r)
	}
	for _, w := range l.Cfg.ManagedWarnings() {
		fmt.Fprintf(l.Stdout, "warn  managed: %s\n", w)
	}
}
