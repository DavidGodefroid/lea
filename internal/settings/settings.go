// Package settings is the catalog of the configuration keys the deck's settings screen shows and
// edits: each key's value, where it comes from (lea's defaults, the user file, the environment)
// and when a change applies. It reads and writes the user configuration on the host only, in the
// deck's own process: no daemon method reaches it, so an agent can neither read nor drive it.
package settings

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/DavidGodefroid/lea/internal/launcher"
)

// Kind is how a setting is edited.
type Kind string

const (
	Toggle Kind = "toggle" // "1" or "0"
	Choice Kind = "choice" // one of Choices
	Text   Kind = "text"   // free text, checked by the setting
)

// Where a value comes from.
const (
	SourceDefault     = "default"
	SourceFile        = "file"
	SourceEnvironment = "environment"
)

// When a change applies.
const (
	ApplyNow       = "applies now"
	ApplyAfterStop = "applies after lea stop --all"
	ApplyNextStart = "applies at the next lea"
)

// Setting is one key of the screen, with its current value.
type Setting struct {
	Key, Group, Name, Help string
	Kind                   Kind
	Choices                []string
	Default                string
	Apply                  string
	// Live: the deck applies a change at once (the daemon's sound switch).
	Live bool
	// Value is the effective value (Default when nothing sets it), Source where it comes from.
	Value, Source string
	// Locked says why the setting is read-only, "" when it can be changed.
	Locked string

	check func(string) error
}

var catalog = []Setting{
	{Key: "LEA_PROJECTS_DIR", Group: "Projects", Name: "Projects directory", Kind: Text, Apply: ApplyNextStart,
		Help: "the directory lea's agents see", check: projectsDir},
	{Key: "LEA_APPROVALS", Group: "Agents", Name: "Approvals", Kind: Choice, Choices: []string{"deck", "pane"}, Default: "deck",
		Apply: ApplyAfterStop, Help: "who answers permission requests: the deck, or the agent's own dialog"},
	{Key: "LEA_APPROVAL_TIMEOUT", Group: "Agents", Name: "Approval timeout", Kind: Text, Default: "60", Apply: ApplyAfterStop,
		Help: "seconds before the agent's own dialog asks (1 to 600)", check: seconds},
	{Key: "LEA_SOUND", Group: "Sounds", Name: "Sounds", Kind: Toggle, Default: "1", Apply: ApplyNow, Live: true,
		Help: "a sound when an agent waits for you and when its turn ends"},
	{Key: "LEA_SOUND_ATTENTION", Group: "Sounds", Name: "Attention sound", Kind: Text, Apply: ApplyAfterStop,
		Help: "sound file when an agent waits for you; 0 mutes it, empty: freedesktop"},
	{Key: "LEA_SOUND_DONE", Group: "Sounds", Name: "Done sound", Kind: Text, Apply: ApplyAfterStop,
		Help: "sound file when a turn ends; 0 mutes it, empty: freedesktop"},
	{Key: "LEA_GIT_SSH", Group: "Egress", Name: "Git SSH endpoints", Kind: Text, Apply: ApplyAfterStop,
		Help: "host:port endpoints git reaches over SSH; empty: HTTPS only", check: launcher.CheckGitSSH},
	{Key: "LEA_OTEL_UPSTREAM", Group: "Egress", Name: "OTel collector", Kind: Text, Apply: ApplyAfterStop,
		Help: "host:port of an OTLP collector for usage telemetry; empty: off", check: hostPort},
	{Key: "LEA_CLIPBOARD", Group: "Container", Name: "Clipboard", Kind: Toggle, Default: "1", Apply: ApplyAfterStop,
		Help: "agents can copy to your host clipboard"},
	{Key: "LEA_AUTOUPDATE", Group: "Updates", Name: "Auto-update", Kind: Toggle, Default: "1", Apply: ApplyNextStart,
		Help: "look for a new release once a day, active at the next lea"},
}

// Store reads and writes the settings of the lea installed at Root, for the deck run from Cwd
// under the launcher (whose environment Getenv reads).
type Store struct {
	Root, Cwd string
	Getenv    func(string) string
}

// load reads the configuration as the launcher does. The launcher exports every key to the deck,
// so a protected key counts as the environment's only when it is named in LEA_ENV_KEYS.
func (s Store) load() (*launcher.Config, map[string]bool, error) {
	fromEnv := map[string]bool{}
	for _, k := range strings.Fields(s.Getenv(launcher.EnvKeysVar)) {
		fromEnv[k] = true
	}
	getenv := func(k string) string {
		if launcher.Protected(k) && !fromEnv[k] {
			return ""
		}
		return s.Getenv(k)
	}
	c, err := launcher.LoadConfig(getenv, s.Root, s.Cwd)
	return c, fromEnv, err
}

// List is every setting with its current value, in the catalog's order.
func (s Store) List() ([]Setting, error) {
	c, fromEnv, err := s.load()
	if err != nil {
		return nil, err
	}
	user := c.UserConfigFile(s.Cwd)
	out := make([]Setting, 0, len(catalog))
	for _, d := range catalog {
		d.Value = c.Get(d.Key)
		if d.Value == "" {
			d.Value = d.Default
		}
		_, file, assigned := c.Assignment(d.Key)
		switch env := s.Getenv(d.Key); {
		case launcher.Protected(d.Key) && fromEnv[d.Key] && env != "":
			d.Source = SourceEnvironment
			d.Locked = "set by the environment, which wins over the files: change it where it is exported"
		case assigned && file == user:
			d.Source = SourceFile
		case assigned:
			d.Source = SourceDefault
		case !launcher.Protected(d.Key) && env != "" && env != d.Default:
			d.Source = SourceEnvironment
		default:
			d.Source = SourceDefault
		}
		out = append(out, d)
	}
	return out, nil
}

// Set checks value and writes it for key in the user configuration. A file the launcher would
// then refuse is put back as it was.
func (s Store) Set(key, value string) error {
	ss, err := s.List()
	if err != nil {
		return err
	}
	var d *Setting
	for i := range ss {
		if ss[i].Key == key {
			d = &ss[i]
		}
	}
	if d == nil {
		return fmt.Errorf("%s is not a setting of this screen", key)
	}
	if d.Locked != "" {
		return fmt.Errorf("%s is %s", d.Name, d.Locked)
	}
	if err := d.validate(value); err != nil {
		return err
	}
	c, _, err := s.load()
	if err != nil {
		return err
	}
	if key == "LEA_PROJECTS_DIR" {
		if value, err = launcher.CheckProjectsDir(c, s.Root, value); err != nil {
			return err
		}
	}
	path := c.UserConfigFile(s.Cwd)
	old, rerr := os.ReadFile(path)
	if err := launcher.WriteUserConfig(path, key, value); err != nil {
		return err
	}
	if _, _, err := s.load(); err != nil {
		if rerr == nil {
			os.WriteFile(path, old, 0o600)
		} else {
			os.Remove(path)
		}
		return err
	}
	return nil
}

func (d Setting) validate(v string) error {
	if strings.ContainsAny(v, "\n\r\x00") {
		return fmt.Errorf("%s: one line only", d.Name)
	}
	switch d.Kind {
	case Toggle:
		if v != "0" && v != "1" {
			return fmt.Errorf("%s is on (1) or off (0), not %q", d.Name, v)
		}
	case Choice:
		for _, c := range d.Choices {
			if v == c {
				return nil
			}
		}
		return fmt.Errorf("%s is one of %s, not %q", d.Name, strings.Join(d.Choices, ", "), v)
	}
	if d.check != nil {
		return d.check(v)
	}
	return nil
}

func seconds(v string) error {
	n, err := strconv.Atoi(v)
	if err != nil || strings.Trim(v, "0123456789") != "" || v[0] == '0' || n < 1 || n > 600 {
		return fmt.Errorf("the approval timeout is a number of seconds from 1 to 600, not %q", v)
	}
	return nil
}

func projectsDir(v string) error {
	if !strings.HasPrefix(v, "/") {
		return errors.New("the projects directory is an absolute path")
	}
	return nil
}

func hostPort(v string) error {
	if v == "" {
		return nil
	}
	h, p, ok := strings.Cut(v, ":")
	if n, err := strconv.Atoi(p); !ok || h == "" || strings.ContainsAny(h, " \t/") || err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("the collector is host:port, not %q", v)
	}
	return nil
}
