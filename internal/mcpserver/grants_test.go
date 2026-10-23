package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/daemon"
	"github.com/DavidGodefroid/lea/internal/grants"
	"github.com/DavidGodefroid/lea/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestNoToolWritesGrants pins the agents' tool set (issue #73): grants are written on the host
// only. A new tool must be added here on purpose, and none may create, change or revoke a grant.
func TestNoToolWritesGrants(t *testing.T) {
	dir, err := os.MkdirTemp("", "lea")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	st, err := store.Open(filepath.Join(dir, "lea.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	d := daemon.New(st, filepath.Join(dir, "run"), func() int64 { return time.Now().Unix() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus, err := d.AttachContainer(ctx, "lea-a")
	if err != nil {
		t.Fatal(err)
	}
	dev := session(t, bus, "dev")
	tools, err := dev.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
	}
	sort.Strings(names)
	want := "agents_list agents_request_spawn agents_spawn agents_stop ask board_read grants_list lease_acquire lease_release lock_request msg_inbox msg_reply msg_send msg_wait providers_list task_update"
	if got := strings.Join(names, " "); got != want {
		t.Fatalf("agent tools changed:\n got %s\nwant %s", got, want)
	}
	if _, err := dev.CallTool(ctx, &mcp.CallToolParams{Name: "agents_list", Arguments: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	if _, err := st.AddGrant(grants.Grant{Container: "lea-a", Action: grants.Merge, Repo: "lea", Created: now, Expires: now + 60}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddGrant(grants.Grant{Container: "lea-b", Action: grants.CloseIssue, Created: now, Expires: now + 60}); err != nil {
		t.Fatal(err)
	}
	r, err := dev.CallTool(ctx, &mcp.CallToolParams{Name: "grants_list", Arguments: map[string]any{}})
	if err != nil || r.IsError || !strings.Contains(text(r), `"action":"merge"`) || strings.Contains(text(r), "close-issue") {
		t.Fatalf("grants_list: %v %s", err, text(r))
	}
}
