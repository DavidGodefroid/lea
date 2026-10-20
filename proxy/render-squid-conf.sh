#!/bin/sh
# Renders squid.conf on stdout from an internal-hosts file. Usage: render-squid-conf.sh [--peek] INTERNAL_HOSTS
#
# LEA_EGRESS_ALLOW (domain names separated by commas or blanks; empty: none) restricts the web
# destinations on 80 and 443 to those domains and their subdomains, plus the ones lea's agents
# always need (EGRESS_ALWAYS: the model API and its login). Internal hosts are not affected.
#
# An internal name usually sits behind a shared ingress that routes TLS on SNI and Host, so the main
# squid hands an allowed CONNECT to an internal name to a second, loopback squid (--peek; nothing
# when there is no internal name). That one peeks at the ClientHello, never decrypts, and splices
# only when the SNI is the CONNECT host: an allowed name cannot reach another vhost of its ingress.
# IP literals grant the address: no SNI check. The peek squid is a separate process because its
# port must be ssl-bump, and on an ssl-bump port squid bumps a refused CONNECT to show its error.
set -eu
peek=
if [ "${1:-}" = --peek ]; then peek=1; shift; fi
hosts_file=$1

# Keep in step with libexec/lib/doctor.sh.
EGRESS_ALWAYS="anthropic.com claude.ai claude.com"

# egress_domains: LEA_EGRESS_ALLOW then EGRESS_ALWAYS, lower-cased, checked, without a name that
# another one already covers (squid refuses a dstdomain list where one entry covers another).
egress_domains() {
  all=
  for d in $(printf '%s' "$LEA_EGRESS_ALLOW" | tr ',' ' ') $EGRESS_ALWAYS; do
    case $d in
      *[!ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789.-]*|.*|*.|*..*) bad=1 ;;
      *[!0-9.]*) bad= ;;
      *) bad=1 ;;
    esac
    if [ -n "$bad" ]; then
      echo "render-squid-conf: LEA_EGRESS_ALLOW: '$d' is not a domain name" >&2; return 1
    fi
    all="$all $(printf '%s' "$d" | tr '[:upper:]' '[:lower:]')"
  done
  kept=
  for d in $all; do
    covered=
    for f in $all $kept; do
      case $d in *."$f") covered=1 ;; esac
    done
    case " $kept " in *" $d "*) covered=1 ;; esac
    [ -n "$covered" ] || kept="$kept $d"
  done
  printf '%s\n' "$kept"
}

acls=$(mktemp)
trap 'rm -f "$acls"' EXIT
set -f
egress=
if [ -n "${LEA_EGRESS_ALLOW:-}" ]; then egress=$(egress_domains) || exit 1; fi
n=0
names=0
lineno=0
while IFS= read -r line || [ -n "$line" ]; do
  lineno=$((lineno + 1))
  line=${line%%#*}
  # shellcheck disable=SC2086
  set -- $line
  [ $# -eq 0 ] && continue
  if [ $# -ne 2 ]; then echo "render-squid-conf: line $lineno: expected '<host> <ports>'" >&2; exit 1; fi
  host=$1 ports=$2
  case $ports in
    *[!0-9,]*|,*|*,|*,,*) echo "render-squid-conf: line $lineno: bad port list '$ports'" >&2; exit 1 ;;
  esac
  n=$((n + 1))
  case $host in
    # -n: match the requested name only, never a reverse lookup of an IP literal.
    *[!0-9.]*)
      names=$((names + 1))
      echo "acl ih_${n}_dst dstdomain -n $host"
      echo "acl ih_names dstdomain -n $host" ;;
    *) echo "acl ih_${n}_dst dst $host/32" ;;
  esac
  echo "acl ih_${n}_port port $(printf '%s' "$ports" | tr ',' ' ')"
  echo "http_access allow ih_${n}_dst ih_${n}_port"
done <"$hosts_file" >"$acls"

if [ -n "$peek" ]; then
  [ "$names" -eq 0 ] && exit 0
  # The certificate only makes the port valid for ssl-bump: this squid never bumps a tunnel.
  # ssl::server_name falls back to the CONNECT host when the ClientHello has no SNI, which would
  # let a client without SNI reach the ingress's default vhost: the helper sees the raw SNI.
  cat <<'CONF'
http_port 127.0.0.1:3129 ssl-bump tls-cert=/etc/squid/lea-peek.pem generate-host-certificates=off
visible_hostname lea-proxy-peek
cache deny all
cache_mem 0 MB
shutdown_lifetime 1 seconds
connect_timeout 10 seconds
pid_filename none
access_log none
cache_log stdio:/dev/stderr
forwarded_for delete
via off
CONF
  cat "$acls"
  cat <<'CONF'
http_access deny all
external_acl_type lea_sni ttl=3600 negative_ttl=3600 children-max=4 %ssl::>sni %DST /usr/local/bin/sni-matches.sh
acl ih_sni external lea_sni
acl ih_step1 at_step SslBump1
ssl_bump peek ih_step1
ssl_bump splice ih_names ih_sni
ssl_bump terminate all
on_unsupported_protocol respond all
CONF
  exit 0
fi

cat <<'CONF'
http_port 3128
visible_hostname lea-proxy
cache deny all
cache_mem 0 MB
shutdown_lifetime 1 seconds
connect_timeout 10 seconds
pid_filename none
error_directory /etc/squid/lea-errors
logformat lea %ts.%03tu %>a %rm %ru %>Hs %<a
access_log stdio:/dev/stdout lea
cache_log stdio:/dev/stderr
forwarded_for delete
via off
acl CONNECT method CONNECT
acl web_http port 80
acl web_tls port 443
CONF
cat "$acls"
if [ "$names" -gt 0 ]; then
  cat <<'CONF'
cache_peer 127.0.0.1 parent 3129 0 no-query no-digest no-netdb-exchange name=lea_peek
cache_peer_access lea_peek allow CONNECT ih_names
cache_peer_access lea_peek deny all
never_direct allow CONNECT ih_names
CONF
fi
# The allowlist is checked on the requested name, before private_dst resolves it: an unlisted name
# is refused without a DNS lookup.
if [ -n "$egress" ]; then
  printf 'acl egress_allow dstdomain -n'
  for d in $egress; do printf ' .%s' "$d"; done
  printf '\n'
  cat <<'CONF'
deny_info ERR_LEA_EGRESS egress_allow
http_access deny !egress_allow
CONF
fi
cat <<'CONF'
acl private_dst dst 0.0.0.0/8 10.0.0.0/8 100.64.0.0/10 127.0.0.0/8 169.254.0.0/16 172.16.0.0/12 192.168.0.0/16 224.0.0.0/4 240.0.0.0/4
acl private_dst dst ::/128 ::1/128 ::ffff:0:0/96 fc00::/7 fe80::/10 ff00::/8
deny_info ERR_LEA_PRIVATE private_dst
http_access deny private_dst
http_access allow CONNECT web_tls
http_access allow !CONNECT web_http
deny_info ERR_LEA_DENIED all
http_access deny all
CONF
