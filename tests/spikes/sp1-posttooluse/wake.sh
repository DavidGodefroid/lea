#!/bin/sh
# asyncRewake PostToolUse hook: stands for the hub's decision arriving 25 s later.
cat >/dev/null
printf '%s WakeStart\n' "$(date +%T)" >> "$(dirname "$0")/hooks.log"
sleep 25
printf '%s WakeFire\n' "$(date +%T)" >> "$(dirname "$0")/hooks.log"
echo "ticket 42 approved: reply with the single word APPROVED" >&2
exit 2
