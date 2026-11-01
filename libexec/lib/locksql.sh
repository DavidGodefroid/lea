# shellcheck shell=sh
# locksql: its console runs hub-attached only, under lockhub's `lox up`, in its own compose
# service (lockhub's docs/lox-up.md). lea starts none: its agents reach the socket volume of lox
# up's locksql lock of the projects directory (.lockfamily/up.json, internal/launcher), the deck
# attaches to that console (lea lock open locksql), and the queries the console asks about wait
# for an approval through the hub (lea's deck, hub view, or lox approve).

# lea locksql: whether locksql is on, and how its console runs. It never starts a console.
lea_locksql() {
  [ $# -eq 0 ] || lea_die "usage: lea locksql (lea never starts the console: lox up does; lox attach NAME reaches it)"
  if ! lea__tool_on locksql; then
    lea_log "locksql is off (LEA_LOCKSQL=0): lea init --locksql turns it on"
    return 0
  fi
  lea__tool_by_lock locksql || lea__locksql_console_hint
}
