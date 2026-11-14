package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The deny-lists of a lead's children (#423) are read from lea's config files at each spawn: an
// edit of the user configuration while the daemon runs applies to the next child.
func TestHostChildDenyReadsTheConfigurationAtEachCall(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(t.TempDir(), "lea.conf")
	t.Setenv("LEA_CONFIG", conf)
	t.Setenv("LEA_CHILD_PLUGINS", "")
	t.Setenv("LEA_CHILD_MCP", "")
	deny := hostChildDeny(filepath.Join(root, "build", "lea"))
	if p, m := deny(); p != "" || m != "" {
		t.Fatalf("defaults: %q %q", p, m)
	}
	if err := os.WriteFile(conf, []byte("LEA_CHILD_PLUGINS='foo, bar'\nLEA_CHILD_MCP='claude.ai Gmail'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, m := deny(); p != "foo,bar" || m != "claude.ai Gmail" {
		t.Fatalf("after the edit: %q %q", p, m)
	}
	if err := os.WriteFile(conf, []byte("LEA_CHILD_PLUGINS=baz\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, m := deny(); p != "baz" || m != "" {
		t.Fatalf("after the second edit: %q %q", p, m)
	}
}
