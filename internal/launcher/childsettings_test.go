package launcher

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// childClaudeDir is a ~/.claude with plugins foo (two marketplaces), bar and lea's own name
// enabled or installed.
func childClaudeDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	if err := os.MkdirAll(d+"/plugins", 0o755); err != nil {
		t.Fatal(err)
	}
	w := func(p, s string) {
		if err := os.WriteFile(filepath.Join(d, p), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	w("settings.json", `{"enabledPlugins":{"foo@mkt":true,"bar@mkt":true,"lea@other":true}}`)
	w("plugins/installed_plugins.json", `{"version":2,"plugins":{"foo@mkt2":[{}],"bar@mkt":[{}]}}`)
	return d
}

func childSettingsOf(t *testing.T, base string, dir string, plugins, mcp []string) map[string]any {
	t.Helper()
	b, err := ChildSettings([]byte(base), dir, plugins, mcp)
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestChildSettingsDeniesPluginsAndMCPServersButKeepsLeasAndTheLocks(t *testing.T) {
	d := childClaudeDir(t)
	base := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"lea hook claude Stop"}]}]},"env":{"A":"1"}}`
	s := childSettingsOf(t, base, d, []string{"foo", "baz@m", "lea", "locksql"},
		[]string{"claude.ai Gmail", "lea", "locksql", "lockmq-crm", "browser"})
	if want := map[string]any{"foo@mkt": false, "foo@mkt2": false, "baz@m": false}; !reflect.DeepEqual(s["enabledPlugins"], want) {
		t.Fatalf("enabledPlugins=%v, want %v", s["enabledPlugins"], want)
	}
	want := []any{map[string]any{"serverName": "claude.ai Gmail"}, map[string]any{"serverName": "browser"}}
	if !reflect.DeepEqual(s["deniedMcpServers"], want) {
		t.Fatalf("deniedMcpServers=%v, want %v", s["deniedMcpServers"], want)
	}
	// lea's own settings are all there.
	var b map[string]any
	json.Unmarshal([]byte(base), &b)
	for k, v := range b {
		if !reflect.DeepEqual(s[k], v) {
			t.Fatalf("%s=%v, want %v", k, s[k], v)
		}
	}
}

func TestChildSettingsWithEmptyListsIsLeasSettings(t *testing.T) {
	base := `{"env":{"A":"1"},"enabledPlugins":{"x@y":true},"deniedMcpServers":[{"serverName":"z"}]}`
	s := childSettingsOf(t, base, childClaudeDir(t), nil, nil)
	var b map[string]any
	json.Unmarshal([]byte(base), &b)
	if !reflect.DeepEqual(s, b) {
		t.Fatalf("got %v, want %v", s, b)
	}
	// Denying only kept names changes nothing either.
	if s := childSettingsOf(t, base, childClaudeDir(t), []string{"lea"}, []string{"lea", "locklog"}); !reflect.DeepEqual(s, b) {
		t.Fatalf("kept names: got %v", s)
	}
	if _, err := ChildSettings([]byte(`[]`), "", nil, nil); err == nil {
		t.Fatal("a settings file that is no object was accepted")
	}
}

func TestChildDenyListNormalises(t *testing.T) {
	got := ChildDenyList(" foo, bar@mkt ,,foo,claude.ai Gmail,-x,a;b ")
	if want := []string{"foo", "bar@mkt", "claude.ai Gmail"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if ChildDenyList("") != nil {
		t.Fatal("an empty value is a list")
	}
}

func TestRunChildSettingsReadsTheChildsEnvironment(t *testing.T) {
	home := t.TempDir()
	if err := os.Rename(childClaudeDir(t), home+"/.claude"); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(t.TempDir(), "s.json")
	os.WriteFile(base, []byte(`{}`), 0o600)
	env := map[string]string{"HOME": home, ChildPluginsKey: "bar", ChildMCPKey: "m1"}
	b, err := RunChildSettings(base, func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"deniedMcpServers":[{"serverName":"m1"}],"enabledPlugins":{"bar@mkt":false}}` {
		t.Fatalf("got %s", b)
	}
	if _, err := RunChildSettings(base+".none", func(k string) string { return env[k] }); err == nil {
		t.Fatal("a missing settings file gave settings")
	}
}
