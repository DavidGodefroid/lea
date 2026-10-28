---
name: check
description: Use when the user asks for a progress status, a "point", a "topo", or types /check ("où en est-on", "fais le point", "status", "/lea:check"). Answers with a very short status in a fixed shape - done since the last check, in progress, left, blocked or to decide.
---

# Check

The user wants to know where the work stands, at a glance. Give a status that is **very clear
and very short**, then stop. Write it in the user's language: translate the section labels, keep
the emojis.

## Gather

Read only; a check changes nothing and starts no work.

- **A lead or a control tower** (your row in `agents_list` is a `lead`): build the status from the
  team, never by doing work yourself. Call `msg_inbox` (fresh reports), `board_read` (every agent's
  status, next step and blocker) and `agents_list` (who runs, who is idle or stopped), and use
  your ledger if you keep one. Do not ask the agents for a status just for a check: their rows
  and their reports are enough. These tools are all allowed in control-tower mode.
- **Any other agent**: build it from your own task: your row in `board_read` (status, next step,
  blocker), your conversation, and what your work proves (commits, test runs, PRs). Then refresh
  your row with `task_update` if it is out of date.

## The last check

Your previous `/check` answer in this conversation is the last check. Compare with it:

- ✅ lists only what finished **since the last check**; on the first check, what is finished so far.
- If nothing changed since the last check, answer in a **single line** (for example "Nothing new
  since the last check: <what is in progress>") and nothing else.

## Output

Exactly this structure, in this order:

```
✅ Done
<one line per item: what finished since the last check>
🔄 In progress
<one line per item: what is being worked on now>
⏳ Left
<one line per item: what remains to do>
⚠️ Blocked / to decide
<one line per item: only if there is something>
```

- At most **10 lines** in all, section labels included.
- One line per item, at most **12 words** per line.
- `⚠️ Blocked / to decide` only when something is blocked or a decision is the user's; otherwise
  omit the section. An empty ✅, 🔄 or ⏳ section is omitted too.
- No prose, no intro, no conclusion, no tool narration.
- No technical detail (file names, commands, stack traces) unless it is needed to understand a
  blocker.
- Report what is proven (a merged PR, a passing test run), not what is hoped.
- For a team, name the agent or the task on each line: `dev-2: login fix, PR #42 open`.
