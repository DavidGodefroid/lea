# Changelog

## Unreleased

Features:

- `LEA_LOCKHTTP=1` works with the lockhttp launcher that `brew install
  davidgodefroid/tap/lockfamily` or lockfamily's `install.sh` installs (#411). lea used to accept
  only a root-owned `/usr/local/bin/lockhttp`. It now looks there first, then in `~/.local/bin`,
  then in brew's `bin`. A launcher that is not root-owned in `/usr/local/bin` can be rewritten by
  your account, so lea runs the image it pins only when that image is the one `config/locks.lock`
  pins for its release, or when `cosign verify` shows lockhttp's release workflow signed it at
  the launcher's version tag. If the check fails, lea does not start and `lea doctor` reports it.
  lea's messages now say to use `brew install davidgodefroid/tap/lockfamily` / `brew upgrade` and
  `lox run`, not lockhttp's `scripts/install.sh`.
- `lea web` (#414): the deck in your browser. It serves, on `127.0.0.1` only, a live board of
  the lea's agents (a tree of leads and their agents, with state, status, next step, blocker,
  branch, context use, cost, unread messages), each agent's messages with a box to write to it,
  rename, pause, hold, role and stop, a form to start an agent or a Control Tower, the tower's
  chat with its questions, and the permissions agents wait on, with Allow, Deny and Always. While
  a browser shows it, permission prompts wait for it as for the deck in tmux. The printed link
  signs one browser in once, within 2 minutes (a 32-byte token traded for an `HttpOnly`,
  `SameSite=Strict` cookie); `--open` hands the browser a private local page that posts the
  token, so it is never in a command line (#436); foreign `Host` or `Origin` headers are refused, and only a fixed list of the daemon's
  methods, for this lea's agents, is reachable. Phone width, light and dark, keyboard shortcuts.

Changes:

- lea no longer copies on select (#419): a drag in an agent's terminal no longer enters tmux's
  copy mode, copies the selection or flashes "Copied". Claude Code copies on select itself, through
  the same clipboard bridge: a copy worked and looked one way or the other depending on whether
  Claude held the mouse. In a pane
  whose program does not take the mouse (a split shell, the scrolled-back history), `shift`+drag
  lets your terminal select, as in any tmux. `ctrl+v` paste and wheel scrolling are unchanged.

Fixes:

- A granted `git commit` runs at once (#393): the pinned commit no longer runs `rm -rf` in its
  `sh -c` script (Claude Code's safety check asked for approval, hook allow or not, and the commit
  timed out). It copies the index to `lea-commit/index` in the worktree's git directory, reused
  and overwritten at each commit, beside `lea-push` (#381); nothing is left in `/tmp`.

## v0.15.1 (2026-11-09)

Fixes:

- The control tower replies in the language of your latest message (#396): it switches as soon as
  you do, its agents' messages (mostly in English) never change it, and a wake-up keeps the last
  language you used. In tower mode lea reminds it on every prompt; nothing changes outside it.

- `LEA_LOCKHTTP=1 lea` no longer starts lea without the gate in silence (#398): the
  configuration (lea's default `LEA_LOCKHTTP=0`, or yours) still wins over the environment,
  since lea-proxy and its gate are shared by every lea on the machine, but lea now warns
  `LEA_LOCKHTTP=1 in the environment is ignored (the configuration says LEA_LOCKHTTP=0): set it
  in ~/.config/lea/lea.conf, then lea stop --all, and lea`; the same for `LEA_LOCKHTTP_MITM`.
  `lea doctor` prints a `lockhttp` line in every case: `off`, with how to turn it on, or `on`
  with the gate's state.
- `lea update` on a Homebrew install runs `brew update` and `brew upgrade davidgodefroid/tap/lea`,
  then the new Cellar version refreshes its images and checks the dependencies (#354). It used to
  run lea's own installer, which left a second lea in `~/.local/share/lea-install`, linked at
  `~/.local/bin/lea`, beside Homebrew's. A failing brew fails `lea update` with brew's status;
  without brew on `PATH`, lea refuses to update a Homebrew install itself. The background update
  installs nothing on a Homebrew install, even with `LEA_AUTOUPDATE=1`: it only announces the
  release. `lea init` no longer links `~/.local/bin/lea` to the Cellar version on a Homebrew
  install (brew links `<prefix>/bin/lea`; that link outlived `brew upgrade`). install.sh and git
  installs are unchanged.
- Docker out of address pools is reported as such (#397): when Docker refuses lea's network with
  `all predefined address pools have been fully subnetted`, lea quotes Docker's message and says to
  remove unused networks (`docker network prune`) or add `default-address-pools` in
  `/etc/docker/daemon.json`, instead of asking for Docker Engine 28.0 or `LEA_ALLOW_GATEWAY=1`. The
  gateway-mode advice comes only when Docker rejects `gateway_mode_ipv4` itself, with Docker's
  message; any other refusal is Docker's own message. `lea doctor` counts Docker's free address
  pools and warns when none is left.

## v0.15.0 (2026-11-08)

Changes:

- Pause the tower's inbox while you answer it (#95): `H` in the deck (or Hold messages in a
  row's menu) or `lea hold [--off] [AGENT]` holds the messages agents send it; your notes and
  the replies to what it sends during the pause still arrive, an agent's `ask` to it fails at
  once, and the row shows `⏸ N`. On release (yours, or lea's after `LEA_HOLD_MAX` minutes, 30;
  0: never) the tower gets them all and briefs you once. Only the user pauses or releases an
  inbox. `Q` in the deck (Questions… in the tower's menu) lists its questions to you and drops a
  queued one; the tower's status line shows `❓ 1 · 3 queued` and `⏸ messages paused (N)`.
- Security: the text of the tower's next question, and of a question's reminder, reaches its
  context framed as untrusted data, as `msg_inbox` frames an agent's message, with its control,
  bidi and zero-width characters replaced (#95): an agent's words in a queued question could
  pass for lea's instructions.
- One handle per agent, `label (name)` (#356, #368): the deck row, the tower, `lea board` and
  `lea agents` name an agent by its label then its name. A lead gives a spawn a `label`
  (`agents_spawn`, `agents_request_spawn`); without one, an agent started with a brief is named
  after its brief, not after lea's first prompt. Labels are unique among the running agents of a
  container, whatever their case: an automatic label that clashes gets ` 2`, ` 3`…, and the user's
  rename to a running agent's label is refused. An agent's label, or the namer's from an agent's
  brief or prompt, is plain ASCII (letters, digits, spaces, `- _ . #`) and never reads as `user`,
  `the user`, `lea`, `tower` (alone or numbered: `Tower 2`, `user #1`) or holds `Control
  Tower`, `Tower Control` or `Ctrl Tower` (`The Control Tower`, `CT`), whatever its case,
  punctuation or look-alike digits (`C0ntrol 7ower`): those are the user's and lea's.
- `lea config claude` (#334): your base settings for Claude Code in lea, without editing a file or
  rebuilding the image: `defaultMode`, entries to add to or remove from `allow`, `ask`, `deny` and
  `additionalDirectories` (lea's built-in rules included), `env`, `model`, `language`,
  `outputStyle`, `alwaysThinkingEnabled`; `lea config claude` alone shows every effective setting
  with its source. Kept in `claude.json` next to `lea.conf`, never in `~/.claude/settings.json`;
  they outrank the host's `permissions` and apply to the containers started from then on. lea's
  hooks, `allowManagedPermissionRulesOnly`, `disableBypassPermissionsMode`, the status line and
  lea's own variables are refused with the reason. Host only; `lea doctor` lists the overrides,
  `lea config export` never carries them.
- The deck's first agent is a Control Tower (#339): `lea deck-open` with no agent running starts a
  lead on Claude labelled `Control Tower`, whose first prompt runs `/lea:control-tower`; `N` starts
  another (`Control Tower 2`…), and the provider picker moves from `N` to `P`. Host only.
- The agent containers no longer rely on Docker's default resolver (#98): lea starts them with
  `--dns 192.0.2.1` (TEST-NET-1, never routed, out of reach of the internal network), so a lookup of an external name, from an agent or a nested container, is
  never forwarded to the host's resolver (lea-proxy, which resolves names for the agents, is still
  answered). `lea doctor` fails on a running container that resolves an external name, and warns
  about one started without it (`lea stop`, then `lea`). `make test-isolation` checks that no lookup, direct
  query to a public resolver or long TXT name reaches a decoy DNS server, on the decoy network or on the host's 127.0.0.1:53.
- The control tower asks you one question at a time (#95): its new `user_ask` tool (leads only)
  opens a question when none is open and queues it otherwise, by priority (`incident`,
  `blocking`, `normal`) then in order; the queue is kept in lea.db. Your next prompt, or your
  answer to the tower's `AskUserQuestion`, answers the open question: lea tells the tower which
  agent the answer goes back to and gives it the next question. While you have a question, lea denies the tower any other
  `AskUserQuestion` and holds the questions agents `ask` it until you answer. One left unanswered `LEA_QUESTION_REMIND`
  minutes (120; 0: never) is asked again once, prefixed with `reminder:`. The deck shows the
  tower as `❓ 1 · 3 queued`; `lea questions` lists the questions and `lea questions drop ID`
  drops a queued one.
- A `gh pr create --body-file` or `gh release create --notes-file` refused for its file now says
  why (#384): the hint names the file (cut past 80 characters) and the rule the matcher applies
  (a relative path inside the worktree of at most 256 characters from `A-Za-z0-9._/-`, not
  starting with `.`, `-` or `/`, with no empty, `.` or `..` segment, outside `.git`) with an
  example, `pr-body.md`.
- Deck colour themes (#337): `LEA_THEME` picks `catppuccin` (the default), `gruvbox`, `nord`,
  `tokyo-night`, `solarized`, `high-contrast` or `mono`; `T` in the deck (or its right-click menu)
  opens a picker that previews each theme live (`enter` keeps and saves it, `esc` restores), the
  settings screen's Theme row applies at once, and the tmux pane borders follow. `NO_COLOR` gives
  `mono` while `LEA_THEME` is not set; an unknown name gives `catppuccin` with a warning.
- The agents show your own Claude Code status line (#374): when your `~/.claude/settings.json`
  has a `statusLine` command, `lea statusline` still reports the agent's context and usage to the
  daemon, then runs your command with the same JSON (1.5 s at most, killed past it) and prints its
  output; lea's own line comes back when it fails, prints nothing or times out. A script below
  `~/.claude` the command names is mounted read-only into the containers (and lea-ctl).
- `release-create` grant action (#353): `gh release create <tag> --verify-tag [--title T]
  [--notes-file F | --generate-notes] [--prerelease] [--latest]`, one plain command at the root of
  the agent's worktree, pinned like every allowed gh call, for a pushed semver tag matching the
  grant's `--tag` glob (`lea allow release --repo R --tag 'v0.15.*'`, or `grants_request`
  with `tag`). `--draft`, `--target`, assets and the other `gh release` subcommands stay
  uncovered; `deliver` never includes it. A call the grant does not cover tells the agent
  the form, the glob and `grants_request`.
- In the deck, a folder header's summary sets each state mark apart from its count (`6 ◐ 6`, not
  `6 ◐6`) (#355): the marks are East Asian Ambiguous width, and a terminal that draws them wide
  drew the half-moon over the digit.
- A granted `git push -u origin <branch>` from a worktree branch created from `origin/main` goes
  through at the first try (#381): the pinned push no longer runs `rm` in its `sh -c` script
  (Claude Code refused it, hook allow or not), it runs from a bare repository reused in the
  worktree's git directory; once pushed, it records `origin/<branch>` and `-u` makes it the
  branch's upstream (it did nothing, so the branch kept tracking `main`). The agent prompt says to
  create the worktree with `git worktree add --no-track`.
- The first run and upgrades ask the deck question (#335): the first `lea` on a terminal (the
  Homebrew journey, `lea init --first-run`) asks "Use the lea deck ...? [Y/n]" and, on no, the
  command to start in the lea container, before it opens anything. An install whose user config
  has no `LEA_DECK` (made before v0.10.0) gets the same question once, at its next `lea` on a
  terminal. The answer is recorded (the end of input records the deck) with the command that
  switches it; no terminal, or `LEA_DECK` already set (environment or user config): no question,
  the deck as before.
- `lea allow` (#319): one allowance model and one command for what `lea grant` and `lea delivery`
  did. `lea allow deliver` (push, pr, merge, delete-branch), `lea allow merge issues --repo api`,
  `lea allow push --branch 'feat/*' --agent dev-1 --for 2h`, `lea allow stop-finished`: one call
  records one set of grants, shown as one line by `lea allow` (capabilities, repositories and pins,
  time left, last use) and revoked as one unit by `lea allow off N` (or a capability, `deliver`, or
  everything of the lea). `lea allow log` lists this lea's uses. The container is always the
  projects directory's lea, and every forge capability is pinned to each repository's `origin`
  (GitHub or GitLab, `--gitlab HOST` remembered), with the pin confirmation `lea delivery on` had:
  on a terminal, `record these? [y/N]`; off one, `--yes DIGEST`. Recorded again, an allowance that
  holds until revoked keeps the ids of its unchanged rows.
- **Lifetime**: an allowance holds until revoked unless `--for DURATION` (1m to 30 days) says
  otherwise, hand grants included (they used to expire after 8 h).
- The deck has an Allowed screen (`g`): the lea's allowances, with `x` to revoke one after the
  usual confirmation.
- An agent's first prompt lists the forms lea runs without asking only for what covers it when it
  starts; otherwise it says `lea allow deliver --repo <repo>` lets it push and open PRs without
  asking. A merge, PR or push no allowance covers gets a hint naming `lea allow <capability> --repo
  <repo>`.
- `lea doctor` reports `allowances` (the pin and credential checks of delivery).
- Deprecated: `lea delivery on|off|status` and `lea grant add|ls` are aliases of `lea allow` for this
  minor release, with a notice; they go in the next one. `lea grant add --github X` is refused when
  X is not the repository's origin. The daemon methods `grant_add`, `grant_list` and `delivery_set`
  are replaced by `allow_set`, `allow_list` and `allow_off` (host only).
- `lea.db` gains `grant_sets` and `grants.set_id`: on open, a lea's standing grants become one
  `deliver` allowance and every other active grant an allowance of one; ids do not change, and an
  older lea still reads the table.
- Agents point the user to `lox run`, never `lox up` or `lox approve` (#343): a new `lock_hint`
  tool (read-only, agents only) tells an agent whose Lock client finds no console what the user
  must do, from lea's state of the lock rather than a guess: "lock NAME waits for you: answer in
  lox run (or lox attach NAME)" while `lox run` runs it, "start lox run in the projects directory"
  when none serves, "restart lea" for a lock added while lea runs; never a command that restarts
  or recreates a lock. The first prompt sends agents to it, and the agents' status line says
  "no lox run: start it in the projects directory" while none serves. The `LEA_LOX_UP` warning
  and LockHTTP's refusal page no longer mention `lox up` or `lox approve`.
- The "Copied" notice a mouse selection's release shows closes after 0.6 s instead of 1.5 s
  (#373).
- A granted `gh pr create` or `gh release create` whose `--title` reads like the file flag (`-F`,
  `--body-file`, `--notes-file`) hands gh the body or notes file the parser approved (#382): the
  pinned command took the file's index from a second scan of the args, which took the title's
  value for the flag and swapped the wrong argument for `-`.
- A lead's child spawned without `repo` starts where its lead works (#364): the lead's repository,
  or without one the lead's own directory, not the directory `lea` was last launched from. An
  explicit `repo` still wins; `agents_spawn` with `for` keeps the request's repo and branch.

## v0.14.1 (2026-11-08)

Changes:

- A spawn never gets the name of an agent that still runs (#350, #358): a row marked done while its
  session lived (a nested Claude's SessionEnd, or a missed reconcile) could be reused in place, and
  the live session received the new brief, repository and branch. A name now comes free only once
  its row is removed and no running process answers to it; hooks from another Claude session than
  the agent's own are ignored; reconcile marks an agent done only after a 3 s grace check.
- In the deck, an agent that gets back the name of a done or forgotten agent leaves that agent's
  folder and joins its own lead's folder, or stays at the root (#340, #357).
- Removed: rtk is no longer installed in the agent image (#359, #360). Nothing in lea called it, and
  its release download was an extra fetch on every image build.

## v0.14.0 (2026-11-08)

Changes:

- `lea egress allow|deny|list|denied` (#345): the user allows a domain from the host, live. The
  proxy's allowlist moves into a file on its tmpfs; `lea egress allow DOMAIN` checks the domain as
  `LEA_EGRESS_ALLOW` is checked (no URL, IP literal, wildcard or DNS-over-HTTPS resolver; at most
  64 entries), writes it back to `lea.conf` (`default` kept, private, no symbolic link followed)
  and reconfigures squid in the running proxy: no proxy, container or agent restart. `deny` takes a
  domain back, `list` shows the list, `denied` the hosts the proxy refused lately (the access log
  now carries squid's error name). An edited `LEA_EGRESS_ALLOW` is applied the same way at the next
  `lea` instead of recreating the proxy. The command is host-only (refused inside lea, kept on the
  host with `LEA_CTL=1`); no MCP tool or grant reaches it.
- Refusal pages start with one plain line naming the host, why, and, for the allowlist, the exact
  `lea egress allow` command; an agent's first prompt says how to recognise an egress refusal behind
  `CONNECT tunnel failed, response 403` and to hand the user that command instead of working
  around the proxy.
- The deck's width no longer changes when a drag crosses the divider (#341, #347): selecting text
  in the deck, or in a program that takes the mouse, then moving over the divider resized the deck
  and stored the new width. Only a press on the divider itself starts a resize now.
- Granted `gh` calls work again (#348, #349): lea's read-only forge-gh config now carries gh's
  current `version: "1"`, so gh no longer migrates it and tries to write it back, which made every
  granted call fail with `failed to write config after migration`.

## v0.13.0 (2026-11-07)

Changes:

- Grant requests (#342, #344): a lead turns the user's approval given in its chat into a lea grant
  its children honour. The new `grants_request` tool (leads only, checked like `lea grant add`)
  queues a request and records nothing; the user decides on the host with `lea requests`,
  `lea approve gN [gM...] [--expires 30d]`, `lea deny gN [reason]` or `lea grant
  requests|approve|deny`. An approved grant covers the lead and its children only. The lead is
  told on approval, denial and expiry (a day without an answer). A merge no grant covers now tells
  the agent to ask its lead for one. The control tower may call `grants_list` and `grants_request`.
- `release-tag` grant action (#342, #344): `git push origin refs/tags/vX.Y.Z`, one new semver tag
  on a commit of the remote's default branch, pushed by lea's pinned command without force. Every
  other tag push stays a hard deny; `lea delivery on` never records it.

## v0.12.0 (2026-11-07)

Changes:

- lea's agents start in the permission mode a local Claude Code starts in (#333, #336): the managed
  settings no longer pin `defaultMode` to `default` (manual), so Claude Code's own default applies
  (auto on the accounts that have it). A valid `permissions.defaultMode` in the host's
  `~/.claude/settings.json` is still the agents'; bypass stays disabled. An agent that writes a
  `defaultMode` into its own settings can change its own mode, never to bypass.

## v0.11.0 (2026-11-07)

Changes:

- locklog (#324, #330): `LEA_LOCKLOG=1` (or a locklog lock under `lox run`) lets lea's agents read
  logs through a locklog console. Records are masked, and an unmask is approved at `lox run`. Its
  client is in lea's image (`config/locks.lock`, now the lockkit v0.13.1 set with locklog v0.2.1
  and lox v0.10.0), Codex agents get it as an MCP server, and `lea locklog`, `lea init --locklog`
  and `lea doctor` know it.

## v0.10.0 (2026-11-06)

Upgrading from v0.9.x:

- lea refuses to start an agent container on an internal network without
  `gateway_mode_ipv4=isolated` (#307): Docker Engine older than 28.0, Podman, or a network left by
  an older lea. Upgrade Docker or remove the old network; `LEA_ALLOW_GATEWAY=1` opts in to the
  weaker network. `lea doctor` prints each internal network's gateway mode and fails on a
  non-isolated one.
- The GitHub token now lives in the proxy only (#304): agents use it through `gh` and `git` but
  cannot read it. A token found in an agent's state is adopted once, when the proxy has none, and
  is otherwise discarded; `lea github login` sets it.
- The agent image has no `sudo` and no setuid/setgid file any more (#100, #305).
- The deck is optional now (#323): `lea config deck off` runs your own tool (Claude Code by
  default, `lea config tool CMD`) in the lea container instead, `lea config deck on` brings it
  back. Since #335, an install without `LEA_DECK` is asked once at its next `lea` on a terminal.

Changes:

- Optional deck (#323): `LEA_DECK=0` (`lea init`, `install.sh --no-deck [--tool CMD]`,
  `lea config deck on|off`, `lea config tool`) runs your own tool in the lea container.
  `lea attach [--print] [AGENT]`; `lea requests`, `lea approve` and `lea deny` answer lea's
  permission and spawn requests, with desktop notifications without the deck; `lea board --watch`.
- Plugins and permissions work inside lea (#111, #311): each projects directory's container has its
  own writable copy of `~/.claude/settings.json` and `~/.claude/plugins`, kept across restarts and
  never written back to the host, so `/plugin install`, marketplaces and `/permissions` work. lea's
  hooks are now in Claude Code's managed settings, so an agent cannot remove them, and
  `lea doctor` checks them.
- `LEA_RUNTIME` runs the agent containers on gVisor (`runsc`) or Kata Containers (`kata`), a second
  kernel between the agents and the host; `lea doctor` shows each container's runtime (#106, #309).
- Limits (#132, #303): lea restarts an idle lea container to apply limits it could not apply in
  place; rootless Podman without the cpu, memory or pids controller starts without that limit
  (with a warning) instead of failing; `lea doctor` reports stale limits and cgroup delegation.
  Fixed: limits were never applied to running containers on Docker 29 (`PidsLimit` printed as
  `<no value>`).
- The deck's width is resizable (`<`/`>`, a border drag, `LEA_DECK_WIDTH`), persisted across
  restarts and clamped to 24..min(60, half the window) (#275, #318).
- Deck folders are calm section headers (#295, #322): each shows its agent count and a status
  summary (needs you, working, limited, done); folder agents lose the vertical guide, and a blank
  line separates the folders and the root agents after them.
- An idle agent is woken for a message in its inbox after a lea daemon restart, after its session
  is resumed, and after more than an hour idle (#297, #317). Wakes are held while the agent is
  paused or limited, are rate-limited per agent, and show as "woken by lea" in the deck.
- Security (#100, #305): lea's seccomp profile is Docker's default (pinned moby/profiles) plus a
  reviewed, justified list of the syscalls the nested Podman needs; keyctl, reboot, syslog,
  fanotify, the new mount API's fsopen family and others are refused again. The AppArmor profile
  restores docker-default's `/proc` write denies.
- Security (#102, #308): lea-proxy runs confined: all capabilities dropped except SETUID/SETGID,
  no-new-privileges, a read-only root, pids and memory limits; squid, the TCP forwarders and
  lea-keyproxy no longer run as root.
- `lea doctor` and the README list the credentials an agent can still read (#103, #304).
- Bash guard: a jq filter such as `.[]` is no longer denied as a glob on a protected file; glob
  brackets follow bash's rules (still failing closed), and redirect targets are checked as globs
  (#273, #314).
- Deck: an agent spawned by a lead in a folder always joins that folder, even when a layout save
  happens in the same second (#296, #316).
- Drag-selecting in a pane and holding the pointer at its top or bottom edge (or past it) keeps
  scrolling the history, faster the longer it is held (#280, #321).
- install.sh creates its install directories 0755 under any umask; an existing group-writable one
  gets a warning with the fix (lockfamily#15, #313).
- Tests: the daemon's keepalive, subagent TTL and stack idle timers are driven by the test instead
  of racing the wall clock (#278, #315).

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
