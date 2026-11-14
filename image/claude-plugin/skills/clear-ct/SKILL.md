---
name: clear-ct
description: Use when the user types /lea:clear-ct, or asks to clear the control tower's context ("vide le contexte de la tour", "clear the tower", "reset the tower's context"). Saves what the board cannot give back to a private handoff file, then has the user type /clear; the cleared session comes back in tower mode, reads the file and resumes the board with every child still running.
---

# Clear the control tower

A control tower runs for hours and its context keeps growing. `/clear` empties it; lea keeps
tower mode on across it (the tower guard follows the new session) and the new session resumes
the board. This skill first saves what the board cannot give back.

## Not a tower

lea says in this prompt's context whether this session is a control tower. When it says it is
not one, reply with exactly this line and nothing else, call no tool, change nothing:

```
not a control tower session; use /clear
```

## Write the snapshot

lea gives the handoff file's path in this prompt's context (`clear-ct: the handoff file is …`): a
private file outside every repository, which it has just removed. Write it **once**, with the
`Write` tool and that exact path (the tower guard allows that one file), at most 40 lines:

```
# tower handoff · <your name> · <date>
## Ledger
<label> (<name>) · <state> · <task in 6 words> · <last news> · waits for <what>
## Open with the user
<the open question, the decisions they still owe, the queued questions, or "nothing">
## Given this session
<what the user approved or allowed (grants, merges, "always" answers), or "nothing">
## Notes
<anything else the next session needs: a queued task and what it waits for, a promise made>
```

- One line per agent of your ledger: your children and the other agents you follow. No message
  bodies, no tool output, no secrets.
- Only what `agents_list` and `board_read` cannot tell: they give every agent's state, task,
  next step and blocker back after the clear.
- Never append to an older snapshot: the file is new.

## Clear

Claude Code does not let a skill run `/clear`. Tell the user, in one line, in their language:

```
Snapshot saved. Type /clear: I come back in tower mode and resume the board.
```

Then stop: no other tool, no briefing.

After `/clear`, lea's SessionStart hook keeps the tower guard on (no gap: `Bash`, `Edit` and the
other tools stay denied) and tells the new session to load `lea:control-tower` and run its
**Start** step, resume branch, with this file. The next turn (the user's next message, or a
child's message that wakes the tower) resumes the board. A plain `/clear` without this skill
resumes the same way, from `agents_list` and `board_read` alone.
