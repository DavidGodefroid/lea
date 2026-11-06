# Changelog

## v0.9.0 (2026-11-06)

Upgrading from v0.8.x:

- `lox up` is gone: lea no longer starts the hub or the locks. Approvals happen in `lox run`
  (lockhub v0.9.0), run in another terminal of its own; `lea doctor` needs lox 0.9.0
  (`brew upgrade lox`).
- `LEA_LOX_UP` and `LEA_SOUND_DONE` are removed: lea warns about `LEA_LOX_UP` and ignores it, and
  no longer uses `LEA_SOUND_DONE` (a turn end rings no sound). `LEA_LOCKSQL_IMAGE` is obsolete
  too (warned). Remove them from your configuration.

Changes:

- Approvals move out of lea into `lox run` (#300): lea no longer runs `lox init`, `lox up` or
  `lox attach`, and shows no hub panel, hub ticket, lock console or lock request. When the
  workspace has locks and no `lox run` serves them, lea's start, the deck's status line and
  `lea doctor` say to run it, and lea starts anyway (agents' requests wait). `lea lock`, the deck's
  `L`/`H`/`Y`/`O` keys, the agents' `lock_request` tool and `LEA_LOCKSQL_AGENT_PROFILES` are
  gone. This supersedes #291 (merged after v0.8.1, naming the start question a waiting lock
  console asks): lea attaches to no lock console any more.
- lea's image ships the Lock clients as one set, `config/locks.lock` (#134): lockkit v0.13.0, with
  locksql v0.17.0, lockmq v0.8.0, lockfile v0.8.0, lockhttp v0.8.0 and lox v0.9.0, each pinned
  by digest or checksum and verified by its cosign signature. The set is bumped as a whole.
  `lea doctor` checks each lock lox runs against lea's client of its type (from the image it pins,
  or the console's hello handshake) and says what to run (`lea update`, or `brew upgrade lox` then
  `lox add TYPE NAME --version`); a pair with different protocols fails. lea no longer builds a
  locksql image or updates Lock releases. `LEA_<LOCK>_REF` is kept for this release as a dev
  override that `lea doctor` warns about.
- Sounds ring only when lea needs you (#294): a question or request to you, a permission or an
  approval to give, an agent blocked on a human. A turn that simply ends no longer rings.
- Codex agents pass `LEA_AGENT` and `LEA_AGENT_TOKEN` to the Locks' MCP servers, so Lock grants,
  plans and tickets name the requesting agent (#253, #302).

## v0.8.1 (2026-11-04)

- `a`, `d` and `Y` work after a click on the hub ticket shown in the right pane (#289): a click
  (left or right) on the ticket pane keeps the keyboard on the deck, and ctrl+space no longer stops
  on it. The wheel still scrolls the ticket.
- The ticket pane keeps lea's own words (the name a production ticket awaits, a refusal, the
  approve-all report, the keys) in a banner above the ticket, each line marked `▌`; the ticket's own
  text can never draw such a line. An unknown key in the hub panel now says that `a` approves, `d`
  denies and `esc` leaves.

## v0.8.0 (2026-11-04)

- Hub tickets are shown whole in the large right pane (#284): selecting a ticket in the hub panel
  (`H`, the arrows or a click on its line) shows it where the agent's session shows, with its
  warnings, production, the agent's note, instance, requester, created and expires first and the
  Lock's screen under them; the wheel, `j`/`k` and the page keys scroll it. `esc` brings the agent
  back. There is no fold, no `enter` details and no `v` step any more.
- `a` approves the ticket shown and `d` denies it, at once, once the right pane shows that very
  ticket (a key pressed twice never answers the next one). A ticket lea had to cut is still
  refused. A hub refusal shows under the ticket with its fix (`lox up` for a hub older than
  lockhub 0.7.0, or `lox approve`).
- `Y` in the hub panel approves every pending ticket after one question naming how many and how
  many are production (`approve 3 tickets, 1 production? y/n`). Tickets that ask for a typed
  name, cut ones and duplicates are skipped and listed; refusals are listed with their reasons.
- At start, when a lock's console waits for its start question, lea attaches to it after the
  [Y/n] prompt and detaches by itself as soon as the lock serves, then goes on into the deck: no
  Ctrl-P Ctrl-Q any more (it still detaches sooner). Several waiting consoles are answered one
  after the other, each named "n of m" (#282).
- After `brew upgrade lea`, `lea` and `lea stop --all` stop the daemon of the previous version,
  even when Homebrew removed its directory (#283). A daemon that ignores SIGTERM for 5 s gets
  SIGKILL, and lea says so in one line.

## v0.7.1 (2026-11-04)

- The done sound rings when the agent's turn really ends (#274): it no longer rings 10 s or more
  after the deck shows the agent done, nor while the agent works again after a Stop hook blocked
  its stop. It waits for an agent-type Stop hook's verdict (60 s at most), and its quiet wait
  drops from 10 s to 3 s. The hook binary in a running container keeps the old behaviour until
  the agent restarts.

## v0.7.0 (2026-11-04)

- The deck asks for a name to type before approving a hub ticket only when the hub's queue item
  names one (its `retype`, lockhub 0.7.0); otherwise `a` approves at once, production or not
  (#260). A hub older than lockhub (lox) 0.7.0 sends no `retype`: approving a production ticket
  from the deck is then refused by the hub, and the refusal stays under the ticket's card until
  the selection moves (use `lox approve`, or upgrade lox).
- The hub panel is clickable (#256): a click on a ticket selects it, a click on the title enters
  the panel, and `enter` (or a click on the selected ticket) shows its details: who requested it,
  when it was created and when it expires. The panel's title names its keys. A ticket whose lock
  was removed reads as an orphan on approve, deny, reveal and renew, and any other hub refusal
  names its code and HTTP status.
- A ticket requested by an agent that runs its Lock through the shell (`locksql run`) now
  reaches that agent once decided (#261): lea delivers the decision to the agent the requester
  names, in its inbox, and wakes it when idle. The card still marks the requester as unverified.
- Renaming an agent or a folder, naming a new folder and editing a text setting open a modal over
  the deck instead of a field in the status line (#257).
- Agents inside a folder are indented under it (#258).
- At each start, lea checks the console of every lock its agents mount (#259): a console that is
  not running says to run `lox up`; one waiting for its start question says `lox attach NAME`,
  and at a terminal lea offers to attach to it now. `LEA_LOX_UP=1` also runs `lox up` when the
  hub runs but a lock's console does not.
- After `lea stop`, an agent whose Claude session never wrote a transcript (it sat on its login
  or trust dialog) starts afresh instead of failing with "No conversation found" (#255).
- A delivery grant covers `gh pr merge N --delete-branch` and `glab mr merge N
  --remove-source-branch` on the caller's own branch (#271). A merge, create or push that the
  caller's grant would cover in another form gets a hint naming the covered forms.

## v0.6.1 (2026-11-03)

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
