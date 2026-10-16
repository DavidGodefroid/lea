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
