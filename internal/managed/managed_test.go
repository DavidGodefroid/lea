package managed

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	km "github.com/DavidGodefroid/lockkit/managed"
)

func parse(t *testing.T, s string) (*Policy, error) {
	t.Helper()
	f, err := km.Parse([]byte("version = 1\norganisation = \"ICTools\"\n" + s))
	if err != nil {
		t.Fatalf("lockkit refused the file: %v", err)
	}
	return FromFile(f)
}

func TestFromFileReadsTheLeaSection(t *testing.T) {
	p, err := parse(t, "[lea]\napprovals = \"deck\"\nautoupdate = true\negress_allow = [\"Default\", \"corp.example\", \"corp.example\"]\n")
	if err != nil {
		t.Fatal(err)
	}
	if p.Approvals != "deck" || !p.Autoupdate || !slices.Equal(p.EgressAllow, []string{"default", "corp.example"}) {
		t.Fatalf("policy %+v", p)
	}
	if p.Organisation() != "ICTools" || len(p.Fingerprint()) != 64 || !strings.HasPrefix(p.Describe(), "Managed by ICTools (sha256:") {
		t.Fatalf("org %q fp %q describe %q", p.Organisation(), p.Fingerprint(), p.Describe())
	}
	rules := strings.Join(p.Rules(), "\n")
	for _, want := range []string{"lea.approvals = deck", "lea.autoupdate = true", "lea.egress_allow = default,corp.example"} {
		if !strings.Contains(rules, want) {
			t.Errorf("rules lack %q:\n%s", want, rules)
		}
	}
}

// A file without [lea] bounds nothing of lea, and a nil Policy answers as no file.
func TestNoLeaSection(t *testing.T) {
	p, err := parse(t, "[locksql.bounds]\ntier = \"read\"\n")
	if err != nil || p == nil || p.Approvals != "" || p.Autoupdate || p.EgressAllow != nil {
		t.Fatalf("%+v %v", p, err)
	}
	if r := p.Rules(); len(r) != 1 || !strings.Contains(r[0], "no [lea] rule") {
		t.Fatalf("rules %v", r)
	}
	var none *Policy
	if none.Fingerprint() != "" || none.Organisation() != "" || none.Describe() != "" || none.Rules() != nil {
		t.Fatal("a nil Policy is not empty")
	}
	if p, err := FromFile(nil); p != nil || err != nil {
		t.Fatalf("%v %v", p, err)
	}
}

// Fail closed: an unknown key or a bad value of [lea] is an error, never ignored.
func TestFromFileRefuses(t *testing.T) {
	for in, want := range map[string]string{
		"[lea]\napprovals = \"none\"\n":              "lea.approvals must be",
		"[lea]\napprovals = \"required\"\n":          "lea.approvals must be",
		"[lea]\nautoupdate = false\n":                "lea.autoupdate can only be true",
		"[lea]\negress_allow = []\n":                 "lea.egress_allow is empty",
		"[lea]\negress_allow = [\"*\"]\n":            "is not a domain name",
		"[lea]\negress_allow = [\"1.2.3.4\"]\n":      "is not a domain name",
		"[lea]\negress_allow = [\"https://x.io\"]\n": "is not a domain name",
		"[lea]\negress_allow = [\".x.io\"]\n":        "is not a domain name",
		"[lea]\nunknown_rule = true\n":               `unknown key "unknown_rule"`,
		"[lea]\napprovals = 1\n":                     "lea",
	} {
		if p, err := parse(t, in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %+v, %v; want %q", in, p, err, want)
		}
	}
}

// Load: no file is no policy; a file lockkit refuses (a badly owned one) or a bad [lea] section
// is an error that says lea refuses to start.
func TestLoad(t *testing.T) {
	old := load
	t.Cleanup(func() { load = old })
	load = func(context.Context) (*km.File, error) { return nil, nil }
	if p, err := Load(context.Background()); p != nil || err != nil {
		t.Fatalf("no file: %v %v", p, err)
	}
	load = func(context.Context) (*km.File, error) {
		return nil, errors.New("managed: /etc/lockfamily/managed.toml must be owned by root (it is owned by uid 1000)")
	}
	if _, err := Load(context.Background()); err == nil || !strings.Contains(err.Error(), "owned by root") || !strings.Contains(err.Error(), "lea refuses to start") {
		t.Fatalf("badly owned: %v", err)
	}
	f, _ := km.Parse([]byte("version = 1\n[lea]\napprovals = \"nobody\"\n"))
	load = func(context.Context) (*km.File, error) { return f, nil }
	if _, err := Load(context.Background()); err == nil || !strings.Contains(err.Error(), "lea refuses to start") {
		t.Fatalf("bad [lea]: %v", err)
	}
}

// claude_enforced goes through CheckClaude, which the launcher sets; without it, refused.
func TestClaudeEnforcedNeedsAChecker(t *testing.T) {
	old := CheckClaude
	t.Cleanup(func() { CheckClaude = old })
	CheckClaude = nil
	if _, err := parse(t, "[lea.claude_enforced]\nmodel = \"opus\"\n"); err == nil || !strings.Contains(err.Error(), "cannot check") {
		t.Fatalf("no checker: %v", err)
	}
	CheckClaude = func(k string, v any) error {
		if k != "model" {
			return errors.New("no")
		}
		return nil
	}
	p, err := parse(t, "[lea.claude_enforced]\nmodel = \"opus\"\n")
	if err != nil || p.ClaudeEnforced["model"] != "opus" || !strings.Contains(strings.Join(p.Rules(), "\n"), "lea.claude_enforced.model = opus") {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := parse(t, "[lea.claude_enforced]\nhooks = \"x\"\n"); err == nil || !strings.Contains(err.Error(), "lea.claude_enforced.hooks: no") {
		t.Fatalf("refused key: %v", err)
	}
}

func TestParseTTL(t *testing.T) {
	for in, want := range map[string]time.Duration{"8h": 8 * time.Hour, "90m": 90 * time.Minute, "7d": 7 * 24 * time.Hour} {
		if d, ok := ParseTTL(in); !ok || d != want {
			t.Errorf("%s: %v %v", in, d, ok)
		}
		if got := FormatTTL(want); got != in && got != "1h30m" {
			t.Errorf("FormatTTL(%v) = %s", want, got)
		}
	}
	for _, in := range []string{"", "0d", "-1h", "+3d", "367d", "x"} {
		if _, ok := ParseTTL(in); ok {
			t.Errorf("%q accepted", in)
		}
	}
}
