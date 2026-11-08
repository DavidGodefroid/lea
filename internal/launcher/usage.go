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
  lea attach [--print] [AGENT]
                              run LEA_TOOL in the lea container, or join an agent's terminal;
                              --print: the docker exec command, for an IDE or another terminal
  lea new [-r role] [-p provider] [-m model] [--repo R] [--branch B] [--brief file] [name] [-- args]
                              start one more agent in the deck (roles: lead, dev, reviewer;
                              providers: claude, codex and those of ~/.config/lea/providers)
  lea role <agent> <role>     change an agent's role (only you can)
  lea delivery on [--yes DIGEST] [--gitlab HOST]...|off|status
                              let every agent of this lea push its own branch, open and merge
                              its PR (GitHub) or MR (GitLab: gitlab.com, --gitlab hosts) on
                              each repository here, without asking (standing grants; on lists
                              new or changed pins and their digest, --yes DIGEST records
                              exactly those; off revokes them)
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
  lea stop [--all] [--forget]
                              stop lea; its agents resume at the next lea (--forget drops them)
  lea config export [--output FILE] [--include-personal]
                              the shared part of your configuration, as a file for your team
  lea config import FILE [--dry-run] [--replace] [--yes]
                              take a team's configuration (lea config --help: what needs a flag)
  lea config deck on|off      the deck, or your own tool in the lea container (from the next lea)
  lea config tool CMD [ARG...]
                              the tool lea runs in the lea container without the deck (claude)
  lea update [--deps ask|all|none]
                              the latest lea, then each newer locksql release (lockmq,
                              lockfile, locklog: their launchers update themselves)
                              (asked one by one; all or none without asking; signatures checked)
  lea rollback                back to the previous version; the background update skips this one
  lea doctor
`
