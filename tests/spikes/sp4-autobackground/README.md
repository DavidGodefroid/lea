# Spike SP4: a Lock approval that waits longer than 2 minutes (#157)

Claude Code 2.1.296, in a lea agent container, 2026-10-29. No production code.

`stub.py` is an MCP server `stub` with one tool `approve` that answers "ticket 42 approved" after
`WAIT` seconds (default 180: a TTY approval of 3 minutes), without progress notifications. It
logs each request to `calls.log` (`SP4_DIR`). `drive.py` runs the interactive TUI in a pty, the
way lea runs an agent, and types the prompt.

```sh
D=tests/spikes/sp4-autobackground; W=/tmp/sp4; mkdir -p $W
printf '{"mcpServers":{"stub":{"command":"python3","args":["%s/stub.py"],"env":{"SP4_DIR":"%s"}}}}\n' "$PWD/$D" $W > $W/mcp.json
P="Call the mcp__stub__approve tool once, then reply with RESULT: followed by exactly what it returned."
claude -p "$P" --mcp-config $W/mcp.json --strict-mcp-config --allowedTools mcp__stub__approve      # headless
python3 $D/drive.py $W 330 $W/screen.log "$P" claude --mcp-config $W/mcp.json --strict-mcp-config --allowedTools mcp__stub__approve
```

## Results

**Headless (`claude -p`)**: no background. The call blocks for the 3 minutes and the result comes
back as the tool result: `RESULT: ticket 42 approved`, exit 0, 187 s.

**Interactive** (lea's case), from the transcript:

| t | What happens |
|---|---|
| 0 s | `tools/call approve` |
| 120 s | the tool result the model gets is Claude Code's notice: *MCP tool "stub/approve" is still running after 120s. It was moved to the background as task kn8pa8cdu and keeps running; you'll receive a notification with the result when it completes. You can keep working in the meantime. To stop it, use TaskStop with task_id "kn8pa8cdu". Note: it does not survive exiting this session.* |
| 123 s | the model says it waits, its turn ends, **Stop fires** (lea: idle, Rewake starts) |
| 180 s | the server answers; a `<task-notification>` (status completed, `<result>ticket 42 approved</result>`) starts a new turn by itself |
| 182 s | `RESULT: ticket 42 approved` |

With `CLAUDE_CODE_MCP_AUTO_BACKGROUND_MS=8000` and a 25 s wait the same happens at 8 s: the
variable is honoured. **PostToolUse fires once, when the call is moved to the background**, with
the background notice as `tool_response`; it does not fire when the real result arrives (the
task notification is not a tool call).

## Verdict: PASS, the result is delivered; three things to know

1. A Lock approval longer than 2 minutes is not lost today: the result reaches the agent as a
   task notification, which wakes it. No lea change is needed for the current 5-minute console
   wait.
2. Between the 2-minute mark and the result the agent is idle to lea (Stop fired) and the model
   is told it may keep working: it may run other tools before the approval lands. And the task
   "does not survive exiting this session": a container stop or an agent restart while it runs
   loses the result.
3. For the suspend hook (#160): a Lock call that runs past the threshold reaches PostToolUse with
   the background notice, never with its result, so the hook must not take that notice for a
   result (it carries no `pending` and must be ignored). The hub's design avoids it anyway: a Lock
   answers `pending` at once and the turn ends there (SP1, #154). If lea ever needs the old
   blocking behaviour, it can raise `CLAUDE_CODE_MCP_AUTO_BACKGROUND_MS` in
   `image/claude-settings.json` `env` above the console timeout.
