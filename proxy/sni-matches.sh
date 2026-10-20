#!/bin/sh
# squid external ACL helper: reads "<SNI> <CONNECT host>" lines, answers OK when the client's SNI
# names the CONNECT host (case and a trailing dot aside), ERR otherwise, "-" (no SNI) included.
# squid appends the ACL arguments ("-" when none): ignored.
while read -r sni host _; do
  sni=$(printf '%s' "$sni" | tr '[:upper:]' '[:lower:]') host=$(printf '%s' "$host" | tr '[:upper:]' '[:lower:]')
  if [ -n "$host" ] && [ "$sni" != - ] && [ "${sni%.}" = "${host%.}" ]; then
    echo OK
  else
    echo ERR
  fi
done
