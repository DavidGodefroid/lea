#!/bin/sh
# Renders squid.conf on stdout from an internal-hosts file. Usage: render-squid-conf.sh INTERNAL_HOSTS
set -eu
hosts_file=$1

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

set -f
n=0
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
    *[!0-9.]*) echo "acl ih_${n}_dst dstdomain -n $host" ;;
    *) echo "acl ih_${n}_dst dst $host/32" ;;
  esac
  echo "acl ih_${n}_port port $(printf '%s' "$ports" | tr ',' ' ')"
  echo "http_access allow ih_${n}_dst ih_${n}_port"
done <"$hosts_file"

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
