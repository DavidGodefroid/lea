# Spike SP3: MCP Tasks (#156)

Claude Code 2.1.296 (the version lea's image pins), in a lea agent container, 2026-10-29.
No production code.

`main.go` (`//go:build ignore`, out of the module's build) is a stdio MCP server in plain JSON-RPC,
since go-sdk v1.8.0 has no Tasks. It logs every request to `calls.log`, advertises Tasks both ways
(the 2025-11-25 `capabilities.tasks` and the `io.modelcontextprotocol/tasks` extension), marks its
tool `slow` with `execution.taskSupport: "optional"`, answers `tools/call` with a task
(`{"task":{"taskId":"t-42","status":"working",...}}`) and serves `tasks/get` / `tasks/result`.

```sh
go build -o /tmp/sp3/tasks-server tests/spikes/sp3-mcp-tasks/main.go
printf '{"mcpServers":{"tasks":{"command":"/tmp/sp3/tasks-server","env":{"SP3_DIR":"/tmp/sp3"}}}}\n' > /tmp/sp3/mcp.json
claude -p "Call the mcp__tasks__slow tool once and report exactly what it returned." \
  --mcp-config /tmp/sp3/mcp.json --strict-mcp-config --allowedTools mcp__tasks__slow
cat /tmp/sp3/calls.log
```

## Results

What the client sent, in order:

1. `server/discover`, with `io.modelcontextprotocol/protocolVersion: "2026-07-28"` in `_meta` and
   client capabilities `roots` and `elicitation` only. The stub answered `{}`, and the client fell
   back to:
2. `initialize`, `protocolVersion: "2025-11-25"`, capabilities `roots.listChanged` and
   `elicitation.form/url`. **No `tasks`, no `extensions`.**
3. `tools/call` on `slow` with `_meta.progressToken` and `claudecode/toolUseId`, **no `task`
   parameter**, although the server advertised Tasks and the tool allows them.
4. No `tasks/get`, no `tasks/result`. The task answer is refused: *MCP server "tasks" returned a
   malformed result that failed schema validation: ... content is required when the body carries
   'task'*. The model sees that error as the tool result.

## Verdict: NO-GO for v1 (as planned)

- Claude Code 2.1.296 does not speak MCP Tasks: it neither advertises them nor asks for a
  task-augmented call, and it rejects a task result. Waiting for an approval cannot ride on Tasks
  with the pinned version; the hub keeps its plan (a `pending` result, then the suspend hook, SP1
  #154).
- Not tested: 2.1.304+ (npm `latest` is 2.1.312 today). A newer binary would share `~/.claude`
  with the agents running 2.1.296, so it was not run from an agent.
- Revisit when both hold: go-sdk ships Tasks (or the extension), and lea's image moves to a Claude
  Code that advertises `io.modelcontextprotocol/tasks` in `initialize`. Rerun this stub then:
  pass = `tasks` in the logged capabilities, a `task` parameter on `tools/call`, and `tasks/get`
  polls until `tasks/result`.
- Side finding: 2.1.296 already tries the stateless `server/discover` (protocol 2026-07-28) before
  `initialize`. An empty answer made it fall back to `initialize`; the method-not-found error a
  go-sdk server sends was not tried here.
