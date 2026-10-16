package launcher

// Usage is lea's usage (recorded in testdata/golden/command-help.txt).
const Usage = `Usage:
  lea init [--no-locksql]     set up lea on this workstation (once; rerun at will)
  lea                         open the deck: your agents on the left, the selected one on the right
                              (keys: j/k select, enter focus, ctrl+space back, n new, x stop, q quit)
  lea new [-r role] [--repo R] [--branch B] [--brief file] [name] [-- claude args]
                              start one more Claude agent in the deck (roles: lead, dev, reviewer)
  lea role <agent> <role>     change an agent's role (only you can)
  lea shell [-- cmd...]       open a shell, or run a command, in the same lea
  lea kube refresh            mint a read-only Kubernetes token (8 h)
  lea board --md              the board: every agent's task, as markdown
  lea agents                  list agents (lea msg <agent> <text>, lea inbox, lea approvals, lea requests)
  lea stop [--all]
  lea update
  lea doctor
`
