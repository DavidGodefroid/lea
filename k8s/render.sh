#!/bin/sh
# Renders the read-only lea service account for one developer.
# Usage: k8s/render.sh DEV SUBJECT_KIND SUBJECT_NAME [SUBJECT_NAMESPACE]
#   DEV           the developer's workstation login (service account lea-DEV)
#   SUBJECT_KIND  User, Group or ServiceAccount: the developer's own Kubernetes identity
#   SUBJECT_NAME  its name; SUBJECT_NAMESPACE is required for a ServiceAccount
set -eu
usage() { sed -n '2,7p' "$0" >&2; exit 2; }
[ $# -ge 3 ] || usage
dev=$1 kind=$2 name=$3 ns=${4:-}
case $dev in ''|*[!a-z0-9-]*) echo "render: DEV must match [a-z0-9-]+" >&2; exit 2 ;; esac
case $name in ''|*[!A-Za-z0-9_.@:-]*) echo "render: SUBJECT_NAME must match [A-Za-z0-9_.@:-]+" >&2; exit 2 ;; esac
[ -z "$ns" ] || case $ns in *[!a-z0-9-]*) echo "render: SUBJECT_NAMESPACE must match [a-z0-9-]+" >&2; exit 2 ;; esac
kns=${LEA_K8S_NAMESPACE:-lea}
case $kns in ''|*[!a-z0-9-]*) echo "render: LEA_K8S_NAMESPACE must match [a-z0-9-]+" >&2; exit 2 ;; esac
case $kind in
  User|Group) extra="    apiGroup: rbac.authorization.k8s.io" ;;
  ServiceAccount) [ -n "$ns" ] || { echo "render: a ServiceAccount subject needs SUBJECT_NAMESPACE" >&2; exit 2; }
                  extra="    namespace: $ns" ;;
  *) echo "render: SUBJECT_KIND must be User, Group or ServiceAccount" >&2; exit 2 ;;
esac
sed -e "s/@DEV@/$dev/g" -e "s/@NS@/$kns/g" -e "s/@KIND@/$kind/" -e "s|@NAME@|$name|" -e "s|^@EXTRA@\$|$extra|" \
  "$(dirname "$0")/lea-readonly.yaml.tmpl"
