# Spike SP2: resume a session with a prompt (#155)

Claude Code 2.1.296, in a lea agent container, 2026-10-29 (b: 2.1.314, 2026-10-30). No production code.

```sh
tests/spikes/sp2-resume/run.sh             # headless cases a and c
tests/spikes/sp2-resume/run.sh truncated   # adds b1, b2 (cuts transcripts: by hand only)
LEA_E2E_CLAUDE=$(command -v claude) go test -run TestClaudeResumesATruncatedTranscript ./internal/daemon  # b, isolated
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
| b. last JSONL line cut (2.1.314, see below) | exit 0, the history and the new prompt reach the model; a second resume after Claude appended to the cut file too | the prompt is submitted with the history |
| c. sync SessionStart `additionalContext` | exit 0, `PAPAYA`: seen by the first turn | `OK PAPAYA`: seen by the first turn |
| c. asyncRewake `additionalContext`, exit 0 | exit 0, `MANGO`: `-p` waits for the hook | **lost**: the first turn runs without it, even with a 2 s hook; nothing arrives later |
| c. asyncRewake stderr, exit 2 | exit 0, `NONE`: `-p` ends with no new turn | the first turn runs without it (`OK NONE`), then a **second turn** wakes with the text (`GUAVA`) |

The SessionStart hook sees `source=resume` in every resumed case.

**b (truncated transcript)**, run on 2026-10-30 with Claude Code 2.1.314, as the opt-in e2e
`TestClaudeResumesATruncatedTranscript` and `...Interactively` (internal/daemon/e2e_truncated_test.go,
`LEA_E2E_CLAUDE=<claude>`): the real Claude, on lea's fake Messages API, in a throwaway
`CLAUDE_CONFIG_DIR` (so no real transcript is touched, which the agent classifier refuses). b1: the
last line of the file (a `cost-state` record) cut in half; b2: the last `assistant` message cut in
half, nothing after it. Both resume with exit 0, the first prompt and the new one in the request;
Claude then appends to the cut file, and a second resume still carries both earlier prompts. The
interactive resume (lea's `claude --resume <id> "<prompt>"`) of b1 submits the prompt with the
history. So lea needs no repair of the JSONL; the e2e stays as the guard against a Claude Code
release that changes it.

## Verdict: GO for resume + prompt, for a sync HubResume and for a truncated transcript

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
4. Truncated transcript: Claude Code skips the cut line and resumes; lea does not repair the JSONL
   (the plan's fail branch is not needed). `TestClaudeResumesATruncatedTranscript*` re-checks it.
