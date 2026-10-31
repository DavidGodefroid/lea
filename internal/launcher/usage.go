package launcher

// Usage is lea's usage (recorded in testdata/golden/command-help.txt).
const Usage = `Usage:
  lea init [--no-locksql] [--lockmq] [--lockfile]
                              set up lea on this workstation (once; rerun at will)
  lea locksql | lea lockmq | lea lockfile
                              whether agents reach that tool's console, and how it runs
                              (under lox up only; lea never starts it)
  lea lock open|close locksql [profile] | lea lock list
                              lox up's locksql console in a pane of the deck, under the agent
                              (the deck's L): its own questions, typed by you only
  lea                         open the deck: your agents on the left, the selected one on the right
                              (keys: j/k select, enter focus, ctrl+space back, n new, x stop, q quit)
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
  lea shell [-- cmd...]       open a shell, or run a command, in the same lea
  lea kube refresh            mint a read-only Kubernetes token (8 h)
  lea board --md              the board: every agent's task, as markdown
  lea agents                  list agents (lea msg <agent> <text>, lea inbox, lea approvals, lea requests)
  lea stop [--all] [--forget]
                              stop lea; its agents resume at the next lea (--forget drops them)
  lea config export [--output FILE] [--include-personal]
                              the shared part of your configuration, as a file for your team
  lea config import FILE [--dry-run] [--replace] [--yes]
                              take a team's configuration (lea config --help: what needs a flag)
  lea update [--deps ask|all|none]
                              the latest lea, then each newer locksql release (lockmq and
                              lockfile: their launchers update themselves)
                              (asked one by one; all or none without asking; signatures checked)
  lea rollback                back to the previous version; the background update skips this one
  lea doctor
`
