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
	"github.com/DavidGodefroid/lea/internal/providers"
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
	{Key: "LEA_PROVIDER", Group: "Agents", Name: "Default provider", Kind: Text, Default: providers.Default, Apply: ApplyNextStart,
		Help: "the provider of a new agent: claude, codex or a name of the providers file", check: providerName},
	{Key: "LEA_MODEL", Group: "Agents", Name: "Default model", Kind: Text, Apply: ApplyNextStart,
		Help: "the model of a new agent on the default provider; empty: the provider's own", check: providers.CheckModel},
	{Key: "LEA_SOUND", Group: "Sounds", Name: "Sounds", Kind: Toggle, Default: "1", Apply: ApplyNow, Live: true,
		Help: "a sound when an agent waits for you and when its turn ends"},
	{Key: "LEA_SOUND_ATTENTION", Group: "Sounds", Name: "Attention sound", Kind: Text, Apply: ApplyAfterStop,
		Help: "sound file when an agent waits for you; 0 mutes it, empty: freedesktop", check: soundFile},
	{Key: "LEA_SOUND_DONE", Group: "Sounds", Name: "Done sound", Kind: Text, Apply: ApplyAfterStop,
		Help: "sound file when a turn ends; 0 mutes it, empty: freedesktop", check: soundFile},
	{Key: "LEA_GIT_SSH", Group: "Egress", Name: "Git SSH endpoints", Kind: Text, Apply: ApplyAfterStop,
		Help: "host:port endpoints git reaches over SSH; empty: HTTPS only", check: launcher.CheckGitSSH},
	{Key: "LEA_OTEL_UPSTREAM", Group: "Egress", Name: "OTel collector", Kind: Text, Apply: ApplyAfterStop,
		Help: "host:port of an OTLP collector for usage telemetry; empty: off", check: launcher.CheckOtelUpstream},
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
		if value, err = launcher.CheckProjectsDir(c, s.Root, s.Cwd, value); err != nil {
			return err
		}
	}
	path := c.UserConfigFile(s.Cwd)
	old, rerr := os.ReadFile(path)
	if rerr != nil && !os.IsNotExist(rerr) {
		return rerr
	}
	if err := launcher.WriteUserConfig(path, key, value); err != nil {
		return err
	}
	if key == "LEA_GIT_SSH" {
		// The legacy key sets the endpoints when LEA_GIT_SSH is empty: it goes with it.
		if err := dropKey(path, "LEA_GITLAB_SSH"); err != nil {
			return errors.Join(err, restore(path, old, rerr == nil))
		}
	}
	c, _, err = s.load()
	if err == nil && key == "LEA_GIT_SSH" && c.GitSSH() != strings.Join(strings.Fields(value), " ") {
		err = fmt.Errorf("LEA_GITLAB_SSH (the key of older lea) still sets the git SSH endpoints to '%s': remove it where it is set", c.GitSSH())
	}
	if err != nil {
		return errors.Join(err, restore(path, old, rerr == nil))
	}
	return nil
}

// dropKey removes every line that sets key from the user configuration at path.
func dropKey(path, key string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var out strings.Builder
	for _, l := range strings.SplitAfter(string(b), "\n") {
		if !launcher.SetsKey(l, key) {
			out.WriteString(l)
		}
	}
	if out.Len() == len(b) {
		return nil
	}
	return launcher.ReplaceUserConfig(path, []byte(out.String()))
}

// restore puts the user configuration back as it was before a refused write: its old content, or
// no file when there was none.
func restore(path string, old []byte, existed bool) error {
	if !existed {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return launcher.ReplaceUserConfig(path, old)
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

// soundFile is a sound setting: empty (the freedesktop sound), 0 (muted) or an absolute path,
// which the sound player can never read as one of its options.
func soundFile(v string) error {
	if v != "" && v != "0" && !strings.HasPrefix(v, "/") {
		return fmt.Errorf("a sound is an absolute file path, 0 (muted) or empty (the freedesktop sound), not %q", v)
	}
	return nil
}

// providerName accepts the name of a built-in provider or of the user's providers file, as the
// launcher would read it now.
func providerName(v string) error {
	if v == "" {
		return nil
	}
	reg, err := launcher.LoadProviders(nil, os.Getenv)
	if err != nil {
		return err
	}
	_, err = reg.Get(v)
	return err
}
