# Spike SP1: PostToolUse `continue:false` after an MCP tool call (#154)

Claude Code 2.1.296, in a lea agent container, 2026-10-29. No production code: this directory
holds the stub MCP server, the hooks and the driver that produced the results below.

```sh
tests/spikes/sp1-posttooluse/run.sh headless      # claude -p
tests/spikes/sp1-posttooluse/run.sh interactive   # TUI in a pty (trust the folder once first)
```

`stub.py` is an MCP server `stub` with one tool `wait` that answers
`{"status":"pending","ticket":42}`. A PostToolUse hook with matcher `mcp__stub__.*` prints
`{"continue":false,"stopReason":"waiting for approval of ticket 42"}`. The prompt is
"Call the mcp__stub__wait tool once, then reply with the single word DONE."

## Results

| Check | Headless (`claude -p`) | Interactive (TUI) |
|---|---|---|
| Turn ends right after the tool result | yes: `stop_reason:"tool_use"`, `result:""` | yes |
| No assistant text after the result, no "DONE" | yes | yes |
| `stopReason` shown | in the transcript only (`attachment.type:"hook_stopped_continuation"`) | yes: `PostToolUse:mcp__stub__wait hook stopped continuation: waiting for approval of ticket 42` |
| **Stop hook fires** | **no** | **no** |
| `Notification` `idle_prompt` | n/a | yes, about 60 s later |
| Other PostToolUse hooks of the same event still run | yes | yes: an unmatched hook's `additionalContext` coexists with `continue:false` |
| asyncRewake hook on the same PostToolUse (exit 2 after 25 s) | the process waits for it, then exits; no new turn | **wakes the agent**: the stderr text arrives as hook feedback, the model answers "APPROVED", then Stop fires normally |

Shape of `tool_response` in the PostToolUse input (it matters for the hook that reads `pending`):

- with `structuredContent`: a **string**, the JSON of `structuredContent` (even with an
  `outputSchema` and several text blocks): `"{\"status\":\"pending\",\"ticket\":42}"`;
- without `structuredContent`: the content blocks as an array,
  `[{"type":"text","text":"..."}, ...]`.

The input also carries `mcp_server.name` and `tool_use_id`; never a `structuredContent` field.

## Verdict: GO, with two corrections to the plan

1. `continue:false` from a matched PostToolUse hook ends the turn after the tool result, shows
   the reason and lets no assistant text through. It works alongside lea's own unmatched
   PostToolUse hook.
2. **No Stop fires after it.** lea sets `idle` on Stop (`internal/daemon/daemon.go` `hookState`),
   so lea would leave the agent `working` until `idle_prompt` comes about 60 s later, and lea's
   Rewake (an asyncRewake hook on Stop) is never started for that turn: an inbox message would
   not wake the agent. The suspend hook must therefore set the agent's state itself (the
   `hub_wait_register` call moves it to `suspended_warm`) and must not count on Stop.
3. The warm wake works without Stop: an asyncRewake hook on the same PostToolUse that exits 2
   wakes the agent with its stderr as the next prompt. It is the natural carrier of the
   keepalive: wait on the bus for the decision or an inbox message, exit 2 with the text,
   exit 0 at keepalive end (soft stop follows). Headless `-p` waits for that hook before it
   exits, so it is not usable there; lea agents are interactive.
4. The hook must read `pending` from `tool_response` as a JSON string (parse it) or a content
   array, not from `tool_response.structuredContent`.

No fallback (`decision:"block"`, text-only suspension) is needed.
