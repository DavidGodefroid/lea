# Spike SP2: resume a session with a prompt (#155)

Claude Code 2.1.296, in a lea agent container, 2026-10-29. No production code.

```sh
tests/spikes/sp2-resume/run.sh             # headless cases a and c
tests/spikes/sp2-resume/run.sh truncated   # adds b1, b2 (cuts transcripts: by hand only)
tests/spikes/sp2-resume/drive.py <dir> <seconds> <screen> claude --resume <id> --settings <f> "<prompt>"
```

`hook.sh` is a SessionStart hook that, on `source=resume`, tells the model a secret word in one of
three ways: `sync` (a plain hook printing `additionalContext`), `async-ctx` (an asyncRewake hook
printing `additionalContext` after 2 s, exit 0) and `async-wake` (an asyncRewake hook writing the
word on stderr after 2 s, exit 2). Every case resumes a fresh session (one `-p "Reply READY"`) so no
earlier answer leaks the word. Interactive runs are driven in a pty by `drive.py`, the way lea
starts an agent: `claude --resume <id> "<prompt>"`.

## Results

| Case | Headless (`claude -p --resume`) | Interactive (`claude --resume <id> "<prompt>"`) |
|---|---|---|
| a. resume + prompt | exit 0, `OK` | the prompt is submitted on resume, `OK` |
| b. last JSONL line cut | **not run** (see below) | not run |
| c. sync SessionStart `additionalContext` | exit 0, `PAPAYA`: seen by the first turn | `OK PAPAYA`: seen by the first turn |
| c. asyncRewake `additionalContext`, exit 0 | exit 0, `MANGO`: `-p` waits for the hook | **lost**: the first turn runs without it, even with a 2 s hook; nothing arrives later |
| c. asyncRewake stderr, exit 2 | exit 0, `NONE`: `-p` ends with no new turn | the first turn runs without it (`OK NONE`), then a **second turn** wakes with the text (`GUAVA`) |

The SessionStart hook sees `source=resume` in every resumed case.

**b (truncated transcript) was not run.** It edits a transcript under `~/.claude/projects`, and the
agent's permission classifier refuses that ("Session Transcript Tampering"). The cases are in
`run.sh truncated` for a person to run in a throwaway account. Until then, the plan's fail branch
holds: lea cuts a transcript back to its last valid JSON line before `--resume`.

## Verdict: GO for resume + prompt and for a sync HubResume; b stays open

1. `--resume <id> "<prompt>"` works headless and interactive: lea can resume a suspended agent with
   its decision as the prompt.
2. **HubResume must be a plain (sync) SessionStart hook, not asyncRewake.** An asyncRewake hook's
   `additionalContext` is lost in an interactive session, and its exit-2 text comes only after the
   first turn, which has already run without the decision. A sync hook holds the session start
   while it runs, so it reads the decision already in `hub_waits` and prints it; it never waits
   for one. lea's LimitResume stays asyncRewake: it waits, then wakes the agent, which is what
   exit 2 is for.
3. Simplest of all: put the decision in the resume prompt itself (case a), and keep HubResume for
   a restart that resumes without lea choosing the prompt (`ResumeStopped`).
4. Truncated transcript: unverified; lea repairs the JSONL (cut to the last valid line) before a
   resume, as the plan's fail branch says.
