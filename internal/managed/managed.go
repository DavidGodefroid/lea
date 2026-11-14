// Package managed is the [lea] section of the company's managed configuration (#399): one file
// for the whole Lock Family, /etc/lockfamily/managed.toml on Linux and /Library/Application
// Support/lockfamily/managed.toml on macOS, read and trusted by lockkit's managed package (owned by
// root and writable by root only, an optional cosign signature). Its [lea] section is read after
// the user's lea.conf and wins over it: the user may tighten what it allows, never loosen it.
//
// Fail closed: a file lockkit refuses (badly owned, invalid, a signature that does not verify)
// or a [lea] section this package refuses (an unknown key, a bad value) is an error, and lea
// refuses to start; it is never a file ignored. No file is no managed policy: lea behaves as
// without one.
package managed

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	km "github.com/DavidGodefroid/lockkit/managed"
)

// Section is the [lea] section's name in the managed file.
const Section = "lea"

// MaxGrantTTL is lea's own cap on a grant's life; grant_ttl_max can only lower it.
const MaxGrantTTL = 30 * 24 * time.Hour

// Approvals values: who answers the agents' permission requests (LEA_APPROVALS).
var Approvals = []string{"deck", "pane"}

// lea is [lea] as the file writes it.
type lea struct {
	Approvals      string   `toml:"approvals"`
	Autoupdate     *bool    `toml:"autoupdate"`
	GrantTTLMax    string   `toml:"grant_ttl_max"`
	StandingGrants *bool    `toml:"standing_grants"`
	EgressAllow    []string `toml:"egress_allow"`
}

// Policy is the managed file and its [lea] rules, checked. A nil Policy is no managed file:
// every method answers as without one.
type Policy struct {
	// File is the managed file lockkit read.
	File *km.File
	// Approvals is the enforced LEA_APPROVALS ("" none).
	Approvals string
	// Autoupdate forces LEA_AUTOUPDATE=1.
	Autoupdate bool
	// GrantTTLMax caps every grant's life (0 none).
	GrantTTLMax time.Duration
	// NoStanding refuses grants that hold until revoked (standing_grants = false).
	NoStanding bool
	// EgressAllow are the domains (each with its subdomains) the agents may reach at most, in the
	// file's order, lower-cased; "default" stands for lea's default list. Nil: no bound.
	EgressAllow []string
}

// load reads the machine's managed file (lockkit's Load); tests replace it.
var load = km.Load

// Load reads the machine's managed file and its [lea] section. It returns nil and no error when
// there is none. Any other failure is an error: the caller refuses to start.
func Load(ctx context.Context) (*Policy, error) {
	f, err := load(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w; lea refuses to start until the company's managed configuration is fixed (ask your administrator)", err)
	}
	if f == nil {
		return nil, nil
	}
	p, err := FromFile(f)
	if err != nil {
		return nil, fmt.Errorf("%w; lea refuses to start until the company's managed configuration is fixed (ask your administrator)", err)
	}
	return p, nil
}

// FromFile checks the [lea] section of f (nil f: nil Policy).
func FromFile(f *km.File) (*Policy, error) {
	if f == nil {
		return nil, nil
	}
	p := &Policy{File: f}
	var s lea
	if _, err := f.Decode(&s, Section); err != nil {
		return nil, err
	}
	where := func(key string) string { return "managed: " + f.Path + ": lea." + key }
	if f.Path == "" {
		where = func(key string) string { return "managed: lea." + key }
	}
	if s.Approvals != "" {
		if !slices.Contains(Approvals, s.Approvals) {
			return nil, fmt.Errorf("%s must be %q or %q (who answers the agents' permission requests), not %q", where("approvals"), "deck", "pane", s.Approvals)
		}
		p.Approvals = s.Approvals
	}
	if s.Autoupdate != nil {
		if !*s.Autoupdate {
			return nil, fmt.Errorf("%s can only be true (it forces LEA_AUTOUPDATE=1); leave it out to let the user choose", where("autoupdate"))
		}
		p.Autoupdate = true
	}
	if s.GrantTTLMax != "" {
		d, ok := ParseTTL(s.GrantTTLMax)
		if !ok || d < time.Minute || d > MaxGrantTTL {
			return nil, fmt.Errorf("%s must be a duration from 1m to 30d (8h, 90m, 7d), not %q", where("grant_ttl_max"), s.GrantTTLMax)
		}
		p.GrantTTLMax = d
	}
	if s.StandingGrants != nil {
		p.NoStanding = !*s.StandingGrants
	}
	if s.EgressAllow != nil {
		if len(s.EgressAllow) == 0 {
			return nil, fmt.Errorf("%s is empty: list the domains agents may reach (the model API is always allowed), or leave it out", where("egress_allow"))
		}
		for _, d := range s.EgressAllow {
			n := strings.ToLower(d)
			if n != "default" && !domainName(n) {
				return nil, fmt.Errorf("%s: %q is not a domain name (letters, digits, dots and dashes; no wildcard, URL or IP address; \"default\" is lea's default list)", where("egress_allow"), d)
			}
			if !slices.Contains(p.EgressAllow, n) {
				p.EgressAllow = append(p.EgressAllow, n)
			}
		}
	}
	return p, nil
}

// ParseTTL reads a grant's life as lea's commands do: a Go duration (8h, 90m) or a number of days
// (30d).
func ParseTTL(s string) (time.Duration, bool) {
	if n, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil || days < 1 || days > 366 || strings.HasPrefix(n, "+") {
			return 0, false
		}
		return time.Duration(days) * 24 * time.Hour, true
	}
	d, err := time.ParseDuration(s)
	return d, err == nil && d > 0
}

// FormatTTL writes d as ParseTTL reads it, in days when it is a whole number of them.
func FormatTTL(d time.Duration) string {
	if d >= 24*time.Hour && d%(24*time.Hour) == 0 {
		return strconv.Itoa(int(d/(24*time.Hour))) + "d"
	}
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

func domainName(n string) bool {
	return n != "" && strings.Trim(n, "abcdefghijklmnopqrstuvwxyz0123456789.-") == "" && n[0] != '.' && n[len(n)-1] != '.' &&
		!strings.Contains(n, "..") && strings.Trim(n, "0123456789.") != ""
}

// Fingerprint is the managed file's sha256 (hex), "" without one: recorded with what lea does
// under it.
func (p *Policy) Fingerprint() string {
	if p == nil {
		return ""
	}
	return p.File.Fingerprint
}

// Organisation is the file's organisation ("" without a file or when it names none).
func (p *Policy) Organisation() string {
	if p == nil {
		return ""
	}
	return p.File.Organisation
}

// Describe is "Managed by <organisation> (sha256:…)", "" without a file.
func (p *Policy) Describe() string {
	if p == nil {
		return ""
	}
	return p.File.Describe()
}

// Key is a [lea] key's full name, as messages cite it: "lea.grant_ttl_max".
func Key(k string) string { return Section + "." + k }

// Blocked is the message of a user's value beyond the managed file: "blocked by company policy:
// lea.<key> = <bound> (<what>)".
func Blocked(key, bound, what string) error {
	return errors.New("blocked by company policy: " + Key(key) + " = " + bound + " (" + what + ")")
}

// Rules are the active [lea] rules, one line each, for lea doctor.
func (p *Policy) Rules() []string {
	if p == nil {
		return nil
	}
	var out []string
	if p.Approvals != "" {
		out = append(out, Key("approvals")+" = "+p.Approvals+": LEA_APPROVALS is "+p.Approvals)
	}
	if p.Autoupdate {
		out = append(out, Key("autoupdate")+" = true: LEA_AUTOUPDATE is 1")
	}
	if p.GrantTTLMax > 0 {
		out = append(out, Key("grant_ttl_max")+" = "+FormatTTL(p.GrantTTLMax)+": no grant lives longer")
	}
	if p.NoStanding {
		out = append(out, Key("standing_grants")+" = false: every lea allow needs --for")
	}
	if p.EgressAllow != nil {
		out = append(out, Key("egress_allow")+" = "+strings.Join(p.EgressAllow, ",")+": the agents reach these domains at most")
	}
	if len(out) == 0 {
		out = append(out, "no [lea] rule: lea runs on your configuration")
	}
	return out
}

// GrantTTL is the life of a grant the user or a lead asks for, within grant_ttl_max: a life the
// user typed (lea allow --for, lea approve --expires, lea grant add --expires) beyond it is
// refused, any other (a lead's request, a default) is clamped to it. standing is a grant that
// holds until revoked (lea allow without --for): refused under standing_grants = false or
// grant_ttl_max.
func (p *Policy) GrantTTL(ttl time.Duration, typed, standing bool) (time.Duration, error) {
	if p == nil {
		return ttl, nil
	}
	if standing {
		switch {
		case p.NoStanding:
			return 0, Blocked("standing_grants", "false", "an allowance needs --for, at most "+p.maxText())
		case p.GrantTTLMax > 0:
			return 0, Blocked("grant_ttl_max", FormatTTL(p.GrantTTLMax), "an allowance needs --for, at most "+FormatTTL(p.GrantTTLMax))
		}
		return ttl, nil
	}
	if p.GrantTTLMax <= 0 || ttl <= p.GrantTTLMax {
		return ttl, nil
	}
	if typed {
		return 0, Blocked("grant_ttl_max", FormatTTL(p.GrantTTLMax), FormatTTL(ttl)+" asked")
	}
	return p.GrantTTLMax, nil
}

func (p *Policy) maxText() string {
	if p.GrantTTLMax > 0 {
		return FormatTTL(p.GrantTTLMax)
	}
	return FormatTTL(MaxGrantTTL)
}

// GrantBounds are what grants.Bound takes: the longest life of a grant in seconds (0 none) and
// whether a grant that holds until revoked still applies.
func (p *Policy) GrantBounds() (maxTTL int64, noStanding bool) {
	if p == nil {
		return 0, false
	}
	return int64(p.GrantTTLMax / time.Second), p.NoStanding
}
