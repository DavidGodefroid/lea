<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/logo.svg">
    <img src="assets/logo-light.svg" alt="lea" width="280">
  </picture>
</p>

<h3 align="center">Many Claude Code agents. None of your machine.</h3>

<p align="center"><a href="https://leadeck.dev"><strong>leadeck.dev</strong></a></p>

<p align="center">
  <a href="https://github.com/DavidGodefroid/lea/releases/latest"><img alt="release" src="https://img.shields.io/github/v/release/DavidGodefroid/lea?color=89b4fa&labelColor=1e1e2e"></a>
  <a href="https://github.com/DavidGodefroid/lea/actions/workflows/ci.yml"><img alt="ci" src="https://github.com/DavidGodefroid/lea/actions/workflows/ci.yml/badge.svg"></a>
  <a href="LICENSE"><img alt="license" src="https://img.shields.io/badge/license-Apache_2.0-a6e3a1?labelColor=1e1e2e"></a>
  <img alt="platforms" src="https://img.shields.io/badge/Linux%20%C2%B7%20macOS%20%C2%B7%20WSL2-cdd6f4?labelColor=1e1e2e">
</p>

<p align="center"><img src="assets/demo.svg" alt="Three Claude Code agents work in the lea container. One pushes to github.com through the proxy; its attempts to read ~/.ssh and to reach 192.168.1.10 are blocked. Another asks for a permission, answered from the deck." width="900"></p>

lea starts Claude Code in a container that sees one directory of your projects (read/write), a
read-only allow-list of your `~/.claude` configuration, and nothing else of your machine. The
agents get their own container engine (so `make test` and `docker run` work), reach the
internet only through an egress proxy that refuses your private network, and never see your SSH
keys or credentials.

You drive them from **the deck**: a terminal with your agents in a column on the left and the
selected agent's terminal on the right.

```
╭─ ●●● lea ──────── 3 agents ─╮│
│› ⠹ fix login timeout  42%   ││  ✻ Claude Code
│    shop · fix/login · 2m    ││
│  ● review cart api    18%   ││  > the timeout is set in session.go…
│    reviewer · shop · now    ││
│  ● add dark mode       7%   ││
│    shop · needs you         ││
╰─ n new · m msg · ? keys ────╯│
```

## Why lea

Claude Code does more when you stop approving every command, and a few agents in parallel do more
than one. Both are risky on your workstation: an agent runs as you, with your keys, your tokens
and your network. lea moves the boundary from the prompt to the container.

- **Isolated by construction.** What an agent can reach is fixed by the container and the proxy,
  not by its good behaviour. A prompt-injected agent with every permission granted still cannot
  read `~/.ssh` or call your intranet.
- **Many agents, one deck.** States, context use, branches and each agent's own task report at a
  glance; one key to reach the next agent that needs you.
- **Your Claude Code, unchanged.** Your settings, skills, plugins, hooks and agents, read-only.
  lea runs the real Claude Code with your subscription or API key.
- **Local and open.** No account, no server, no forge lock-in. Apache 2.0.

```mermaid
flowchart LR
  subgraph host["Your machine"]
    keys["~/.ssh, ~/.aws, ~/.kube<br/>Docker socket"]
    lan["Private network<br/>192.168.0.0/16, 10.0.0.0/8 …"]
    proj["~/projects"]
    cfg["~/.claude (allow-list)"]
    subgraph lea["lea container"]
      a1["agent: lead"]
      a2["agent: dev"]
      a3["agent: reviewer"]
      podman["rootless Podman"]
    end
    proxy["egress proxy"]
  end
  net["Internet: GitHub, npm, Claude API …"]
  proj <-->|read/write| lea
  cfg -->|read-only| lea
  lea --> proxy -->|HTTPS, pinned SSH| net
  proxy -.-x|refused| lan
  keys -.-x|not mounted| lea
```

**Contents:** [Quick start](#quick-start) · [The deck](#the-deck) · [Git and forges](#git-and-forges) ·
[Databases](#databases-with-locksql) · [Queues and files](#queues-and-files-with-lockmq-and-lockfile) · [Web requests](#web-requests-with-lockhttp) · [Teams of agents](#teams-of-agents) · [Commands](#commands) ·
[Configure](#configure) · [Safety](#how-lea-keeps-your-machine-safe) · [FAQ](#faq) ·
[Troubleshooting](#troubleshooting) · [Development](#development)

## Quick start

```sh
curl -fsSL https://leadeck.dev/install.sh | sh
lea
```

The installer checks the requirements, downloads the release for your platform and verifies its
checksum and the release's signature (see [Verify a release](#verify-a-release)), links `~/.local/bin/lea`, then runs `lea init`, which asks for your projects directory
and pulls the images. In the first agent, run `/login` once (Claude Code's own login, kept per
projects directory). Press `n` for another agent.

### Requirements

- Linux, macOS, or Windows with WSL2 (run lea from WSL).
- Docker Engine (Linux) or Docker Desktop, running, usable without `sudo`; a patched host
  kernel (see [How lea keeps your machine safe](#how-lea-keeps-your-machine-safe)).
- On macOS, tmux 3.2 or later (`brew install tmux`); the Linux release brings its own.
- `curl`, `tar`, `ssh-keygen`.
- A Claude subscription or API key for Claude Code.

lea never runs `sudo` itself, except once in `lea init` on Linux hosts where Docker uses
AppArmor: it installs lea's AppArmor profile, and only when that profile changed.

### Verify a release

Each release is signed keyless with [cosign](https://docs.sigstore.dev) (Sigstore, GitHub
OIDC) by the release workflow, on its `v*` tag: `checksums.txt` (bundle
`checksums.txt.sigstore.json`, a release asset) and the `lea` and `lea-proxy` images, by digest.
The installer verifies `checksums.txt` with cosign before trusting it: the `cosign` on your
`PATH`, else cosign v3.0.6, which it downloads from Sigstore's GitHub release and checks against
the SHA-256 pinned in `install.sh` (it never fetches a "latest" cosign). It stops on a bad
signature, on a missing signature, and when no cosign can be had: it never installs on the
checksum alone. The only exception is v0.1.0 and v0.1.1, which predate signing: their
`checksums.txt` must then be the genuine one, whose SHA-256 the installer pins (it stops
otherwise), and the install goes on by checksum, with a warning. `LEA_REQUIRE_COSIGN=1` removes
that exception too. `lea update` and the background update verify the signature the same way
before they run the new release's installer.

```sh
curl -fsSL https://leadeck.dev/install.sh | LEA_REQUIRE_COSIGN=1 sh
```

By hand, with the same identity and issuer:

```sh
cosign verify-blob --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github\.com/DavidGodefroid/lea/\.github/workflows/release\.yml@refs/tags/v[0-9]' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
cosign verify \
  --certificate-identity-regexp '^https://github\.com/DavidGodefroid/lea/\.github/workflows/release\.yml@refs/tags/v[0-9]' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/davidgodefroid/lea:<version>   # and ghcr.io/davidgodefroid/lea-proxy:<version>
```

The limit: the installer comes from the same GitHub account as the release. The signature
protects against a tampered asset or mirror, and every signature is logged publicly (Rekor), so
it can be audited; it does not replace trusting that account.

## The deck

`lea` opens the deck. Each agent is Claude Code running in the lea container under `abduco`, so
closing the deck, or the terminal, does not stop it: the next `lea` reattaches.

| Key | Action |
|---|---|
| `j` / `k`, `↓` / `↑`, `1`–`9` | select an agent (the right side follows) |
| `enter` | type into the selected agent |
| `ctrl+space` | back to the column (and forth) |
| `space` | the next agent that needs you (a permission, a message for you) |
| `n` | start a new agent |
| `e` | rename the selected agent (enter saves, esc cancels, empty clears) |
| `m` / `t` | send the selected agent a message / read its messages (see below) |
| `a` / `d` / `A` | allow / deny / always allow the selected agent's permission request |
| `r`, then `l` / `d` / `r` | make the selected agent a lead, a dev or a reviewer |
| `s` | open a shell in the container (`exit` or `x` closes it) |
| `-` / `\|` | split: a shell below the selected agent, or at its right, in its directory |
| `S` | sounds on or off (the top border says `muted` while they are off) |
| `p` | pause the selected agent, or resume it where it stopped (its conversation stays) |
| `,` | settings (also in a row's right-click menu) |
| `x`, then `y` | stop the selected agent |
| `f` | new folder (enter names it) |
| `o` | move the selected agent into a folder, or out of it |
| `J` / `K` | move the selected folder, or an agent within its folder, down / up |
| `enter`, `h` / `l` on a folder | fold / unfold it (`e` renames it, `x` deletes it: its agents go back, never stopped) |
| `q` | leave the deck; agents keep running |
| `?` | list the keys |

The mouse works too: click a row to select it, scroll to move, right-click a row for its menu
(Split horizontally / Split vertically open a shell beside the agent, as Terminator does; the
shell shows with its agent and steps aside while another agent is shown). A right-click anywhere
else in the column opens the deck's own menu: new agent, new shell, new folder, sounds, keys,
detach; ctrl + right-click makes a folder, holding the agent under the pointer. The
pointer over a row shows its whole name (cut in the row when long) with the agent's name, role,
profile and state in the bottom border.

- **States.** A red `●` needs you (a permission, or a blocker the agent named with
  `task_update`), an orange spinner is working or waiting (its subagents, or a next step it
  named: CI, a review, a reply), a green `●` has finished with nothing pending, an empty `○` has
  been quiet for 30 minutes, a grey `‖` is paused (`p`: lea freezes the
  agent's processes, tool calls included, until you resume it), a lavender `◷` stopped on its
  usage limit (see below). Agents stay in the order you started them.
- **Usage limits.** When Claude Code stops on its provider's usage limit, the row shows `◷` and
  `usage limit, resumes 15:02`: lea resumes the agent at that time, the limit's reset (from the
  status line's 5-hour and 7-day windows) plus `LEA_RESUME_DELAY` seconds (60) and a few seconds
  of jitter, or after a backoff (5 minutes, doubling, at most an hour) when no reset time is
  known. Resuming wakes the agent with one fixed line of lea's ("the usage limit … should have
  reset. Continue your task"); lea never types into its terminal. After 6 resumes without
  progress, or with `LEA_AUTO_RESUME=0`, the row says `usage limit, resume it` and the agent
  waits for you. A pending resume survives `lea stop` and the next container: the agent comes back
  with `--resume` and lea resumes it at the same time (at least a minute after the restart; given
  up past 8 days). Codex agents are not detected yet: Codex gives no structured signal for its
  limit (its failed turn runs no hook, and the error is not kept in its session file).
- **Messages.** Typing into an agent's pane (`enter`) talks to it now, in its conversation.
  `m` (Send message) instead leaves it a note in its inbox, as another agent would: it reads it
  on its next turn, or is woken for it once its turn is over. `t` (Messages) shows everything
  the agent sent and received: your notes, its reports to you, and its messages with other
  agents, questions (`ask`) included; an answer names the message it answers (`result to #12`).
  Highlighted in a row's menu, each says what it does. No agent ever asks you a question: it
  writes to you with `msg_send`, and you answer with `m`.
- **Folders.** Folders come first, in your order, then the agents in no folder. A folded
  folder's dot sums up its agents: red when one needs you, the spinner while one works, green
  once none works any more. The layout is yours: the deck keeps it on the host
  (`~/.local/share/lea/host/deck/<container>.json`), out of every container's reach.
  Each row shows the agent's context use, its directory and branch, and its own report on its
  task. The pane that has the keyboard is the bright one: the deck's frame turns blue when it
  has it; otherwise the terminal on the right shows a blue `▶ <agent> · typing here` bar on its
  top border (grey, with the keys that give it the keyboard, while the deck has it).
- **Scrolling.** The wheel scrolls an agent's history; the bar then reads `history 251/256: type
  or q to go back`, the line at the top of the view out of the history's lines, counting down as
  you scroll up. Any typed key (`q`, `Escape`, `Enter`, a letter…) goes back to the prompt; that key is
  not typed, the next ones are.
- **Sounds.** lea plays a sound when an agent starts waiting for you, and another when a turn
  ends, while a deck shows the agent. `lea sound on|off` and the deck's `S` switch them at
  runtime; `LEA_SOUND=0` starts with them off; `LEA_SOUND_ATTENTION` and `LEA_SOUND_DONE` pick
  the files, or mute one event with `0`.
- **Names.** An agent you have not renamed is named after its first prompt, by a short
  `claude -p` run on the host with a small model (`LEA_AUTONAME_MODEL`, `haiku` by default;
  `LEA_AUTONAME=0` turns it off). The prompt is not stored.
- **Copy and paste.** Drag to select text in an agent's terminal: releasing the button copies it
  to your clipboard and flashes "Copied". `ctrl+v` pastes your clipboard's text into the pane that
  has the keyboard (the agent's input, a shell, the deck). lea reads your clipboard only when you
  press `ctrl+v` (with `wl-paste`, `xclip` or `pbpaste`); agents never read it.
- **Permissions.** When an agent asks for a permission, its row shows the request and the deck
  answers it. Unanswered after `LEA_APPROVAL_TIMEOUT` seconds (60), Claude asks in its own
  terminal as usual; `LEA_APPROVALS=pane` keeps Claude's dialog only.

## Git and forges

lea needs no forge account, and works with any of them.

- **HTTPS, by default.** `git clone`, `pull` and `push` over HTTPS reach GitHub, GitLab, Gitea,
  Bitbucket or any public forge through the proxy. Log in from a shell in lea (`lea shell`, or `s`
  in the deck) with the forge's own tool or a token: `gh auth login`, `glab auth login`, or a
  token in git's credential store. `gh` and `glab` are in the image; what they keep lives in
  lea's state, never in your home directory.
- **SSH, if you prefer.** List the endpoints in `~/.config/lea/lea.conf`, pin their host keys,
  and rerun `lea init`: it creates lea's own key and prints the public key to add to your forge.

  ```sh
  echo 'LEA_GIT_SSH="github.com:22 gitlab.com:22"' >> ~/.config/lea/lea.conf
  ssh-keyscan github.com gitlab.com >> ~/.config/lea/git_known_hosts   # compare the fingerprints
  lea init
  ```

  Each endpoint gets its own forwarder in the proxy; agents use lea's key, never yours.
- **Container registries.** `podman login` (or `docker login`) in lea; the login is kept for the
  next sessions.

## Go

The image ships Go 1.26 with `staticcheck` and `govulncheck`, and agents run `go`, `staticcheck`
and `govulncheck` without a permission prompt. Go never downloads another toolchain
(`GOTOOLCHAIN=local`); its module and build caches, and what `go install` builds, live in lea's
state (`/lea/state/go`) and are kept for the next sessions. A permission rule never matches a
command that starts with `GOOS=… GOARCH=…`, so cross-compile with `go-cross <GOOS> <GOARCH> …`
instead, for example `go-cross darwin arm64 vet ./...`.

## Databases, with locksql

lea comes with [locksql](https://github.com/DavidGodefroid/locksql): a console that stands
between your agents and your databases. The agents propose SQL; you approve each statement, and
the console alone holds the credentials. The console runs hub-attached only, under the Lock
Family's `lox up` ([lockhub's docs/lox-up.md](https://github.com/DavidGodefroid/lockhub/blob/main/docs/lox-up.md)),
in a compose service of its own: lea starts no locksql console. Add a locksql lock to your projects
directory (`lox add locksql NAME`, with `allowed_uids: [<your uid>]` in
`.lockfamily/lockfamily.yaml`: lea's agents run as your uid, which has no account in locksql's
image) and run `lox up`. lea's agents then reach that lock's socket volume (read-only, once docker
confirms it is lox up's, see below); without such a lock they get no locksql socket and lea says to
run `lox up`. `lea locksql` and `lea doctor` say which applies.

`lea init` builds lea's locksql image (from the source of the release lea pins, cloned at its tag
and built only once cosign proves that locksql's release workflow signed that release on that very
commit; a branch is refused) and, when your projects directory has no database profile yet, walks
you through a first one with it (engine, host, TLS, where the password comes from; never the
password). `lox up` reviews that profile (`.locksql` in your projects directory) and copies it into
the lock; the agents see `.locksql` read-only. Vault profiles wait for lox up to pass Vault to the
console (lockhub#45). lea puts no `locksql` command on the host (it removes the link older releases
made).

Each query of an agent waits for your approval through the hub: in lea's deck (its hub view) or
with `lox approve`. The console's own questions (its policy review on a first start or after a
loosening, a production profile's typed name, the credentials its profile does not take from
Vault) wait at its terminal: `lox attach NAME`, or **in the deck**: `L` in the deck (or
`lea lock open locksql [profile]`) opens a pane under the agent attached to lox up's console of
that profile (the only locksql lock without one), with a red `LOCK` border, without taking the
keyboard: click it to answer (ctrl+space from the deck always goes to the agent, never to the
console). Attaching replays nothing: on a first start the pending question is the policy review,
`Apply these changes? [y/N]`. It serves the deck's own projects directory, and only a console docker
confirms is lox up's for it (its compose service, its socket volume). When no such console runs,
or the attach ends at once, lea says so instead of opening a pane. The last console opened takes
that slot; another one waits in a window of its own and comes back with `lea lock open` again.
`lea lock list` lists the deck's consoles, `lea lock close locksql [profile]` closes one (the
attach ends; lox up's console keeps running). Only you open them: no agent tool reaches a console,
lea never types into it nor reads it, ctrl+v pastes nothing into it (agents can write your
clipboard) and its screen cannot be copied (a copy goes through the clipboard bridge the agents'
containers share). The pane keeps a short history, and both the pane and its history go when the
attach ends; lox up's console keeps no log (`docker logs`). lockmq and lockfile consoles are not
shown in the deck: `lox attach NAME` reaches them.

An agent may **ask** you to open one (its `lock_request` tool), for a profile you allowed agents to
ask for: `LEA_LOCKSQL_AGENT_PROFILES="staging prod"` in your lea configuration (empty by default:
agents may ask for none; `lea config export` never carries it). The request shows as a card on the
agent's row, with a sound: `O` opens that console as `L` would (a second after the card comes into
view, so a key meant for something else never opens one), `d` declines. The card names the
tool and the profile only, never text of the agent's. The agent gets back `requested`, then a
message: opened, declined, failed or expired (after 10 minutes); never what the console shows,
whether one runs, nor why it failed. It chooses no flag and no argument; one request per tool per
minute per container (counted from your answer too), and one pending at a time. Each request is recorded (who, what, when, the
outcome) in lea's database.

The two projects are independent: lea runs without locksql, and locksql works with any agent.
`lea init --no-locksql` (or `LEA_LOCKSQL=0`) leaves no trace of locksql in lea.

## Queues and files, with lockmq and lockfile

[lockmq](https://github.com/DavidGodefroid/lockmq) (RabbitMQ) and
[lockfile](https://github.com/DavidGodefroid/lockfile) (files on S3, SFTP or a local directory)
keep a human between the agents and production the same way: the agents ask, a console that alone
holds the credentials shows each request and waits for your approval. Both are pre-releases, so
they are **off** by default; `lea init` asks (no by default), or `lea init --lockmq` /
`lea init --lockfile`.

Their console runs hub-attached only, in a container of its own that the Lock Family's `lox up`
starts for your projects directory (`lox add lockmq <name>` or `lox add lockfile <name>`, then
`lox up`; lockhub's `docs/lox-up.md`): no host account, no `sudo <tool> install`. lea's agents run
as your uid, which has no account in the console's image: the console serves it only when you name
it in the lock's `allowed_uids` (`allowed_uids: [<your uid>]` in `.lockfamily/lockfamily.yaml`;
`lox up` reviews it with you); without it the agents get no console. You set it; no agent can.
lea installs nothing for them; their launchers update themselves (`lockmq update`,
`lockfile update`). `lea init` points to the launcher when it is missing, then runs lockmq's
profile wizard (`lockmq setup`, never a password). lockfile below v0.1.1 is refused (a plugin
sandbox escape).

lea never starts, sees or drives these consoles. Its containers get only the socket volume of
`lox up`'s lock of the tool (`.lockfamily/up.json`) on `/run/<tool>`, read-only, and the tool's
clients group (`lockmq-clients` 10010, `lockfile-clients` 10030): lea's image names the console
accounts with the ids of the tools' images, so the client checks that the socket is the console's.
The agents reach the socket and nothing else (no credential, no profile, no audit log). A tool
that is on without a lock of `lox up` in the projects directory gets no socket, with a warning
that says to run `lox up`: there is no fixed `<tool>-run` volume of a console started by the
tool's own launcher any more. `lea lockmq` / `lea lockfile` tell you whether the tool is on and how
its console runs. Restart lea (`lea stop --all`, then `lea`) after turning one on.

## Web requests, with LockHTTP

[LockHTTP](https://github.com/DavidGodefroid/lockhttp) puts a human between the agents and the
web: every outbound HTTP(S) request of an agent goes through a LockHTTP gate beside lea's proxy,
which lets through what its policy trusts and asks you, on the Lock Family hub (`lox approve`, the
deck, a paired phone), about the rest.
It is **off** by default (`LEA_LOCKHTTP=0`): lea then behaves as before.

LockHTTP runs in Docker only (`docs/docker.md` in the lockhttp repository). To turn it on,
install the launcher of the lockhttp release lea pins, v0.5.1 (`LOCKHTTP_VERSION=v0.5.1 sh
install.sh` with lockhttp's `scripts/install.sh`, which checks the release's signatures and
pins its image by digest; `lea doctor` warns about an older one), add a lockhttp lock to your
projects directory (`lox add lockhttp <name>`; its policy in `.lockhttp/config.toml`) and run
`lox up`, which runs the hub and the lockhttp console. Then set
`LEA_LOCKHTTP=1` in `~/.config/lea/lea.conf` and restart lea (`lea stop --all`, then `lea`);
`lea doctor` checks each step. The gate runs hub-attached only: without a trusted
`.lockfamily/up.json` naming a lockhttp lock, lea does not start (it says to run `lox up`). Nothing else on the host: no account, group or socket directory.

With it on, squid listens on the proxy's loopback only, behind the gate (its floor stays: DoH
resolvers, IP literals and private ranges are refused; the allowlist is the gate's policy now).
lea runs the gate as a container of its own, `lea-lockhttp-gate`, in the proxy's network
namespace, from the image the launcher pins (never a tag), as the image's `gate` account (never
root), without capabilities, on a read-only root; it is the only container with the console's
socket volume (`lox up`'s `<project>_<name>-run`): no agent container gets it. Its audit stays in the volume
`lea-lockhttp-audit`, its status (for the deck) in `~/.local/share/lea/lockhttp/status/`. A proxy
started again gets a new gate. The gate names the calling container from the network the
request arrives on, which no agent can forge: **the container is the trust boundary**. Each
agent also has its own proxy password, and the daemon tells the gate whose it is
(`sources.json`, under `~/.local/share/lea/lockhttp/`), but the agents of one container share a
uid and can read each other's environment: that name is for the audit and the console's display
(attribution), never a permission, and one agent can pass for another of the same container.
lea-keyproxy goes through the gate too, and so do the forge calls of grants, with the calling
agent's own password. The daemon also tells the gate which Claude login each container holds
(`credentials.json`: hashes of the OAuth access token in its state, and of the keys lea-keyproxy
injects); under MITM the gate denies a Claude API key that is not the caller's (`E_SECRET_EGRESS`).
That login file is the agent's to write: the check stops a key sent from the wrong container,
not an agent that rewrites its own login file. `LEA_GIT_SSH` and `LEA_OTEL_UPSTREAM` are refused (their raw TCP forwarders
would bypass the gate: use git over HTTPS), and claude.ai's connectors are turned off in the
agents' Claude Code (WebSearch and WebFetch stay).

**The hub** (lockhub's `docs/lox-up.md`): `lox up` runs a lockhttp lock for the projects
directory (its `.lockfamily/up.json`), and the gate asks the Lock Family hub about every held
request. It mounts that lock's socket volume and gate volume (`--home`), and reads
lea's read-only copies (in `/run/lockfamily`) of the hub's key; of the host facades' keys, for a
lock lock up lets reveal (`reveal: true`: `--facade-keys-file`, so that a reveal, Ctrl-O, is
sealed only to them); and of the paired phones, for a lock that pins them (`devices: true`:
`/run/lockfamily/devices.json`, with `--require-device` and `--seal-clear` as `hub.mobile` says;
without it, a phone's decision is refused). Only the proxy joins the hub's network, whose
addresses the gate refuses as targets: no agent reaches the hub. lea checks first the volumes' and
the network's labels, the key, and the facade keys and devices as the gate reads them (lockkit's
parsers; it writes them in canonical form); anything it cannot trust stops lea, which says why and
to run `lox up`; so does an up.json that does not say what the gate pins (`lockhttp_gate` 1: a
lock's devices, required and sealed, its reveals), and a lock whose required or sealed devices lea
cannot pass. The pinned lockhttp release decides too: the hub needs v0.3.0 (an older one stops
lea), the devices v0.4.0, the facade keys v0.5.0; lea passes no flag that release lacks, and says
so. One
lockhttp lock at a time: the gate serves every projects directory, and asks the hub of the one
whose lea started it last; lea refuses to switch it to another hub while agents of another
projects directory run.

`LEA_LOCKHTTP_MITM=1` (with `LEA_LOCKHTTP=1`) lets the gate decrypt HTTPS, but for the policy's
splice hosts, so that it decides on methods, paths and bodies. lea makes a local CA on first use
through the launcher (`lockhttp ca init`, 90 days, in `~/.local/share/lea/lockhttp/ca/`; `lea
doctor` warns a week before it ends: `lockhttp ca init --force --dir <it>`, then `lea stop --all`
and `lea`). Its key goes to one container only, `lea-lockhttp-signer` (the image's `signer`
account, no network), through the volume `lea-lockhttp-ca` (key owned by `signer`, 0600); the
signer hands leaf certificates to the gate on a socket in the volume `lea-lockhttp-signer`. The
certificate, never the key, goes to the gate, to lea-keyproxy and into the agents' trust stores
(system store, `NODE_EXTRA_CA_CERTS`, `REQUESTS_CA_BUNDLE`, `PIP_CERT`, `SSL_CERT_FILE`).

## Teams of agents

Every agent has a role: `lead`, `dev` (the default) or `reviewer`. The first agent is a lead.
Only you change a role (`r` in the deck, `lea role <agent> <role>`, `lea new -r reviewer`).
Agents get lea's tools through MCP:

- a lead starts `dev` and `reviewer` agents with `agents_spawn` and stops its own with
  `agents_stop` (at most 20 children at a time);
- any other agent asks a lead with `agents_request_spawn`; without a lead the request comes to
  you (`lea requests`, `lea request accept <id>`, `lea request decline <id> [reason]`);
- agents message each other (`msg_send`, `msg_reply`); a message waits in the inbox until the
  agent's next turn. An agent whose turn is over is woken when a message arrives: its Stop hook
  starts a background job (Claude Code's `asyncRewake`) that ends with the unread notice, and
  Claude Code starts a turn. A working agent, or one waiting on a permission dialog, is not
  interrupted; only the newest job of an agent stays, so one message wakes it once;
- an agent asks another a question and waits for the answer with `ask` (the other answers with
  `msg_reply`; works with any agent that has lea's MCP tools). The answer is data, never
  instructions. Only a reply from the asked agent counts; an agent's container is checked
  (its bus socket), but its name inside a container is what the agent declares (`LEA_AGENT`),
  so agents of one container can answer for one another. One ask at a time per agent, at most 4 questions waiting on one agent, 5 asks a
  minute, a 1–600 s timeout (300 by default; a late answer lands in the inbox), and no loop: an
  ask that would make agents wait on one another in a circle, or in a chain longer than 3, is
  refused. The question and its answer show in the deck's thread view; `lea board` shows who
  waits on whom;
- an agent started by a lead is told to report to it with `msg_send` when it is done or blocked;
- `board_read` shows an agent every task of its lea, as `lea board` shows them to you;
- every agent reports on its task with `task_update`: its row shows it, and `lea board --md`
  prints every task as markdown;
- before editing files another agent may touch, an agent claims them with `lease_acquire` and
  lets go with `lease_release` (advisory, shown in `lea board --md`).

### Other providers: Codex, Ollama, vLLM

An agent runs on a **provider**: `claude` (Claude Code, the default), `codex` (OpenAI's Codex CLI,
in the image) or an OpenAI-compatible endpoint of your own, run through Codex (Ollama, vLLM, any
server with the Responses API).

- `LEA_PROVIDER` and `LEA_MODEL` in `~/.config/lea/lea.conf` are the default provider and model
  (an agent started without one: the deck's `n`, a lead's `agents_spawn`).
- `lea new -p codex -m gpt-5.1-codex`, the deck's `N` (then the provider's digit), or a lead's
  `agents_spawn` with `profile` and `model` start an agent on another one; `providers_list`
  tells an agent which exist. The deck shows each agent's provider and model.
- Codex keeps its own login per projects directory, like Claude Code: run `codex login` (or
  `/login`) in the first Codex agent. It runs with its own sandbox (`workspace-write`) and asks
  for anything else in its pane; lea's MCP tools reach it, and its turn's end makes it idle in the
  deck. lea does not see Codex's permission dialogs (the deck shows the agent working) and does
  not wake an idle Codex agent when a message arrives: it reads its inbox with `msg_inbox` or
  waits with `msg_wait`.

Endpoints go in `~/.config/lea/providers` (`LEA_PROVIDERS`), one per line
(`config/providers.example`):

```
# <name>  <base URL>                          [key=<key file>]             [model=<default model>]
ollama    http://192.168.1.10:11434/v1                                     model=qwen3-coder:30b
vllm      https://vllm.example.com/v1         key=/home/me/.config/lea/keys/vllm
```

- An endpoint's API key never reaches the agents. The file names a key file, not a key: it must
  be yours, mode `600` (or `400`), a regular file with a single hard link, and outside every
  directory lea mounts into the agents' container (the projects directory, lea's data, `~/.claude`,
  `~/.codex`; real paths are compared). Nor may it sit in a Docker volume lea mounts there, such
  as the nested engine's storage (`<prefix>-podman-<id>`, under Docker's own data directory):
  every agent reads it. lea
  mounts it into the proxy container only, where `lea-keyproxy` adds it to the agents' requests;
  an agent reaches the endpoint at `http://lea-proxy:4400/<name>` and sees neither the key nor the
  upstream. The key is read at each request: to rotate it, rewrite the file in place
  (`cat new-key > file`); a file replaced by a new one (`mv`, most editors) is seen once the
  proxy restarts, which the next `lea` does. Every agent of every lea container can use a
  configured endpoint (none can read its key); the proxy's egress network (your host, other
  containers there) cannot.
- `lea-keyproxy` sends every request through the proxy's own egress rules: an endpoint on your
  network (an Ollama on your LAN, or on this machine at its Docker bridge address) must be listed
  in `LEA_INTERNAL_HOSTS` with its port, as any internal host; nothing is open by default, and
  loopback, `localhost` and link-local addresses are refused. A key goes over HTTPS only, except
  to a host you listed there. A public endpoint's domain must be in the egress allowlist too
  (`LEA_EGRESS_ALLOW`: `default,<its domain>`). The forwarder answers the agents' networks only,
  never the proxy's egress network (your host, other containers there).
- Codex needs the endpoint to serve the Responses API (`/v1/responses`): Ollama 0.13.3 or later,
  a recent vLLM.
- A change of the providers file restarts the proxy at the next `lea`.

### Control tower

lea brings its own Claude Code plugin, `lea`, loaded in every agent (from the image, for the
session only: nothing is installed in your `~/.claude`). Its skill `/lea:control-tower` turns a
lead into your control tower: you talk to it only, it splits your requests into tasks, starts
`dev` and `reviewer` agents with self-contained briefs, answers their questions when it can, and
follows them with `board_read`, and briefs you in one line per agent. Between your requests an
agent's report wakes it, as it wakes every agent whose turn is over:

```
fix-login · done · PR #42, 18 tests pass
dark-mode · blocked · which palette: Catppuccin or Nord?
Needs you: palette for dark-mode
```

Ask it for the `board`, the `detail` of an agent, or to `stop` one, in your own words.

The tower never does the work itself, not even a `git push`: every request goes to an agent. The
plugin enforces it with a hook: once `/lea:control-tower` runs, the session may only use lea's
team tools (`agents_*`, `msg_*`, `board_read`, `task_update`), ask you questions and run
`lea inbox --agent --wait`; any other tool (Bash, Edit, Read, git, gh, the web…) is denied with a
reminder to delegate. `/lea:control-tower off` gives the session its tools back.
The skill's old name, `/lea:tower-control`, still works for now.

Type `/lea:check` (or ask for a status, a "point", a "topo") for the state of the work in
progress, in at most 10 lines: done since the last check, in progress, left, and what is blocked
or yours to decide. A tower builds it from the board and the agents' reports, without doing any
work; any other agent from its own task. Nothing new since the last check: one line.

```
✅ Done
dev-1: login fix merged, CI green
🔄 In progress
dev-2: dark mode, tests being written
⚠️ Blocked / to decide
dev-2: palette, Catppuccin or Nord?
```

### Grants: let every agent merge, close issues, stop finished agents

Claude Code judges each action from its own conversation: "you may merge" told to a lead does
not reach its children, and relayed in a lea message it is rightly distrusted. A **grant** is
that decision recorded by you on the host, applied the same way to every agent it covers:

```sh
lea grant add merge --container lea-myproj --repo api --github me/api --expires 8h  # gh pr merge N|own-branch --squash|--merge|--rebase (--delete-branch: own-branch only; the PR's head must be the agent's branch)
lea grant add stop-finished --container lea-myproj                  # agents_stop on an idle or ended child
lea grant add close-issue --container lea-myproj --repo api --github me/api  # gh issue close|reopen|comment N
lea grant add delete-own-branch --container lea-myproj --repo api --github me/api  # git push origin --delete <the agent's own branch>
lea grant add commit-identity --container lea-myproj --repo api --github me/api --branch 'feat/*'  # git commit -m (no --amend), git push [-u] origin <own branch>
lea grant add pr-create --container lea-myproj --repo api --github me/api  # gh pr create --head <own branch> --title T --body B|--body-file F
lea grant ls [--all]                                                # active grants (--all: revoked, expired) and recent uses
lea grant revoke 3
```

`--container` is the lea (`lea board` shows its name); `--repo` (the directory) and `--github`
(its GitHub repository) go together on every grant but stop-finished; `--branch` (a glob on the
agent's branch) and `--agent NAME` (that agent and its children) narrow it. A grant expires
after 8 hours unless `--expires` says otherwise (1m to 30 days). lea's `PreToolUse` hook asks the
daemon before each tool call: a call a grant covers is allowed, with the grant's id as the
reason, and audited (`lea grant ls` lists the uses: agent, action, target, never the command);
anything else takes the usual path. Revocation and expiry apply on the next call.

An allowed shell call never runs the agent's text: lea runs the command it parsed, rebuilt word by
word, in a fresh `/bin/sh` under `env -i` (so the agent's variables, aliases and shell
functions do not apply), with a fixed `PATH` and only `HOME`, `GH_CONFIG_DIR`, `SSH_AUTH_SOCK` and `LANG` as
Claude's own process has them. It reaches the forge through lea's proxy, which lea names itself
(`HTTPS_PROXY` for gh, `http.proxy` for git, `http://lea-proxy:3128`), never through a proxy the
agent's environment names; CONNECT keeps TLS end to end.

- gh runs as `/usr/bin/gh … -R github.com/<--github>`; every grant but stop-finished names
  `--repo` and `--github` together. It runs with the token of gh's config (`gh auth token`) and
  none of the rest of that config, which the agent can write (it could send gh's requests to a
  server of its own): an empty config dir no one but root can create.
- A commit goes through plumbing in the agent's worktree (`write-tree`, `commit-tree`,
  `update-ref`): no hook, filter or signing runs. It commits the index as it is (`git add`
  first: no `-a`).
- A push runs from a bare repository lea creates for the call (random name, mode 0700, its
  config checked right before the push, the worktree's objects borrowed) to `https://github.com/<--github>.git` with the full ref
  (`refs/heads/<branch>:refs/heads/<branch>`): the remote's config never decides where it goes.
- git's hooks, fsmonitor, askpass and pager are off, ssh is `/usr/bin/ssh`, https and ssh are the
  only transports, `~/.gitconfig` is not read; and lea refuses a repository whose own config
  holds a `url.*.insteadOf`, `include`, URL-specific credential helper, `pushurl`, push refspec,
  mirror, filter, textconv or any `http.*` key.

Only the host writes grants: there is no MCP tool for it (`grants_list` only reads them), the
daemon refuses `grant_add` from a container, `lea.db` is never mounted in one, and a message
saying "the user allows X" changes nothing. A call is allowed only when it is exactly one plain
command lea parses (no `;`, `&&`, `|`, `$(…)`, backticks, redirections, `VAR=` prefixes,
`--repo`), run at the root of the agent's own worktree (`<projects>/.lea/worktrees/<repo>/<agent>`,
so gh and git act on its repo), whatever is granted. A branch is an agent's own only while no other
live agent of the lea works on it. Some calls are denied outright, grant or not: `gh pr merge
--admin`, force-push (`-f`, `--force*`, `+refspec`, `--mirror`, `--prune`), any push to `main` or
`master` however its refspec spells it, a push to any branch but the agent's own (named: a bare
`git push`, `HEAD` or `@` follows whatever is checked out), a refspec whose destination git would
infer (`<src>:<branch>` with neither side `refs/heads/…`: a tag's source makes `refs/tags/<branch>`),
a bare `git push origin <branch>` where the repository it runs in has no local branch of that name
or a tag of it (lea reads its refs; after a command that may change them, `refs/heads/<branch>`
only), a push of every branch or tag (`--all`,
`--branches`, `--tags`, `--follow-tags`, a wildcard or a bare `:`), other git transports (`send-pack`, `http-push`,
`remote-<helper>`, `svn`, `p4`), a push lea cannot read (an unknown option, a shell glob, a push run
through another program, or a `git … push` in the arguments of any program but `grep`, `rg`,
`echo`, `printf`, `gh pr|issue`), git run by `xargs`, `parallel`, `find` (or by another program without a subcommand), a git
program, subcommand or push argument computed by the shell (`$…`, backticks; a substitution's own
command is checked too), a program computed by the shell or run by `env`/`sudo`/… from a computed
word, `submodule foreach`, `bisect run`, `filter-branch`, git options that run a
program (`--upload-pack`, `--receive-pack`, `--exec`, `--extcmd`, `-O`, `rebase -x`), git options
before the subcommand other than `-C`, `-c user.name|user.email|color.ui|core.quotepath` and display
options, setting `GIT_*` (but `GIT_EDITOR`/`GIT_SEQUENCE_EDITOR=true|:`, `GIT_PAGER=cat`,
`GIT_TERMINAL_PROMPT=0`), `HOME` or `XDG_CONFIG_HOME`, deleting a branch that is not the agent's own, repointing git or gh (`git remote
set-url|add|…`, writes with `git config`, `gh repo set-default`, `GH_REPO=`/`GH_HOST=`), and
writing a file that makes a later command run something else: Claude's settings, hooks and shell
snapshots, git's config and hooks (`.git/config`, `.git/hooks/`, `~/.gitconfig`), the shell's
startup files (`~/.bashrc`…), `/usr/local/share/lea` or `/lea/state`. A command that hides
words behind escapes or expansions (`\`, `$…`, backticks) and touches any of these is denied
too: run it plainly.

**What grants protect against.** Grants and hard denies keep a *confused* agent (one that
misreads a message, a brief or its own transcript) from merging, pushing or deleting where you did
not say it may; they are not a sandbox against a *hostile* one. An agent can still write and run
code through the tools it is allowed (`go run`, a test, a script), and it can read the gh token
in `/lea/state/gh`: what keeps a hostile agent in check is the container, its network rules and
the scope of the tokens you give lea, not grants. Two ways a hostile agent gets past them, among
others: a shell function named `/usr/bin/env` in Claude's shell snapshot replaces the pinned
call, and a loop rewriting the per-call bare repository's config (same user) between lea's check
and the push can send the push elsewhere.

To check that a Claude Code release runs the command lea rebuilds (the hook's `updatedInput`)
before relying on grants, in a lea: `lea grant add commit-identity --container <lea> --repo <r>
--github <owner>/<r> --expires 10m`, ask an agent working on a branch of `<r>` to `git add` a
file and run `git commit -m test`, then check that `git reflog -1` in its worktree says
`commit (lea grant)` (a plain `git commit` writes `commit: test`); `lea grant ls` lists the use.

### Let agents deliver (push, PR/MR, merge)

To let every agent of a lea push its own branch, open its pull request (GitHub) or merge request
(GitLab) and merge it without a prompt, run once, on the host, in the projects directory:

```sh
lea delivery on                # lists the forge repository each repository here is pinned to, and their digest
lea delivery on --yes DIGEST   # records exactly those: standing grants on each one on github.com or GitLab
lea delivery on --gitlab git.example.com  # also a self-hosted GitLab (gitlab.com is known)
lea delivery status            # which repositories are covered
lea delivery off               # revoke them: agents ask as usual
```

`on` reads each repository's `origin` from its `.git/config` (as a file: no git runs on the host)
at that moment, and records, for each one on github.com, gitlab.com or a `--gitlab` host (a host
once pinned stays known), the grants `commit-identity`, `pr-create`, `merge` and
`delete-own-branch` for every agent of this lea, pinned to that repository (`github.com/OWNER/NAME`
or `gitlab:HOST/GROUP/…/NAME`), with no expiry (`lea grant ls` shows them as `standing`, and their uses). A repository
added later, or an origin changed, is covered once you run `lea delivery on` again and confirm (it replaces
the standing grants; grants you added by hand stay). Agents can write a repository's
`.git/config` (or add a repository): `on` records nothing while a pin is new or differs from the
last one recorded for that repository of this lea (revoked ones included); it lists them, you
check them, then run `lea delivery on --yes DIGEST` with the digest it printed: if any pin
was read differently in between, nothing is recorded. Once confirmed, a plain `on` applies. Delivery is
off until you turn it on, and only the host can: the daemon refuses `delivery_set` from a
container.

`lea doctor` checks it: each covered repository's `origin` still matches its pin (an origin
rewritten since, or a repository on a forge not covered yet, is a warning: run `lea delivery on`
again), and each forge host has credentials in lea's state (`gh auth login`, `glab auth login
--hostname HOST`; git's credential store alone covers pushes only, a warning); without them it
fails. It only reads.

Everything in Grants above holds: an agent's own branch only, in its own worktree, every hard
deny (no push to `main`, no force-push, no `--admin`). The calls covered:

- `git commit -m …`, `git push [-u] origin <own branch>`;
- `gh pr create --head <own branch> --title T (--body B | --body-file F) [--base B] [--draft]`:
  `--head` is required, `F` is a path relative to the agent's worktree that must resolve to a
  plain file inside it, outside `.git` (lea checks it is a plain file, opens it once and gh reads that open file; a quoted heredoc, `--body "$(cat <<'EOF' … EOF)"`, is
  not covered: write the body to a file);
- `gh pr merge <n>|<own branch> --squash|--merge|--rebase [--delete-branch]` (the PR's head must be
  the agent's branch), and `git push origin --delete <own branch>`;
- on GitLab: `glab mr create --source-branch <own branch> --title T [--description D]
  [--target-branch B] [--draft] [--remove-source-branch] [--yes]` and `glab mr merge <n>|<own
  branch> [--squash] [--remove-source-branch] [--yes]` (`--rebase` is not covered). lea does not
  run them with glab, whose config the agent can write (it could send glab's requests elsewhere):
  glab only reads the token of its config for HOST, from `/`, and lea calls the project's API
  (`https://HOST/api/v4`) with curl (no `.curlrc`, https only, no redirect, no proxy variable), its answers read by jq,
  both with no `HOME` of the agent's.
  A merge first reads the MR: it must be open and come from the agent's branch of the project
  itself, not a fork (by branch: the one open MR of that branch); the merge then names the head
  commit it checked, so GitLab refuses it if the branch moved in between. Pushes go to
  `https://HOST/PATH.git` with glab's credential helper. A multi-line description is not covered
  (no file option): it takes the usual path.

Branch protection on the forge (required reviews, required checks) stays the control that decides
whether a merge goes through: delivery only removes the prompt (on GitLab, an MR whose pipeline is still
running is set to merge once it passes, as `glab mr merge` does).

`lea new --brief FILE` gives a new agent its first message; `--repo NAME` starts it in a
directory of the projects directory; `--branch B` has it work in its own git worktree under
`<projects>/.lea/worktrees/<repo>/<agent>`.

## Commands

```sh
lea                          # the deck, for $LEA_PROJECTS_DIR
lea new [name] [-- args]     # one more agent (its CLI's arguments after --; -p provider, -m model)
lea shell [-- cmd...]        # a shell, or one command, in the same lea
lea stop [--all] [--forget] # stop this lea (--all: every lea); the next lea resumes its agents
                             # in their Claude conversations (--forget: drop them for good)
lea update [--deps ask|all|none]  # the latest release, its images, then newer dependencies
lea rollback                 # back to the previous version; the background update skips this one
lea doctor                   # check the setup
lea init                     # set up again (idempotent)
lea config export > team.json  # your team's shared settings, as one file
lea config import team.json  # take them on another workstation (see Configure)
LEA_PROJECTS_DIR=~/other lea # another projects directory, for this call only
lea grant add|ls|revoke      # let agents take an action without asking (see Grants)
lea delivery on|off|status   # let agents push, open and merge their own PRs (see Let agents deliver)
```

A release install updates itself: at most once a day, `lea` looks for a new release in the
background. A newer minor or patch release is installed beside the current one (its signature
checked with cosign) and becomes active at the next `lea`; a newer major
release is only announced in the deck, and so is a newer minor release while lea is 0.x (only
its patch releases install themselves). Running containers keep their image until they stop.
`LEA_AUTOUPDATE=0` in `~/.config/lea/lea.conf` turns it off; the log is
`~/.local/share/lea/host/update.log`.

`lea update` then looks for a newer release of each dependency lea installs and that you turned
on: locksql (its console image), lockmq and lockfile (their binary on this host). It shows the
current and the available version with a link to the release notes, and asks for each one (no by
default). `--deps all` takes them all, `--deps none` (and no terminal) none; the background update
never touches them. An accepted release is checked before anything is built or installed: cosign
must prove that the tool's release workflow signed it **at that tag** (for locksql, also on the
very commit that was cloned, whose source is then built); then it is recorded in
`~/.config/lea/lea.conf` (`LEA_LOCKSQL_REF` and `LEA_LOCKSQL_IMAGE`, `LEA_LOCKMQ_REF`,
`LEA_LOCKFILE_REF`), and the profiles and volumes stay. Only a plain `vX.Y.Z` newer than the
current one is offered, never one below a security floor (lockfile v0.1.1); an empty ref or a
locksql checkout is left alone. A recorded release older than the one a newer lea pins gives way
to lea's pin. A running console keeps its version until you restart it. lea's image ships the
lockmq and lockfile clients of the releases lea pins: a newer console may need a newer lea.

After an update only the current and the previous versions stay in
`~/.local/share/lea-install` (never the one a running daemon still uses). `lea rollback` links the
previous version again and sets `LEA_AUTOUPDATE_SKIP=<this version>` in `~/.config/lea/lea.conf`,
so the background update does not reinstall it (`lea update` still does); it fails when there is
no previous version.

Each projects directory gets its own container, internal network, Claude history and login. The
projects directory cannot be `/`, your home directory or one of its parents, nor overlap
`~/.claude` or lea's own directories.

## Configure

`~/.config/lea/lea.conf` holds your settings as `KEY=value` lines (`lea init` writes it);
`config/lea.conf` in the installation documents every key and its default.

The deck's `,` opens a settings screen over the same file: the keys that matter, grouped, each
with its value, where it comes from (default, your file, the environment) and a one-line help.
`enter` or `space` flips a toggle, takes the next choice or opens a field (enter saves, esc
cancels); a value is checked before it is written. Sounds apply at once; the screen says when the
others apply (after `lea stop --all`, or at the next `lea`). A key the environment sets
(`LEA_PROJECTS_DIR`, `LEA_APPROVALS`, `LEA_APPROVAL_TIMEOUT`) wins over the files, so it is shown
read-only. The screen runs in the deck on the host: agents cannot open or drive it.

| Key | Default | What it does |
|---|---|---|
| `LEA_PROJECTS_DIR` | asked by `lea init` | the directory lea's agents see |
| `LEA_GIT_SSH` | empty | git SSH endpoints (`host:port …`) |
| `LEA_GIT_KNOWN_HOSTS` | `~/.config/lea/git_known_hosts` | their pinned host keys |
| `LEA_LOCKSQL` | `1` | wire lea for the locksql console of `lox up` (`0`: no locksql) |
| `LEA_LOCKSQL_VAULT_ADDR` | empty | no longer used: `lox up` does not pass Vault to the console yet (lockhub#45) |
| `LEA_LOCKMQ`, `LEA_LOCKFILE` | `0`, asked by `lea init` | let the agents reach a lockmq / lockfile console on this host |
| `LEA_LOCKMQ_REF`, `LEA_LOCKFILE_REF` | the pinned releases | the release `lea init` installs on the host (lockfile v0.1.1 or later); `lea update` records a newer one you accept |
| `LEA_LOCKHTTP` | `0` | send the agents' web requests through a LockHTTP gate (refuses `LEA_GIT_SSH` and `LEA_OTEL_UPSTREAM`) |
| `LEA_HUB` | `1` | follow the Lock Family approval hub (lockhub's facade socket: `LEA_HUB_SOCKET`, else `$XDG_RUNTIME_DIR/lockhub/facade.sock`): an agent whose Lock call waits for a decision there is suspended until it comes, and the deck shows and answers the hub's tickets; while no hub answers, no agent is suspended, the deck says `lockhub unreachable` and `lea doctor` warns. `0`: lea reads no hub and resumes the agents it suspended. **Upgrading from v0.2**: the default was `0`; put `LEA_HUB=0` in `~/.config/lea/lea.conf` to keep that, then restart lea |
| `LEA_HUB_KEEPALIVE` | `300` | seconds (`0` to `3600`) an agent suspended on a hub ticket keeps its process before lea stops it alone, keeping its session for the decision or a message to resume it; read only with `LEA_HUB=1` (changing it while the hub is off restarts nothing) |
| `LEA_INTERNAL_HOSTS` | none | private hosts the proxy may reach (format in `config/internal-hosts`) |
| `LEA_EGRESS_ALLOW` | empty (lea's default list) | the only web domains agents may reach, comma separated (subdomains included); `default` stands for lea's list, `*` opens the web |
| `LEA_NESTED_NETWORKS` | empty | networks your compose files expect, created at start |
| `LEA_CPUS`, `LEA_MEMORY`, `LEA_MEMORY_SWAP`, `LEA_PIDS_LIMIT` | `75%`, `50%`, no swap, `16384` | the lea container's limits: CPUs (a share of Docker's, at least 2, a number such as `8`, or `0`), memory (a share of Docker's, a size such as `16g`, or `0`), memory plus swap (`-1`: no limit), processes and threads (`0`: no limit); the next `lea` applies them to a running container |
| `LEA_APPROVALS`, `LEA_APPROVAL_TIMEOUT` | `deck`, `60` | who answers permission requests |
| `LEA_PROVIDER`, `LEA_MODEL` | `claude`, empty | the default provider and model of a new agent ([providers](#other-providers-codex-ollama-vllm)) |
| `LEA_PROVIDERS` | `~/.config/lea/providers` | your OpenAI-compatible endpoints and their key files |
| `LEA_AUTO_RESUME`, `LEA_RESUME_DELAY` | on, `60` | resume agents stopped on a usage limit, so many seconds after its reset (`0`: off) |
| `LEA_STACK_IDLE` | `10` | minutes an agent stays idle before lea stops (not removes) the containers it started in the nested Podman (`0`: never); `lea board` lists what it freed |
| `LEA_AGENT_CPUS` | `4` | each agent's CPU window, the least shared CPUs: the agent, its tools and its nested containers run there, so `nproc`, PHPStan, Go… see that many CPUs (`0`: no window) |
| `LEA_HEAVY_SLOTS`, `LEA_HEAVY_WORKERS` | `0` (¾ of the CPUs ÷ workers), `4` | heavy jobs (`lea heavy -- <command>`): how many run at once per container, and the CPUs and workers each gets |
| `LEA_ADMIT_CPU_PSI`, `LEA_ADMIT_MEM_PCT`, `LEA_ADMIT_MEM_PSI`, `LEA_ADMIT_IO_PSI`, `LEA_ADMIT_LOAD` | `40`, `85`, `10`, `40`, `2` | [host pressure](#host-pressure) above which new agents wait: PSI in percent, memory in percent of the lea container's limit (the host's from 90 %), the host's load per CPU (`0`: that one off) |
| `LEA_SHED_MEM_PCT`, `LEA_SHED_MEM_PSI`, `LEA_SHED_CPU_PSI`, `LEA_SHED_AFTER`, `LEA_SHED_RESUME` | `95`, `30`, `80`, `30`, `60` | [host pressure](#host-pressure) that pauses the lowest-priority agent once it lasts `LEA_SHED_AFTER` seconds (`0`: never), resumed after `LEA_SHED_RESUME` seconds of calm |
| `LEA_SOUND`, `LEA_AUTONAME` | on | the deck's sounds, automatic names (`0`: off) |
| `LEA_SOUND_ATTENTION`, `LEA_SOUND_DONE` | freedesktop sounds | the file of each sound (`0`: that one off) |
| `LEA_OTEL_UPSTREAM` | empty | send Claude Code usage telemetry to this OTLP collector |
| `LEA_ASCII` | off | ASCII marks in the deck (`1`) |

### Share a configuration with your team

`lea config export [--output FILE] [--include-personal]` writes the shared part of your
configuration as one JSON file, fit to commit in a team repository: the keys your
`~/.config/lea/lea.conf` sets among `LEA_GIT_SSH`, `LEA_EGRESS_ALLOW`, `LEA_OTEL_UPSTREAM`,
`LEA_IMAGE`, `LEA_PROXY_IMAGE`, `LEA_LOCKSQL`, `LEA_LOCKSQL_IMAGE`, `LEA_LOCKSQL_REF`,
`LEA_LOCKSQL_VAULT_ADDR`, `LEA_RELEASES_URL`, `LEA_NESTED_NETWORKS`, `LEA_K8S_NAMESPACE`,
`LEA_APPROVALS` and `LEA_APPROVAL_TIMEOUT` (`LEA_SOUND` too with `--include-personal`), and the
content of your pinned `git_known_hosts` and of your `internal-hosts` file. Never exported:
`LEA_PROJECTS_DIR` and every path, any other key (skipped with a warning), lea's SSH key, forge
tokens, locksql and Vault credentials, `~/.claude` and lea's state. A value or a line that does
not pass the import's checks (a locksql checkout's path, a `@cert-authority` line…) is skipped
with a warning.

`lea config import FILE|- [--dry-run] [--replace] [--yes]` reads such a file as data, never as
shell: only those keys and the two files are taken, and every value and line is checked with
lea's own rules (an approval mode, a timeout of 1 to 600 s, `host:port` endpoints, plain host
names and pinned keys without wildcards, `https://` URLs, image references…). Anything else, a
value on more than one line or holding shell syntax, or a format newer than this lea reads, is
refused and nothing is written. The configuration it gives is loaded once more before any write.

It prints what changes (`KEY: old -> new`, `+` and `-` lines of the files), then:

- a change that widens what the agents reach (`[egress]`: `LEA_GIT_SSH`, `LEA_EGRESS_ALLOW`,
  `LEA_OTEL_UPSTREAM`, `internal-hosts` lines), pins SSH host keys (`[host key]`) or chooses the
  code lea runs (`[code]`: images, `LEA_LOCKSQL_REF`, `LEA_LOCKSQL_VAULT_ADDR`,
  `LEA_RELEASES_URL`) or wires a lock tool (`[lock tool]`: `LEA_LOCKSQL` turned on) needs its own
  flag, `--allow-egress`, `--allow-host-keys`, `--allow-code` or `--allow-lock`, on top of `--yes`;
  `--dry-run` shows which and writes nothing (`LEA_LOCKMQ` and `LEA_LOCKFILE` are not shared);
- without `--yes` it asks on the terminal (and refuses without one).

By default the import merges: the keys it carries replace yours, the others (`LEA_PROJECTS_DIR`,
your sounds…) stay; its files' lines are added to yours, and a host you already pinned to another
key is refused (`--replace` replaces your pins). `--replace` replaces the shared part: shared keys
the file does not set are removed, and the files it carries replace yours. The files are written to
`~/.config/lea/git_known_hosts` and `~/.config/lea/internal-hosts` (`LEA_GIT_KNOWN_HOSTS` or
`LEA_INTERNAL_HOSTS` then points there), only when the import adds lines: a file you use at its
own path (an organisation's shared file) stays in use, untouched, when the import adds nothing to it; every file it replaces is kept as `*.bak`; every write is private (0600), at once,
and refused through a symbolic link. A key the environment sets beats the file: the import says
so. Changes apply after `lea init` (endpoints, keys, images, locksql) or `lea stop --all`
(approvals, egress), as it reminds you.

## How lea keeps your machine safe

- The container sees the projects directory, a read-only allow-list of `~/.claude`
  (`settings.json`, `keybindings.json`, `hooks`, `skills`, `plugins`, `agents`, `commands`,
  `output-styles`, the top-level `*.md` files) and, with locksql, lockmq or lockfile, the socket
  volume of their `lox up` lock. Nothing else.
- `.lockfamily` in the projects directory (the Lock Family hub's config, the locks' public keys,
  the routing) is read-only in the container: lea makes it, owned by you on the host, when it is
  missing, and refuses to start when it is a symbolic link or a file.
- With the Lock Family's `lox up` in the projects directory, its locks are the consoles lea's
  agents reach (locksql, lockmq, lockfile and LockHTTP run under `lox up` only): for each tool that is on (`LEA_LOCKSQL`, `LEA_LOCKMQ`,
  `LEA_LOCKFILE`), lea mounts the socket volume `lox up` names in `.lockfamily/up.json` (lockhub's
  `docs/lock-up.md`) on `/run/<tool>`, read-only, once docker confirms it is that lock's socket
  volume for this projects directory (compose project, `dev.lockfamily.role=socket`, instance and
  workspace labels): never a lock's home or the hub's network. lea ignores an `up.json` that is a
  link, that others than you can write, or that names another projects directory. A tool that
  `lock up` runs but lea cannot use (two locks of it, a volume docker does not confirm) gets no
  socket at all, never the fixed one of another console; a warning says so.
- Egress goes through a proxy: HTTPS (and plain HTTP) to an allowlist of public domains (plus
  `LEA_INTERNAL_HOSTS`), SSH only to `LEA_GIT_SSH`. Private and link-local addresses are refused.
- By default the allowlist is lea's own: `github.com`, `githubusercontent.com`, `ghcr.io`,
  `gitlab.com` (git, `gh`, `glab`, release assets), `golang.org` (Go modules and checksums),
  `registry.npmjs.org`, `registry.yarnpkg.com`, `pypi.org`, `pythonhosted.org`, `docker.io`,
  `docker.com`, `quay.io` (images for nested builds), `fedoraproject.org`, `alpinelinux.org`
  (their packages), `openai.com`, `chatgpt.com` (Codex), and always `anthropic.com`, `claude.ai`,
  `claude.com` (the model API and its login). Each domain covers its subdomains. Anything else
  (documentation sites fetched by an agent, another forge, a Fedora mirror outside
  `fedoraproject.org`) is refused with a page naming `LEA_EGRESS_ALLOW`.
- To add domains, set `LEA_EGRESS_ALLOW="default,corp.example,docs.example.org"` (`default` keeps
  lea's list); without `default`, only the domains you list (and the model API) are reachable. To
  open the web to any domain, as lea did before, set `LEA_EGRESS_ALLOW='*'` (`lea doctor` warns).
  Internal hosts are not affected. The proxy reads it at start: `lea stop --all`, then `lea`.
  `lea doctor` shows the active list.
- Whatever `LEA_EGRESS_ALLOW` says, the proxy refuses public DNS-over-HTTPS resolvers
  (`dns.google`, `cloudflare-dns.com`, `quad9.net`, `nextdns.io`, any `doh.*` name …), which would
  reopen the DNS channel the internal network closes, and public IP literals (`https://1.1.1.1/`,
  decimal or hex forms, IPv6): a public host is reached by its name. A host of your network is
  reached by its address once listed in `LEA_INTERNAL_HOSTS`.
- An internal host name grants that name only, not every vhost of the ingress behind it: the proxy
  peeks at the TLS handshake (no decryption) and passes a tunnel only when its SNI is the allowed
  name, so a tunnel to an internal name must be TLS with SNI. The proxy cannot see the `Host` header
  inside: if your ingress serves a `Host` other than the SNI, require SNI = Host on it. An IPv4
  literal grants the whole address, every vhost on it included.
- The API keys of your providers' endpoints stay in the proxy container: no agent mounts a key
  file, and the key forwarder reaches an endpoint only through the proxy's egress rules.
- lea never gets the Docker socket; the agents' own containers run in a rootless Podman inside
  lea, under seccomp and, on Linux, AppArmor. lea's seccomp profile (`image/seccomp.json`, not
  Docker's default) refuses `socketcall` and the kernel crypto API sockets (`AF_ALG`,
  CVE-2026-31431). 32-bit x86 (i386) programs do not run in lea: their `socketcall` hides the
  socket family from seccomp, so the profile leaves that architecture out.
- Every lea container shares the host kernel, so the host's patch level is part of the
  containment. The kernel must carry the fixes for CVE-2026-74753, CVE-2026-80521 and CVE-2026-80841; on Ubuntu, check with
  `apt changelog linux-image-$(uname -r) | grep -E 'CVE-2026-(74753|80521|80841)'`. `lea doctor`
  shows the kernel, Docker Engine, containerd and runc versions and fails below the kernel minimum
  of `config/host-minimums`; a distribution without a kernel minimum there gets a warning. On a
  Podman host (`docker` is Podman), lea is not refused: `lea doctor` warns that lea is built for
  Docker Engine, and below Podman 5.8.4 (CVE-2026-57231) the launcher warns too.
- Work in lea, not on the host, inside the projects directory: an agent can write files your host
  tools would run. When the last session closes, lea reports changes to git hooks, risky git
  configuration, `.mcp.json` and `.claude/` settings.
- Change Claude Code's configuration on the host: it is read-only in lea. After editing a single
  file (`settings.json`, a top-level `*.md`, `keybindings.json`), restart lea (`lea stop`).

## FAQ

**Is lea a replacement for Claude Code?** No. lea runs the real Claude Code, with your own
configuration, and gives it a safe place to run and a deck to run several at once.

**Why not `--dangerously-skip-permissions` on the host?** On the host that flag hands an agent
everything your user can reach. In lea an agent can go just as fast, but what it can reach is fixed
by the container and the proxy.

**How is lea different from a devcontainer?** A devcontainer isolates a toolchain; lea isolates the
agent: an egress proxy that refuses private networks, no credentials, a nested container engine, an
audit of risky changes on exit, and a deck for many agents.

**Does my code leave my machine?** Only the way Claude Code already sends it, to the Claude API. lea
has no server and no account, and sends no telemetry of its own; Claude Code's usage metrics go
nowhere unless you set `LEA_OTEL_UPSTREAM`.

**Can an agent reach a service on my LAN?** Not by default. List the hosts it may reach in
`LEA_INTERNAL_HOSTS` (format in `config/internal-hosts`).

## Troubleshooting

- **Start with `lea doctor`.** It checks Docker and the host versions (kernel, Docker Engine,
  containerd, runc), the images, the proxy and the internal networks, tmux, the AppArmor profile,
  the projects directory and the configuration.
- **An agent cannot reach a host.** Private and link-local addresses are refused on purpose; add
  the host to `LEA_INTERNAL_HOSTS`. A public domain outside lea's default list goes in
  `LEA_EGRESS_ALLOW` (`default,<domain>`); a public IP literal or a DNS-over-HTTPS resolver is
  always refused. The proxy's refusal page says which host it refused and why.
- **A change to `~/.claude` does not show.** Single files are mounted as they were at start:
  `lea stop`, then `lea`.
- **`docker compose` fails with a network error.** Known limitation, below; `docker run` works.

## Install from source

```sh
git clone https://github.com/DavidGodefroid/lea.git ~/.local/share/lea-src
~/.local/share/lea-src/bin/lea init      # builds build/lea and the images (needs only Docker)
lea
```

`lea update` in a checkout pulls and rebuilds. `make tmux-build` builds a static tmux 3.5a into
`build/tmux` for hosts without a recent one.

## Uninstall

```sh
lea stop --all --forget
rm -rf ~/.local/bin/lea ~/.local/share/lea-install ~/.local/share/lea ~/.config/lea
docker image ls 'ghcr.io/davidgodefroid/lea*'      # then docker image rm the ones listed
```

## Development

```sh
go test ./...         # Go unit tests (the tmux ones need tmux 3.2+)
make test             # shellcheck + shell unit tests, in a container
make build            # the lea:dev and lea-proxy:dev images
make test-proxy       # proxy rules against fake DNS and decoys
make test-isolation   # isolation checks (needs make build and, on Linux, lea init)
```

A tag `vX.Y.Z` publishes a release: the archives for Linux and macOS (amd64, arm64), their
checksums, and the images on `ghcr.io/davidgodefroid`.

## CPUs

Test suites, static analysis and builds start one worker per CPU they see. lea gives each agent a
window of `LEA_AGENT_CPUS` CPUs (4), the ones fewest agents share. The agent, its tools and the
containers it starts in the nested Podman run there: `nproc`, PHPStan, php-cs-fixer, Go, pytest-xdist
and Node's `os.availableParallelism()` see that many CPUs, without anything in your projects. (lea
runs the nested Podman's `crun` through a wrapper that pins an agent's containers, `podman exec`
included.) Tools that count `/proc/cpuinfo` still see every CPU. The window is not a hard limit:
the lea container's `LEA_CPUS` is. `lea doctor` lists the windows.

## Heavy jobs

Test suites, static analysis, builds and browsers start one worker per core of the host: a few
agents running them at once take every core. Agents run them through `lea heavy`:

```sh
lea heavy -- vendor/bin/phpstan analyse
lea heavy -- go test ./...
```

`lea heavy` waits for one of the container's `LEA_HEAVY_SLOTS` slots, in order, saying who holds
them, then runs the command on `LEA_HEAVY_WORKERS` CPUs of its own (tools that count the CPUs
they may use, such as `nproc`, see that many) with `GOMAXPROCS`, `MAKEFLAGS=-jN`,
`CMAKE_BUILD_PARALLEL_LEVEL`, `CARGO_BUILD_JOBS`, `PYTEST_XDIST_AUTO_NUM_WORKERS` and
`LEA_HEAVY_WORKERS` set. A slot whose job was killed frees itself within three minutes; a job holds its slot two hours at
most. `lea board`
lists the jobs that run and wait.

## Host pressure

The lea daemon samples the host and each lea container every 5 seconds: PSI (`cpu some`,
`memory full`, `io full`, avg10), the memory in use against the container's limit (page cache the
kernel drops first left out), and the host's load. It reads `/proc` and the container's cgroup v2,
found from its process, so it works the same with Docker and rootless Podman.

Above a threshold a lead's `agents_spawn` is refused with the reason, for example
`under pressure (container cpu PSI 63% > 40%, memory 92% of 24.0 GiB > 85%); 14 agents run, 2 heavy
slots busy`. With `queue: true` the spawn waits instead, up to 30 minutes, and the lead gets a
message when it starts; one calm sample starts one queued agent at most. The queue lives in the
daemon's memory: a daemon restart drops it without a message. `agents_request_spawn` is not
refused: a request starts nothing, the lead's `agents_spawn` is the gate. Meanwhile each container gets half its heavy slots (at least one): jobs
that run keep theirs, new ones wait.

When a container (or the host) stays critical for `LEA_SHED_AFTER` seconds (30: memory at 95 % of
the limit, memory PSI 30 %, or cpu PSI 80 %), lea pauses its lowest-priority agent together with
the containers it started in the nested Podman (SIGSTOP: the nested Podman has no cgroup freezer).
A frozen process keeps its memory, so when memory is what is critical lea first stops those
containers, and starts them again when it resumes the agent. It pauses another one every
`LEA_SHED_AFTER` only while each pause makes the reading better, and at most half the agents it
may pause: no cascade when what weighs is elsewhere. Idle agents go first, then the newest dev,
reviewers after devs; never a lead, never an agent that holds a heavy slot. A busy agent is paused
only once no idle one is left: it carries on where it stopped when resumed. Its lead gets a message, and `lea board` lists it
under "Paused under pressure". Once the pressure stays under the admission thresholds for
`LEA_SHED_RESUME` seconds (60), lea resumes them one by one, the last paused first; one you
resumed yourself (the deck's p) is left alone. The list survives a daemon restart.

`lea doctor` checks that PSI is readable, that each running lea container's cgroup is, and that
the shedding thresholds sit above the admission ones.

`lea board` shows the host's gauge and each container's
(`CPU 14.8/16 · PSI 22% · mem 18.1/24.0 GiB · heavy 3/4 (+2 queued)`); the deck shows the
container's on its last line, amber near a threshold, red over one.

## Known limitations

- What an agent starts in the nested Podman (`docker`, `docker compose`, `podman`, through its own
  Podman service) is removed when the agent ends, and stopped (not removed) once the agent has
  been idle `LEA_STACK_IDLE` minutes (10): the agent starts it again when it needs it. What you
  start from a deck shell is neither. Every `lea` removes what the agents that are gone left,
  stops the stacks of agents idle for that long already, and names the containers no agent
  started.

- `docker compose` with its own networks does not work inside lea yet: the nested Podman's
  network backend (netavark) sets `net/ipv4/conf/<bridge>/route_localnet` to 1 in its rootless
  network namespace, which Docker's read-only `/proc/sys` refuses. It skips the write when the
  value is already 1, but that namespace takes its defaults from the host's
  `net.ipv4.conf.default`, where it is 0 on most hosts. `docker run` works.
- The AppArmor profile (`apparmor/lea.tmpl`) omits docker-default's `/proc` write denies and mount
  narrowing for now: they may break rootless Podman and are pending validation.

## License

[Apache 2.0](LICENSE). lea is an independent project, not affiliated with Anthropic. Claude and
Claude Code are trademarks of Anthropic.

The brand assets (logo, demo) are in [`assets/`](assets), generated by [`tools/brand`](tools/brand);
the website is [`site/`](site), published by `.github/workflows/pages.yml`.
