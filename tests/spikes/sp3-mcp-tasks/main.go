//go:build ignore

// Spike SP3 (#156): a stdio MCP server, hand-rolled JSON-RPC (go-sdk v1.8.0 has no Tasks), that
// logs every request it gets to calls.log (the client's initialize capabilities first) and answers
// tools/call on `slow` with a task (SEP-1686, extension io.modelcontextprotocol/tasks): the
// client should then poll tasks/get and fetch tasks/result.
//
//	go run tests/spikes/sp3-mcp-tasks/main.go   (Claude Code starts it from mcp.json)
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type msg struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
}

func main() {
	dir := filepath.Dir(os.Args[0])
	if d := os.Getenv("SP3_DIR"); d != "" {
		dir = d
	}
	logf, err := os.OpenFile(filepath.Join(dir, "calls.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		os.Exit(1)
	}
	defer logf.Close()
	out := json.NewEncoder(os.Stdout)
	reply := func(id json.RawMessage, result any) {
		_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	created := time.Now().UTC().Format(time.RFC3339)
	task := func(status string) map[string]any {
		return map[string]any{"taskId": "t-42", "status": status, "createdAt": created,
			"lastUpdatedAt": time.Now().UTC().Format(time.RFC3339), "ttl": 600000, "pollInterval": 2000}
	}
	start := time.Time{}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var m msg
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		fmt.Fprintf(logf, "%s %s %s\n", time.Now().Format("15:04:05"), m.Method, m.Params)
		switch m.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(m.Params, &p)
			reply(m.ID, map[string]any{
				"protocolVersion": p.ProtocolVersion,
				"capabilities": map[string]any{
					"tools": map[string]any{},
					// Both shapes: the 2025-11-25 core capability and the SEP-2663 extension.
					"tasks": map[string]any{"list": map[string]any{}, "cancel": map[string]any{},
						"requests": map[string]any{"tools": map[string]any{"call": map[string]any{}}}},
					"extensions": map[string]any{"io.modelcontextprotocol/tasks": map[string]any{}},
				},
				"serverInfo": map[string]any{"name": "tasks", "version": "0"},
			})
		case "tools/list":
			reply(m.ID, map[string]any{"tools": []any{map[string]any{
				"name": "slow", "description": "Waits for an approval; answers with a task",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
				"execution":   map[string]any{"taskSupport": "optional"},
			}}})
		case "tools/call":
			start = time.Now()
			reply(m.ID, map[string]any{"task": task("working")})
		case "tasks/get":
			st := "working"
			if !start.IsZero() && time.Since(start) > 10*time.Second {
				st = "completed"
			}
			reply(m.ID, task(st))
		case "tasks/result":
			reply(m.ID, map[string]any{"content": []any{map[string]any{"type": "text", "text": "ticket 42 approved"}}})
		default:
			if len(m.ID) > 0 {
				reply(m.ID, map[string]any{})
			}
		}
	}
}
