# Changelog

## Unreleased

- lea initialises itself on its first run: it checks Docker, pulls and verifies its images,
  installs its AppArmor profile, records the projects directory and starts the Lock Family hub
  with `lox init`. The journey is `brew install davidgodefroid/tap/lockfamily`, `lea`, then
  `lox add` for each Lock. `lox` must be on the `PATH`, 0.5.0 or later (`config/host-minimums`).
- The projects directory of a first run is `LEA_PROJECTS_DIR` when it is preset, else the
  working directory.
- A lock tool (locksql, lockmq, lockfile) is on as soon as `lox up` runs a lock of its type, unless
  `LEA_<TOOL>=0` is set in the user's configuration.
- Several locks of one tool reach the agents, each on `/run/<tool>/<instance>`; a Codex agent gets
  one MCP server per lock, and lea tells a Claude agent to pass `--instance NAME`.
- A lock added while lea runs is seen by the daemon: the deck and the agents' status lines say to
  restart lea (`lea stop`, then `lea`).
