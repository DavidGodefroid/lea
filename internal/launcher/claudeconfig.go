package launcher

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"
)

// lea config claude (#334): the user's own base settings for Claude Code in lea, without editing a
// file or rebuilding the image. They live in lea's configuration on the host (claude.json next to
// lea.conf: the agents' guard denies editing ~/.config/lea, and the launcher refuses to run inside
// lea), never in the host's ~/.claude/settings.json. At each container start the launcher hands
// them to the entrypoint with the host's permission rules (hostPermissionsFile), and the entrypoint
// merges them into the image's managed settings. Precedence: lea config claude, then the host's
// settings.json permissions, then the image.

// claudeLists are the permission lists the user adds entries to and removes entries from.
var claudeLists = []string{"allow", "ask", "deny", "additionalDirectories"}

// claudeEnforced are the settings lea keeps, each with the reason the command gives. Every other
// setting lea config claude knows is the user's; a new one is enforced only once added here.
var claudeEnforced = map[string]string{
	"hooks": "lea's hooks are lea: its PreToolUse guard (hard denies, grants), message delivery, rewake and the board; " +
		"removing them breaks lea or disables its guard",
	"disableAllHooks": "lea's hooks are lea: its PreToolUse guard (hard denies, grants), message delivery, rewake and the board; " +
		"turning hooks off disables its guard",
	"allowManagedHooksOnly": "lea's hooks are lea, and so are the plugins' hooks it relies on (#327)",
	"allowManagedPermissionRulesOnly": "without it, an agent can widen its own rules from its writable ~/.claude/settings.json " +
		"or a project's .claude/settings*.json (#111); your rules are honoured anyway: lea config claude writes them into the managed settings",
	"disableBypassPermissionsMode": bypassReason,
	"statusLine":                   "lea's status line reports each agent to the deck",
}

const bypassReason = "bypass skips permission requests, so ask rules, lea's approvals (lea requests) and grants would stop applying; " +
	"dontAsk or auto cover the need for no prompts while keeping the guards"

// claudeReservedEnv are the variables lea sets for itself, with the reason; claudeReservedPrefixes
// the same for whole families.
var claudeReservedEnv = map[string]string{
	"SSH_AUTH_SOCK":                "lea's git delivery depends on it (its SSH agent)",
	"GH_CONFIG_DIR":                "lea's git delivery depends on it (gh's pinned configuration)",
	"GLAB_CONFIG_DIR":              "lea's git delivery depends on it (glab's configuration)",
	"CLAUDE_CODE_ENABLE_TELEMETRY": "lea's telemetry depends on it",
	"DISABLE_UPDATES":              "the pinned Claude Code version depends on it (image/managed-check)",
	"DISABLE_AUTOUPDATER":          "the pinned Claude Code version depends on it (image/managed-check)",
	"ENABLE_CLAUDEAI_MCP_SERVERS":  "LockHTTP turns claude.ai's connectors off with it",
	"HTTP_PROXY":                   "the agents' egress goes through lea-proxy",
	"HTTPS_PROXY":                  "the agents' egress goes through lea-proxy",
	"NO_PROXY":                     "the agents' egress goes through lea-proxy",
	"http_proxy":                   "the agents' egress goes through lea-proxy",
	"https_proxy":                  "the agents' egress goes through lea-proxy",
	"no_proxy":                     "the agents' egress goes through lea-proxy",
}

var claudeReservedPrefixes = map[string]string{
	"LEA_":  "lea's own variables (its bus, the agent's identity) are lea's",
	"OTEL_": "lea's telemetry depends on it",
}

// claudePrefs are Claude Code's plain preferences the managed settings can hold, with the check of
// a value (the value as the settings file holds it).
var claudePrefs = map[string]func(string) (any, error){
	"model":                 prefString,
	"language":              prefString,
	"outputStyle":           prefString,
	"alwaysThinkingEnabled": prefBool,
}

func prefString(v string) (any, error) {
	if v == "" {
		return nil, errors.New("an empty value")
	}
	if len(v) > 200 || strings.ContainsFunc(v, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return nil, errors.New("a value is one line of at most 200 bytes")
	}
	return v, nil
}

func prefBool(v string) (any, error) {
	switch v {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return nil, errors.New("the value is true or false")
}

// claudeList is what the user changed in one permission list: entries added, and entries removed
// from the image's or the host's.
type claudeList struct {
	Add    []string `json:"add,omitempty"`
	Remove []string `json:"remove,omitempty"`
}

// claudeConfig is claude.json.
type claudeConfig struct {
	DefaultMode string                 `json:"defaultMode,omitempty"`
	Lists       map[string]*claudeList `json:"lists,omitempty"`
	Env         map[string]string      `json:"env,omitempty"`
	Settings    map[string]any         `json:"settings,omitempty"`
}

const claudeUsage = `Usage:
  lea config claude                              the effective settings, each with its source
  lea config claude defaultMode MODE|--unset     default, acceptEdits, plan, dontAsk or auto
  lea config claude allow|ask|deny|additionalDirectories add|remove ENTRY
  lea config claude allow|ask|deny|additionalDirectories --unset
  lea config claude env NAME VALUE|--unset
  lea config claude model|language|outputStyle|alwaysThinkingEnabled VALUE|--unset
Precedence: lea config claude, then the permissions of the host's ~/.claude/settings.json, then
lea's image. lea keeps its hooks, allowManagedPermissionRulesOnly, disableBypassPermissionsMode and
its own variables. Applies to lea containers started from now on (lea stop --all, then lea).
`

const claudeApplies = "applies to lea containers started from now on (lea stop --all, then lea); running agents keep theirs until then"

// claudeConfigPath is claude.json, next to the user configuration (lea.conf).
func (l *Launcher) claudeConfigPath() string {
	return filepath.Join(filepath.Dir(l.Cfg.UserConfigFile(l.Cwd)), "claude.json")
}

// readClaudeConfig reads claude.json: empty when there is none; a link, a file that is not JSON
// or a value lea would not have written is an error (never taken as no settings: the user's deny
// rules would be lost).
func readClaudeConfig(p string) (*claudeConfig, error) {
	c := &claudeConfig{}
	b, err := readRegular(p)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %v", p, err)
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(c); err != nil {
		return nil, fmt.Errorf("cannot read %s: %v (fix it with lea config claude, or remove it)", p, err)
	}
	if err := c.check(); err != nil {
		return nil, fmt.Errorf("%s: %v (fix it with lea config claude, or remove it)", p, err)
	}
	return c, nil
}

// check applies the command's rules to a stored configuration.
func (c *claudeConfig) check() error {
	if c.DefaultMode != "" {
		if err := checkClaudeMode(c.DefaultMode); err != nil {
			return err
		}
	}
	for k, l := range c.Lists {
		if !hasString(claudeLists, k) {
			return fmt.Errorf("%s is not a permission list", k)
		}
		if l == nil {
			continue
		}
		for _, e := range append(append([]string{}, l.Add...), l.Remove...) {
			if err := checkClaudeEntry(k, e); err != nil {
				return err
			}
		}
	}
	for k, v := range c.Env {
		if err := checkClaudeEnv(k, v); err != nil {
			return err
		}
	}
	for k, v := range c.Settings {
		chk, ok := claudePrefs[k]
		if !ok {
			return fmt.Errorf("%s is not a setting lea config claude knows", k)
		}
		s := fmt.Sprint(v)
		if _, err := chk(s); err != nil {
			return fmt.Errorf("%s: %v", k, err)
		}
	}
	return nil
}

func checkClaudeMode(m string) error {
	if m == "bypassPermissions" {
		return fmt.Errorf("defaultMode bypassPermissions is lea's to refuse: %s", bypassReason)
	}
	if !permissionModes[m] {
		return fmt.Errorf("defaultMode %q is not a permission mode: default, acceptEdits, plan, dontAsk or auto", m)
	}
	return nil
}

func checkClaudeEntry(list, e string) error {
	switch {
	case e == "":
		return fmt.Errorf("%s: an empty entry", list)
	case len(e) > 1024:
		return fmt.Errorf("%s: an entry is at most 1024 bytes", list)
	case strings.ContainsFunc(e, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		return fmt.Errorf("%s: an entry is one line", list)
	}
	return nil
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

func checkClaudeEnv(k, v string) error {
	if !envName.MatchString(k) {
		return fmt.Errorf("env %q is not a variable name", k)
	}
	if why, ok := claudeReservedEnv[k]; ok {
		return fmt.Errorf("env %s is lea's: %s", k, why)
	}
	for p, why := range claudeReservedPrefixes {
		if strings.HasPrefix(k, p) {
			return fmt.Errorf("env %s is lea's: %s", k, why)
		}
	}
	if strings.ContainsAny(v, "\n\r\x00") || len(v) > 4096 {
		return fmt.Errorf("env %s: a value is one line of at most 4096 bytes", k)
	}
	return nil
}

// writeClaudeConfig replaces claude.json at once, private; a link is refused.
func writeClaudeConfig(p string, c *claudeConfig) error {
	for k, l := range c.Lists {
		if l == nil || len(l.Add) == 0 && len(l.Remove) == 0 {
			delete(c.Lists, k)
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(c); err != nil {
		return err
	}
	return ReplaceUserConfig(p, buf.Bytes())
}

// configClaude is lea config claude.
func (l *Launcher) configClaude(args []string) error {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		_, err := io.WriteString(l.Stdout, claudeUsage)
		return err
	}
	usage := func() error { io.WriteString(l.Stderr, claudeUsage); return exitError{2} }
	p := l.claudeConfigPath()
	c, err := readClaudeConfig(p)
	if err != nil {
		return die("%v", err)
	}
	if len(args) == 0 {
		return l.claudeShow(p, c)
	}
	k := args[0]
	if why, ok := claudeEnforced[k]; ok {
		return die("%s is lea's to keep: %s", k, why)
	}
	var said string
	switch {
	case k == "defaultMode":
		if len(args) != 2 {
			return usage()
		}
		if args[1] == "--unset" {
			c.DefaultMode, said = "", "defaultMode: back to the host's settings.json, else lea's image"
			break
		}
		if err := checkClaudeMode(args[1]); err != nil {
			return die("%v", err)
		}
		c.DefaultMode, said = args[1], "defaultMode "+args[1]
	case hasString(claudeLists, k):
		if said, err = l.claudeListChange(c, k, args[1:]); err != nil {
			return err
		}
	case k == "env":
		if len(args) != 3 {
			return usage()
		}
		if args[2] == "--unset" {
			if !envName.MatchString(args[1]) {
				return die("env %q is not a variable name", args[1])
			}
			delete(c.Env, args[1])
			said = "env " + args[1] + ": unset"
			break
		}
		if err := checkClaudeEnv(args[1], args[2]); err != nil {
			return die("%v", err)
		}
		if c.Env == nil {
			c.Env = map[string]string{}
		}
		c.Env[args[1]], said = args[2], "env "+args[1]+"="+args[2]
	case claudePrefs[k] != nil:
		if len(args) != 2 {
			return usage()
		}
		if args[1] == "--unset" {
			delete(c.Settings, k)
			said = k + ": unset"
			break
		}
		v, err := claudePrefs[k](args[1])
		if err != nil {
			return die("%s: %v", k, err)
		}
		if c.Settings == nil {
			c.Settings = map[string]any{}
		}
		c.Settings[k], said = v, k+" "+args[1]
	case k == "permissions":
		return die("permissions: set its parts: defaultMode, allow, ask, deny, additionalDirectories")
	default:
		return die("%s is not a setting lea config claude knows (lea config claude --help)", k)
	}
	if err := writeClaudeConfig(p, c); err != nil {
		return die("%v", err)
	}
	l.logf("%s: %s", said, claudeApplies)
	return nil
}

// claudeListChange is lea config claude LIST add|remove ENTRY, or LIST --unset.
func (l *Launcher) claudeListChange(c *claudeConfig, k string, args []string) (string, error) {
	if c.Lists == nil {
		c.Lists = map[string]*claudeList{}
	}
	if c.Lists[k] == nil {
		c.Lists[k] = &claudeList{}
	}
	cl := c.Lists[k]
	if len(args) == 1 && args[0] == "--unset" {
		delete(c.Lists, k)
		return k + ": lea config claude's changes dropped", nil
	}
	if len(args) != 2 || args[0] != "add" && args[0] != "remove" {
		io.WriteString(l.Stderr, claudeUsage)
		return "", exitError{2}
	}
	e := args[1]
	if err := checkClaudeEntry(k, e); err != nil {
		return "", die("%v", err)
	}
	base := hasString(l.imageClaudeList(k), e) || hasString(hostClaudeList(l.Cfg.Get("HOME")+"/.claude", k), e)
	if args[0] == "add" {
		switch {
		case hasString(cl.Remove, e):
			cl.Remove = without(cl.Remove, e)
			if !base {
				cl.Add = append(cl.Add, e)
			}
			return k + " " + e + ": back", nil
		case hasString(cl.Add, e):
			return k + " " + e + ": already added", nil
		case base:
			return k + " " + e + ": already one of lea's or the host's", nil
		}
		if len(cl.Add) >= 1000 {
			return "", die("%s: at most 1000 entries", k)
		}
		cl.Add = append(cl.Add, e)
		return k + " " + e + ": added", nil
	}
	switch {
	case hasString(cl.Remove, e):
		return k + " " + e + ": already removed", nil
	case !base && !hasString(cl.Add, e):
		return "", die("%s is not one of the %s entries (lea's, the host's or lea config claude's): nothing to remove", e, k)
	}
	cl.Add = without(cl.Add, e)
	if base {
		if len(cl.Remove) >= 1000 {
			return "", die("%s: at most 1000 removals", k)
		}
		cl.Remove = append(cl.Remove, e)
	}
	return k + " " + e + ": removed", nil
}

func without(l []string, s string) []string {
	var out []string
	for _, x := range l {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}

// imageManaged is the image's managed settings as this lea ships them (image/managed-settings.json
// of the checkout or the release); nil when lea cannot read them.
func (l *Launcher) imageManaged() map[string]any {
	b, err := os.ReadFile(filepath.Join(l.Root, "image", "managed-settings.json"))
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

func (l *Launcher) imageClaudeList(k string) []string {
	perms, _ := l.imageManaged()["permissions"].(map[string]any)
	return strList(perms[k])
}

func strList(v any) []string {
	var out []string
	a, _ := v.([]any)
	for _, x := range a {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// hostClaudeList is one list of hostPermissions (src: the host's ~/.claude).
func hostClaudeList(src, k string) []string {
	var m map[string]any
	json.Unmarshal(hostPermissions(src), &m)
	return strList(m[k])
}

// claudeHanded is what lea hands the entrypoint (HostPermissionsPath): the host's permissions
// (host, from hostPermissions) with lea config claude's on top. Each list is lea config claude's
// additions, then the host's (at most 1000), without the entries lea config claude removes, which
// "remove" names for the entrypoint to take out of the image's too; defaultMode is lea config
// claude's, else the host's; env and settings join the image's managed settings, whose own keys
// win.
func claudeHanded(host []byte, c *claudeConfig) []byte {
	out := map[string]any{}
	json.Unmarshal(host, &out)
	remove := map[string][]string{}
	for _, k := range claudeLists {
		hl := strList(out[k])
		cl := c.Lists[k]
		if cl == nil {
			continue
		}
		var merged []string
		for _, e := range append(append([]string{}, cl.Add...), hl...) {
			if !hasString(merged, e) && !hasString(cl.Remove, e) && len(merged) < 1000 {
				merged = append(merged, e)
			}
		}
		delete(out, k)
		if len(merged) > 0 {
			out[k] = merged
		}
		if len(cl.Remove) > 0 {
			remove[k] = cl.Remove
		}
	}
	if len(remove) > 0 {
		out["remove"] = remove
	}
	if c.DefaultMode != "" {
		out["defaultMode"] = c.DefaultMode
	}
	if len(c.Env) > 0 {
		out["env"] = c.Env
	}
	if len(c.Settings) > 0 {
		out["settings"] = c.Settings
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	enc.Encode(out)
	return buf.Bytes()
}

const (
	srcImage    = "lea default"
	srcHost     = "host ~/.claude/settings.json"
	srcLea      = "lea config claude"
	srcEnforced = "lea, enforced"
)

// claudeShow prints every effective setting with its source.
func (l *Launcher) claudeShow(p string, c *claudeConfig) error {
	img := l.imageManaged()
	src := l.Cfg.Get("HOME") + "/.claude"
	var host map[string]any
	json.Unmarshal(hostPermissions(src), &host)
	tw := tabwriter.NewWriter(l.Stdout, 0, 0, 2, ' ', 0)
	row := func(k, v, from string) { fmt.Fprintf(tw, "  %s\t%s\t%s\n", k, v, from) }
	fmt.Fprintf(l.Stdout, "Claude's base settings in lea (lea config claude --help), stored in %s:\n", p)
	if img == nil {
		fmt.Fprintf(l.Stdout, "  (lea's image defaults are unreadable here: %s)\n", filepath.Join(l.Root, "image", "managed-settings.json"))
	}
	perms, _ := img["permissions"].(map[string]any)
	mode, _ := perms["defaultMode"].(string)
	switch hm, _ := host["defaultMode"].(string); {
	case c.DefaultMode != "":
		row("defaultMode", c.DefaultMode, srcLea)
	case hm != "":
		row("defaultMode", hm, srcHost)
	case mode != "":
		row("defaultMode", mode, srcImage)
	default:
		row("defaultMode", "(Claude Code's own default)", srcImage)
	}
	for _, k := range claudeLists {
		cl := c.Lists[k]
		if cl == nil {
			cl = &claudeList{}
		}
		var seen []string
		show := func(e, from string) {
			if hasString(seen, e) {
				return
			}
			seen = append(seen, e)
			if hasString(cl.Remove, e) {
				from = "removed by " + srcLea
			}
			row(k, e, from)
		}
		for _, e := range strList(perms[k]) {
			show(e, srcImage)
		}
		for _, e := range strList(host[k]) {
			show(e, srcHost)
		}
		for _, e := range cl.Add {
			show(e, srcLea)
		}
		for _, e := range cl.Remove {
			show(e, srcLea)
		}
	}
	env := map[string]string{}
	from := map[string]string{}
	imgEnv, _ := img["env"].(map[string]any)
	for k, v := range c.Env {
		env[k], from[k] = v, srcLea
	}
	for k, v := range imgEnv {
		env[k], from[k] = fmt.Sprint(v), srcImage
		if checkClaudeEnv(k, "") != nil {
			from[k] = srcEnforced
		}
	}
	for _, k := range sortedKeys(env) {
		row("env "+k, env[k], from[k])
	}
	for _, k := range sortedKeys(claudePrefs) {
		if v, ok := c.Settings[k]; ok {
			row(k, fmt.Sprint(v), srcLea)
		} else if v, ok := img[k]; ok {
			row(k, fmt.Sprint(v), srcImage)
		}
	}
	if hooks, ok := img["hooks"].(map[string]any); ok {
		row("hooks", strings.Join(sortedKeys(hooks), " "), srcEnforced)
	}
	row("allowManagedPermissionRulesOnly", "true", srcEnforced)
	row("disableBypassPermissionsMode", "disable", srcEnforced)
	if err := tw.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintf(l.Stdout, "Changes %s.\n", claudeApplies)
	return err
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// claudeDoctor is lea doctor's line on lea config claude: the active overrides.
func (l *Launcher) claudeDoctor() error {
	c, err := readClaudeConfig(l.claudeConfigPath())
	if err != nil {
		fmt.Fprintf(l.Stdout, "FAIL  lea config claude: %v\n", err)
		return exitError{1}
	}
	var parts []string
	if c.DefaultMode != "" {
		parts = append(parts, "defaultMode "+c.DefaultMode)
	}
	for _, k := range claudeLists {
		if cl := c.Lists[k]; cl != nil && (len(cl.Add) > 0 || len(cl.Remove) > 0) {
			parts = append(parts, fmt.Sprintf("%s +%d -%d", k, len(cl.Add), len(cl.Remove)))
		}
	}
	for _, k := range sortedKeys(c.Env) {
		parts = append(parts, "env "+k)
	}
	for _, k := range sortedKeys(c.Settings) {
		parts = append(parts, k+" "+fmt.Sprint(c.Settings[k]))
	}
	if len(parts) == 0 {
		fmt.Fprintln(l.Stdout, "ok    lea config claude: no overrides (lea's defaults and the host's ~/.claude/settings.json permissions)")
		return nil
	}
	fmt.Fprintf(l.Stdout, "ok    lea config claude: %s\n", strings.Join(parts, ", "))
	return nil
}
