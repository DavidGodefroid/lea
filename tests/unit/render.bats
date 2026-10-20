load helpers
setup() { setup_fakes; R="$LEA_ROOT/proxy/render-squid-conf.sh"; }

hosts() { printf '%s\n' "$@" >"$BATS_TEST_TMPDIR/hosts"; }

@test "names become dstdomain ACLs with their ports" {
  hosts "# comment" "" "gitlab.com 443,5050   # trailing comment"
  run sh "$R" "$BATS_TEST_TMPDIR/hosts"
  [ "$status" -eq 0 ]
  [[ "$output" == *"acl ih_1_dst dstdomain -n gitlab.com"* ]]
  [[ "$output" == *"acl ih_1_port port 443 5050"* ]]
  [[ "$output" == *"http_access allow ih_1_dst ih_1_port"* ]]
}

@test "IPv4 literals become dst ACLs" {
  hosts "10.0.0.10 443"
  run sh "$R" "$BATS_TEST_TMPDIR/hosts"
  [[ "$output" == *"acl ih_1_dst dst 10.0.0.10/32"* ]]
}

@test "internal allows precede the private deny, which precedes web allows" {
  hosts "a.example 443"
  run sh "$R" "$BATS_TEST_TMPDIR/hosts"
  allow=$(printf '%s\n' "$output" | grep -n 'http_access allow ih_1_dst' | cut -d: -f1)
  deny=$(printf '%s\n' "$output" | grep -n 'http_access deny private_dst' | cut -d: -f1)
  web=$(printf '%s\n' "$output" | grep -n 'http_access allow CONNECT web_tls' | cut -d: -f1)
  [ "$allow" -lt "$deny" ]
  [ "$deny" -lt "$web" ]
  [ "$(printf '%s\n' "$output" | tail -n1)" = "http_access deny all" ]
}

@test "private ranges include CGNAT and IPv4-mapped IPv6" {
  hosts
  run sh "$R" "$BATS_TEST_TMPDIR/hosts"
  for r in 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 127.0.0.0/8 169.254.0.0/16 100.64.0.0/10 ::ffff:0:0/96 fc00::/7; do
    [[ "$output" == *"$r"* ]]
  done
}

@test "a malformed line is refused with its line number" {
  hosts "gitlab.com"
  run sh "$R" "$BATS_TEST_TMPDIR/hosts"
  [ "$status" -ne 0 ]
  [[ "$output" == *"line 1"* ]]
  hosts "ok.example 443" "gitlab.com 44a3"
  run sh "$R" "$BATS_TEST_TMPDIR/hosts"
  [ "$status" -ne 0 ]
  [[ "$output" == *"line 2"* ]]
}

@test "outbound requests do not leak the client address" {
  hosts
  run sh "$R" "$BATS_TEST_TMPDIR/hosts"
  [[ "$output" == *"forwarded_for delete"* ]]
  [[ "$output" == *"via off"* ]]
}

@test "CONNECT to an internal name goes through the peek squid" {
  hosts "issues.example.internal 443"
  run sh "$R" "$BATS_TEST_TMPDIR/hosts"
  [ "$status" -eq 0 ]
  [ "$(printf '%s\n' "$output" | grep '^http_port')" = "http_port 3128" ]
  [[ "$output" != *"ssl"* ]]
  [[ "$output" == *"acl ih_names dstdomain -n issues.example.internal"* ]]
  [[ "$output" == *"cache_peer 127.0.0.1 parent 3129 0 no-query"*"name=lea_peek"* ]]
  [[ "$output" == *"cache_peer_access lea_peek allow CONNECT ih_names"* ]]
  [[ "$output" == *"never_direct allow CONNECT ih_names"* ]]
}

@test "the peek squid splices a tunnel only when its SNI is the CONNECT host" {
  hosts "issues.example.internal 443"
  run sh "$R" --peek "$BATS_TEST_TMPDIR/hosts"
  [ "$status" -eq 0 ]
  [[ "$output" == *"http_port 127.0.0.1:3129 ssl-bump tls-cert=/etc/squid/lea-peek.pem generate-host-certificates=off"* ]]
  [[ "$output" == *"http_access allow ih_1_dst ih_1_port"* ]]
  [ "$(printf '%s\n' "$output" | grep '^http_access' | tail -n1)" = "http_access deny all" ]
  [[ "$output" == *"external_acl_type lea_sni "*" %ssl::>sni %DST /usr/local/bin/sni-matches.sh"* ]]
  rules=$(printf '%s\n' "$output" | grep -E '^ssl_bump ')
  [ "$rules" = "ssl_bump peek ih_step1
ssl_bump splice ih_names ih_sni
ssl_bump terminate all" ]
  [[ "$output" == *"on_unsupported_protocol respond all"* ]]
}

@test "IP literals are not sent to the peek squid" {
  hosts "10.0.0.10 443" "issues.example.internal 443"
  run sh "$R" "$BATS_TEST_TMPDIR/hosts"
  [[ "$output" != *"ih_names dst 10.0.0.10"* ]]
  [[ "$output" == *"acl ih_names dstdomain -n issues.example.internal"* ]]
}

@test "no internal names: no peek squid" {
  hosts "10.0.0.10 443"
  run sh "$R" "$BATS_TEST_TMPDIR/hosts"
  [[ "$output" != *"cache_peer"* ]]
  [[ "$output" != *"never_direct"* ]]
  run sh "$R" --peek "$BATS_TEST_TMPDIR/hosts"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "sni-matches: OK only when the SNI is the CONNECT host" {
  run sh "$LEA_ROOT/proxy/sni-matches.sh" <<'IN'
issues.example.internal issues.example.internal
admin.example.internal issues.example.internal
- issues.example.internal
Issues.Example.Internal. issues.example.internal -
 issues.example.internal
IN
  [ "$status" -eq 0 ]
  [ "$output" = "OK
ERR
ERR
OK
ERR" ]
}
