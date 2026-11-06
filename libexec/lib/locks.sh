# shellcheck shell=sh
# The set of Lock releases lea's clients come from (config/locks.lock, lea#134): read here, never
# sourced (it is data). lea doctor's comparison with the locks lox runs is the Go launcher's
# (internal/launcher/lockcompat.go).

# lea__lockset KEY: KEY's value in config/locks.lock (LOCKS_SET, LOCKS_LOCKSQL_VERSION ...), empty
# when it has none.
lea__lockset() {
  sed -n "s/^$1=\([^ ]*\)\$/\1/p" "$LEA_ROOT/config/locks.lock" 2>/dev/null | tail -n 1
}

# lea__lock_version TOOL: the release of TOOL's client in lea's image (the set's), vX.Y.Z.
lea__lock_version() {
  lea__lockset "LOCKS_$(printf '%s' "$1" | tr '[:lower:]' '[:upper:]')_VERSION"
}

# lea__lockset_summary: the set in one line, for lea update and lea init.
lea__lockset_summary() {
  printf '%s: locksql %s, lockmq %s, lockfile %s, lockhttp %s, lox %s' "$(lea__lockset LOCKS_SET)" \
    "$(lea__lock_version locksql)" "$(lea__lock_version lockmq)" "$(lea__lock_version lockfile)" \
    "$(lea__lock_version lockhttp)" "$(lea__lockset LOCKS_LOX_VERSION)"
}
