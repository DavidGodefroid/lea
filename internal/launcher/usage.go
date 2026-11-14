package launcher

// Usage is lea's usage (recorded in testdata/golden/command-help.txt).
const Usage = `Usage:
  lea init [--no-locksql] [--lockmq] [--lockfile] [--locklog]
                              set up lea on this workstation (once; rerun at will)
  lea locksql | lea lockmq | lea lockfile | lea locklog
                              whether agents reach that tool's console, and how it runs
                              (under lox run only, in another terminal: lea starts none)
  lea                         open the deck: your agents on the left, the selected one on the right
                              (keys: j/k select, enter focus, ctrl+space back, n new, x stop, q quit)
                              (LEA_DECK=0: your own tool, LEA_TOOL, in the lea container instead)
  lea web [--port N] [--open]  the deck in your browser: the board, threads, the control tower,
                              new agents and permissions, on 127.0.0.1 behind a one-time link
  lea attach [--print] [AGENT]
                              run LEA_TOOL in the lea container, or join an agent's terminal;
                              --print: the docker exec command, for an IDE or another terminal
  lea new [-r role] [-p provider] [-m model] [--repo R] [--branch B] [--brief file] [name] [-- args]
                              start one more agent in the deck (roles: lead, dev, reviewer;
                              providers: claude, codex and those of ~/.config/lea/providers)
  lea role <agent> <role>     change an agent's role (only you can)
  lea allow [log]             what agents of this lea may do without asking, and their last
                              uses (log: every use)
  lea allow CAPABILITY... [--repo R]... [--branch GLOB] [--agent NAME] [--for 2h] [--gitlab HOST]...
                              let agents of this lea act without asking: push, pr, merge,
                              delete-branch, issues, stop-finished, release-tag, release
                              (gh release create; --tag GLOB narrows its tags), or deliver
                              (push pr merge delete-branch); every repository here with a
                              forge origin unless --repo; until lea allow off unless --for;
                              new or changed pins are asked about (off a terminal: --yes DIGEST)
  lea allow off [N|CAPABILITY|deliver]
                              revoke allowance N, a capability, the deliver sets, or everything
                              (lea delivery and lea grant add|ls: old forms, until the next minor)
  lea github login|logout|status
                              lea's GitHub token, kept by the proxy only (agents use it through
                              gh and git, never read it); login reads it on stdin:
                              gh auth token | lea github login
  lea egress allow DOMAIN...|deny DOMAIN...|list|denied [--since 24h]
                              the agents' egress allowlist (LEA_EGRESS_ALLOW): allow or deny a
                              domain live, without a restart, kept in lea.conf; list it; denied:
                              the hosts the proxy refused lately (on the host only: your call)
  lea shell [-- cmd...]       open a shell, or run a command, in the same lea
  lea kube refresh            mint a read-only Kubernetes token (8 h)
  lea board --md              the board: every agent's task, as markdown
  lea agents                  list agents (lea msg <agent> <text>, lea inbox, lea approvals, lea requests)
  lea requests                what waits for you: permissions and spawn requests of lea's agents
                              (lea approve ID [always], lea deny ID [reason]); Lock requests: lox run
  lea questions [drop ID]     the control towers' questions to you: the open one, then the queue
                              (one at a time); drop removes a queued one
  lea hold [--off] [AGENT]    hold the messages agents send the tower (or AGENT) while you answer
                              it; --off releases them (the deck's H; LEA_HOLD_MAX)
  lea limits [set KEY VALUE | reset KEY]
                              the live limits (max_running, relay_tokens): shown, or changed on the running lea
                              at once, no restart, kept in your file (the deck's settings, ,)
  lea stop [--all] [--forget]
                              stop lea; its agents resume at the next lea (--forget drops them)
  lea config export [--output FILE] [--include-personal]
                              the shared part of your configuration, as a file for your team
  lea config import FILE [--dry-run] [--replace] [--yes]
                              take a team's configuration (lea config --help: what needs a flag)
  lea config deck on|off      the deck, or your own tool in the lea container (from the next lea)
  lea config tool CMD [ARG...]
                              the tool lea runs in the lea container without the deck (claude)
  lea config claude [KEY VALUE|KEY --unset|LIST add|remove ENTRY]
                              Claude's base settings for your agents (defaultMode, allow, ask,
                              deny, additionalDirectories, env, model...), each with its source;
                              kept in lea's configuration, from the next lea container
  lea update [--deps ask|all|none]
                              the latest lea, then each newer locksql release (lockmq,
                              lockfile, locklog: their launchers update themselves)
                              (asked one by one; all or none without asking; signatures checked)
  lea rollback                back to the previous version; the background update skips this one
  lea doctor
`
