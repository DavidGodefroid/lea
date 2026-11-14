package launcher

import (
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/DavidGodefroid/lea/internal/providers"
)

// Lean children (#423): every agent of a projects directory shares its copy of the host's
// ~/.claude (claudeLayer), so a child loads the skill listing of every host plugin and the tool
// listing of every MCP server, re-read on each of its turns. LEA_CHILD_PLUGINS and LEA_CHILD_MCP
// are deny-lists of the host plugins and MCP servers a lead's children (dev, reviewer) start
// without. The daemon reads them from lea's configuration at each spawn (ChildDeny), hands them to
// the child's docker exec, and lea-session turns them into the child's own --settings
// (ChildSettings): enabledPlugins false, deniedMcpServers. Leads and the user's sessions get
// neither, and a running child keeps what it started with.
const (
	ChildPluginsKey = "LEA_CHILD_PLUGINS"
	ChildMCPKey     = "LEA_CHILD_MCP"
)

// childEntryRe is one name of a deny-list: a plugin (name or name@marketplace) or an MCP server
// (claude.ai's connectors have spaces in theirs: "claude.ai Gmail").
var childEntryRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._:@-]{0,127}$`)

// ChildDenyList is the names of a deny-list value (name,name), trimmed, without duplicates and
// without an entry that is no name (ignored).
func ChildDenyList(v string) []string {
	var out []string
	for _, e := range strings.Split(v, ",") {
		e = strings.TrimSpace(e)
		if childEntryRe.MatchString(e) && !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	return out
}

// ChildDeny is the two deny-lists as the configuration sets them now, each normalised
// (ChildDenyList) and joined with commas: "" when unset.
func (c *Config) ChildDeny() (plugins, mcp string) {
	return strings.Join(ChildDenyList(c.Get(ChildPluginsKey)), ","), strings.Join(ChildDenyList(c.Get(ChildMCPKey)), ",")
}

// childKeptPlugin and childKeptMCP are never denied: lea's own plugin, lea's MCP server and the
// Locks' clients (a lock instance's server is <tool>-<instance>).
func childKeptPlugin(name string) bool {
	n, _, _ := strings.Cut(name, "@")
	return n == "lea" || childKeptMCP(n)
}

func childKeptMCP(name string) bool {
	if name == "lea" {
		return true
	}
	for _, l := range providers.LockNames {
		if name == l || strings.HasPrefix(name, l+"-") {
			return true
		}
	}
	return false
}

// childPluginIDs is the plugin ids (name@marketplace) Claude Code may enable from claudeDir: the
// user settings' enabledPlugins and the installed plugins. Unreadable files give none.
func childPluginIDs(claudeDir string) []string {
	var ids []string
	var s struct {
		EnabledPlugins map[string]json.RawMessage `json:"enabledPlugins"`
	}
	if b, err := readRegular(claudeDir + "/settings.json"); err == nil && json.Unmarshal(b, &s) == nil {
		for id := range s.EnabledPlugins {
			ids = append(ids, id)
		}
	}
	var inst struct {
		Plugins map[string]json.RawMessage `json:"plugins"`
	}
	if b, err := readRegular(claudeDir + "/plugins/installed_plugins.json"); err == nil && json.Unmarshal(b, &inst) == nil {
		for id := range inst.Plugins {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return slices.Compact(ids)
}

// ChildSettings is a child's --settings: base (lea's own settings: its hooks, its env) with each
// denied plugin disabled (enabledPlugins false: a name denies it from every marketplace, a
// name@marketplace that one) and each denied MCP server in deniedMcpServers. lea's plugin and
// MCP server and the Locks' clients are kept whatever the lists say. claudeDir is the child's
// ~/.claude, where its plugins are found.
func ChildSettings(base []byte, claudeDir string, plugins, mcp []string) ([]byte, error) {
	var s map[string]any
	if err := json.Unmarshal(base, &s); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errors.New("the settings are not a JSON object")
	}
	ids := childPluginIDs(claudeDir)
	off := map[string]bool{}
	for _, p := range plugins {
		if childKeptPlugin(p) {
			continue
		}
		if strings.Contains(p, "@") {
			off[p] = true
			continue
		}
		for _, id := range ids {
			if n, _, _ := strings.Cut(id, "@"); n == p {
				off[id] = true
			}
		}
	}
	if len(off) > 0 {
		ep, _ := s["enabledPlugins"].(map[string]any)
		if ep == nil {
			ep = map[string]any{}
		}
		for id := range off {
			ep[id] = false
		}
		s["enabledPlugins"] = ep
	}
	denied, _ := s["deniedMcpServers"].([]any)
	n := len(denied)
	for _, m := range mcp {
		if !childKeptMCP(m) {
			denied = append(denied, map[string]any{"serverName": m})
		}
	}
	if len(denied) > n {
		s["deniedMcpServers"] = denied
	}
	return json.Marshal(s)
}

// RunChildSettings is `lea child-settings BASE` in a child's container (lea-session): BASE with
// the deny-lists of the child's environment (LEA_CHILD_PLUGINS, LEA_CHILD_MCP), on stdout.
func RunChildSettings(base string, getenv func(string) string) ([]byte, error) {
	b, err := readRegular(base)
	if err != nil {
		return nil, err
	}
	dir := getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		dir = getenv("HOME") + "/.claude"
	}
	return ChildSettings(b, dir, ChildDenyList(getenv(ChildPluginsKey)), ChildDenyList(getenv(ChildMCPKey)))
}
