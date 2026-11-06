load helpers
setup() { setup_fakes; load_lea; F="$BATS_TEST_TMPDIR/locks.lock"; cp "$LEA_ROOT/config/locks.lock" "$F"; }

# lea#134: one set of Lock clients, config/locks.lock, bumped as a whole by tools/locks-bump.
@test "the shipped set is well-formed" {
  run "$LEA_ROOT/tools/locks-bump" --check
  [ "$status" -eq 0 ]
  [[ "$output" == *"is a well-formed set"* ]]
}

@test "a set that mixes releases or is incomplete is refused" {
  for e in 's|locksql/.github/workflows/release.yml@refs/tags/v[0-9.]*|locksql/.github/workflows/release.yml@refs/tags/v0.0.1|' \
    's|^LOCKS_LOCKMQ_IMAGE=ghcr.io/davidgodefroid/lockmq@|LOCKS_LOCKMQ_IMAGE=ghcr.io/davidgodefroid/lockfile@|' \
    '/^LOCKS_LOCKFILE_SHA256_ARM64=/d' \
    's|^LOCKS_LOCKHTTP_VERSION=.*|LOCKS_LOCKHTTP_VERSION=latest|' \
    's|^LOCKS_SET=.*|LOCKS_SET="lockkit-v1.0.0"|'; do
    cp "$LEA_ROOT/config/locks.lock" "$F"
    sed -i "$e" "$F"
    run "$LEA_ROOT/tools/locks-bump" --check "$F"
    [ "$status" -ne 0 ] || { echo "accepted: $e"; false; }
  done
}

@test "locks-bump verifies before it writes: a bad release or a missing tool writes nothing" {
  run "$LEA_ROOT/tools/locks-bump" --lockkit v0.13.0 --lox v0.9.0 --locksql main --lockmq v0.8.0 --lockfile v0.8.0 --lockhttp v0.8.0 --out "$F"
  [ "$status" -ne 0 ]
  [[ "$output" == *"every release is vX.Y.Z (got 'main')"* ]]
  cmp "$F" "$LEA_ROOT/config/locks.lock"
}

@test "no per-Lock pin is left outside the set, but the dev overrides" {
  ! grep -Eq '^LEA_LOCK(SQL|MQ|FILE)_REF=|^LEA_LOCKSQL_IMAGE=' "$LEA_ROOT/config/lea.conf"
  ! grep -Eq '\b(V|MQ|LF)=[0-9]+\.[0-9]+\.[0-9]+' "$LEA_ROOT/image/Containerfile"
  ! grep -Eq '^LEA_LOCKHTTP_RELEASE=v[0-9]' "$LEA_ROOT/libexec/lib/lockhttp.sh"
  [ "$LEA_LOCKHTTP_RELEASE" = "$(lea__lock_version lockhttp)" ]
}
