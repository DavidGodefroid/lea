# Changelog

## Unreleased

- The image ships locksql 0.16.0. It brings grants: with `locksql_grant` (CLI `locksql grant`),
  an agent asks once for a bounded read scope (tables, duration, budget) and, once you approve
  it, runs its covered masked reads without a ticket each. Grants need lockhub (lox) 0.6.0 or
  later; an older hub refuses the grant's ticket and everything else works as before. lea
  already gives each agent the `LEA_AGENT_TOKEN` a grant needs.
- It also brings locksql 0.15.0's TLS change: transport security is left to the database driver,
  and a remote profile without `tls` now connects with `prefer` instead of `verify-full` (listed
  once on the first start after the upgrade). Set `tls = "verify-full"` in a profile to keep
  verifying the server's certificate.

## v0.6.0 (2026-11-03)

- `LEA_LOX_UP=1` in `~/.config/lea/lea.conf` (default `0`, #243): when `lea` starts and the
  projects directory has `.lockfamily/lockfamily.yaml` but no running hub, lea runs `lox up` there
  first, then lists each console waiting for its start questions with its `lox attach NAME` line.
  A failing `lox up` is a warning; lea starts anyway.
- `lea init --locksql` with a locksql lock declared in `.lockfamily/lockfamily.yaml` (found by
  its type) builds no console image when the lock pins its own, skips the profile wizard and says
  the one step left: `lea stop`, then `lea`. It warns when the lock's `allowed_uids` lacks your uid.
- `lea init --<tool>` or `--no-<tool>` configures the tools it names only: the other lock tools
  keep their setting and are asked about on a bare `lea init` alone.
- `/lea:tower-control` removed, use `/lea:control-tower`.
- Deck folders are simpler (#241): a click on a folder folds or unfolds it, `h` on an agent folds
  its folder, `u` takes an agent out of its folder (also Remove from folder in its menu), `x` on
  a folder asks before deleting it, and the status line shows the keys of the selected folder.
- A folder can take a colour (`c`, or Colour… in its menu), saved by name in the deck's layout.
- An agent a lead starts joins the lead's folder.
- The deck shows every pending ticket of the hub, attributed to an agent or not (#244): a
  `⚠ hub · N pending` panel above the rows, one line per ticket with its lock, instance, risk,
  the agent it waits for (or `no agent`), its age and time left. `H` moves the selection into
  the panel, where `a`, `d`, `v`, `o` and `r` act on the selected ticket as on a card. A ticket
  for no agent rings the attention sound.
- A stray `p` typed on the deck no longer freezes an agent (#240): `p` on a running agent asks
  first (`y` pauses it, any other key cancels), `p` on a paused one resumes it at once, and
  `enter` on a paused agent resumes it before focusing it. The row menu's Pause / Resume stay
  one click.

## v0.5.0 (2026-11-01)

- The image ships locksql 0.14.0, lockmq 0.7.0 and lockfile 0.7.0, which reach one lock among
  several with `--instance`.
- lea initialises itself on its first run: it checks Docker, pulls and verifies its images,
  installs its AppArmor profile, records the projects directory and starts the Lock Family hub
  with `lox init`. The journey is `brew install davidgodefroid/tap/lockfamily`, `lea`, then
  `lox add` for each Lock. `lox` must be on the `PATH`, 0.5.0 or later (`config/host-minimums`).
- An image lea cannot pull stops it with the command to retry; lea no longer builds an unverified
  `lea:dev` in its place and records it.
- The projects directory of a first run is `LEA_PROJECTS_DIR` when it is preset, else the
  working directory.
- A lock tool (locksql, lockmq, lockfile) is on as soon as `lox up` runs a lock of its type, unless
  `LEA_<TOOL>=0` is set in the user's configuration.
- Several locks of one tool reach the agents, each on `/run/<tool>/<instance>`; a Codex agent gets
  one MCP server per lock, and lea tells a Claude agent to pass `--instance NAME`.
- A lock added while lea runs is seen by the daemon: the deck and the agents' status lines say to
  restart lea (`lea stop`, then `lea`).
